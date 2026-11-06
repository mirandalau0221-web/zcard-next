package license

// wire providers（）。

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"

	"github.com/google/wire"
)

// ProviderSet license providers。
var ProviderSet = wire.NewSet(
	ProvideLicenseRepo,
	NewAdminLicenseService,
	// ：專業套餐在線購買（storefront 面）
	NewPurchaseRepo,
	NewStoreLicenseService,
)

// ProvideLicenseRepo 裝配（設置讀寫經 *settings.RepoImpl——通道 A，wire 注入）。
func ProvideLicenseRepo(repo *settings.RepoImpl) *LicenseRepo {
	return NewLicenseRepo(repo)
}
