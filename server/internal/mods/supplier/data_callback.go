package supplier

// 下游回調轉發重試：
// 交付完成 → downstream_callbacks pending → POST 下游 notify_url（簽名同四頭口徑，
// 下游驗我們）→ success / 失敗退避重試（間隔表耗盡入死信，可手動重發）。
// CallbackUrlGuard：HTTPS 強制 + 私網段拒絕（httpx.ValidateURL + 連接期 IP 複核）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/downstreamcallback"
	"github.com/NovaWorks/zcard-next/server/internal/platform/httpx"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"
)

// CallbackTaskType 回調轉發任務類型（default 隊列）。
const CallbackTaskType = "supplier.callback"

// 回調退避間隔（秒）：30s, 1m, 5m, 30m, 2h——耗盡入死信（admin 手動重發）。
var callbackIntervals = []int{30, 60, 300, 1800, 7200}

// EnqueueCallback 登記回調任務（交付完成後調用；有 Redis 入 default 隊列，否則進程內）。
func (s *SupplyAPIService) EnqueueCallback(ctx context.Context, supplyOrderID uint64) {
	payload, _ := json.Marshal(map[string]uint64{"supply_order_id": supplyOrderID})
	if s.enq == nil || !s.enq.Enabled() {
		go func() {
			ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			_ = s.DeliverCallback(ctx2, supplyOrderID)
		}()
		return
	}
	_ = s.enq.Enqueue(ctx, queue.Task{
		Type:      CallbackTaskType,
		Payload:   payload,
		Queue:     queue.QueueDefault,
		DedupeKey: CallbackTaskType + ":" + strconv.FormatUint(supplyOrderID, 10),
	})
}

// RunCallbackTask 隊列任務入口（payload {"supply_order_id": N}）。
func (s *SupplyAPIService) RunCallbackTask(ctx context.Context, payload []byte) error {
	var req struct {
		SupplyOrderID uint64 `json:"supply_order_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("supplier.callback: 解析載荷失敗: %w", err)
	}
	if req.SupplyOrderID == 0 {
		return nil
	}
	return s.DeliverCallback(ctx, req.SupplyOrderID)
}

// DeliverCallback 執行一次回調轉發（成功 → success；失敗 → 退避/死信）。
func (s *SupplyAPIService) DeliverCallback(ctx context.Context, supplyOrderID uint64) error {
	cb, err := s.repo.GetCallbackByOrder(ctx, supplyOrderID)
	if err != nil {
		return err
	}
	if cb.CallbackStatus == downstreamcallback.CallbackStatusSuccess {
		return nil
	}
	o, err := s.repo.GetSupplyOrder(ctx, supplyOrderID)
	if err != nil {
		return err
	}
	if cb.AccountID != o.AccountID || cb.DownstreamOrderNo != o.DownstreamOrderNo {
		return errors.New("supplier.callback: 訂單與回調賬戶不匹配")
	}
	// CallbackUrlGuard：http/https 均放行（下游可按環境選擇）+ 私網拒絕
	// （SSRF 校驗不變；配置錯誤不入死信重試，提示重配）
	if !strings.HasPrefix(cb.CallbackURL, "https://") && !strings.HasPrefix(cb.CallbackURL, "http://") {
		_ = s.repo.MarkCallbackResult(ctx, cb.ID, false, "回調地址必須以 http:// 或 https:// 開頭")
		return nil
	}
	if err := httpx.ValidateURL(cb.CallbackURL); err != nil {
		_ = s.repo.MarkCallbackResult(ctx, cb.ID, false, "SSRF 攔截: "+err.Error())
		return nil
	}

	// 回調簽名按賬號協議分支（ B：dujiao 兼容賬號發 dujiao 事件格式 +
	// 3 頭簽名——簽名 path 固定為協議常量 /api/v1/upstream/callback，非實際 URL）
	apiKey, secret, protocol, err := s.repo.CredentialsWithProtocolOf(ctx, o.AccountID)
	if err != nil {
		_ = s.repo.MarkCallbackResult(ctx, cb.ID, false, err.Error())
		return nil // 密鑰問題不入死信（提示重置）
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	client := httpx.NewSafeClient(15 * time.Second)

	var body []byte
	var req *http.Request
	if protocol == "dujiao_next" {
		fulfillment := map[string]any{"type": "auto", "status": "delivered"}
		if cards, cerr := s.cardsPayloadOf(ctx, o); cerr == nil {
			fulfillment["payload"] = strings.Join(cards, "\n")
		}
		body, _ = json.Marshal(map[string]any{
			"event":               "order.fulfilled",
			"order_id":            strconv.FormatUint(o.ID, 10),
			"order_no":            o.DownstreamOrderNo,
			"downstream_order_no": strings.TrimPrefix(o.DownstreamOrderNo, ""),
			"status":              "delivered",
			"fulfillment":         fulfillment,
			"timestamp":           time.Now().Unix(),
		})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, cb.CallbackURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Dujiao-Next-Api-Key", apiKey)
		req.Header.Set("Dujiao-Next-Timestamp", ts)
		req.Header.Set("Dujiao-Next-Signature", dujiaoSign(secret, http.MethodPost, "/api/v1/upstream/callback", ts, body))
	} else {
		body, _ = json.Marshal(map[string]any{
			"supply_order_id":     o.ID,
			"downstream_order_no": o.DownstreamOrderNo,
			"status":              string(o.Status),
			"amount":              o.Amount,
		})
		nonce := fmt.Sprintf("cb_%d_%d", time.Now().UnixNano(), o.ID)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, cb.CallbackURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Supply-Key", apiKey)
		req.Header.Set("X-Supply-Timestamp", ts)
		req.Header.Set("X-Supply-Nonce", nonce)
		req.Header.Set("X-Supply-Signature", supplySign(secret, http.MethodPost, req.URL.Path, req.URL.RawQuery, ts, nonce, body))
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = s.repo.MarkCallbackResult(ctx, cb.ID, false, err.Error())
		return s.scheduleCallbackRetry(ctx, cb)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = s.repo.MarkCallbackResult(ctx, cb.ID, true, "")
		return nil
	}
	_ = s.repo.MarkCallbackResult(ctx, cb.ID, false, fmt.Sprintf("下游返回 %d", resp.StatusCode))
	return s.scheduleCallbackRetry(ctx, cb)
}

// scheduleCallbackRetry 退避重試（間隔表耗盡 → 死信留檔，admin 可手動重發）。
func (s *SupplyAPIService) scheduleCallbackRetry(ctx context.Context, cb *ent.DownstreamCallback) error {
	if int(cb.RetryCount) >= len(callbackIntervals) {
		s.log.Warn("supplier.callback.dead_letter", "callback_id", cb.ID, "retry", cb.RetryCount)
		return nil
	}
	delay := time.Duration(callbackIntervals[cb.RetryCount]) * time.Second
	s.log.Info("supplier.callback.retry", "callback_id", cb.ID, "delay", delay.String())
	// 進程內延時重試（多實例場景由 worker 隊列重投；降級模式單進程足夠）
	go func() {
		time.Sleep(delay)
		ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = s.DeliverCallback(ctx2, cb.SupplyOrderID)
	}()
	return nil
}
