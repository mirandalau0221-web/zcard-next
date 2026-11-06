package procurement

// E：上游回調接收端點（採購三通道之回調通道的接收側）。
//
// POST /api/v1/procurement/callback?connection_id=N
// - 不掛 JWT（架構測試規則 9）；connection_id 經 query 定位連接 → 按驅動驗籤
// （zcard 四頭 / dujiao 三頭；acg 無回調）
// - 驗籤/查單失敗統一 401 防枚舉（1.x 紀律：不區分原因防探測）
// - 回填走 confirmResult（與輪詢/巡檢冪等匯聚——狀態機 CAS，先到先終態）
// - zcard 回調不攜帶卡密（確認後轉 PollOne 查單獲取）；dujiao 事件帶 payload

import (
	"context"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productdeliverysource"
	"io"
	"net/http"
	"strconv"
	"strings"

	khttp "github.com/go-kratos/kratos/v3/transport/http"

	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
)

// UpstreamCallbackPath 回調接收路徑（Gateway.Submit 登記給上游的是連接配置的
// callback_url——運營應填 <本站公網地址>/api/v1/procurement/callback）。
const UpstreamCallbackPath = "/api/v1/procurement/callback"

// RegisterUpstreamCallback 掛載回調接收路由（須在 SPA 兜底前註冊）。
func RegisterUpstreamCallback(srv *khttp.Server, svc *ProcureService, gw supplyport.UpstreamGateway) {
	srv.HandleFunc(UpstreamCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		connID, err := strconv.ParseUint(r.URL.Query().Get("connection_id"), 10, 64)
		if err != nil || connID == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{
			"X-Supply-Key":          r.Header.Get("X-Supply-Key"),
			"X-Supply-Timestamp":    r.Header.Get("X-Supply-Timestamp"),
			"X-Supply-Nonce":        r.Header.Get("X-Supply-Nonce"),
			"X-Supply-Signature":    r.Header.Get("X-Supply-Signature"),
			"Dujiao-Next-Api-Key":   r.Header.Get("Dujiao-Next-Api-Key"),
			"Dujiao-Next-Timestamp": r.Header.Get("Dujiao-Next-Timestamp"),
			"Dujiao-Next-Signature": r.Header.Get("Dujiao-Next-Signature"),
		}
		result, err := gw.VerifyUpstreamCallback(r.Context(), connID, &supplyport.UpstreamCallbackAuth{
			Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Headers: headers, Body: body,
		})
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized) // 防枚舉：不區分驗籤失敗原因
			return
		}
		if strings.HasPrefix(result.DownstreamOrderNo, "reuse:") {
			src, e := data.Client(r.Context(), svc.repo.data).ProductDeliverySource.Query().Where(productdeliverysource.PurchaseKey(result.DownstreamOrderNo)).Only(r.Context())
			if e != nil || src.ConnectionID != connID {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if result.Status == "delivered" && len(result.Cards) > 0 {
				e = svc.confirmReusable(r.Context(), src.ID, result.Cards, result.Amount, src.UpstreamOrderID)
			} else {
				e = svc.processReusable(r.Context(), src.ID)
			}
			if e != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		po, err := svc.repo.GetByDownstreamOrderNo(r.Context(), result.DownstreamOrderNo)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := svc.HandleUpstreamCallback(r.Context(), po.ID, result); err != nil {
			svc.log.Warn("procurement.callback_handle_failed", "po_id", po.ID, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

// HandleUpstreamCallback 回調結果回填：delivered 且帶卡密 → 直接終態；
// 其餘（確認型回調 / 無卡密）→ 轉查單通道獲取。
func (s *ProcureService) HandleUpstreamCallback(ctx context.Context, poID uint64, result *supplyport.UpstreamCallbackResult) error {
	if result.Status == "delivered" && len(result.Cards) > 0 {
		return s.confirmResult(ctx, poID, "delivered", result.Cards, result.Amount)
	}
	return s.PollOne(ctx, poID)
}
