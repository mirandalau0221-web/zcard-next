package migratev1

// P4 交易域遷移：orders 五表合成（orders/order_items/order_amount_lines/
// order_status_events）→ payments（聚合支付取首單+raw 保真）→ recharge_orders →
// coupons → order_deliveries → cards.order_id 回填。
// 映射規格《數據遷移工具開發計劃》§5.5 與附錄 A；對外單號 order_no 原樣保留。
//
// 金額不變量：total_amount = SUM(order_amount_lines.amount)（base_price 正、
// coupon_discount 負、subsite_markup 正，按需合成）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/coupon"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderamountline"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderdelivery"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderstatusevent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/payment"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/rechargeorder"
)

// MigrateTrade P4 階段。
func (m *Migrator) MigrateTrade(ctx context.Context) error {
	steps := []func(context.Context) error{
		m.migrateOrders,
		m.migrateRecharges, // 先於 payments（payments.recharge_order_id 引用）
		m.migratePayments,
		m.migrateCoupons,
		m.migrateOrderDeliveries,
		m.backfillCardOrderIDs, // 最後：訂單落庫後回填 P3 置 0 的 cards.order_id
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

// mapOrderStatus 1.x (status, delivery_status) → 2.0 狀態（附錄 A）。
func mapOrderStatus(status, deliveryStatus string) string {
	switch status {
	case "paid":
		if deliveryStatus == "delivered" {
			return "completed"
		}
		return "paid"
	case "closed":
		return "canceled"
	case "refunded":
		return "refunded"
	default: // pending
		return "pending_payment"
	}
}

// migrateOrders 1.x orders（+order_items）→ 2.0 orders + items + amount_lines + status_events。
func (m *Migrator) migrateOrders(ctx context.Context) error {
	var (
		id                                     int64
		userID, quantity                       sql.NullInt64
		merchant, subsiteID                    sql.NullInt64
		productID                              sql.NullInt64
		orderNo                                string
		amount, discount, cost, subsiteProfit  int64
		couponCode                             sql.NullString
		baseCur, dispCur                       sql.NullString
		exchangeRate                           sql.NullFloat64
		amountDisplay                          sql.NullInt64
		status, deliveryStatus                 string
		fulfillmentSnap                        sql.NullString
		payChannel                             sql.NullString
		subsiteDomain                          sql.NullString
		source                                 sql.NullString
		upstreamOrderID                        sql.NullString
		skuName                                sql.NullString
		instructions, deliveryMsg              sql.NullString
		extra                                  sql.NullString
		contact                                sql.NullString
		createDevice                           sql.NullString
		createIP                               sql.NullString
		paidAt, closedAt, createdAt, updatedAt sql.NullString
	)
	return m.scanTable(ctx, "orders",
		[]string{"id", "order_no", "merchant_id", "user_id", "product_id", "quantity",
			"amount", "discount_amount", "cost", "coupon_code", "base_currency", "display_currency",
			"exchange_rate", "amount_display", "status", "delivery_status",
			"fulfillment_type_snapshot", "payment_channel", "subsite_id", "subsite_domain",
			"subsite_profit", "source", "upstream_order_id", "sku_name",
			"instructions_snapshot", "delivery_message_snapshot", "extra", "contact",
			"create_device", "create_ip", "paid_at", "closed_at", "created_at", "updated_at"},
		func() []any {
			return []any{&id, &orderNo, &merchant, &userID, &productID, &quantity,
				&amount, &discount, &cost, &couponCode, &baseCur, &dispCur,
				&exchangeRate, &amountDisplay, &status, &deliveryStatus,
				&fulfillmentSnap, &payChannel, &subsiteID, &subsiteDomain,
				&subsiteProfit, &source, &upstreamOrderID, &skuName,
				&instructions, &deliveryMsg, &extra, &contact,
				&createDevice, &createIP, &paidAt, &closedAt, &createdAt, &updatedAt}
		},
		func(int64) error {
			siteID, siteOK := m.subsiteFor(ctx, nullInt(merchant))
			if !siteOK {
				m.st.Record("orders", "skip") // 分站訂單：P6 補
				return nil
			}
			if _, ok := m.IDs.Get(ctx, "orders", uint64(id)); ok {
				m.st.Record("orders", "skip")
				return nil
			}
			ca, caOK, err := mustTime(nullStr(createdAt), m.TZ)
			if err != nil {
				return err
			}
			ua, _, err := mustTime(nullStr(updatedAt), m.TZ)
			if err != nil {
				return err
			}
			st := mapOrderStatus(status, deliveryStatus)

			// extra：v1 快照保真 + query_password 提升為列
			extraOut := map[string]any{}
			if e := nullStr(extra); e != "" && json.Valid([]byte(e)) {
				_ = json.Unmarshal([]byte(e), &extraOut)
			}
			queryHash := ""
			if qp, ok := extraOut["query_password"].(string); ok {
				queryHash = qp
				delete(extraOut, "query_password")
			}
			for k, v := range map[string]string{
				"v1_fulfillment_type":  nullStr(fulfillmentSnap),
				"v1_source":            nullStr(source),
				"v1_upstream_order_id": nullStr(upstreamOrderID),
				"v1_instructions":      nullStr(instructions),
				"v1_delivery_message":  nullStr(deliveryMsg),
				"v1_create_device":     nullStr(createDevice),
				"v1_coupon_code":       nullStr(couponCode),
			} {
				if v != "" {
					extraOut[k] = v
				}
			}

			b := m.Client.Order.Create().
				SetOrderNo(orderNo).
				SetSubsiteID(siteID).
				SetTotalAmount(amount).
				SetCost(cost).
				SetSubsiteProfit(subsiteProfit).
				SetStatus(order.Status(st))
			if caOK {
				b.SetCreatedAt(ca)
			}
			b.SetUpdatedAt(ua)
			if u := nullInt(userID); u > 0 {
				if newU, ok := m.IDs.Get(ctx, "users", uint64(u)); ok {
					b.SetUserID(newU)
				}
			}
			if c := nullStr(contact); c != "" {
				if nullInt(userID) > 0 {
					b.SetContact(c)
				} else {
					b.SetGuestContact(c)
				}
			}
			if queryHash != "" {
				b.SetQueryPasswordHash(queryHash) // bcrypt 直遷（$2y$ 兼容已由 golden vector 釘死）
			}
			if bc := nullStr(baseCur); bc != "" {
				b.SetBaseCurrency(bc)
			}
			if dc := nullStr(dispCur); dc != "" {
				b.SetDisplayCurrency(dc)
			}
			if er := nullFloat(exchangeRate); er != 0 {
				b.SetExchangeRate(er)
			}
			if ad := nullInt(amountDisplay); ad != 0 {
				b.SetAmountDisplay(ad)
			}
			if pc := nullStr(payChannel); pc != "" {
				b.SetPaymentChannel(pc)
			}
			if sd := nullStr(subsiteDomain); sd != "" {
				b.SetSubsiteDomain(sd)
			}
			if ip := nullStr(createIP); ip != "" {
				b.SetClientIP(ip)
			}
			if len(extraOut) > 0 {
				b.SetExtra(extraOut)
			}
			if t, ok, err := mustTime(nullStr(paidAt), m.TZ); err != nil {
				return err
			} else if ok {
				b.SetPaidAt(t)
			}
			if t, ok, err := mustTime(nullStr(closedAt), m.TZ); err != nil {
				return err
			} else if ok {
				b.SetClosedAt(t)
				b.SetExpiredAt(t) // 1.x 無獨立 expired 概念，closed 即終態錨點
			}
			o, err := b.Save(ctx)
			if err != nil {
				return err
			}

			// order_items：以 1.x order_items 為準（單商品模型恆 1 行）
			qty := nullInt(quantity)
			if qty <= 0 {
				qty = 1
			}
			newPID, pidOK := m.IDs.Get(ctx, "products", uint64(nullInt(productID)))
			fulfillment := mapFulfillmentType(nullStr(fulfillmentSnap))
			fulfillStatus := "pending"
			if deliveryStatus == "delivered" {
				fulfillStatus = "delivered"
			}
			item, err := m.Client.OrderItem.Create().
				SetOrderID(o.ID).
				SetProductID(pidOrZero(newPID, pidOK)).
				SetSkuName(nullStr(skuName)).
				SetUnitPrice(amount / qty).
				SetQuantity(int32(qty)).
				SetAmount(amount).
				SetCost(cost).
				SetFulfillmentType(orderitem.FulfillmentType(fulfillment)).
				SetFulfillmentStatus(fulfillStatus).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("order_items 合成失敗: %w", err)
			}
			if !pidOK {
				m.RW.AddError("orders", uint64(id), "商品未遷移（分站？），item.product_id=0")
			}

			// amount_lines（恆等式 total = SUM(lines)）：
			// base_price = amount + discount - subsiteMarkup；coupon_discount 為負；
			// subsite_markup 僅分站單合成（1.x subsite_profit 是利潤：分站售價=基礎價+利潤，
			// 拆 base=amount-profit / markup=+profit；主站單利潤只進 orders.subsite_profit 列）
			subsiteMarkup := int64(0)
			if (siteID > 0 || nullInt(subsiteID) > 0) && subsiteProfit > 0 {
				subsiteMarkup = subsiteProfit
			} else if subsiteProfit > 0 {
				m.RW.AddError("orders", uint64(id),
					fmt.Sprintf("主站訂單帶 subsite_profit=%d（僅記入 orders 列，不參與金額行）", subsiteProfit))
			}
			lines := []*ent.OrderAmountLineCreate{
				m.Client.OrderAmountLine.Create().
					SetOrderID(o.ID).
					SetItemID(item.ID).
					SetType(orderamountline.TypeBasePrice).
					SetAmount(amount + discount - subsiteMarkup).
					SetSeq(1),
			}
			if discount > 0 {
				lines = append(lines, m.Client.OrderAmountLine.Create().
					SetOrderID(o.ID).
					SetItemID(item.ID).
					SetType(orderamountline.TypeCouponDiscount).
					SetAmount(-discount).
					SetSeq(2).
					SetMeta(map[string]any{"v1_coupon_code": nullStr(couponCode)}))
			}
			if subsiteMarkup > 0 {
				lines = append(lines, m.Client.OrderAmountLine.Create().
					SetOrderID(o.ID).
					SetType(orderamountline.TypeSubsiteMarkup).
					SetAmount(subsiteMarkup).
					SetSeq(3))
			}
			if _, err := m.Client.OrderAmountLine.CreateBulk(lines...).Save(ctx); err != nil {
				return fmt.Errorf("amount_lines 合成失敗: %w", err)
			}

			// status_events：created/paid/closed 三個錨點（operator=system）
			events := []*ent.OrderStatusEventCreate{
				m.Client.OrderStatusEvent.Create().
					SetOrderID(o.ID).
					SetFromStatus("").
					SetToStatus("pending_payment").
					SetEvent("created").
					SetOperator(orderstatusevent.OperatorSystem).
					SetCreatedAt(ca),
			}
			if t, ok, _ := mustTime(nullStr(paidAt), m.TZ); ok {
				events = append(events, m.Client.OrderStatusEvent.Create().
					SetOrderID(o.ID).
					SetFromStatus("pending_payment").
					SetToStatus("paid").
					SetEvent("paid").
					SetOperator(orderstatusevent.OperatorSystem).
					SetCreatedAt(t))
			}
			if t, ok, _ := mustTime(nullStr(closedAt), m.TZ); ok {
				events = append(events, m.Client.OrderStatusEvent.Create().
					SetOrderID(o.ID).
					SetFromStatus("pending_payment").
					SetToStatus(st).
					SetEvent("canceled").
					SetOperator(orderstatusevent.OperatorSystem).
					SetCreatedAt(t))
			}
			if _, err := m.Client.OrderStatusEvent.CreateBulk(events...).Save(ctx); err != nil {
				return fmt.Errorf("status_events 回填失敗: %w", err)
			}

			if _, err := m.IDs.Put(ctx, m.Client, "orders", uint64(id), o.ID); err != nil {
				return err
			}
			m.st.Record("orders", "migrated")
			return nil
		},
	)
}

func mapFulfillmentType(v1 string) string {
	switch v1 {
	case "manual":
		return "manual"
	case "upstream":
		return "upstream"
	default: // auto_card / fixed
		return "auto"
	}
}

func pidOrZero(id uint64, ok bool) uint64 {
	if !ok {
		return 0
	}
	return id
}

// migratePayments 1.x payments → payments。聚合支付（order_ids JSON 數組）：
// order_id 取首單，完整數組保 raw（已確認決策）。
func (m *Migrator) migratePayments(ctx context.Context) error {
	var (
		id                           int64
		rechargeID                   sql.NullInt64
		orderID                      sql.NullInt64
		orderIDs                     sql.NullString
		channel, channelOrderNo      string
		channelOrderNoNull           sql.NullString
		amount, fee                  int64
		status                       string
		chargedCur                   sql.NullString
		chargedAmount                sql.NullInt64
		channelRate                  sql.NullFloat64
		raw                          sql.NullString
		paidAt, createdAt, updatedAt sql.NullString
	)
	_ = channelOrderNo
	return m.scanTable(ctx, "payments",
		[]string{"id", "order_id", "order_ids", "recharge_id", "channel", "channel_order_no",
			"amount", "fee", "status", "charged_currency", "charged_amount",
			"channel_exchange_rate", "raw", "paid_at", "created_at", "updated_at"},
		func() []any {
			return []any{&id, &orderID, &orderIDs, &rechargeID, &channel, &channelOrderNoNull,
				&amount, &fee, &status, &chargedCur, &chargedAmount,
				&channelRate, &raw, &paidAt, &createdAt, &updatedAt}
		},
		func(int64) error {
			if _, ok := m.IDs.Get(ctx, "payments", uint64(id)); ok {
				m.st.Record("payments", "skip")
				return nil
			}
			b := m.Client.Payment.Create().
				SetChannel(channel).
				SetAmount(amount).
				SetFee(fee).
				SetStatus(payment.Status(status))
			if no := nullStr(channelOrderNoNull); no != "" {
				b.SetChannelOrderNo(no)
			}
			// 聚合支付：首單關聯 + raw 保真
			if oid := nullInt(orderID); oid > 0 {
				if newO, ok := m.IDs.Get(ctx, "orders", uint64(oid)); ok {
					b.SetOrderID(newO)
				}
			} else if idsJSON := nullStr(orderIDs); idsJSON != "" && json.Valid([]byte(idsJSON)) {
				var ids []int64
				if json.Unmarshal([]byte(idsJSON), &ids) == nil && len(ids) > 0 {
					if newO, ok := m.IDs.Get(ctx, "orders", uint64(ids[0])); ok {
						b.SetOrderID(newO)
						m.RW.AddError("payments", uint64(id),
							fmt.Sprintf("聚合支付關聯 %d 單，已取首單（完整清單見 raw）", len(ids)))
					}
				}
			}
			if rid := nullInt(rechargeID); rid > 0 {
				if newR, ok := m.IDs.Get(ctx, "recharges", uint64(rid)); ok {
					b.SetRechargeOrderID(newR)
				}
			}
			if cc := nullStr(chargedCur); cc != "" {
				b.SetChargedCurrency(cc)
			}
			// 1.x charged_amount 是實收幣種最小單位 → 2.0 charged_units；
			// charged_amount（基礎貨幣實收分）取應收近似，差異由對賬報告暴露
			if ca := nullInt(chargedAmount); ca != 0 {
				b.SetChargedUnits(ca).
					SetChargedAmount(amount)
			}
			if er := nullFloat(channelRate); er != 0 {
				b.SetExchangeRate(er)
			}
			if r := nullStr(raw); r != "" && json.Valid([]byte(r)) {
				b.SetRaw(json.RawMessage(r))
			}
			if t, ok, err := mustTime(nullStr(paidAt), m.TZ); err != nil {
				return err
			} else if ok {
				b.SetPaidAt(t)
			}
			if t, ok, _ := mustTime(nullStr(createdAt), m.TZ); ok {
				b.SetCreatedAt(t)
			}
			p, err := b.Save(ctx)
			if err != nil {
				return err
			}
			if _, err := m.IDs.Put(ctx, m.Client, "payments", uint64(id), p.ID); err != nil {
				return err
			}
			m.st.Record("payments", "migrated")
			return nil
		},
	)
}

// migrateRecharges 1.x recharges → recharge_orders（1.x 單號無 2.0 列，進報告）。
func (m *Migrator) migrateRecharges(ctx context.Context) error {
	var (
		id, userID, amount int64
		rechargeNo         string
		status, target     string
		paidAt, createdAt  sql.NullString
	)
	return m.scanTable(ctx, "recharges",
		[]string{"id", "recharge_no", "user_id", "amount", "status", "target", "paid_at", "created_at"},
		func() []any {
			return []any{&id, &rechargeNo, &userID, &amount, &status, &target, &paidAt, &createdAt}
		},
		func(int64) error {
			if _, ok := m.IDs.Get(ctx, "recharges", uint64(id)); ok {
				m.st.Record("recharges", "skip")
				return nil
			}
			newU, ok := m.IDs.Get(ctx, "users", uint64(userID))
			if !ok {
				m.st.Record("recharges", "skip")
				return nil
			}
			st := "pending"
			switch status {
			case "paid":
				st = "success"
			case "closed":
				st = "expired"
			}
			b := m.Client.RechargeOrder.Create().
				SetUserID(newU).
				SetAmount(amount).
				SetTarget(rechargeorder.Target(target))
			if t, ok, _ := mustTime(nullStr(createdAt), m.TZ); ok {
				b.SetCreatedAt(t)
			}
			b.SetStatus(rechargeorder.Status(st))
			if t, ok, _ := mustTime(nullStr(paidAt), m.TZ); ok {
				b.SetPaidAt(t)
			}
			r, err := b.Save(ctx)
			if err != nil {
				return err
			}
			m.RW.AddError("recharges", uint64(id), "1.x 單號 "+rechargeNo+" 無 2.0 列（充值單新體系）")
			if _, err := m.IDs.Put(ctx, m.Client, "recharges", uint64(id), r.ID); err != nil {
				return err
			}
			m.st.Record("recharges", "migrated")
			return nil
		},
	)
}

// migrateCoupons 1.x coupons → coupons（percent ×100 萬分比；門檻進 scope meta）。
func (m *Migrator) migrateCoupons(ctx context.Context) error {
	var (
		id, minAmount                int64
		productID, categoryID        sql.NullInt64
		usedBy, orderID              sql.NullInt64
		code, ctype                  string
		value                        int64
		status                       string
		note                         sql.NullString
		expiresAt, usedAt, createdAt sql.NullString
	)
	return m.scanTable(ctx, "coupons",
		[]string{"id", "code", "type", "value", "product_id", "category_id", "min_amount",
			"status", "expires_at", "used_at", "used_by", "order_id", "note", "created_at"},
		func() []any {
			return []any{&id, &code, &ctype, &value, &productID, &categoryID, &minAmount,
				&status, &expiresAt, &usedAt, &usedBy, &orderID, &note, &createdAt}
		},
		func(int64) error {
			if _, ok := m.IDs.Get(ctx, "coupons", uint64(id)); ok {
				m.st.Record("coupons", "skip")
				return nil
			}
			exists, err := m.Client.Coupon.Query().Where(coupon.Code(code)).Exist(ctx)
			if err != nil {
				return err
			}
			if exists {
				m.st.Record("coupons", "skip")
				return nil
			}
			v := value
			if ctype == "percent" {
				v = value * 100 // 百分比整數 → 萬分比
			}
			scope := map[string]any{}
			if pid := nullInt(productID); pid > 0 {
				if newP, ok := m.IDs.Get(ctx, "products", uint64(pid)); ok {
					scope["product_ids"] = []uint64{newP}
				}
			}
			if cid := nullInt(categoryID); cid > 0 {
				if newC, ok := m.IDs.Get(ctx, "categories", uint64(cid)); ok {
					scope["category_ids"] = []uint64{newC}
				}
			}
			if minAmount > 0 {
				scope["min_amount"] = minAmount // 2.0 無獨立門檻列，scope 保真
			}
			name := nullStr(note)
			if name == "" {
				name = "v1-" + code
			}
			st := "unused"
			switch status {
			case "used":
				st = "used"
			case "disabled":
				st = "disabled"
			}
			b := m.Client.Coupon.Create().
				SetCode(code).
				SetName(name).
				SetType(coupon.Type(ctype)).
				SetValue(v).
				SetStatus(coupon.Status(st))
			if len(scope) > 0 {
				b.SetScope(scope)
			}
			if u := nullInt(usedBy); u > 0 {
				if newU, ok := m.IDs.Get(ctx, "users", uint64(u)); ok {
					b.SetUserID(newU)
				}
			}
			if o := nullInt(orderID); o > 0 {
				if newO, ok := m.IDs.Get(ctx, "orders", uint64(o)); ok {
					b.SetUsedOrderID(newO)
				}
			}
			if t, ok, _ := mustTime(nullStr(usedAt), m.TZ); ok {
				b.SetUsedAt(t)
			}
			if t, ok, _ := mustTime(nullStr(expiresAt), m.TZ); ok {
				b.SetExpireAt(t)
			}
			if t, ok, _ := mustTime(nullStr(createdAt), m.TZ); ok {
				b.SetCreatedAt(t)
			}
			c, err := b.Save(ctx)
			if err != nil {
				return err
			}
			if _, err := m.IDs.Put(ctx, m.Client, "coupons", uint64(id), c.ID); err != nil {
				return err
			}
			m.st.Record("coupons", "migrated")
			return nil
		},
	)
}

// migrateOrderDeliveries 1.x order_deliveries → 2.0 order_deliveries。
// card_id 定位：P3 回填映射（v1id_maps order_deliveries）或源庫 (product, order) 反查。
func (m *Migrator) migrateOrderDeliveries(ctx context.Context) error {
	var (
		id                 int64
		orderID, productID sql.NullInt64
		deliveredMode      string
		deliveredAt        sql.NullString
	)
	return m.scanTable(ctx, "order_deliveries",
		[]string{"id", "order_id", "product_id", "delivered_mode", "delivered_at"},
		func() []any {
			return []any{&id, &orderID, &productID, &deliveredMode, &deliveredAt}
		},
		func(int64) error {
			if _, ok := m.IDs.Get(ctx, "order_deliveries_v2", uint64(id)); ok {
				m.st.Record("order_deliveries", "skip")
				return nil
			}
			newO, ok := m.IDs.Get(ctx, "orders", uint64(nullInt(orderID)))
			if !ok {
				m.st.Record("order_deliveries", "skip") // 分站訂單：P6
				return nil
			}
			// card_id：優先 P3 回填映射；否則源庫按 (product, order) 反查 1.x 卡
			var newCardID uint64
			if cid, ok := m.IDs.Get(ctx, "order_deliveries", uint64(id)); ok {
				newCardID = cid
			} else {
				var oldCard int64
				err := m.Src.DB.QueryRowContext(ctx,
					"SELECT id FROM `cards` WHERE `product_id` = ? AND `order_id` = ? LIMIT 1",
					nullInt(productID), nullInt(orderID)).Scan(&oldCard)
				if err == nil {
					if nc, ok := m.IDs.Get(ctx, "cards", uint64(oldCard)); ok {
						newCardID = nc
					}
				} else if err != sql.ErrNoRows {
					return err
				}
			}
			if newCardID == 0 {
				m.RW.AddError("order_deliveries", uint64(id), "無法定位 card_id（卡未遷或分站），該交付記錄跳過")
				return nil
			}
			// item：該訂單首 item（1.x 單商品模型）
			item, err := m.Client.OrderItem.Query().Where(orderitem.OrderIDEQ(newO)).First(ctx)
			if err != nil {
				return fmt.Errorf("訂單 %d 無 item: %w", newO, err)
			}
			d, err := m.Client.OrderDelivery.Create().
				SetOrderID(newO).
				SetItemID(item.ID).
				SetCardID(newCardID).
				SetDeliveredMode(orderdelivery.DeliveredMode(deliveredMode)).
				SetDeliveryTokenHash(""). // 1.x 無取貨令牌（歷史單走訂單查詢密碼取貨）
				SetFetchCount(1).
				Save(ctx)
			if err != nil {
				return err
			}
			if t, ok, _ := mustTime(nullStr(deliveredAt), m.TZ); ok {
				if _, err := m.Client.OrderDelivery.UpdateOne(d).SetDeliveredAt(t).Save(ctx); err != nil {
					return err
				}
			}
			if _, err := m.IDs.Put(ctx, m.Client, "order_deliveries_v2", uint64(id), d.ID); err != nil {
				return err
			}
			m.st.Record("order_deliveries", "migrated")
			return nil
		},
	)
}

// backfillCardOrderIDs P3 置 0 的 cards.order_id 回填：
// ① 1.x cards.order_id → orders map；② order_deliveries 回填卡 → delivery.order_id → orders map。
func (m *Migrator) backfillCardOrderIDs(ctx context.Context) error {
	if m.dry {
		return nil
	}
	t := m.st.table("card_order_backfill")
	rows, err := m.Src.DB.QueryContext(ctx,
		"SELECT `id`, `order_id` FROM `cards` WHERE `order_id` > 0")
	if err != nil {
		return err
	}
	type pair struct{ card, order uint64 }
	var pairs []pair
	for rows.Next() {
		var c, o int64
		if err := rows.Scan(&c, &o); err != nil {
			return err
		}
		pairs = append(pairs, pair{uint64(c), uint64(o)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pairs {
		newC, okC := m.IDs.Get(ctx, "cards", p.card)
		newO, okO := m.IDs.Get(ctx, "orders", p.order)
		if !okC || !okO {
			t.SkippedExists++
			continue
		}
		cardRow, err := m.Client.Card.Get(ctx, newC)
		if err != nil {
			return err
		}
		if cardRow.OrderID != 0 { // 冪等
			t.SkippedExists++
			continue
		}
		if _, err := m.Client.Card.UpdateOne(cardRow).SetOrderID(newO).Save(ctx); err != nil {
			return err
		}
		t.Migrated++
	}
	// order_deliveries 回填卡（P3 重建行）也回填 order_id
	drows, err := m.Src.DB.QueryContext(ctx,
		"SELECT `id`, `order_id` FROM `order_deliveries` WHERE `order_id` > 0")
	if err != nil {
		return err
	}
	var dpairs []pair
	for drows.Next() {
		var d, o int64
		if err := drows.Scan(&d, &o); err != nil {
			return err
		}
		dpairs = append(dpairs, pair{uint64(d), uint64(o)})
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return err
	}
	for _, p := range dpairs {
		newC, okC := m.IDs.Get(ctx, "order_deliveries", p.card)
		newO, okO := m.IDs.Get(ctx, "orders", p.order)
		if !okC || !okO {
			continue
		}
		cardRow, err := m.Client.Card.Get(ctx, newC)
		if err != nil {
			return err
		}
		if cardRow.OrderID != 0 {
			continue
		}
		if _, err := m.Client.Card.UpdateOne(cardRow).SetOrderID(newO).Save(ctx); err != nil {
			return err
		}
		t.Migrated++
	}
	return nil
}

func nullFloat(v sql.NullFloat64) float64 {
	if v.Valid {
		return v.Float64
	}
	return 0
}
