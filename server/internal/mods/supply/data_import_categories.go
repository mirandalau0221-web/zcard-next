package supply

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/category"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplymapping"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply/adapter"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

// 保存前先校驗選擇範圍；事務只包含分類和配置寫入，不包含上游請求或圖片下載。
func (s *AdminSupplyService) saveImportCategories(ctx context.Context, req *adminv1.ImportProductsRequest, products map[string]adapter.Product, mode string, percent float64, amount int64) (map[string]uint64, error) {
	tree := map[string]adapter.Category{}
	if req.SnapshotId != "" {
		conn, e := s.repo.GetConnection(ctx, req.ConnectionId)
		if e != nil {
			return nil, e
		}
		row, e := s.snapshotRow(ctx, conn, req.SnapshotId)
		if e != nil {
			return nil, e
		}
		var payload catalogPayload
		if e = json.Unmarshal(row.Payload, &payload); e != nil {
			return nil, e
		}
		if _, e = categoryPaths(payload.Tree); e != nil {
			return nil, e
		}
		for _, node := range payload.Tree {
			tree[node.ID] = node
		}
	}
	selected := map[string]bool{}
	for _, code := range req.Codes {
		p, ok := products[code]
		if !ok {
			return nil, fmt.Errorf("商品 %s 已不可用，請刷新目錄後重試", code)
		}
		selected[p.CategoryID] = true
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("請先選擇商品")
	}
	for code := range req.CategoryMap {
		if !selected[code] {
			return nil, fmt.Errorf("只能保存所選商品涉及的分類映射")
		}
	}
	drafts := map[string]bool{}
	invalidNames := []string{}
	for _, draft := range req.CategoryDrafts {
		if draft == nil || !selected[draft.UpstreamCode] {
			return nil, fmt.Errorf("待新建分類必須對應所選商品")
		}
		if drafts[draft.UpstreamCode] {
			return nil, fmt.Errorf("重複的分類草稿")
		}
		drafts[draft.UpstreamCode] = true
		if _, ok := req.CategoryMap[draft.UpstreamCode]; ok {
			return nil, fmt.Errorf("同一分類不能同時新建和指定映射")
		}
		name := strings.TrimSpace(draft.Name)
		if data.ValidateCategoryName(name) != nil {
			label := []rune(name)
			if len(label) > 40 {
				label = append(label[:40], '…')
			}
			invalidNames = append(invalidNames, fmt.Sprintf("「%s」（上游分類 %s）", string(label), draft.UpstreamCode))
		}
	}
	if len(invalidNames) > 0 {
		return nil, fmt.Errorf("以下分類名稱須為 1–100 個字符：%s", strings.Join(invalidNames, "、"))
	}
	result := map[string]uint64{}
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		client := s.repo.entClient(ctx)
		// 寫鎖串行化同一渠道的提交；重試讀取最新映射，避免重複創建。
		if err := client.SupplyConnection.UpdateOneID(req.ConnectionId).AddRetryMax(0).Exec(ctx); err != nil {
			return err
		}
		conn, err := client.SupplyConnection.Get(ctx, req.ConnectionId)
		if err != nil {
			return err
		}
		settings := conn.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		for k, v := range categoryMapFromSettings(settings) {
			result[k] = v
		}
		mappings, err := client.SupplyMapping.Query().Where(supplymapping.ConnectionID(conn.ID)).All(ctx)
		if err != nil {
			return err
		}
		for _, m := range mappings {
			if _, ok := result[m.UpstreamCategory]; !ok && m.LocalCategoryID > 0 {
				result[m.UpstreamCategory] = m.LocalCategoryID
			}
		}
		tenant := tenancy.FromContext(ctx).SubsiteID
		validate := func(id uint64) error {
			if id == 0 {
				return nil
			}
			exists, err := client.Category.Query().Where(category.ID(id), category.SubsiteID(tenant)).Exist(ctx)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("所選本地分類或父分類已不存在，請刷新後重試")
			}
			return nil
		}
		for code, id := range req.CategoryMap {
			if err := validate(id); err != nil {
				return err
			}
			result[code] = id // 0 是顯式清除，不能回退舊映射。
		}
		for _, draft := range req.CategoryDrafts {
			if len(tree) > 0 && draft.ParentId == 0 {
				id, e := ensurePageCategory(ctx, client, tenant, draft.UpstreamCode, tree, result, strings.TrimSpace(draft.Name))
				if e != nil {
					return e
				}
				result[draft.UpstreamCode] = id
				continue
			}
			if err := validate(draft.ParentId); err != nil {
				return err
			}
			name := strings.TrimSpace(draft.Name)
			q := client.Category.Query().Where(category.SubsiteID(tenant), category.Name(name))
			if draft.ParentId == 0 {
				q.Where(category.Or(category.ParentIDIsNil(), category.ParentID(0)))
			} else {
				q.Where(category.ParentID(draft.ParentId))
			}
			matches, err := q.All(ctx)
			if err != nil {
				return err
			}
			if len(matches) > 1 {
				return fmt.Errorf("同一位置有多個同名分類「%s」，請手動選擇", name)
			}
			if len(matches) == 1 {
				result[draft.UpstreamCode] = matches[0].ID
				continue
			}
			create := client.Category.Create().SetSubsiteID(tenant).SetName(name)
			if draft.ParentId > 0 {
				create.SetParentID(draft.ParentId)
			}
			cat, err := create.Save(ctx)
			if err != nil {
				return err
			}
			result[draft.UpstreamCode] = cat.ID
		}
		for code := range selected {
			if err := validate(result[code]); err != nil {
				return err
			}
		}
		merged := map[string]any{}
		for code, id := range result {
			merged[code] = id
		}
		if !req.SelectedCategoriesOnly {
			settings["category_map"] = merged
		}
		if req.SaveCategoryRules {
			settings["category_rules"] = req.CategoryRules
		}
		if req.SaveDefault {
			settings["import_pricing"] = map[string]any{"mode": mode, "markup_percent": percent, "markup_amount_cents": amount}
		}
		return client.SupplyConnection.UpdateOneID(conn.ID).SetSettings(settings).Exec(ctx)
	})
	return result, err
}
