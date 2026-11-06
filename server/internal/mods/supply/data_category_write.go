package supply

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/category"
	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
)

// 同步任務可能緩存了合併前的映射。每次落商品前在渠道鎖內讀取最新設置，
// 與分類合併採用相同鎖序；圖片下載已在調用前完成，不佔用數據庫事務。
func (s *SyncService) writeProductCategory(ctx context.Context, upstreamCategory string, write *catalogport.UpstreamProductInput) (id uint64, created bool, err error) {
	err = data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		c := s.repo.entClient(ctx)
		if err := c.SupplyConnection.UpdateOneID(write.ConnectionID).AddRetryMax(0).Exec(ctx); err != nil {
			return err
		}
		conn, err := c.SupplyConnection.Get(ctx, write.ConnectionID)
		if err != nil {
			return err
		}
		if categoryID, ok := categoryMapFromSettings(conn.Settings)[upstreamCategory]; ok && !write.CategoryProtected {
			write.CategoryID = categoryID
			write.CategorySet = true
		} else if !write.CategoryProtected {
			latest, err := s.repo.GetMapping(ctx, write.ConnectionID, write.UpstreamProductCode, "")
			if err != nil && err != ErrNotFound {
				return err
			}
			if latest != nil && latest.LocalCategoryID > 0 {
				write.CategoryID = latest.LocalCategoryID
				write.CategorySet = true
			}
		}
		id, created, err = s.writer.UpsertUpstreamProduct(ctx, *write)
		return err
	})
	return
}

// 首次下載封面也不能用同步任務啓動時的舊 settings 覆蓋剛保存的分類映射。
func (s *SyncService) saveCoverDirectory(ctx context.Context, connectionID uint64, dir string) (string, error) {
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		c := s.repo.entClient(ctx)
		if err := c.SupplyConnection.UpdateOneID(connectionID).AddRetryMax(0).Exec(ctx); err != nil {
			return err
		}
		conn, err := c.SupplyConnection.Get(ctx, connectionID)
		if err != nil {
			return err
		}
		settings := conn.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		if current, ok := settings["cover_dir"].(string); ok && current != "" {
			dir = current
			return nil
		}
		settings["cover_dir"] = dir
		return c.SupplyConnection.UpdateOneID(connectionID).SetSettings(settings).Exec(ctx)
	})
	return dir, err
}

// 商品寫入與映射寫入之間也可能發生分類合併，保存映射前再以最新分類為準。
func (s *SyncService) saveProductMapping(ctx context.Context, mapping *ent.SupplyMapping) error {
	return data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		c := s.repo.entClient(ctx)
		if err := c.SupplyConnection.UpdateOneID(mapping.ConnectionID).AddRetryMax(0).Exec(ctx); err != nil {
			return err
		}
		conn, err := c.SupplyConnection.Get(ctx, mapping.ConnectionID)
		if err != nil {
			return err
		}
		p, err := c.Product.Get(ctx, mapping.LocalProductID)
		if err != nil && !ent.IsNotFound(err) {
			return err
		}
		if p != nil && p.CategoryProtected {
			mapping.LocalCategoryID = p.CategoryID
		} else if categoryID, ok := categoryMapFromSettings(conn.Settings)[mapping.UpstreamCategory]; ok {
			mapping.LocalCategoryID = categoryID
		} else if mapping.LocalCategoryID > 0 {
			exists, err := c.Category.Query().Where(category.ID(mapping.LocalCategoryID)).Exist(ctx)
			if err != nil {
				return err
			}
			if !exists {
				p, err := c.Product.Get(ctx, mapping.LocalProductID)
				if err != nil {
					return err
				}
				mapping.LocalCategoryID = p.CategoryID
			}
		}
		return s.repo.UpsertMapping(ctx, mapping)
	})
}
