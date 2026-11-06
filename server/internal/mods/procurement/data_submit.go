package procurement

// /：採購提交與到手即加密。
//
// 流程（訂閲 order.paid → 逐 upstream 項）：
// 1. 冪等建單（order_item_id 唯一）pending
// 2. fail-open 庫存校驗（stock_mode=real 且緩存不足時實時查；失敗放行）
// 3. Gateway.Submit（冪等鍵 = 採購單 dedupe_key，隨請求發送）
// - delivered → 卡密到手即加密（CardCipher.Seal）→ 落 procurement_items
// → 交付出口與 MarkFulfilled 同事務提交 → 發佈 fulfilled
// - pending → MarkSubmitted（記 upstream_order_id + 退避調度）
// - 永久錯誤 → MarkRejected → 失敗策略分流（auto_refund / manual）
// - 可重試 → BumpRetry（退避後由輪詢/巡檢再試）
//
// 鐵律 11（採購側）：上游卡密內存態 → 立即 Seal → 密文落庫；全程零明文落盤零日誌。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"log/slog"
	"time"

	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	fulfillmentport "github.com/NovaWorks/zcard-next/server/internal/mods/fulfillment/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	paymentport "github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"
)

// orderPaidPayload order.paid 事件載荷（與 order 模塊發佈結構對齊）。
type orderPaidPayload struct {
	OrderNo   string `json:"order_no"`
	OrderID   uint64 `json:"order_id"`
	SubsiteID uint64 `json:"subsite_id"`
	Items     []struct {
		OrderItemID     uint64 `json:"order_item_id"`
		ProductID       uint64 `json:"product_id"`
		SkuID           uint64 `json:"sku_id"`
		Quantity        int32  `json:"quantity"`
		FulfillmentType string `json:"fulfillment_type"`
	} `json:"items"`
}

// ProcureService 採購服務（ 提交 / 三通道 / 失敗策略）。
type ProcureService struct {
	repo   *ProcureRepo
	gw     supplyport.UpstreamGateway
	reader catalogport.ProductReader
	cipher *inventory.CardCipher
	attach fulfillmentport.AttachUpstreamDelivery
	refund paymentport.OrderRefunder
	outbox events.Writer
	enq    queue.Enqueuer
	log    *slog.Logger
}

// NewProcureService 構造。
func NewProcureService(
	repo *ProcureRepo,
	gw supplyport.UpstreamGateway,
	reader catalogport.ProductReader,
	cipher *inventory.CardCipher,
	attach fulfillmentport.AttachUpstreamDelivery,
	refund paymentport.OrderRefunder,
	outbox events.Writer,
	enq queue.Enqueuer,
	log *slog.Logger,
) *ProcureService {
	return &ProcureService{repo: repo, gw: gw, reader: reader, cipher: cipher, attach: attach, refund: refund, outbox: outbox, enq: enq, log: log}
}

// OnOrderPaid 訂閲 order.paid（Dispatcher 註冊；冪等由 processed_events 兜底）。
func (s *ProcureService) OnOrderPaid(ctx context.Context, env events.Envelope) error {
	var payload orderPaidPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return fmt.Errorf("procurement: 解析 order.paid 載荷失敗: %w", err)
	}
	for _, it := range payload.Items {
		if it.FulfillmentType == "reuse" {
			// Shared sources are also recovered by the durable cron worker.
			if row, e := data.Client(ctx, s.repo.data).OrderItem.Get(ctx, it.OrderItemID); e == nil {
				_ = s.processReusable(ctx, row.DeliverySourceID)
			}
			continue
		}
		if it.FulfillmentType != "upstream" {
			continue // 本地卡密項由 fulfillment 模塊履約
		}
		if err := s.processItem(ctx, payload, it.OrderItemID, it.ProductID, it.SkuID, it.Quantity); err != nil {
			if errors.Is(err, ErrOrderNotPurchasable) {
				continue
			}
			// 單條失敗不阻斷其餘（錯誤留痕，重試由輪詢/人工兜底）
			s.log.Error("procurement.process_item_failed",
				"order_no", payload.OrderNo, "order_item_id", it.OrderItemID, "err", err)
			if e := s.repo.recordMissing(ctx, it.OrderItemID); e != nil {
				s.log.Error("procurement.record_missing_failed", "order_item_id", it.OrderItemID, "err", e)
			}
		}
	}
	return nil
}

