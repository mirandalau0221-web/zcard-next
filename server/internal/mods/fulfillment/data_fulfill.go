package fulfillment

// 自動交付管線 + 取貨三重門 + 人工發貨（ 最後一個 M1a 模塊）。
//
// 核心安全設計（ 無明文快照）：
// 交付記錄 = card_id 引用 + 一次性 delivery_token_hash——不存明文。
// 取貨時現場解密返回，絕不落庫明文（區別於 1.x card_content / 友商 Payload text）。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"time"

	"entgo.io/ent/dialect/sql"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderdelivery"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
	auditport "github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/fulfillment/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
)

// DeliveryRepoImpl 交付倉儲。
type DeliveryRepoImpl struct {
	data    *data.Data
	cipher  *inventory.CardCipher
	inv     inventory.CardRepo
	gate    auditport.RiskGate // 取貨失敗鎖定（nil = 未裝配跳過）
	auditor auditport.Auditor  // 取貨安全審計（nil = 未裝配跳過）
}

// actorTypeOf 訂單歸屬（遊客 user_id=0 → guest）。
func actorTypeOf(o *ent.Order) string {
	if o.UserID == 0 {
		return "guest"
	}
	return "user"
}

// NewDeliveryRepoImpl 構造。
func NewDeliveryRepoImpl(d *data.Data, cipher *inventory.CardCipher, gate auditport.RiskGate, auditor auditport.Auditor) *DeliveryRepoImpl {
	return &DeliveryRepoImpl{data: d, cipher: cipher, gate: gate, auditor: auditor}
}

// ── 自動交付管線 ─────────────────────────────────────────

// FulfillOrder 自動交付（order.paid 事件觸發）：
// 1) 取訂單 reserved 卡密 → 2) MarkUsed/即刪 → 3) 寫交付記錄（card_id + 令牌）
func (r *DeliveryRepoImpl) FulfillOrder(ctx context.Context, orderNo string) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error { return r.fulfillOrder(ctx, orderNo) })
}

