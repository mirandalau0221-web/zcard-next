package reseller

// 分賬引擎（ 事件接線）：
// 訂閲 order.paid → 按訂單快照（subsite_profit/profit_eligible）入賬
// → SettleOrderProfit（冪等鍵 order_profit:<orderID>）→ 凍結 confirm_days 後可用。
// 防自購快照不產生利潤：profit_eligible=false 直接 ACK 不入賬。

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// paidPayload order.paid 載荷（與 order 模塊發佈結構對齊；分站快照隨事件攜帶免回查）。
type settlePaidPayload struct {
	OrderID        uint64 `json:"order_id"`
	SubsiteID      uint64 `json:"subsite_id"`
	SubsiteProfit  int64  `json:"subsite_profit"`  // 分站利潤快照（分；subsite_markup 合計）
	ProfitEligible bool   `json:"profit_eligible"` // 防自購快照（false = 不產生利潤）
}

// SettleService 分站分賬服務（order.paid 消費方；通道 C 異步、冪等）。
type SettleService struct {
	repo *ResellerRepo
	log  *slog.Logger
}

// NewSettleService 構造。
func NewSettleService(repo *ResellerRepo, logger *slog.Logger) *SettleService {
	return &SettleService{repo: repo, log: logger}
}

// OnOrderRefunded 訂閲 order.refunded：按訂單快照扣回分站利潤
// （refund_deduct 負行；餘額不足 → 負債態，後續利潤優先抵扣）。
func (s *SettleService) OnOrderRefunded(ctx context.Context, env events.Envelope) error {
	var p struct {
		OrderID     uint64 `json:"order_id"`
		RefundRatio int64  `json:"refund_ratio"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == 0 {
		return nil // 載荷不合法：ACK 不重試
	}
	// 訂單快照（subsite_profit 為利潤基數；主站單/無利潤單無動作）
	o, err := data.Client(ctx, s.repo.data).Order.Get(ctx, p.OrderID)
	if err != nil {
		return err
	}
	if o.CommerceVersion == 1 {
		return s.reconcilePhysical(ctx, o.ID)
	}
	if o.SubsiteID == 0 || o.SubsiteProfit <= 0 {
		return nil
	}
	if err := s.repo.RefundDeduct(ctx, o.SubsiteID, o.ID, o.SubsiteProfit, p.RefundRatio); err != nil {
		return err
	}
	return nil
}

// OnOrderPaid 訂閲 order.paid：按訂單快照分站利潤入賬（冪等 ACK）。
func (s *SettleService) onOrderPaid(ctx context.Context, env events.Envelope) error {
	var p settlePaidPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil // 載荷不合法：ACK 不重試（order 側契約破壞屬異常路徑）
	}
	if p.SubsiteID == 0 || !p.ProfitEligible || p.SubsiteProfit <= 0 {
		return nil // 主站單 / 自購快照 / 無利潤：不產生分賬
	}
	if err := s.repo.SettleOrderProfit(ctx, SettleInput{
		SubsiteID: p.SubsiteID,
		OrderID:   p.OrderID,
		Amount:    money.Cents(p.SubsiteProfit),
	}); err != nil {
		return err
	}

	return nil
}
