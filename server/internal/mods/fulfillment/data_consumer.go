package fulfillment

// 事件消費（ 接線）：order.paid → 自動交付。
// 與 procurement/affiliate/reseller/notify 同款消費形態——outbox relay 投遞，冪等由
// FulfillOrder（已交付直接返回）兜底。

import (
	"context"
	"encoding/json"

	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
)

// OnOrderPaid 訂閲 order.paid：解析訂單號 → FulfillOrder（自動交付）。
func (r *DeliveryRepoImpl) OnOrderPaid(ctx context.Context, env events.Envelope) error {
	var p struct {
		OrderNo string `json:"order_no"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderNo == "" {
		return nil // 載荷不合法：ACK 不重試（order 側契約破壞屬異常路徑）
	}
	return r.FulfillOrder(ctx, p.OrderNo)
}
