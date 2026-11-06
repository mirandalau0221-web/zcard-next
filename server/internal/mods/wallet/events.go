package wallet

// wallet 模塊事件聲明（附錄 C）。
//
// 發佈：
//   - recharge.succeeded  充值成功（贈送計算後）
//   - withdrawal.reviewed 提現審核完成（M3）
//
// 訂閲：
//   - payment.succeeded(type=recharge) → 充值入賬 + 贈送（冪等鍵 recharge:<payID>）
//   - order.refunded → 錢包退款逆向入賬（冪等鍵 order_refund:<id>:<seq>）

import "github.com/NovaWorks/zcard-next/server/internal/platform/events"

const (
	EventRechargeSucceeded  = events.RechargeSucceeded
	EventWithdrawalReviewed = events.WithdrawalReviewed
)
