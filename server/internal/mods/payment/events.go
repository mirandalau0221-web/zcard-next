package payment

// payment 模塊事件聲明（附錄 C）。
//
// 發佈：
//   - payment.succeeded  支付成功（回調事務提交後，訂閲者：fulfillment/procurement/
//                         affiliate/reseller/wallet/notify——異步展開，核心事務短小）
//   - payment.failed     支付失敗
//   - refund.requested   退款發起（工單/管理員）
//   - refund.succeeded   退款成功（逆向：佣金扣回/分站利潤扣回/庫存回補/通知買家）
//
// 訂閲：無（payment 是事件源頭；order.paid 由 order 模塊在 MarkPaid 時發佈）。

import (
	"fmt"

	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
)

const (
	EventSucceeded       = events.PaymentSucceeded
	EventFailed          = events.PaymentFailed
	EventRefundRequested = events.RefundRequested
	EventRefundSucceeded = events.RefundSucceeded
)

// DedupeKey outbox dedupe_key（重複回調只發一次 payment.succeeded）。
func DedupeKey(paymentID uint64) string { return fmt.Sprintf("payment:%d:succeeded", paymentID) }
