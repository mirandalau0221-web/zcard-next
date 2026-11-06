package catalog

import (
	"context"
	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/category"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
)

// BatchUpdateProductCategory 全部成功或全部回滾，只修改本站未刪除商品的分類。
func (s *AdminCatalogService) BatchUpdateProductCategory(ctx context.Context, req *adminv1.BatchUpdateProductCategoryRequest) (*adminv1.BatchUpdateProductCategoryReply, error) {
	if len(req.GetIds()) == 0 || len(req.GetIds()) > 1000 || req.GetCategoryId() == 0 {
		return nil, errors.BadRequest("catalog.BATCH_CATEGORY_INVALID", "請選擇 1～1000 件商品和目標分類")
	}
	ids := make([]uint64, 0, len(req.Ids))
	seen := make(map[uint64]bool)
	for _, id := range req.Ids {
		if id == 0 {
			return nil, errors.BadRequest("catalog.BATCH_CATEGORY_INVALID", "商品編號無效，請刷新後重試")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	var skipped, updated int32
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		c := data.Client(ctx, s.repo.data)
		subsite := tenancy.FromContext(ctx).SubsiteID
		// 條件寫鎖定分類，避免校驗後被刪除；不改變分類排序或可見性。
		n, err := c.Category.Update().Where(category.ID(req.CategoryId), category.SubsiteID(subsite)).AddSort(0).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.BadRequest("catalog.CATEGORY_NOT_FOUND", "目標分類不存在，請刷新分類後重試")
		}
		rows, count, err := editableBatch(ctx, s.repo.data, ids)
		if err != nil {
			return err
		}
		skipped = count
		for _, p := range rows {
			if err = c.Product.UpdateOneID(p.ID).SetCategoryID(req.CategoryId).SetCategoryProtected(true).Exec(ctx); err != nil {
				return err
			}
			updated++
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return &adminv1.BatchUpdateProductCategoryReply{Updated: updated, SkippedLocked: skipped}, nil
}
