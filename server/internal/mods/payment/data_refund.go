package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderstatusevent"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/affiliatecommission"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/payment"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	kerrors "github.com/go-kratos/kratos/v3/errors"
)

func refundInvalid(message string) error {
	return kerrors.BadRequest("payment.REFUND_INVALID", message)
}

// RefundToWallet commits the receipt, balance, order state and outbox together.
// expectedRefunded is the amount the administrator saw before confirming; stale
// or repeated submissions cannot issue a second credit.
type RefundFeeInput struct {
	Amount   int64
	Expected *int64
}

func (r *PaymentRepoImpl) RefundToWallet(ctx context.Context, orderID uint64, amount int64, expectedRefunded *int64, reason string, operatorID uint64, fees ...RefundFeeInput) (*ent.RefundOrder, error) {
	feeAmount := int64(0)
	var expectedFee *int64
	if len(fees) > 0 {
		feeAmount = fees[0].Amount
		expectedFee = fees[0].Expected
	}
	if !money.ValidCents(amount) || !money.ValidCents(feeAmount) || amount+feeAmount <= 0 || !money.ValidCents(amount+feeAmount) {
		return nil, refundInvalid("退款本金和手續費不能為負，合計必須大於 0 且不超限")
	}
	if expectedRefunded == nil {
		return nil, refundInvalid("請刷新訂單詳情後重新確認退款金額")
	}
	if len([]rune(reason)) > 180 {
		return nil, refundInvalid("退款原因不能超過 180 字")
	}
	var result *ent.RefundOrder
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		c := data.Client(ctx, r.data)
		o, err := c.Order.Get(ctx, orderID)
		if err != nil {
			return err
		}
		if err := data.GuardSMSRefund(ctx, c, o.ID); err != nil {
			return refundInvalid(err.Error())
		}
		if err := data.GuardSocialRefund(ctx, c, o.ID); err != nil {
			return refundInvalid(err.Error())
		}
		if o.CommerceVersion == 1 {
			return refundInvalid("該訂單必須按商品項退款")
		}
		if o.UserID == 0 {
			return refundInvalid("遊客訂單沒有會員餘額，不能退款到錢包，請聯繫買家核實退款方式")
		}
		receipts, err := c.RefundOrder.Query().Where(refundorder.OrderID(orderID)).All(ctx)
		if err != nil {
			return err
		}
		var refunded, refundedFee int64
		for _, rf := range receipts {
			if rf.Status == refundorder.StatusSucceeded {
				if rf.Amount < 0 || rf.Amount > o.TotalAmount-refunded {
					return refundInvalid("歷史退款金額異常，請核對退款記錄")
				}
				refunded += rf.Amount
				if rf.FeeAmount < 0 || refundedFee > money.MaxCents-rf.FeeAmount {
					return refundInvalid("歷史手續費退款金額異常")
				}
				refundedFee += rf.FeeAmount
			}
			if rf.Status == refundorder.StatusProcessing || (rf.Status == refundorder.StatusCreated && rf.Channel != refundorder.ChannelWallet) {
				return refundInvalid("存在待核對的其他渠道退款，請先核對，避免重複退款")
			}
		}
		limit, hasSocial, socialErr := data.SocialRetailRefundLimit(ctx, c, o.ID)
		if socialErr != nil {
			return refundInvalid(socialErr.Error())
		}
		if hasSocial && (refunded > limit || amount > limit-refunded) {
			return refundInvalid("退款超過已確認服務額度")
		}
		paid, payErr := c.Payment.Query().Where(payment.OrderID(o.ID), payment.StatusEQ(payment.StatusSuccess), payment.ReviewReasonEQ("")).Order(ent.Asc(payment.FieldID)).First(ctx)
		if payErr != nil && !ent.IsNotFound(payErr) {
			return payErr
		}
		totalFee := int64(0)
		if payErr == nil {
			totalFee = paid.Fee
		}
		if refundedFee > totalFee || feeAmount > totalFee-refundedFee {
			return refundInvalid("手續費退款超過剩餘可退金額")
		}
		if (feeAmount > 0 && expectedFee == nil) || (expectedFee != nil && *expectedFee != refundedFee) {
			return refundInvalid("手續費退款金額已變化，請刷新後確認")
		}
		if refunded != *expectedRefunded {
			return refundInvalid("退款金額已變化，請刷新訂單詳情核對，勿重複退款")
		}
		switch o.Status {
		case order.StatusRefunded:
			if amount != 0 {
				return refundInvalid("訂單本金已退清，只能退還剩餘手續費")
			}
		case order.StatusPaid, order.StatusFulfilling, order.StatusPartiallyDelivered, order.StatusDelivered, order.StatusCompleted:
		default:
			return refundInvalid("當前訂單狀態不支持退款")
		}
		if amount > o.TotalAmount-refunded {
			return refundInvalid("退款金額超過訂單剩餘可退金額")
		}
		full := amount > 0 && refunded+amount == o.TotalAmount
		if !full {
			pending, e := c.OrderItem.Query().Where(orderitem.OrderID(o.ID), orderitem.FulfillmentTypeEQ(orderitem.FulfillmentTypeManual), orderitem.FulfillmentStatusNotIn("delivered", "refunded")).Exist(ctx)
			if e != nil {
				return e
			}
			if pending {
				return refundInvalid("人工服務未完成，暫不支持無法歸屬到商品項的部分退款")
			}
		}
		// Existing commission reversal consumers are order-level, so such orders must
		// be refunded in full until per-refund commission allocation is available.
		if !full && amount > 0 {
			hasCommission, err := c.AffiliateCommission.Query().Where(affiliatecommission.OrderID(orderID)).Exist(ctx)
			if err != nil {
				return err
			}
			if hasCommission || o.SubsiteProfit > 0 {
				return refundInvalid("該訂單有關聯佣金或分站利潤，請使用全額退款")
			}
		}
		if r.wallet == nil || r.outbox == nil {
			return kerrors.ServiceUnavailable("payment.REFUND_UNAVAILABLE", "退款服務暫不可用")
		}
		// Version CAS serializes refunds with other order changes before any credit.
		update := c.Order.Update().Where(order.ID(o.ID), order.Version(o.Version), order.StatusEQ(o.Status)).AddVersion(1)
		next := o.Status
		if full {
			next = order.StatusRefunded
			update.SetStatus(next)
		}
		n, err := update.Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return refundInvalid("訂單狀態已變化，請刷新後核對退款結果")
		}
		result, err = c.RefundOrder.Create().SetOrderID(orderID).SetAmount(amount).SetFeeAmount(feeAmount).SetChannel(refundorder.ChannelWallet).SetStatus(refundorder.StatusSucceeded).SetOperatorID(operatorID).SetReason(reason).Save(ctx)
		if err != nil {
			return err
		}
		reference := fmt.Sprintf("order_refund:%d", result.ID)
		if err = r.wallet.CreditInTx(ctx, walletport.Entry{UserID: o.UserID, Direction: "in", Type: "refund", Amount: money.Cents(amount + feeAmount), Reference: reference, OrderID: o.ID, Remark: "訂單 " + o.OrderNo + " 退款"}); err != nil {
			return err
		}
		// Old wallet rows were intents only; supersede them after an actual credit.
		if _, err = c.RefundOrder.Update().Where(refundorder.OrderID(orderID), refundorder.ChannelEQ(refundorder.ChannelWallet), refundorder.StatusEQ(refundorder.StatusCreated)).SetStatus(refundorder.StatusFailed).SetReason("舊退款申請未執行，已由新的餘額退款記錄替代").Save(ctx); err != nil {
			return err
		}
		event := "refund_partial"
		if amount == 0 {
			event = "refund_fee"
		}
		if full {
			event = "refunded"
		}
		audit := fmt.Sprintf("退至會員餘額：本金 %d 分，手續費 %d 分；累計本金 %d 分，手續費 %d 分", amount, feeAmount, refunded+amount, refundedFee+feeAmount)
		if strings.TrimSpace(reason) != "" {
			audit += "；" + reason
		}
		if _, err = c.OrderStatusEvent.Create().SetOrderID(orderID).SetFromStatus(string(o.Status)).SetToStatus(string(next)).SetEvent(event).SetOperator(func() orderstatusevent.Operator {
			if operatorID == 0 {
				return "system"
			}
			return "admin"
		}()).SetOperatorID(operatorID).SetReason(audit).Save(ctx); err != nil {
			return err
		}
		if full {
			if _, err = c.OrderItem.Update().Where(orderitem.OrderID(orderID)).SetFulfillmentStatus("refunded").Save(ctx); err != nil {
				return err
			}
			if _, err = c.Card.Update().Where(card.OrderID(orderID), card.StatusEQ(card.StatusReserved)).SetStatus(card.StatusAvailable).ClearOrderID().ClearLockedAt().Save(ctx); err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"order_id": orderID, "order_no": o.OrderNo, "refund_id": result.ID, "refund_ratio": 10000, "amount": amount, "user_id": o.UserID})
			if err = r.outbox.Write(ctx, "order", events.OrderRefunded, o.OrderNo, "order:"+o.OrderNo+":refunded", raw); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
