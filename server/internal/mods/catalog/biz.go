// Package catalog 商品目錄模塊（M1）：商品/SKU/分類/標籤/控件/虛擬數據/會員商品組。
//
// M0 骨架打通「租户上下文 → Ent 查詢 → 薄 service」鏈路；價格計算管線
// （PriceCalculator，§5.2）屬 order 模塊，catalog 只提供基礎價與庫存類型。
package catalog

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
)

// ProductRepo 商品倉儲（模塊內端口，實現於 data.go）。
type ProductRepo interface {
	StockBatch(context.Context, []uint64) (map[uint64]int64, error)
	StockSnapshotBatch(context.Context, []uint64) (map[uint64]port.StockSnapshot, error)
	ListVisible(ctx context.Context, f port.VisibleFilter) ([]port.Product, int64, error)
	Get(ctx context.Context, subsiteID, id uint64) (*port.Product, error)
	ListControls(ctx context.Context, productID uint64) ([]port.Control, error)
	ListProductReviews(ctx context.Context, productID uint64) ([]port.ReviewItem, error)
	ListSkus(ctx context.Context, productID uint64) ([]port.Sku, error)
	ListVisibleCategories(ctx context.Context, subsiteID uint64) ([]port.Category, error)
}

// CatalogUsecase 目錄用例。
type CatalogUsecase struct {
	repo ProductRepo
}

// NewCatalogUsecase 構造。
func NewCatalogUsecase(repo ProductRepo) *CatalogUsecase { return &CatalogUsecase{repo: repo} }

// ListVisible 顧客可見商品列表（上架 + 租户過濾）。
func (uc *CatalogUsecase) ListVisible(ctx context.Context, f port.VisibleFilter) ([]port.Product, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 100 {
		f.PageSize = 20
	}
	return uc.repo.ListVisible(ctx, f)
}

// ListAllVisible 全量上架商品（sales 內存排序用；上架商品量可控不分頁）。
func (uc *CatalogUsecase) ListAllVisible(ctx context.Context, f port.VisibleFilter) ([]port.Product, error) {
	f.Page, f.PageSize = 0, 0
	items, _, err := uc.repo.ListVisible(ctx, f)
	return items, err
}

// ListVisibleCategories 可見分類（導航/篩選用）。
func (uc *CatalogUsecase) ListVisibleCategories(ctx context.Context, subsiteID uint64) ([]port.Category, error) {
	return uc.repo.ListVisibleCategories(ctx, subsiteID)
}

// GetVisible 商品詳情；下架/隱藏商品對顧客返回 NOT_FOUND（§5.2 必測項）。
func (uc *CatalogUsecase) GetVisible(ctx context.Context, subsiteID, id uint64) (*port.Product, error) {
	p, err := uc.repo.Get(ctx, subsiteID, id)
	if err != nil {
		return nil, err
	}
	// status: 1=上架（遊客+會員可見）；2=隱藏（僅會員可見，M1 接會員上下文後放開）
	if p.Status != 1 {
		claims := identity.ClaimsFromContext(ctx)
		if p.ProductKind != "sms_channel" || subsiteID != 0 || p.Status != 2 || claims == nil || claims.Subject == 0 || claims.Realm != authn.RealmUser {
			return nil, ErrProductNotFound
		}
	}
	return p, nil
}

// ListControls 商品自定義控件（下單表單渲染）。
func (uc *CatalogUsecase) ListControls(ctx context.Context, productID uint64) ([]port.Control, error) {
	return uc.repo.ListControls(ctx, productID)
}

// ListProductReviews 商品評價（真實 approved + 虛擬合併）。
func (uc *CatalogUsecase) ListProductReviews(ctx context.Context, productID uint64) ([]port.ReviewItem, error) {
	return uc.repo.ListProductReviews(ctx, productID)
}

// ListSkus 商品多規格（前台只下發 id/名稱/價格）。
func (uc *CatalogUsecase) ListSkus(ctx context.Context, productID uint64) ([]port.Sku, error) {
	return uc.repo.ListSkus(ctx, productID)
}
