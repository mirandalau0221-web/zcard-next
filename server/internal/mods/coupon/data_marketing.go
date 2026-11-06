package coupon

// 擴展：券範圍矩陣/每人限用/領取/返還 + 秒殺（同鎖防超賣）+ 促銷（多促最優）。

import (
	"context"
	"encoding/json"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/coupon"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/flashsale"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/promotion"
	"github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// ── / 券：範圍矩陣 + 每人限用 + 領取/返還 ──────────────

// ResolveScoped 範圍矩陣版校驗（port.CouponResolver 實現）。
func (r *CouponRepoImpl) ResolveScoped(ctx context.Context, code string, userID, levelID uint64, items []port.CartItem) (money.Cents, uint64, error) {
	if code == "" {
		return 0, 0, nil
	}
	client := data.Client(ctx, r.data)
	c, err := client.Coupon.Query().Where(coupon.Code(code)).Only(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("coupon.INVALID: %w", err)
	}
	if c.Status != coupon.StatusUnused {
		return 0, 0, fmt.Errorf("coupon.NOT_UNUSED")
	}
	if !c.ExpireAt.IsZero() && time.Now().UTC().After(c.ExpireAt) {
		return 0, 0, fmt.Errorf("coupon.EXPIRED")
	}
	if c.UserID != 0 && c.UserID != userID {
		return 0, 0, fmt.Errorf("coupon.USER_MISMATCH")
	}
	// 範圍矩陣（全場默認命中；商品/分類/等級三清單任一配置即精確匹配）
	eligible := scopeItems(c.Scope, items, levelID)
	if len(eligible) == 0 {
		return 0, 0, fmt.Errorf("coupon.SCOPE_MISMATCH")
	}
	// 每人限用：多次碼按已用累計（同 code 歷史核銷次數）；單次碼由 status 保證
	if c.PerUserLimit > 1 && userID > 0 {
		used, err := client.Coupon.Query().
			Where(coupon.Code(code), coupon.StatusEQ(coupon.StatusUsed)).
			Count(ctx)
		if err == nil && used >= int(c.PerUserLimit) {
			return 0, 0, fmt.Errorf("coupon.PER_USER_LIMIT")
		}
	}
	// 面額（percent 按命中行小計折算——範圍受限時以命中商品金額為基數）
	value, err := couponDiscount(c.Type, c.Value, orderAmountOf(eligible))
	return value, c.ID, err
}

// scopeItems 返回命中範圍的商品行：空 scope/無清單 = 全場；配置任一清單則精確匹配
// （商品 ∨ 分類 ∨ 等級——多清單間 OR，清單內多值 OR）。
func scopeItems(scope map[string]any, items []port.CartItem, levelID uint64) []port.CartItem {
	if len(scope) == 0 {
		return items
	}
	productIDs := idList(scope["product_ids"])
	categoryIDs := idList(scope["category_ids"])
	levelIDs := idList(scope["level_ids"])
	if len(productIDs) == 0 && len(categoryIDs) == 0 && len(levelIDs) == 0 {
		return items // 空清單語義 = 全場
	}
	if len(levelIDs) > 0 && levelID > 0 && levelIDs[levelID] {
		return items
	}
	var matched []port.CartItem
	for _, it := range items {
		if productIDs[it.ProductID] || (it.CategoryID > 0 && categoryIDs[it.CategoryID]) {
			matched = append(matched, it)
		}
	}
	return matched
}

// idList any 列表 → set（JSON 數字解析為 float64）。
func idList(v any) map[uint64]bool {
	out := map[uint64]bool{}
	list, ok := v.([]any)
	if !ok {
		return out
	}
	for _, e := range list {
		switch x := e.(type) {
		case float64:
			out[uint64(x)] = true
		case string:
			var n uint64
			_, _ = fmt.Sscanf(x, "%d", &n)
			if n > 0 {
				out[n] = true
			}
		}
	}
	return out
}

func orderAmountOf(items []port.CartItem) money.Cents {
	var total int64
	for _, it := range items {
		total += int64(it.UnitPrice) * int64(it.Quantity)
	}
	return money.Cents(total)
}

// ReturnByOrder 返還券（取消/退款路徑調用；過期不返）。
func (r *CouponRepoImpl) ReturnByOrder(ctx context.Context, orderID uint64) error {
	// A stale cancellation must never clear a coupon reused by a newer order.
	_, err := data.Client(ctx, r.data).Coupon.Update().
		Where(coupon.UsedOrderID(orderID), coupon.StatusEQ(coupon.StatusUsed), unexpiredCoupon(time.Now().UTC())).
		SetStatus(coupon.StatusUnused).
		ClearUsedAt().
		ClearUsedOrderID().
		Save(ctx)
	return err
}

// Redeem 兑換碼領券（用户憑 code 領取：未指派券回填 user_id）。
func (r *CouponRepoImpl) Redeem(ctx context.Context, code string, userID uint64) error {
	client := data.Client(ctx, r.data)
	c, err := client.Coupon.Query().Where(coupon.Code(code)).Only(ctx)
	if err != nil {
		return fmt.Errorf("coupon.INVALID")
	}
	if c.Status != coupon.StatusUnused {
		return fmt.Errorf("coupon.NOT_UNUSED")
	}
	if !c.ExpireAt.IsZero() && time.Now().UTC().After(c.ExpireAt) {
		return fmt.Errorf("coupon.EXPIRED")
	}
	if c.UserID != 0 && c.UserID != userID {
		return fmt.Errorf("coupon.USER_MISMATCH")
	}
	if c.UserID == userID {
		return nil // 重複領取冪等
	}
	_, err = client.Coupon.UpdateOneID(c.ID).SetUserID(userID).Save(ctx)
	return err
}

// GrantToUser 後台贈送（按批次取未指派券回填 user_id，返回實際贈送數）。
func (r *CouponRepoImpl) GrantToUser(ctx context.Context, batchID string, userID uint64, count int32) (int32, error) {
	client := data.Client(ctx, r.data)
	rows, err := client.Coupon.Query().
		Where(
			coupon.BatchID(batchID),
			coupon.StatusEQ(coupon.StatusUnused),
			coupon.UserIDIsNil(),
		).
		Limit(int(count)).
		All(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range rows {
		if _, err := client.Coupon.UpdateOneID(c.ID).SetUserID(userID).Save(ctx); err != nil {
			return 0, err
		}
	}
	return int32(len(rows)), nil
}

// ListMyCoupons 用户可用券（未用未過期）。
func (r *CouponRepoImpl) ListMyCoupons(ctx context.Context, userID uint64) ([]*ent.Coupon, error) {
	return data.Client(ctx, r.data).Coupon.Query().
		Where(coupon.UserID(userID), coupon.StatusEQ(coupon.StatusUnused), unexpiredCoupon(time.Now().UTC())).
		Order(ent.Desc(coupon.FieldID)).
		Limit(100).
		All(ctx)
}

// ── 秒殺（同鎖防超賣）──────────────────────────────────

// Active 生效中秒殺（窗口判定無狀態）。
func (r *CouponRepoImpl) Active(ctx context.Context, productID, skuID uint64) (*port.FlashInfo, error) {
	rows, err := r.ActiveBatch(ctx, []uint64{productID})
	if err != nil {
		return nil, err
	}
	if offer := rows[port.FlashKey{ProductID: productID, SkuID: skuID}]; offer != nil {
		return offer, nil
	}
	return rows[port.FlashKey{ProductID: productID}], nil // Product-wide campaign also applies to its SKUs.
}

func (r *CouponRepoImpl) ActiveBatch(ctx context.Context, productIDs []uint64) (map[port.FlashKey]*port.FlashInfo, error) {
	out := map[port.FlashKey]*port.FlashInfo{}
	now := time.Now().UTC()
	for start := 0; start < len(productIDs); start += 500 {
		end := start + 500
		if end > len(productIDs) {
			end = len(productIDs)
		}
		rows, err := data.Client(ctx, r.data).FlashSale.Query().Where(flashsale.ProductIDIn(productIDs[start:end]...), flashsale.StartAtLTE(now), flashsale.EndAtGT(now)).Order(ent.Desc(flashsale.FieldID)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, fs := range rows {
			key := port.FlashKey{ProductID: fs.ProductID, SkuID: fs.SkuID}
			if out[key] != nil {
				continue
			}
			out[key] = &port.FlashInfo{ID: fs.ID, FlashPrice: money.Cents(fs.FlashPrice), StartAt: fs.StartAt, EndAt: fs.EndAt, Remaining: fs.LimitQty - fs.SoldQty, PerUserLimit: fs.PerUserLimit}
		}
	}
	return out, nil
}

// Consume checks remaining quota and increments sold quantity in one atomic UPDATE.
// Concurrent buyers cannot oversell or fail just because a previous read became stale.
func (r *CouponRepoImpl) Consume(ctx context.Context, flashID uint64, qty int32) error {
	if qty <= 0 {
		return nil
	}
	affected, err := data.Client(ctx, r.data).FlashSale.Update().
		Where(flashsale.ID(flashID), func(s *sql.Selector) {
			s.Where(sql.P(func(b *sql.Builder) {
				b.Ident(flashsale.FieldSoldQty).WriteString(" + ").Ident(flashsale.FieldReservedQty).WriteString(" + ").Arg(qty).WriteString(" <= ").Ident(flashsale.FieldLimitQty)
			}))
		}).AddSoldQty(qty).Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return port.ErrFlashSoldOut
	}
	return nil
}

// UserPurchasedCount 秒殺窗口內已購（paid+pending 累計；order_items×orders 聯查）。
func (r *CouponRepoImpl) UserPurchasedCount(ctx context.Context, productID, userID uint64, since time.Time) (int32, error) {
	client := data.Client(ctx, r.data)
	// 聯查口徑：該用户窗口內 paid/pending 單中命中商品的數量合計。
	// ent 無跨表 join 表達式——經 order_items 反查（product × order 歸屬過濾）。
	itemRows, err := client.OrderItem.Query().
		Where(
			orderitem.ProductID(productID),
			orderitem.HasOrderWith(
				order.UserID(userID),
				order.StatusIn(order.StatusPaid, order.StatusPendingPayment, order.StatusFulfilling),
				order.CreatedAtGTE(since),
			),
		).
		All(ctx)
	if err != nil {
		return 0, err
	}
	var n int32
	for _, it := range itemRows {
		n += it.Quantity
	}
	return n, nil
}

// ListFlash 秒殺列表（進行中/即將開始；storefront 營銷位）。
func (r *CouponRepoImpl) ListFlash(ctx context.Context, now time.Time, upcoming bool) ([]*ent.FlashSale, error) {
	q := data.Client(ctx, r.data).FlashSale.Query()
	if upcoming {
		q = q.Where(flashsale.StartAtGT(now)).Order(ent.Asc(flashsale.FieldStartAt))
	} else {
		q = q.Where(flashsale.StartAtLTE(now), flashsale.EndAtGTE(now)).Order(ent.Asc(flashsale.FieldEndAt))
	}
	return q.Limit(50).All(ctx)
}

// ── 促銷（多促最優）────────────────────────────────────

// BestFor 商品命中最優促銷（同時窗取折讓最大；無則 nil）。
func (r *CouponRepoImpl) BestFor(ctx context.Context, productID, categoryID uint64, unitPrice money.Cents) (*port.PromotionInfo, error) {
	now := time.Now().UTC()
	rows, err := data.Client(ctx, r.data).Promotion.Query().
		Where(
			promotion.Enabled(true),
			promotion.StartAtLTE(now),
			promotion.EndAtGTE(now),
		).
		Order(ent.Desc(promotion.FieldID)).
		Limit(50).
		All(ctx)
	if err != nil {
		return nil, err
	}
	var best *port.PromotionInfo
	for _, p := range rows {
		if !promoScopeHit(p.Scope, productID, categoryID) {
			continue
		}
		info := &port.PromotionInfo{
			ID: p.ID, Type: string(p.Type), Name: p.Name,
			Threshold: money.Cents(p.Threshold),
		}
		switch p.Type {
		case promotion.TypeFixed:
			info.Discount = money.Cents(p.Discount)
		case promotion.TypePercent:
			if p.Discount <= 0 || p.Discount > 10000 {
				continue
			}
			info.DiscountRate = int32(p.Discount) // percent：discount 列存萬分比
		case promotion.TypeSpecialPrice:
			info.SpecialPrice = money.Cents(p.SpecialPrice)
		}
		// 門檻判定（滿 X 按單價口徑；多品購物車場景由調用方按行判定後聚合——管線為逐行）
		if info.Threshold > 0 && unitPrice < info.Threshold {
			continue
		}
		// 與價格管線保持一致：促銷必須產生正折讓，且不將單價減為零。
		// 不可應用的促銷不能遮擋其他有效候選。
		discount := info.DiscountFor(unitPrice)
		if discount <= 0 || discount >= unitPrice {
			continue
		}
		if best == nil || discount > best.DiscountFor(unitPrice) {
			best = info
		}
	}
	return best, nil
}

// promoScopeHit 促銷範圍（空=全場；product_ids/category_ids OR）。
func promoScopeHit(scope map[string]any, productID, categoryID uint64) bool {
	if len(scope) == 0 {
		return true
	}
	productIDs := idList(scope["product_ids"])
	categoryIDs := idList(scope["category_ids"])
	if len(productIDs) == 0 && len(categoryIDs) == 0 {
		return true
	}
	if len(productIDs) > 0 && productIDs[productID] {
		return true
	}
	if len(categoryIDs) > 0 && categoryID > 0 && categoryIDs[categoryID] {
		return true
	}
	return false
}

// ── admin CRUD（秒殺/促銷）────────────────────────────────

// CreateFlash 創建秒殺。
func (r *CouponRepoImpl) createFlash(ctx context.Context, productID, skuID uint64, flashPrice int64, startAt, endAt time.Time, limitQty, perUserLimit int32) (*ent.FlashSale, error) {
	return data.Client(ctx, r.data).FlashSale.Create().
		SetProductID(productID).
		SetSkuID(skuID).
		SetFlashPrice(flashPrice).
		SetStartAt(startAt).
		SetEndAt(endAt).
		SetLimitQty(limitQty).
		SetPerUserLimit(perUserLimit).
		Save(ctx)
}

// ListFlashAll 秒殺管理列表。
func (r *CouponRepoImpl) ListFlashAll(ctx context.Context, page, size int) ([]*ent.FlashSale, int, error) {
	q := data.Client(ctx, r.data).FlashSale.Query().Order(ent.Desc(flashsale.FieldID))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// DeleteFlash 刪除秒殺。
func (r *CouponRepoImpl) DeleteFlash(ctx context.Context, id uint64) error {
	n, err := data.Client(ctx, r.data).FlashSale.Delete().Where(flashsale.ID(id), flashsale.ReservedQtyEQ(0)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("存在待付款秒殺訂單，取消或完成支付後再刪除活動")
	}
	return nil
}

// UpsertPromotion 創建/更新促銷。
func (r *CouponRepoImpl) upsertPromotion(ctx context.Context, id uint64, name string, scope map[string]any, typ string, threshold, discount, specialPrice int64, startAt, endAt time.Time, enabled bool) (*ent.Promotion, error) {
	if typ == "percent" && (discount <= 0 || discount > 10000) {
		return nil, fmt.Errorf("coupon.PROMO_RATE_INVALID")
	}
	client := data.Client(ctx, r.data)
	if id > 0 {
		return client.Promotion.UpdateOneID(id).
			SetName(name).
			SetScope(scope).
			SetType(promotion.Type(typ)).
			SetThreshold(threshold).
			SetDiscount(discount).
			SetSpecialPrice(specialPrice).
			SetStartAt(startAt).
			SetEndAt(endAt).
			SetEnabled(enabled).
			Save(ctx)
	}
	return client.Promotion.Create().
		SetName(name).
		SetScope(scope).
		SetType(promotion.Type(typ)).
		SetThreshold(threshold).
		SetDiscount(discount).
		SetSpecialPrice(specialPrice).
		SetStartAt(startAt).
		SetEndAt(endAt).
		SetEnabled(enabled).
		Save(ctx)
}

// ListPromotions 促銷列表。
func (r *CouponRepoImpl) ListPromotions(ctx context.Context, page, size int) ([]*ent.Promotion, int, error) {
	q := data.Client(ctx, r.data).Promotion.Query().Order(ent.Desc(promotion.FieldID))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ProductCategoryOf 商品分類（管線促銷範圍判定輸入）。
func (r *CouponRepoImpl) ProductCategoryOf(ctx context.Context, productID uint64) uint64 {
	p, err := data.Client(ctx, r.data).Product.Get(ctx, productID)
	if err != nil {
		return 0
	}
	return p.CategoryID
}

var _ = product.FieldID
var _ = json.Marshal

func toFlashPB(fs *ent.FlashSale) *adminv1.FlashSaleItem {
	return &adminv1.FlashSaleItem{
		Id: fs.ID, ProductId: fs.ProductID, SkuId: fs.SkuID,
		FlashPrice: fs.FlashPrice, LimitQty: fs.LimitQty, SoldQty: fs.SoldQty, ReservedQty: fs.ReservedQty,
		PerUserLimit: fs.PerUserLimit,
		StartAt:      fs.StartAt.Unix(), EndAt: fs.EndAt.Unix(),
	}
}

func toPromoPB(p *ent.Promotion) *adminv1.PromotionItem {
	out := &adminv1.PromotionItem{
		Id: p.ID, Name: p.Name, Type: string(p.Type),
		Threshold: p.Threshold, Discount: p.Discount, SpecialPrice: p.SpecialPrice,
		StartAt: p.StartAt.Unix(), EndAt: p.EndAt.Unix(), Enabled: p.Enabled,
	}
	if p.Scope != nil {
		if b, err := json.Marshal(p.Scope); err == nil {
			out.ScopeJson = string(b)
		}
	}
	return out
}

// EligibleItems exposes the exact scoped rows for order-line refund allocation.
func (r *CouponRepoImpl) EligibleItems(ctx context.Context, id, level uint64, items []port.CartItem) ([]port.CartItem, error) {
	c, e := data.Client(ctx, r.data).Coupon.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	return scopeItems(c.Scope, items, level), nil
}

func (r *CouponRepoImpl) CreateFlash(ctx context.Context, productID, skuID uint64, flashPrice int64, startAt, endAt time.Time, limitQty, perUserLimit int32) (out *ent.FlashSale, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.createFlash(ctx, productID, skuID, flashPrice, startAt, endAt, limitQty, perUserLimit)
		return e
	})
	return out, err
}

func (r *CouponRepoImpl) UpsertPromotion(ctx context.Context, id uint64, name string, scope map[string]any, typ string, threshold, discount, specialPrice int64, startAt, endAt time.Time, enabled bool) (out *ent.Promotion, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.upsertPromotion(ctx, id, name, scope, typ, threshold, discount, specialPrice, startAt, endAt, enabled)
		return e
	})
	return out, err
}