// processItem 單上游項採購編排（冪等：已存在採購單直接跳過）。
func (s *ProcureService) processItem(ctx context.Context, payload orderPaidPayload, orderItemID, productID, skuID uint64, quantity int32) error {
	it, e := data.Client(ctx, s.repo.data).OrderItem.Get(ctx, orderItemID)
	if e != nil {
		return e
	}
	if it.DeliveryKind != "card" {
		return fmt.Errorf("接碼訂單不能使用卡密採購")
	}
	// 冪等：該訂單項已建採購單（重複投遞 / 手動重試）
	if _, err := s.repo.GetByOrderItem(ctx, orderItemID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	// 商品上游映射（upstream_source_id + product_code 判定）
	p, err := s.reader.Get(ctx, payload.SubsiteID, productID)
	if err != nil {
		return fmt.Errorf("procurement: 商品不可讀: %w", err)
	}
	if p.UpstreamSourceID == 0 || p.UpstreamProductCode == "" {
		s.log.Warn("procurement.skip_non_upstream", "order_item_id", orderItemID, "product_id", productID)
		return fmt.Errorf("procurement: 上游商品映射缺失，需要人工核實")
	}

	// 建單並持久化隨機請求號；失敗策略取渠道級配置，默認自動退款。
	failStrategy := "auto_refund"
	if s.gw != nil {
		if fs := s.gw.FailStrategyOf(ctx, p.UpstreamSourceID); fs != "" {
			failStrategy = fs
		}
	}
	po, err := s.repo.claimPurchase(ctx, orderItemID, p.UpstreamSourceID, p.UpstreamProductCode, quantity, failStrategy, payload.OrderNo)
	if err != nil {
		if errors.Is(err, ErrDuplicatePurchase) {
			return nil // 併發已建
		}
		return err
	}

	traceID := fmt.Sprintf("po-%d-%s", po.ID, payload.OrderNo)
	_ = s.repo.EnsureTraceID(ctx, po.ID, traceID)

	// 規格選擇：客户所選本地 SKU 的上游標識（acg=race|k=v 編碼 / dujiao=sku_id）
	upstreamSKU := ""
	if skuID > 0 {
		upstreamSKU = s.reader.SkuUpstreamCode(ctx, payload.SubsiteID, skuID)
	}

	// 提交
	res, err := s.gw.Submit(ctx, supplyport.PurchaseRequest{
		ConnectionID:      p.UpstreamSourceID,
		ProductCode:       p.UpstreamProductCode,
		UpstreamSKU:       upstreamSKU,
		Quantity:          int(quantity),
		DownstreamOrderNo: po.DedupeKey,
		TraceID:           traceID,
	})
	// Once sent, preserve the outcome even if the caller timed out. This context
	// only covers bounded local persistence/delivery, never another purchase.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err != nil {
		return s.handleSubmitError(ctx, po.ID, err)
	}
	// Save the supplier order separately, BEFORE sealing/storing the receipt.
	// If local receipt persistence fails, recovery can query the original order.
	if err := s.repo.recordUpstreamOrder(ctx, po.ID, res.UpstreamOrderID); err != nil {
		return err
	}

	switch res.Status {
	case "delivered":
		return s.finalizeDelivered(ctx, po.ID, payload.OrderID, orderItemID, productID, payload.SubsiteID, res.Cards, res.Amount)
	case "pending", "submitted", "paid", "accepted", "processing":
		// dujiao 等上游：錢包受理即 paid（異步交付）——輪詢/回調推進
		// 受理：記 upstream_order_id + 退避（默認間隔表首檔 30s）
		next := time.Now().UTC().Add(30 * time.Second)
		if err := s.repo.MarkSubmitted(ctx, po.ID, res.UpstreamOrderID, next); err != nil {
			return err
		}
		s.log.Info("procurement.submitted", "id", po.ID, "upstream_order_id", res.UpstreamOrderID)
		return s.schedulePoll(ctx, po.ID, 30*time.Second)
	default:
		return fmt.Errorf("procurement: 未知上游狀態 %q", res.Status)
	}
}

// finalizeDelivered 同步拿貨成功：到手即加密 → 落庫 → 交付出口 → 事件。
func (s *ProcureService) finalizeDelivered(ctx context.Context, poID, orderID, orderItemID, productID, subsiteID uint64, cards []string, amount int64) error {
	// Persist the encrypted receipt before attempting local delivery. Never buy again to recover it.
	sealed := make([][]byte, 0, len(cards))
	for _, plain := range cards {
		ct, err := s.cipher.Seal(plain, productID, subsiteID)
		if err != nil {
			return fmt.Errorf("procurement: 卡密加密失敗: %w", err)
		}
		sealed = append(sealed, ct)
	}
	if err := s.repo.saveReceipt(ctx, poID, sealed); err != nil {
		return err
	}
	err := s.deliverReceipt(ctx, poID, amount)
	if err != nil {
		s.log.Error("procurement.attach_delivery_failed", "po_id", poID, "err", err)
		// A persisted polling receipt is also recovered by patrol if enqueueing fails.
		_ = s.repo.recordDeliveryFailure(ctx, poID)
		_ = s.schedulePoll(ctx, poID, time.Minute)
	}
	return err
}

