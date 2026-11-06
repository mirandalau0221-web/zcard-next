package order

// OrderLifecycle 適配器（§4.6 破環點：payment 回調事務內經窄接口推進訂單，
// 同事務內調 OrderUsecase——狀態機 CAS + 狀態事件 + outbox(order.paid) 一次性完成）。
// bootstrap 裝配：payment 消費本端口，wire 經 ProvideOrderLifecycle 注入。

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/mods/order/port"
)

// lifecycleAdapter OrderUsecase → port.OrderLifecycle。
type lifecycleAdapter struct {
	uc *OrderUsecase
}

// MarkPaid 支付成功推進（冪等：已 paid 直接成功；發佈 order.paid 事件）。
func (a lifecycleAdapter) MarkPaid(ctx context.Context, fact port.PaidFact) error {
	return a.uc.MarkPaid(ctx, fact.OrderNo)
}

// Cancel 取消訂單（狀態機校驗 + 釋放鎖卡 + 券返還）。
func (a lifecycleAdapter) Cancel(ctx context.Context, orderNo, reason string, op port.Operator) error {
	return a.uc.CancelOrder(ctx, orderNo, reason, op.Type, op.ID)
}

// ReturnCoupons joins the refund transaction after every undelivered item is canceled.
func (a lifecycleAdapter) ReturnCoupons(ctx context.Context, orderID uint64) error {
	if a.uc.Coupon == nil {
		return nil
	}
	return a.uc.Coupon.ReturnByOrder(ctx, orderID)
}

// ProvideOrderLifecycle wire provider（返回端口接口，payment 模塊消費）。
func ProvideOrderLifecycle(uc *OrderUsecase) port.OrderLifecycle {
	return lifecycleAdapter{uc: uc}
}

func (a lifecycleAdapter) ValidateSMSPayment(ctx context.Context, orderID uint64) error {
	return a.uc.validateSMSPayment(ctx, orderID)
}

func (a lifecycleAdapter) ValidateSocialPayment(ctx context.Context, orderID uint64) error {
	return a.uc.validateSocialPayment(ctx, orderID)
}
