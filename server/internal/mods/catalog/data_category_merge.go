package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	placement "github.com/NovaWorks/zcard-next/server/internal/data/ent/categoryproductplacement"
	"strconv"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/category"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplyconnection"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplymapping"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

func (s *AdminCatalogService) MergeCategories(ctx context.Context, req *adminv1.MergeCategoriesRequest) (*adminv1.MergeCategoriesReply, error) {
	return s.repo.mergeCategories(ctx, req)
}

// 預覽和提交共用校驗；商品、子分類、渠道配置與映射在同一事務內遷移。
func (r *ProductRepoImpl) mergeCategories(ctx context.Context, req *adminv1.MergeCategoriesRequest) (*adminv1.MergeCategoriesReply, error) {
	reply := &adminv1.MergeCategoriesReply{}
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		if e := lockCategoryStructure(ctx, r.data); e != nil {
			return e
		}
		c := data.Client(ctx, r.data)
		sources := map[uint64]bool{}
		for _, id := range req.SourceIds {
			if id > 0 {
				sources[id] = true
			}
		}
		if len(sources) == 0 || req.TargetId == 0 || sources[req.TargetId] {
			return fmt.Errorf("請選擇來源分類和不同的目標分類")
		}
		ids := make([]uint64, 0, len(sources))
		for id := range sources {
			ids = append(ids, id)
		}
		tenant := tenancy.FromContext(ctx).SubsiteID
		cats, err := c.Category.Query().Where(category.SubsiteID(tenant)).All(ctx)
		if err != nil {
			return err
		}
		parents := map[uint64]uint64{}
		for _, cat := range cats {
			parents[cat.ID] = cat.ParentID
		}
		if _, ok := parents[req.TargetId]; !ok {
			return fmt.Errorf("目標分類不存在")
		}
		for id := range sources {
			if _, ok := parents[id]; !ok {
				return fmt.Errorf("來源分類已變化，請刷新後重試")
			}
		}
		seen := map[uint64]bool{}
		for id := req.TargetId; id != 0; id = parents[id] {
			if sources[id] || seen[id] {
				return fmt.Errorf("不能合併到來源分類的子分類中")
			}
			seen[id] = true
		}
		// 分類優惠直接改成目標會擴大優惠範圍，因此要求先由運營調整活動。
		referenced := func(scope map[string]any) bool {
			raw, _ := json.Marshal(scope["category_ids"])
			var values []any
			_ = json.Unmarshal(raw, &values)
			for _, v := range values {
				id, _ := strconv.ParseUint(fmt.Sprint(v), 10, 64)
				if sources[id] {
					return true
				}
			}
			return false
		}
		coupons, err := c.Coupon.Query().All(ctx)
		if err != nil {
			return err
		}
		for _, row := range coupons {
			if referenced(row.Scope) {
				return fmt.Errorf("來源分類被優惠券引用，請先調整優惠券適用範圍")
			}
		}
		promotions, err := c.Promotion.Query().All(ctx)
		if err != nil {
			return err
		}
		for _, row := range promotions {
			if referenced(row.Scope) {
				return fmt.Errorf("來源分類被促銷活動引用，請先調整活動適用範圍")
			}
		}
		products, err := c.Product.Query().Where(product.CategoryIDIn(ids...), product.SubsiteID(tenant)).Count(ctx)
		if err != nil {
			return err
		}
		children, err := c.Category.Query().Where(category.ParentIDIn(ids...), category.IDNotIn(ids...), category.SubsiteID(tenant)).Count(ctx)
		if err != nil {
			return err
		}
		reply.Categories = int32(len(ids))
		reply.Products = int32(products)
		reply.Children = int32(children)
		if e := r.guardCategoryProducts(ctx, ids); e != nil {
			return e
		}
		// Source placements require an explicit decision; never silently promote them to the target.
		has, e := c.CategoryProductPlacement.Query().Where(placement.CategoryIDIn(ids...), placement.SubsiteID(tenant)).Exist(ctx)
		if e != nil {
			return e
		}
		if has {
			return fmt.Errorf("來源分類設置了置頂或推薦，請先在置頂與推薦面板移除配置，再合併分類")
		}
		if req.Preview {
			return nil
		}
		conns, err := c.SupplyConnection.Query().Order(ent.Asc(supplyconnection.FieldID)).All(ctx)
		if err != nil {
			return err
		}
		for _, conn := range conns {
			// 獲取最新配置後修改，避免覆蓋其他渠道設置。
			if err := c.SupplyConnection.UpdateOneID(conn.ID).AddRetryMax(0).Exec(ctx); err != nil {
				return err
			}
			fresh, err := c.SupplyConnection.Get(ctx, conn.ID)
			if err != nil {
				return err
			}
			mapping, _ := fresh.Settings["category_map"].(map[string]any)
			changed := false
			for code, v := range mapping {
				id, _ := strconv.ParseUint(fmt.Sprint(v), 10, 64)
				if sources[id] {
					mapping[code] = req.TargetId
					changed = true
				}
			}
			if changed {
				if err := c.SupplyConnection.UpdateOneID(conn.ID).SetSettings(fresh.Settings).Exec(ctx); err != nil {
					return err
				}
			}
		}
		if _, err = c.Product.Update().Where(product.CategoryIDIn(ids...), product.SubsiteID(tenant)).SetCategoryID(req.TargetId).Save(ctx); err != nil {
			return err
		}
		if _, err = c.Category.Update().Where(category.ParentIDIn(ids...), category.IDNotIn(ids...), category.SubsiteID(tenant)).SetParentID(req.TargetId).Save(ctx); err != nil {
			return err
		}
		if _, err = c.SupplyMapping.Update().Where(supplymapping.LocalCategoryIDIn(ids...)).SetLocalCategoryID(req.TargetId).Save(ctx); err != nil {
			return err
		}
		// 先解除來源之間父子引用，再統一刪除；未選中的子分類已遷移。
		if _, err = c.Category.Update().Where(category.IDIn(ids...)).ClearParentID().Save(ctx); err != nil {
			return err
		}
		_, err = c.Category.Delete().Where(category.IDIn(ids...), category.SubsiteID(tenant)).Exec(ctx)
		return err
	})
	return reply, err
}