// handleSubmitError 提交失敗分流：永久錯誤 → rejected → 失敗策略；其餘 → 退避重試。
func (s *ProcureService) handleSubmitError(ctx context.Context, poID uint64, err error) error {
	// 防重鍵衝突（acg request_no 重複即報錯）：上游可能已受理首請求（響應丟失
	// 場景），重試永遠撞牆、自動退款可能造成上游已成交卻退客户款 → 立即轉人工核對
	if errors.Is(err, supplyport.ErrUpstreamDuplicate) {
		reason := "採購請求編號被上游判定重複，無法確認本單是否成交；請核實原上游訂單後處理，系統不會重新採購"
		if merr := s.repo.MarkManual(ctx, poID, reason); merr != nil {
			return merr
		}
		s.log.Warn("procurement.duplicate_submit_manual", "id", poID)
		s.publish(ctx, events.ProcurementFailed, poID, map[string]any{
			"procurement_id": poID, "reason": reason, "strategy": "manual_duplicate_submit",
		})
		return nil
	}
	if errors.Is(err, supplyport.ErrUpstreamBalance) {
		if e := s.repo.MarkManual(ctx, poID, "上游賬户餘額不足，等待管理員補卡或核實後退款"); e != nil {
			return e
		}
		s.publish(ctx, events.ProcurementFailed, poID, map[string]any{"procurement_id": poID, "reason": "上游賬户餘額不足", "strategy": "manual_balance"})
		return nil
	}
	permanent := errors.Is(err, supplyport.ErrUpstreamDeleted) ||
		errors.Is(err, supplyport.ErrUpstreamUnavailable) ||
		errors.Is(err, supplyport.ErrUpstreamBalance) ||
		// 上游明確無庫存：永久拒絕（否則空 upstream_order_id 進輪詢死循環到 24h
		// 才轉人工；即刻走失敗策略讓付款顧客馬上退款，符合 fail-open 補償語義）
		errors.Is(err, supplyport.ErrUpstreamNoStock)
	if permanent {
		reason := err.Error()
		if merr := s.repo.MarkRejected(ctx, poID, reason); merr != nil {
			return merr
		}
		s.log.Warn("procurement.rejected", "id", poID, "reason", reason)
		return s.applyFailStrategy(ctx, poID, reason)
	}
	// 可重試（網絡/上游抖動）：退避後移交輪詢
	s.log.Warn("procurement.submit_retryable", "id", poID, "err", err)
	if err := s.repo.BumpRetry(ctx, poID, time.Now().UTC().Add(30*time.Second), err.Error()); err != nil {
		return err
	}
	return s.schedulePoll(ctx, poID, 30*time.Second)
}

// applyFailStrategy 失敗策略：auto_refund → 退款編排；manual → 人工終態 + 事件。
func (s *ProcureService) applyFailStrategy(ctx context.Context, poID uint64, reason string) error {
	po, err := s.repo.Get(ctx, poID)
	if err != nil {
		return err
	}
	if string(po.FailStrategy) == "manual" {
		if err := s.repo.MarkManual(ctx, poID, reason); err != nil {
			return err
		}
		s.publish(ctx, events.ProcurementFailed, poID, map[string]any{
			"procurement_id": poID, "reason": reason, "strategy": "manual",
		})
		return nil
	}
	// auto_refund：創建本地退款單（channel=upstream）→ 驅動訂單退款流轉
	if err := s.repo.MarkRefunding(ctx, poID); err != nil {
		return err
	}
	if s.refund != nil {
		// 金額 0 = 按訂單實付全額退款（payment 內部核算）
		orderID, oerr := s.repo.OrderIDOf(ctx, poID)
		if oerr != nil {
			s.log.Error("procurement.order_resolve_failed", "po_id", poID, "err", oerr)
		} else if err := s.refund.RefundOrder(ctx, orderID, 0, "上游採購失敗自動退款: "+reason); err != nil {
			s.log.Error("procurement.auto_refund_failed", "po_id", poID, "order_id", orderID, "err", err)
			_ = s.repo.MarkManual(ctx, poID, "自動退款失敗轉人工: "+err.Error())
			s.publish(ctx, events.ProcurementFailed, poID, map[string]any{
				"procurement_id": poID, "reason": reason, "strategy": "auto_refund_failed_manual",
			})
			return nil
		}
	}
	s.publish(ctx, events.ProcurementFailed, poID, map[string]any{
		"procurement_id": poID, "reason": reason, "strategy": "auto_refund",
	})
	return nil
}

// schedulePoll 退避調度：入隊輪詢任務（critical 隊列；降級模式進程內執行）。
func (s *ProcureService) schedulePoll(ctx context.Context, poID uint64, delay time.Duration) error {
	payload, _ := json.Marshal(map[string]uint64{"procurement_id": poID})
	if s.enq == nil || !s.enq.Enabled() {
		// 降級：直接異步輪詢
		go func() {
			time.Sleep(delay)
			runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			if err := s.PollOne(runCtx, poID); err != nil {
				s.log.Warn("procurement.poll_failed", "po_id", poID, "err", err)
			}
		}()
		return nil
	}
	return s.enq.Enqueue(ctx, queue.Task{
		Type:      PollTaskType,
		Payload:   payload,
		Queue:     queue.QueueCritical,
		DedupeKey: PollTaskType + ":" + fmt.Sprint(poID),
	})
}

// publish 發佈採購事件（procurement.fulfilled / procurement.failed）。
func (s *ProcureService) publish(ctx context.Context, typ string, poID uint64, payload map[string]any) {
	if s.outbox == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	agg := fmt.Sprintf("proc:%d", poID)
	_ = s.outbox.Write(ctx, "procurement", typ, agg, agg, raw)
}
