package fulfillment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderdelivery"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
)

// ManualDeliver supplies one outstanding item. Legacy single-item callers may omit itemID.
func (r *DeliveryRepoImpl) ManualDeliver(ctx context.Context, orderNo, content, logisticsNo, remark string, adminID uint64, itemID ...uint64) error {
	content, logisticsNo = strings.TrimSpace(content), strings.TrimSpace(logisticsNo)
	if (content == "") == (logisticsNo == "") {
		return fmt.Errorf("卡密內容與物流單號必須且只能填寫一項")
	}
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		client := data.Client(ctx, r.data)
		o, err := client.Order.Query().Where(order.OrderNo(orderNo)).Only(ctx)
		if err != nil {
			return err
		}
		if err := r.lockDeliveryOrder(ctx, o); err != nil {
			return err
		}
		processing, err := client.RefundOrder.Query().Where(refundorder.OrderID(o.ID), refundorder.StatusEQ(refundorder.StatusProcessing)).Exist(ctx)
		if err != nil {
			return err
		}
		if processing {
			return fmt.Errorf("退款處理中，請先核實退款結果")
		}
		if has, e := data.HasSMSOrder(ctx, client, o.ID); e != nil {
			return e
		} else if has {
			return fmt.Errorf("接碼訂單須等待供貨會話確認，不能人工補發卡密")
		}
		its, err := client.OrderItem.Query().Where(orderitem.OrderID(o.ID)).Order(ent.Asc(orderitem.FieldID)).All(ctx)
		if err != nil {
			return err
		}
		var target *ent.OrderItem
		var selected uint64
		if len(itemID) > 0 {
			selected = itemID[0]
		}
		if selected == 0 && len(its) == 1 {
			target = its[0]
		} else {
			for _, it := range its {
				if it.ID == selected {
					target = it
					break
				}
			}
		}
		if target != nil && target.GoodsType == "physical" {
			return fmt.Errorf("實體商品請使用快遞發貨操作")
		}
		if target == nil {
			return fmt.Errorf("請選擇本訂單中需要補發的商品")
		}
		if target.CanceledQuantity > 0 || target.FulfillmentStatus == "refunded" {
			return fmt.Errorf("該商品已取消或退款，不能補發")
		}

		if target.FulfillmentType == orderitem.FulfillmentTypeManual && target.AssignedAdminID != 0 && target.AssignedAdminID != adminID {
			return fmt.Errorf("該商品已由其他管理員領取")
		}

		counts, err := r.deliveredQuantities(ctx, o.ID, its)
		if err != nil {
			return err
		}
		remaining := int(target.Quantity) - counts[target.ID]
		if remaining <= 0 {
			return fmt.Errorf("該商品已發貨，請刷新訂單")
		}
		var lines []string
		for _, line := range strings.Split(content, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
		}
		if content != "" && len(lines) != remaining {
			return fmt.Errorf("該商品待補發 %d 條，請每行填寫一條卡密", remaining)
		}

		po, err := client.ProcurementOrder.Query().Where(procurementorder.OrderItemID(target.ID)).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return err
		}
		if po != nil {
			switch po.Status {
			case procurementorder.StatusManual, procurementorder.StatusRejected, procurementorder.StatusRefunding:
			default:
				return fmt.Errorf("上游採購仍在進行或已交付，請先在採購單核實並轉人工，避免重複發貨")
			}
			// Also supports old refunding receipts whose executor never ran.
			n, err := client.ProcurementOrder.Update().Where(procurementorder.ID(po.ID), procurementorder.StatusEQ(po.Status)).SetStatus(procurementorder.StatusFulfilled).SetLastError("管理員已人工補發，原採購異常已處理").Save(ctx)
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("採購單已變化，請刷新後重試")
			}
		}
		now := time.Now().UTC()
		createDelivery := func(cardID uint64, logistics map[string]any) error {
			b := client.OrderDelivery.Create().SetOrderID(o.ID).SetItemID(target.ID).SetCardID(cardID).SetDeliveryTokenHash(hashToken(randomToken())).SetDeliveredMode(orderdelivery.DeliveredModeStatus).SetDeliveredBy(adminID).SetFetchCount(0).SetDeliveredAt(now)
			if logistics != nil {
				b.SetLogistics(logistics)
			}
			return b.Exec(ctx)
		}
		for _, line := range lines {
			sealed, err := r.cipher.Seal(line, target.ProductID, o.SubsiteID)
			if err != nil {
				return err
			}
			c, err := client.Card.Create().SetProductID(target.ProductID).SetSkuID(target.SkuID).SetSubsiteID(o.SubsiteID).SetContent(sealed).SetContentHash(r.cipher.ContentHash(line)).SetStatus(card.StatusUsed).SetOrderID(o.ID).SetUsedAt(now).Save(ctx)
			if err != nil {
				return err
			}
			if err := createDelivery(c.ID, nil); err != nil {
				return err
			}
		}
		if logisticsNo != "" {
			if err := createDelivery(0, map[string]any{"tracking_no": logisticsNo, "remark": remark}); err != nil {
				return err
			}
		}
		// Release local reserved cards replaced by manual delivery, not unrelated items.
		skuFilter := card.SkuID(target.SkuID)
		if target.SkuID == 0 {
			skuFilter = card.Or(card.SkuID(0), card.SkuIDIsNil())
		}
		if _, err := client.Card.Update().Where(card.OrderID(o.ID), card.ProductID(target.ProductID), skuFilter, card.StatusEQ(card.StatusReserved)).SetStatus(card.StatusAvailable).ClearOrderID().ClearLockedAt().Save(ctx); err != nil {
			return err
		}
		if _, err := client.RefundOrder.Update().Where(refundorder.OrderID(o.ID), refundorder.StatusEQ(refundorder.StatusCreated), refundorder.ChannelEQ(refundorder.ChannelUpstream)).SetStatus(refundorder.StatusFailed).SetReason("舊自動退款未執行，管理員已選擇人工補發").Save(ctx); err != nil {
			return err
		}
		return r.updateDeliveryProgress(ctx, o, "admin", adminID, remark)
	})
}
