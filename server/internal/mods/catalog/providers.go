package catalog

// wire providers。

import (
	"context"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"

	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"

	"github.com/google/wire"
)

// settingsReaderAdapter settings.RepoImpl → port.SettingsReader 適配
// （低庫存閾值讀取；讀失敗走默認值，不阻斷商品列表）。
type settingsReaderAdapter struct{ repo *settings.RepoImpl }

func (a settingsReaderAdapter) GetJSON(ctx context.Context, group, key string) ([]byte, error) {
	raw, err := a.repo.GetDefault(ctx, group, key, nil)
	if err != nil {
		return nil, nil
	}
	return raw, nil
}

// ProvideSettingsReader settings 適配為通用端口（通道 A）。
func ProvideSettingsReader(repo *settings.RepoImpl) port.SettingsReader {
	return settingsReaderAdapter{repo: repo}
}

// ProviderSet catalog providers。
var ProviderSet = wire.NewSet(
	NewCatalogUsecase,
	NewProductRepoImpl,
	ProvideSettingsReader, // port.SettingsReader 綁定（低庫存閾值等）
	wire.Bind(new(ProductRepo), new(*ProductRepoImpl)),
	wire.Bind(new(port.PricingResolver), new(*ProductRepoImpl)),
	// ：貨源同步商品 upsert 端口綁定（supply 模塊消費，通道 A）
	wire.Bind(new(port.UpstreamProductWriter), new(*ProductRepoImpl)),
	// S1：貨源輕量維護端口（price/status scope + 刪除對賬）
	wire.Bind(new(port.UpstreamProductMaintainer), new(*ProductRepoImpl)),
	// ：商品讀取端口（procurement 消費，通道 A）
	wire.Bind(new(port.ProductReader), new(*ProductRepoImpl)),
	// ：供貨目錄端口（supplier 消費，通道 A）
	wire.Bind(new(port.SupplierCatalog), new(*ProductRepoImpl)),
	NewStoreCatalogService,
	NewStoreReviewService,
	wire.Bind(new(storefrontv1.StoreCatalogServiceServer), new(*StoreCatalogService)),
	NewAdminCatalogService,
)
