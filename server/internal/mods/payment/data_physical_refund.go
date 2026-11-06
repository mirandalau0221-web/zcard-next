package payment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/payment"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// Physical refunds allocate principal, shipping and canceled obligations separately.
// All checks, money, stock, progress and the durable event commit atomically.
func (r *PaymentRepoImpl) RefundPhysical(ctx context.Context, oid, actor uint64, req *adminv1.CreateRefundRequest) (*ent.RefundOrder, error) {
	var allocations []map[string]int64
	if json.Unmarshal([]byte(req.ItemAllocationsJson), &allocations) != nil || len(allocations) > 100 || req.ExpectedRefundedCents == nil || !money.ValidCents(req.AmountCents) || !money.ValidCents(req.FeeCents) || len([]rune(req.Reason)) > 180 {
		return nil, refundInvalid("請填寫有效的商品退款分攤並刷新訂單金額")
	}
	noMoney := req.AmountCents == 0 && req.FeeCents == 0
	if (noMoney && req.RequestKey == "") || (req.RequestKey != "" && (len(req.RequestKey) < 8 || len(req.RequestKey) > 100)) {
		return nil, refundInvalid("請刷新頁面後重試，取消數量必須攜帶有效請求標識")
	}
	var requestKey, requestHash string
	if req.RequestKey != "" {
		requestKey = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", oid, req.RequestKey))))
		sort.Slice(allocations, func(i, j int) bool { return allocations[i]["item_id"] < allocations[j]["item_id"] })
		raw, _ := json.Marshal([]any{allocations, req.AmountCents, req.FeeCents, req.Channel, req.Reason, req.ExternalConfirmed, req.ExternalReference, req.ExpectedRefundedCents, req.ExpectedRefundedFeeCents})
		requestHash = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	externalReference := strings.TrimSpace(req.ExternalReference)
	if noMoney {
		externalReference = ""
	}
	if req.Channel != "wallet" && !(req.Channel == "gateway" && (noMoney || req.ExternalConfirmed && strings.TrimSpace(req.ExternalReference) != "" && len(req.ExternalReference) <= 64)) {
		return nil, refundInvalid("請選擇退至餘額，或核實線下退款成功後填寫退款憑證")
	}
	var result *ent.RefundOrder
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		c := data.Client(ctx, r.data)
		o, e := c.Order.Get(ctx, oid)
		if e != nil {
			return e
		}
		if err := data.GuardSMSRefund(ctx, c, o.ID); err != nil {
			return refundInvalid(err.Error())
		}
		if o.CommerceVersion != 1 {
			return refundInvalid("訂單版本不支持商品退款")
		}
		if requestKey != "" {
			previous, err := c.RefundOrder.Query().Where(refundorder.RequestKey(requestKey)).Only(ctx)
			if err == nil {
				if previous.OrderID != oid || previous.RequestHash != requestHash {
					return refundInvalid("同一退款請求不能更改內容，請刷新核對後重新操作")
				}
				result = previous
				return nil
			}
			if !ent.IsNotFound(err) {
				return err
			}
		}
		if req.Channel == "wallet" && o.UserID == 0 && !noMoney {
			return refundInvalid("遊客訂單不能退至會員餘額")
		}
		switch o.Status {
		case order.StatusPaid, order.StatusFulfilling, order.StatusPartiallyDelivered, order.StatusDelivered, order.StatusCompleted, order.StatusRefunded:
		default:
			return refundInvalid("當前訂單狀態不支持退款")
		}
		receipts, e := c.RefundOrder.Query().Where(refundorder.OrderID(oid)).All(ctx)
		if e != nil {
			return e
		}
		var refunded, feeRefunded int64
		for _, rf := range receipts {
			if !noMoney && req.Channel == "gateway" && rf.Channel == refundorder.ChannelGateway && strings.TrimSpace(rf.UpstreamRefundID) == externalReference && rf.Status != refundorder.StatusFailed {
				return refundInvalid("該外部退款憑證已登記，請核對退款記錄")
			}
			if rf.Status == refundorder.StatusCreated || rf.Status == refundorder.StatusProcessing {
				return refundInvalid("存在待核對退款，請先核對")
			}
			if rf.Status == refundorder.StatusSucceeded {
				refunded += rf.Amount
				feeRefunded += rf.FeeAmount
			}
		}
		if refunded != req.GetExpectedRefundedCents() || refunded > o.TotalAmount || req.AmountCents > o.TotalAmount-refunded {
			return refundInvalid("可退金額已變化，請刷新後確認")
		}
		paid, e := c.Payment.Query().Where(payment.OrderID(oid), payment.StatusEQ(payment.StatusSuccess), payment.ReviewReasonEQ("")).Order(ent.Asc(payment.FieldID)).First(ctx)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		var fee int64
		if paid != nil {
			fee = paid.Fee
		}
		if req.FeeCents > fee-feeRefunded || (req.FeeCents > 0 && req.ExpectedRefundedFeeCents == nil) || (req.ExpectedRefundedFeeCents != nil && req.GetExpectedRefundedFeeCents() != feeRefunded) {
			return refundInvalid("支付手續費可退金額已變化")
		}
		// Order CAS uses the same lock as shipping and digital delivery.
		n, e := c.Order.Update().Where(order.ID(oid), order.Version(o.Version), order.StatusEQ(o.Status)).AddVersion(1).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return refundInvalid("訂單已變化，請刷新重試")
		}
		items, e := c.OrderItem.Query().Where(orderitem.OrderID(oid)).All(ctx)
		if e != nil {
			return e
		}
		byID := map[uint64]*ent.OrderItem{}
		for _, it := range items {
			byID[it.ID] = it
		}
		seen := map[uint64]bool{}
		var sum, shippingSum int64
		var canceled int64
		for _, a := range allocations {
			id := uint64(a["item_id"])
			it := byID[id]
			amount, ship, qty := a["amount_cents"], a["shipping_cents"], a["cancel_quantity"]
			if it == nil || seen[id] || !money.ValidCents(amount) || !money.ValidCents(ship) || qty < 0 || qty > int64(it.Quantity-it.CanceledQuantity) {
				return refundInvalid("商品退款分攤無效")
			}
			seen[id] = true
			if amount > it.PaidAmount-it.RefundedAmount || ship > it.ShippingAmount-it.RefundedShipping {
				return refundInvalid("商品或運費退款超過剩餘可退金額")
			}
			if (amount > 0 || qty > 0) && it.FulfillmentType == orderitem.FulfillmentTypeUpstream && it.FulfillmentStatus != "delivered" {
				busy, err := c.ProcurementOrder.Query().Where(procurementorder.OrderItemID(it.ID), procurementorder.StatusNotIn("rejected", "refunded")).Exist(ctx)
				if err != nil {
					return err
				}
				if busy {
					return refundInvalid("上游採購已提交或結果待核對，請先核實採購結果再退款")
				}
			}
			if qty > 0 {
				if it.GoodsType == "physical" {
					if qty > int64(it.Quantity-it.CanceledQuantity-it.ShippedQuantity) {
						return refundInvalid("已發貨數量不能取消，退貨需另行驗收入庫")
					}
				} else {
					if qty != int64(it.Quantity-it.CanceledQuantity) || it.FulfillmentStatus != "pending" {
						return refundInvalid("虛擬商品只能整項取消尚未交付且尚未採購的數量")
					}
				}
			}
			if sum > money.MaxCents-amount-ship {
				return refundInvalid("退款金額超限")
			}
			sum += amount + ship
			shippingSum += ship
			canceled += qty
		}
		if o.TotalAmount > 0 && refunded+sum == o.TotalAmount {
			for _, it := range items {
				var cancel int64
				for _, a := range allocations {
					if uint64(a["item_id"]) == it.ID {
						cancel = a["cancel_quantity"]
					}
				}
				remaining := it.Quantity - it.CanceledQuantity
				if it.GoodsType == "physical" {
					remaining -= it.ShippedQuantity
				} else if it.FulfillmentStatus == "delivered" || it.FulfillmentStatus == "refunded" {
					remaining = 0
				}
				if int64(remaining) > cancel {
					return refundInvalid("退清整單金額時，請同時取消所有尚未交付的商品數量")
				}
			}
		}
		if sum != req.AmountCents || (sum == 0 && req.FeeCents == 0 && canceled == 0) {
			return refundInvalid("退款金額與商品分攤不一致，或沒有退款/取消內容")
		}
		returnCoupon := refunded+sum == o.TotalAmount
		for _, it := range items {
			quantityCanceled := it.CanceledQuantity
			for _, a := range allocations {
				if uint64(a["item_id"]) == it.ID {
					quantityCanceled += int32(a["cancel_quantity"])
				}
			}
			if quantityCanceled != it.Quantity || it.ShippedQuantity > 0 || it.ReceivedQuantity > 0 || it.FulfillmentStatus == "delivered" || it.FulfillmentStatus == "shipped" || it.FulfillmentStatus == "received" {
				returnCoupon = false
			}
		}
		create := c.RefundOrder.Create().SetOrderID(oid).SetAmount(sum).SetShippingAmount(shippingSum).SetFeeAmount(req.FeeCents).SetItemAllocations(allocations).SetChannel(refundorder.Channel(req.Channel)).SetStatus(refundorder.StatusSucceeded).SetOperatorID(actor).SetReason(req.Reason).SetUpstreamRefundID(externalReference)
		if requestKey != "" {
			create.SetRequestKey(requestKey).SetRequestHash(requestHash)
		}
		result, e = create.Save(ctx)
		if e != nil {
			return e
		}
		if req.Channel == "wallet" && sum+req.FeeCents > 0 {
			if r.wallet == nil {
				return refundInvalid("餘額退款服務不可用")
			}
			if e = r.wallet.CreditInTx(ctx, walletport.Entry{UserID: o.UserID, Direction: "in", Type: "refund", Amount: money.Cents(sum + req.FeeCents), OrderID: oid, Reference: fmt.Sprintf("order_refund:%d", result.ID), Remark: "訂單 " + o.OrderNo + " 商品退款"}); e != nil {
				return e
			}
		}
		for _, a := range allocations {
			it := byID[uint64(a["item_id"])]
			qty := int32(a["cancel_quantity"])
			q := c.OrderItem.UpdateOneID(it.ID).AddRefundedAmount(a["amount_cents"]).AddRefundedShipping(a["shipping_cents"]).AddCanceledQuantity(qty)
			if it.CanceledQuantity+qty == it.Quantity {
				q.SetFulfillmentStatus("refunded")
			}
			if e = q.Exec(ctx); e != nil {
				return e
			}
			if qty > 0 && it.GoodsType == "physical" && it.InventoryTracked {
				if e = data.MovePhysicalStock(ctx, r.data, it.SubsiteID, it.ProductID, it.SkuID, oid, int64(qty), fmt.Sprintf("refund:%d:%d", result.ID, it.ID), "退款取消未發貨數量"); e != nil {
					return e
				}
			}
			if qty > 0 && it.GoodsType != "physical" {
				skuFilter := card.SkuID(it.SkuID)
				if it.SkuID == 0 {
					skuFilter = card.Or(card.SkuID(0), card.SkuIDIsNil())
				}
				if _, e = c.Card.Update().Where(card.OrderID(oid), card.ProductID(it.ProductID), skuFilter, card.StatusEQ(card.StatusReserved)).SetStatus(card.StatusAvailable).ClearOrderID().ClearLockedAt().Save(ctx); e != nil {
					return e
				}
			}
		}
		progressOrder := *o
		if o.TotalAmount > 0 && refunded+sum == o.TotalAmount {
			if e = c.Order.UpdateOneID(oid).SetStatus(order.StatusRefunded).Exec(ctx); e != nil {
				return e
			}
			progressOrder.Status = order.StatusRefunded
		}
		if e = data.RefreshPhysicalProgress(ctx, r.data, &progressOrder); e != nil {
			return e
		}
		if returnCoupon {
			if coupons, ok := r.lifecycle.(interface {
				ReturnCoupons(context.Context, uint64) error
			}); ok {
				if e = coupons.ReturnCoupons(ctx, oid); e != nil {
					return e
				}
			}
		}
		if e = data.PhysicalOrderEvent(ctx, r.data, o, "item_refund", func() string {
			if actor == 0 {
				return "system"
			}
			return "admin"
		}(), actor, fmt.Sprintf("退款憑證 %d；商品 %d 分；運費 %d 分；支付手續費 %d 分；%s", result.ID, sum-shippingSum, shippingSum, req.FeeCents, req.Reason)); e != nil {
			return e
		}
		if r.outbox == nil {
			return refundInvalid("退款事件服務不可用")
		}
		raw, _ := json.Marshal(map[string]any{"order_id": oid, "order_no": o.OrderNo, "refund_id": result.ID, "commerce_version": 1, "amount": sum, "shipping_amount": shippingSum, "user_id": o.UserID})
		return r.outbox.Write(ctx, "order", events.OrderRefunded, o.OrderNo, fmt.Sprintf("order:%s:refund:%d", o.OrderNo, result.ID), raw)
	})
	return result, err
}
