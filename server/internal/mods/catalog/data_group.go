package catalog

// 會員商品組數據層（M1b）：admin CRUD + 訂單管線商品組折扣解析。
// ent import 收口：data 前綴文件（架構測試規則 3b）。

import (
	"context"
	"fmt"
	"slices"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/memberproductgroup"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

// GroupInput 會員商品組創建/更新輸入。
type GroupInput struct {
	Name        string
	ProductIDs  []uint64
	Discount    int32 // 萬分比
	StackMember bool
	StackCoupon bool
	BadgeStyle  string
}

// ── admin CRUD ───────────────────────────────────────────────

// ListMemberGroups 商品組列表。
func (r *ProductRepoImpl) ListMemberGroups(ctx context.Context) ([]*ent.MemberProductGroup, error) {
	tc := tenancy.FromContext(ctx)
	return data.Client(ctx, r.data).MemberProductGroup.Query().
		Where(memberproductgroup.SubsiteID(tc.SubsiteID)).
		Order(ent.Asc(memberproductgroup.FieldID)).
		All(ctx)
}

// CreateMemberGroup 創建商品組。
func (r *ProductRepoImpl) CreateMemberGroup(ctx context.Context, in GroupInput) (*ent.MemberProductGroup, error) {
	tc := tenancy.FromContext(ctx)
	return data.Client(ctx, r.data).MemberProductGroup.Create().
		SetSubsiteID(tc.SubsiteID).
		SetName(in.Name).
		SetProductIds(in.ProductIDs).
		SetDiscount(in.Discount).
		SetStackMember(in.StackMember).
		SetStackCoupon(in.StackCoupon).
		SetBadgeStyle(in.BadgeStyle).
		Save(ctx)
}

// UpdateMemberGroup 更新商品組（零值/空字段不動）。
func (r *ProductRepoImpl) UpdateMemberGroup(ctx context.Context, id uint64, in GroupInput) (*ent.MemberProductGroup, error) {
	q := data.Client(ctx, r.data).MemberProductGroup.UpdateOneID(id)
	if in.Name != "" {
		q.SetName(in.Name)
	}
	if in.ProductIDs != nil {
		q.SetProductIds(in.ProductIDs)
	}
	if in.Discount > 0 {
		q.SetDiscount(in.Discount)
	}
	if in.BadgeStyle != "" {
		q.SetBadgeStyle(in.BadgeStyle)
	}
	q.SetStackMember(in.StackMember).SetStackCoupon(in.StackCoupon)
	if err := q.Exec(ctx); err != nil {
		return nil, err
	}
	return data.Client(ctx, r.data).MemberProductGroup.Get(ctx, id)
}

// DeleteMemberGroup 刪除商品組。
func (r *ProductRepoImpl) DeleteMemberGroup(ctx context.Context, id uint64) error {
	n, err := data.Client(ctx, r.data).MemberProductGroup.Delete().
		Where(memberproductgroup.ID(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("catalog.GROUP_NOT_FOUND")
	}
	return nil
}

// ── 訂單管線折扣解析 ─────────────────────────────────────────

// ResolveGroupDiscount 解析命中商品的會員商品組優惠及疊加策略。
// enabled 語義：discount ∈ (0, 10000)；多組命中取最高折扣（最小 discount 值）。
func (r *ProductRepoImpl) ResolveGroupDiscount(ctx context.Context, productID uint64) (port.GroupDiscount, error) {
	tc := tenancy.FromContext(ctx)
	rows, err := data.Client(ctx, r.data).MemberProductGroup.Query().
		Where(memberproductgroup.SubsiteID(tc.SubsiteID)).
		Order(ent.Asc(memberproductgroup.FieldID)).
		All(ctx)
	if err != nil {
		return port.GroupDiscount{}, err
	}
	var best port.GroupDiscount
	for _, g := range rows {
		if g.Discount <= 0 || g.Discount >= 10000 {
			continue // 無效折扣視為未啓用
		}
		if !slices.Contains(g.ProductIds, productID) {
			continue
		}
		if best.Rate == 0 || g.Discount < best.Rate {
			best = port.GroupDiscount{ID: g.ID, Rate: g.Discount, StackMember: g.StackMember, StackCoupon: g.StackCoupon}
		}
	}
	return best, nil
}