func (r *DeliveryRepoImpl) fulfillOrder(ctx context.Context, orderNo string) error {
	client := data.Client(ctx, r.data)

	// 查訂單
	o, err := client.Order.Query().Where(order.OrderNo(orderNo)).Only(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("fulfillment.ORDER_NOT_FOUND")
	}
	if err != nil {
		return err
	}

	if has, e := data.HasSMSOrder(ctx, client, o.ID); e != nil {
		return e
	} else if has {
		return nil
	}
	if o.Status != order.StatusPaid && o.Status != order.StatusFulfilling && o.Status != order.StatusPartiallyDelivered {
		return nil
	}
	if err := r.lockDeliveryOrder(ctx, o); err != nil {
		return err
	}

	// 取 reserved 卡密
	cards, err := client.Card.Query().
		Where(card.OrderID(o.ID), card.StatusEQ(card.StatusReserved)).
		All(ctx)
	if err != nil {
		return err
	}

	// 取訂單子項（獲取 product_id 用於解密 AAD）
	items, err := client.OrderItem.Query().
		Where(orderitem.OrderID(o.ID)).All(ctx)
	if err != nil {
		return err
	}
	productIDMap := map[uint64]bool{}
	for _, it := range items {
		productIDMap[it.ProductID] = true
	}

	deliveredAt := time.Now().UTC()

	// 商品發貨模式映射（products.delivery_mode：status=標記 / delete=即刪）
	modeByProduct := map[uint64]string{}
	for _, it := range items {
		if _, ok := modeByProduct[it.ProductID]; ok {
			continue
		}
		if prod, err := client.Product.Get(ctx, it.ProductID); err == nil {
			modeByProduct[it.ProductID] = string(prod.DeliveryMode)
		}
	}
	modeOf := func(productID uint64) orderdelivery.DeliveredMode {
		if modeByProduct[productID] == "delete" {
			return orderdelivery.DeliveredModeDelete
		}
		return orderdelivery.DeliveredModeStatus
	}

	// 逐卡：MarkUsed + 交付記錄（即刪模式交付後物理刪除卡密行）
	var deleteCardIDs []uint64
	for _, c := range cards {
		var itemID uint64
		canceled := false
		for _, it := range items {
			if it.ProductID == c.ProductID && it.SkuID == c.SkuID {
				if it.CanceledQuantity >= it.Quantity || it.FulfillmentStatus == "refunded" {
					canceled = true
					break
				}
				itemID = it.ID
				break
			}
		}
		if canceled {
			continue
		}
		if itemID == 0 {
			return fmt.Errorf("fulfillment.CARD_ITEM_MISMATCH")
		}
		// MarkUsed（affected rows 校驗防併發重發）
		affected, err := client.Card.Update().
			Where(card.ID(c.ID), card.StatusEQ(card.StatusReserved), card.OrderID(o.ID)).
			SetStatus(card.StatusUsed).
			SetUsedAt(deliveredAt).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("fulfillment.MARK_USED_FAILED: %w", err)
		}
		if affected == 0 {
			continue // 已處理（併發冪等）
		}

		// 交付記錄（card_id 引用 + 一次性令牌哈希——不存明文）
		token := randomToken()
		tokenHash := hashToken(token)
		mode := modeOf(c.ProductID)
		_, err = client.OrderDelivery.Create().
			SetOrderID(o.ID).
			SetItemID(itemID).
			SetCardID(c.ID).
			SetDeliveryTokenHash(tokenHash).
			SetDeliveredMode(mode).
			SetDeliveredBy(0). // auto
			SetFetchCount(0).
			SetDeliveredAt(deliveredAt).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("fulfillment.DELIVERY_CREATE_FAILED: %w", err)
		}
		if mode == orderdelivery.DeliveredModeDelete {
			deleteCardIDs = append(deleteCardIDs, c.ID) // 即刪：交付後物理刪除（取貨佔位兜底見 FetchDelivery）
		}
	}
	if len(deleteCardIDs) > 0 {
		if _, err := client.Card.Delete().Where(card.IDIn(deleteCardIDs...)).Exec(ctx); err != nil {
			return fmt.Errorf("fulfillment.DELETE_MODE_FAILED: %w", err)
		}
	}

	// 直發商品（url/code）：同一鏈接/兌換碼反覆發貨——寫直發交付記錄
	// （CardID=0，取貨時從商品 direct_content 現場解密）。每訂單項一條。
	for _, it := range items {
		if it.GoodsType == "physical" || it.CanceledQuantity >= it.Quantity {
			continue
		}
		if it.FulfillmentType == orderitem.FulfillmentTypeReuse {
			if err := r.fulfillReusable(ctx, o, it); err != nil {
				return err
			}
			continue
		}
		p, err := client.Product.Get(ctx, it.ProductID)
		if err != nil || p.StockType == product.StockTypeCard || p.UpstreamSourceID > 0 || it.FulfillmentType == orderitem.FulfillmentTypeManual {
			continue // 卡密類走上方逐卡；上游項由 procurement 回填
		}
		exists, err := client.OrderDelivery.Query().Where(orderdelivery.OrderID(o.ID), orderdelivery.ItemID(it.ID)).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		token := randomToken()
		_, err = client.OrderDelivery.Create().
			SetOrderID(o.ID).
			SetItemID(it.ID).
			SetCardID(0).
			SetDeliveryTokenHash(hashToken(token)).
			SetDeliveredMode(orderdelivery.DeliveredModeDirect).
			SetDeliveredBy(0).
			SetFetchCount(0).
			SetDeliveredAt(deliveredAt).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("fulfillment.DIRECT_CREATE_FAILED: %w", err)
		}
	}

	return r.updateDeliveryProgress(ctx, o, "system", 0, "")
}

// ── 取貨三重門 ─────────────────────────────────────────

// FetchResult 取貨結果。
type FetchResult struct {
	OrderNo  string
	Status   string
	Items    []FetchItem
	FetchCnt int32
}

