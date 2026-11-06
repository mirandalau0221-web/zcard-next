package supplier

// wire providers（）。

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	paymentport "github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"

	"github.com/google/wire"
)

// ProviderSet supplier providers。
var ProviderSet = wire.NewSet(
	NewSupplierRepoImpl,
	NewSupplyAPIService,
	NewAdminSupplierService,
	NewStoreSupplierService,
	// target=supply 充值入賬端口（payment 回調消費；SupplierRepoImpl.Recharge 實現）
	wire.Bind(new(paymentport.SupplierRecharger), new(*SupplierRepoImpl)),
	// 依賴經 wire 結構匹配：SupplierCatalog ← catalog（已 Bind）、
	// Inventory/CardContentReader ← inventory、Enqueuer ← bootstrap
)

var _ = port.SupplierCatalog(nil)
