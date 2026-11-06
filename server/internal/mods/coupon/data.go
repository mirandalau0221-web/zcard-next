package coupon

// 優惠券倉儲（M1b 基礎版：批量生成 + 校驗核銷 + 作廢）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/coupon"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/predicate"
	"github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// CouponRepoImpl 券倉儲。
type CouponRepoImpl struct {
	data *data.Data
}

// NewCouponRepoImpl 構造。
func NewCouponRepoImpl(d *data.Data) *CouponRepoImpl {
	return &CouponRepoImpl{data: d}
}

var _ port.CouponResolver = (*CouponRepoImpl)(nil)

// ListCoupons 券列表（狀態/批次篩選 + 分頁；total 與批次去重列表供前端篩選）。
func (r *CouponRepoImpl) ListCoupons(ctx context.Context, status, batchID string, page, size int) ([]*ent.Coupon, int, []string, error) {
	client := data.Client(ctx, r.data)
	q := client.Coupon.Query()
	if status != "" {
		q = q.Where(coupon.StatusEQ(coupon.Status(status)))
	}
	if batchID != "" {
		q = q.Where(coupon.BatchID(batchID))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	rows, err := q.Clone().Order(ent.Desc(coupon.FieldID)).Offset((page - 1) * size).Limit(size).All(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	// 批次去重列表（供篩選下拉；batch_id 建有索引）
	all, err := client.Coupon.Query().Select(coupon.FieldBatchID).All(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	seen := make(map[string]struct{}, len(all))
	batches := make([]string, 0, len(all))
	for _, c := range all {
		if c.BatchID != "" {
			if _, ok := seen[c.BatchID]; !ok {
				seen[c.BatchID] = struct{}{}
				batches = append(batches, c.BatchID)
			}
		}
	}
	return rows, total, batches, nil
}

// DeleteCoupons 按 id 批量刪除（僅未使用；已使用/已作廢跳過保審計痕跡）。
func (r *CouponRepoImpl) DeleteCoupons(ctx context.Context, ids []uint64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	return data.Client(ctx, r.data).Coupon.Delete().
		Where(coupon.IDIn(ids...), coupon.StatusEQ(coupon.StatusUnused)).
		Exec(ctx)
}

// DeleteBatchUnused 刪除整批次全部未使用券。
func (r *CouponRepoImpl) DeleteBatchUnused(ctx context.Context, batchID string) (int, error) {
	if batchID == "" {
		return 0, nil
	}
	return data.Client(ctx, r.data).Coupon.Delete().
		Where(coupon.BatchID(batchID), coupon.StatusEQ(coupon.StatusUnused)).
		Exec(ctx)
}

// ExportCSV 導出券碼 CSV（狀態/批次篩選；含 BOM + 表頭，Excel 直開）。
func (r *CouponRepoImpl) ExportCSV(ctx context.Context, status, batchID string) (string, error) {
	q := data.Client(ctx, r.data).Coupon.Query()
	if status != "" {
		q = q.Where(coupon.StatusEQ(coupon.Status(status)))
	}
	if batchID != "" {
		q = q.Where(coupon.BatchID(batchID))
	}
	rows, err := q.Order(ent.Asc(coupon.FieldID)).Limit(50000).All(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("\uFEFF批次,名稱,類型,面值,券碼,狀態,過期時間\n")
	for _, c := range rows {
		typ := "滿減"
		val := fmt.Sprintf("%d分", c.Value)
		if c.Type == coupon.TypePercent {
			typ = "折扣"
			val = fmt.Sprintf("%g折", float64(c.Value)/1000) // 萬分比 → 折
		}
		statusZh := map[string]string{"unused": "未使用", "used": "已使用", "disabled": "已作廢"}[string(c.Status)]
		if statusZh == "" {
			statusZh = string(c.Status)
		}
		expire := ""
		if !c.ExpireAt.IsZero() {
			expire = c.ExpireAt.Local().Format("2006-01-02 15:04")
		}
		// 名稱/批次含逗號時加引號轉義（RFC 4180）
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%s\n", csvEscape(c.BatchID), csvEscape(c.Name), typ, val, c.Code, statusZh, expire)
	}
	return b.String(), nil
}

// csvEscape CSV 字段含逗號/引號/換行時按 RFC 4180 加引號。
func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// CreateBatch 批量生成券碼。
func (r *CouponRepoImpl) createBatch(ctx context.Context, name, typ string, value int64, count int32, expireAt *time.Time) (int32, error) {
	if _, err := couponDiscount(coupon.Type(typ), value, 0); err != nil {
		return 0, err
	}
	client := data.Client(ctx, r.data)
	batchID := fmt.Sprintf("B%d", time.Now().UnixNano())
	for i := int32(0); i < count; i++ {
		_, err := client.Coupon.Create().
			SetBatchID(batchID).
			SetName(name).
			SetType(coupon.Type(typ)).
			SetValue(value).
			SetCode(fmt.Sprintf("%s%04d", batchID, i)).
			SetNillableExpireAt(expireAt).
			SetStatus(coupon.StatusUnused).
			Save(ctx)
		if err != nil {
			return i, err
		}
	}
	return count, nil
}

// Disable 作廢（按 batch_id）。
func (r *CouponRepoImpl) Disable(ctx context.Context, batchID string) (int, error) {
	return data.Client(ctx, r.data).Coupon.Update().
		Where(coupon.BatchID(batchID), coupon.StatusEQ(coupon.StatusUnused)).
		SetStatus(coupon.StatusDisabled).
		Save(ctx)
}

// Resolve 校驗券並返回面額。
func (r *CouponRepoImpl) Resolve(ctx context.Context, code string, userID uint64, orderAmount money.Cents) (money.Cents, uint64, error) {
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
	value, err := couponDiscount(c.Type, c.Value, orderAmount)
	return value, c.ID, err
}

// couponDiscount 與後台券面顯示一致：9500=9.5折；折讓向下取整到分。
func couponDiscount(typ coupon.Type, configured int64, base money.Cents) (money.Cents, error) {
	var value money.Cents
	switch typ {
	case coupon.TypeFixed:
		if configured < 0 {
			return 0, fmt.Errorf("coupon.VALUE_INVALID")
		}
		value = money.Cents(configured)
	case coupon.TypePercent:
		if configured <= 0 || configured > 10000 {
			return 0, fmt.Errorf("coupon.RATE_INVALID")
		}
		value = money.Cents(int64(base) * (10000 - configured) / 10000)
	default:
		return 0, fmt.Errorf("coupon.TYPE_INVALID")
	}
	if value > base {
		value = base // 券不找零，且不得抵扣範圍外商品。
	}
	return value, nil
}

func unexpiredCoupon(now time.Time) predicate.Coupon {
	return coupon.Or(coupon.ExpireAtIsNil(), coupon.ExpireAtGTE(now))
}

// MarkUsed claims an unused, unexpired coupon atomically inside the order transaction.
func (r *CouponRepoImpl) MarkUsed(ctx context.Context, couponID, orderID uint64) error {
	now := time.Now().UTC()
	n, err := data.Client(ctx, r.data).Coupon.Update().
		Where(coupon.ID(couponID), coupon.StatusEQ(coupon.StatusUnused), unexpiredCoupon(now)).
		SetStatus(coupon.StatusUsed).
		SetUsedAt(now).
		SetUsedOrderID(orderID).
		Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("coupon.NOT_AVAILABLE: 優惠券已使用、已作廢或已過期")
	}
	return nil
}

func (r *CouponRepoImpl) CreateBatch(ctx context.Context, name, typ string, value int64, count int32, expireAt *time.Time) (out int32, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.createBatch(ctx, name, typ, value, count, expireAt)
		return e
	})
	return out, err
}