// FetchItem 取貨項。
type FetchItem struct {
	ItemID, DeliveryID         uint64
	Kind, ProductName, SkuName string
	DeliveredAt                int64
	CardID                     uint64
	Content                    string // 明文（首次）或掩碼
	Masked                     bool
}

// FetchDelivery 取貨（三重門：單號+密碼+限流；憑正確密碼始終返回明文——
// 2026-09-03 修訂，原「首次明文、後續掩碼」致支付頁自動取貨消耗首次後客戶
// 再查只見尾 4 位掩碼，客訴率高）。
func (r *DeliveryRepoImpl) FetchDelivery(ctx context.Context, orderNo, queryPassword, clientIP string, accessToken ...string) (*FetchResult, error) {
	client := data.Client(ctx, r.data)
	var token string
	if len(accessToken) > 0 {
		token = accessToken[0]
	}

	// 門 1+2：單號 + 查詢密碼（錯誤響應一致——防枚舉）
	o, err := client.Order.Query().Where(order.OrderNo(orderNo)).Only(ctx)
	if ent.IsNotFound(err) {
		if token != "" {
			return nil, orderaccess.Required(ctx)
		}
		return nil, orderaccess.Verify(ctx, r.gate, orderNo, "", queryPassword, clientIP)
	}
	if err != nil {
		return nil, err
	}
	if o.SubsiteID != tenancy.FromContext(ctx).SubsiteID {
		if token != "" {
			return nil, orderaccess.Required(ctx)
		}
		return nil, orderaccess.Verify(ctx, r.gate, orderNo, "", queryPassword, clientIP)
	}
	claims := identity.ClaimsFromContext(ctx)
	owner := claims != nil && o.UserID != 0 && claims.Subject == o.UserID
	tokenAuthorized := orderaccess.VerifyToken(o.OrderAccessTokenHash, o.SubsiteID, o.OrderNo, token)
	if !owner && !tokenAuthorized && (o.OrderAccessTokenHash != "" || token != "") && queryPassword == "" {
		return nil, orderaccess.Required(ctx)
	}
	// Email-verified access and the original password remain valid alternatives.
	if !owner && !tokenAuthorized && (o.QueryPasswordHash != "" || o.CommerceVersion > 0 || o.OrderAccessTokenHash != "") {
		if err := orderaccess.Verify(ctx, r.gate, o.OrderNo, o.QueryPasswordHash, queryPassword, clientIP); err != nil {
			return nil, err
		}
	}
	if o.Status == order.StatusPendingPayment {
		return &FetchResult{OrderNo: o.OrderNo, Status: string(o.Status)}, nil
	}

	// 取交付記錄
	deliveries, err := client.OrderDelivery.Query().
		Where(orderdelivery.OrderID(o.ID)).
		Order(ent.Asc(orderdelivery.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	result := &FetchResult{
		OrderNo:  o.OrderNo,
		Status:   string(o.Status),
		FetchCnt: 0,
	}
	// 取貨審計（：誰/何時/IP/訂單——不含明文卡密）
	if r.auditor != nil {
		r.auditor.Security(ctx, auditport.SecurityEntry{
			ActorType: actorTypeOf(o), ActorID: o.UserID,
			Action: "delivery.fetch", IP: clientIP,
			Metadata: map[string]any{"order_no": orderNo, "order_id": o.ID},
		})
	}

	for _, d := range deliveries {
		if len(d.ServiceContent) > 0 {
			result.Items = append(result.Items, FetchItem{Content: r.serviceText(ctx, d)})
			continue
		}

		// 直發交付（url/code 商品）：內容在商品 direct_content（CardID=0 無卡）
		if d.DeliveredMode == orderdelivery.DeliveredModeDirect {
			oi, e := client.OrderItem.Get(ctx, d.ItemID)
			if e == nil && oi.FulfillmentType == orderitem.FulfillmentTypeReuse {
				src, e := client.ProductDeliverySource.Get(ctx, oi.DeliverySourceID)
				if e != nil || src.ProductID != oi.ProductID || src.SkuID != oi.SkuID || src.SubsiteID != o.SubsiteID {
					return nil, fmt.Errorf("歷史交付來源不可讀取")
				}
				plain, e := r.cipher.Open(src.Content, oi.ProductID, o.SubsiteID)
				if e != nil {
					return nil, fmt.Errorf("歷史交付內容不可解密")
				}
				result.Items = append(result.Items, FetchItem{Content: string(plain), Masked: false})
				continue
			}
			var productID uint64
			if d.ItemID > 0 {
				if it, e := client.OrderItem.Get(ctx, d.ItemID); e == nil {
					productID = it.ProductID
				}
			}
			p, perr := client.Product.Get(ctx, productID)
			if perr != nil || len(p.DirectContent) == 0 {
				result.Items = append(result.Items, FetchItem{CardID: 0, Content: "****（直發內容未配置）", Masked: true})
				continue
			}
			plain, derr := r.cipher.Open(p.DirectContent, p.ID, p.SubsiteID)
			if derr != nil {
				result.Items = append(result.Items, FetchItem{CardID: 0, Content: "****（解密失敗）", Masked: true})
				continue
			}
			// 明文規則（2026-09-03 修訂）：單號+查詢密碼取貨始終明文——密碼即門。
			// 曾「首次明文、後續掩碼」：支付成功頁的自動取貨即消耗首次，客戶
			// 再查只見尾 4 位，客訴率高（發卡平台通行做法均為憑密碼可反覆查看）。
			result.Items = append(result.Items, FetchItem{CardID: 0, Content: string(plain), Masked: false})
			continue
		}

		if tracking, ok := d.Logistics["tracking_no"].(string); ok && tracking != "" {
			result.Items = append(result.Items, FetchItem{Content: "物流單號：" + tracking})
			continue
		}
		// 取卡密
		c, err := client.Card.Get(ctx, d.CardID)
		if ent.IsNotFound(err) {
			// 即刪模式：卡密已物理刪除——返回佔位
			result.Items = append(result.Items, FetchItem{
				CardID: d.CardID, Content: "****（已刪除）", Masked: true,
			})
			continue
		}
		if err != nil {
			return nil, err
		}

		// 現場解密（不落庫明文）；憑密碼取貨始終明文（同直發分支註釋）
		plain, err := r.cipher.Open(c.Content, c.ProductID, c.SubsiteID)
		if err != nil {
			result.Items = append(result.Items, FetchItem{
				CardID: c.ID, Content: "****（解密失敗）", Masked: true,
			})
			continue
		}

		result.Items = append(result.Items, FetchItem{
			CardID: c.ID, Content: string(plain), Masked: false,
		})
	}

	// Preserve stable delivery identity and owning order item for every result kind.
	for i, d := range deliveries {
		if i >= len(result.Items) {
			break
		}
		it := &result.Items[i]
		it.ItemID = d.ItemID
		it.DeliveryID = d.ID
		it.Kind = deliveryKind(d)
		if !d.DeliveredAt.IsZero() {
			it.DeliveredAt = d.DeliveredAt.Unix()
		}
		if oi, e := client.OrderItem.Get(ctx, d.ItemID); e == nil {
			it.ProductName = oi.ProductName
			it.SkuName = oi.SkuName
		}
	}
	// 取貨計數 + IP：每次取貨均記（審計）；首次取貨推進訂單 → completed
	for _, d := range deliveries {
		_, _ = client.OrderDelivery.UpdateOne(d).
			SetFetchCount(d.FetchCount + 1).
			SetFetchedIP(clientIP).
			Save(ctx)
	}
	if len(deliveries) > 0 {
		result.FetchCnt = deliveries[0].FetchCount + 1
	}
	if o.CommerceVersion == 0 && o.Status == order.StatusDelivered && len(deliveries) > 0 {
		err := data.Tx(ctx, r.data, func(ctx context.Context) error {
			client := data.Client(ctx, r.data)
			n, err := client.Order.Update().Where(order.ID(o.ID), order.StatusEQ(order.StatusDelivered), order.Version(o.Version)).SetStatus(order.StatusCompleted).AddVersion(1).Save(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
			if err := client.OrderStatusEvent.Create().SetOrderID(o.ID).SetFromStatus("delivered").SetToStatus("completed").SetEvent("completed").SetOperator("user").SetClientIP(clientIP).Exec(ctx); err != nil {
				return err
			}
			result.Status = "completed"
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

// ProductNames 批量商品名（待發貨面板展示用；缺失商品返回空串由調用方回落 #id）。
func (r *DeliveryRepoImpl) ProductNames(ctx context.Context, ids []uint64) (map[uint64]string, error) {
	out := make(map[uint64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	prods, err := data.Client(ctx, r.data).Product.Query().Where(product.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range prods {
		out[p.ID] = p.Name
	}
	return out, nil
}

// ListPending 按訂單分頁，包含待寄出的實體商品和待人工處理的交付項。
func (r *DeliveryRepoImpl) ListPending(ctx context.Context, page, size int) ([]*ent.Order, bool, error) {
	c := data.Client(ctx, r.data)
	pos, err := c.ProcurementOrder.Query().Where(procurementorder.StatusIn(procurementorder.StatusManual, procurementorder.StatusRejected, procurementorder.StatusRefunding)).All(ctx)
	if err != nil {
		return nil, false, err
	}
	var ids []uint64
	for _, po := range pos {
		ids = append(ids, po.OrderItemID)
	}
	physical := orderitem.And(orderitem.GoodsTypeEQ("physical"), func(s *sql.Selector) {
		s.Where(sql.ExprP(fmt.Sprintf("%s > %s + %s", s.C(orderitem.FieldQuantity), s.C(orderitem.FieldShippedQuantity), s.C(orderitem.FieldCanceledQuantity))))
	})
	manual := orderitem.And(orderitem.GoodsTypeNEQ("physical"), orderitem.FulfillmentStatusNotIn("delivered", "refunded"),
		orderitem.Or(orderitem.FulfillmentTypeEQ(orderitem.FulfillmentTypeManual), orderitem.FulfillmentStatusIn("manual", "failed"), orderitem.IDIn(ids...)),
		func(s *sql.Selector) {
			s.Where(sql.ColumnsGT(s.C(orderitem.FieldQuantity), s.C(orderitem.FieldCanceledQuantity)))
		})
	pending := orderitem.Or(physical, manual)
	rows, err := c.Order.Query().Where(order.SubsiteID(tenancy.FromContext(ctx).SubsiteID), order.StatusIn(order.StatusPaid, order.StatusFulfilling, order.StatusPartiallyDelivered), order.HasItemsWith(pending)).WithItems(func(q *ent.OrderItemQuery) { q.Where(pending) }).Order(ent.Desc(order.FieldID)).Offset((page - 1) * size).Limit(size + 1).All(ctx)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > size
	if hasMore {
		rows = rows[:size]
	}
	return rows, hasMore, nil
}

// ListDeliveries 交付記錄列表。
func (r *DeliveryRepoImpl) ListDeliveries(ctx context.Context, orderNo string, page, size int, orderItemID ...uint64) ([]*ent.OrderDelivery, int64, error) {
	client := data.Client(ctx, r.data)
	q := client.OrderDelivery.Query().Order(ent.Desc(orderdelivery.FieldDeliveredAt), ent.Desc(orderdelivery.FieldID))
	if len(orderItemID) > 0 && orderItemID[0] != 0 {
		q = q.Where(orderdelivery.ItemID(orderItemID[0]))
	}
	if orderNo != "" {
		o, err := client.Order.Query().Where(order.OrderNo(orderNo)).Only(ctx)
		if ent.IsNotFound(err) {
			return nil, 0, nil
		}
		if err != nil {
			return nil, 0, err
		}
		q = q.Where(orderdelivery.OrderID(o.ID))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Clone().Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, int64(total), err
}

// ── 工具 ──

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("fulfillment: 隨機數生成失敗")
	}
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// ── （）上游採購交付出口 ─────────────────────────────

// AttachUpstreamDelivery 上游卡密交付（ 交付出口）：
// 入參已是密文（procurement 側 CardCipher.Seal 後透傳），本層只負責：
// 1. 寫 cards（status=used，order_id 綁定——前台取貨按 order 關聯，與本地卡密同鏈路）
// 2. 寫 order_deliveries（card_id 引用 + 一次性令牌 + 掩碼，無明文快照）
//
// 冪等：同 order_item 已交付直接返回（procurement 側也以採購單狀態機兜底）。
func (r *DeliveryRepoImpl) AttachUpstreamDelivery(ctx context.Context, orderID, itemID, productID uint64, items []port.UpstreamDeliveryItem) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		return r.attachUpstreamDelivery(ctx, orderID, itemID, productID, items)
	})
}

func (r *DeliveryRepoImpl) attachUpstreamDelivery(ctx context.Context, orderID, itemID, productID uint64, items []port.UpstreamDeliveryItem) error {
	client := data.Client(ctx, r.data)

	o, err := client.Order.Get(ctx, orderID)
	if err != nil {
		return err
	}
	oi, err := client.OrderItem.Get(ctx, itemID)
	if err != nil {
		return err
	}
	if oi.OrderID != orderID || oi.ProductID != productID || len(items) != int(oi.Quantity) || len(items) == 0 {
		return fmt.Errorf("fulfillment.INVALID_UPSTREAM_DELIVERY")
	}
	// Full delivery is idempotent. Partial historic delivery must be reconciled manually.
	check := func() (bool, error) {
		counts, err := r.deliveredQuantities(ctx, o.ID, []*ent.OrderItem{oi})
		if err != nil {
			return false, err
		}
		if counts[oi.ID] >= int(oi.Quantity) {
			return true, nil
		}
		if counts[oi.ID] > 0 {
			return false, fmt.Errorf("商品已部分交付，請轉人工補發剩餘數量")
		}
		return false, nil
	}
	if done, err := check(); done || err != nil {
		return err
	}
	if err := r.lockDeliveryOrder(ctx, o); err != nil {
		return err
	}
	if done, err := check(); done || err != nil {
		return err
	}
	oi, err = client.OrderItem.Get(ctx, itemID)
	if err != nil {
		return err
	}
	if oi.CanceledQuantity > 0 || oi.FulfillmentStatus == "refunded" {
		return fmt.Errorf("商品已取消，不能交付上游內容")
	}
	processing, err := client.RefundOrder.Query().Where(refundorder.OrderID(o.ID), refundorder.StatusEQ(refundorder.StatusProcessing)).Exist(ctx)
	if err != nil {
		return err
	}
	if processing {
		return fmt.Errorf("退款處理中，請先核實退款結果")
	}

	deliveredAt := time.Now().UTC()
	for _, it := range items {
		// 上游卡密以「已用卡」形態入庫（不進入可售庫存池；防超賣語義不受影響）
		c, err := client.Card.Create().
			SetProductID(productID).
			SetSubsiteID(o.SubsiteID).
			SetSkuID(oi.SkuID).
			SetContent(it.SealedContent).
			SetContentHash(it.ContentHash).
			SetStatus(card.StatusUsed).
			SetOrderID(orderID).
			SetUsedAt(deliveredAt).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("fulfillment.UPSTREAM_CARD_CREATE_FAILED: %w", err)
		}
		token := randomToken()
		_, err = client.OrderDelivery.Create().
			SetOrderID(orderID).
			SetItemID(itemID).
			SetCardID(c.ID).
			SetDeliveryTokenHash(hashToken(token)).
			SetDeliveredMode(orderdelivery.DeliveredModeStatus).
			SetDeliveredBy(0). // auto（上游採購交付）
			SetFetchCount(0).
			SetDeliveredAt(deliveredAt).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("fulfillment.UPSTREAM_DELIVERY_CREATE_FAILED: %w", err)
		}
	}

	return r.updateDeliveryProgress(ctx, o, "system", 0, "上游交付")
}
