package inventory

// wire providers（CardCipher 由 bootstrap 按密鑰解析構造）。

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory/port"

	"github.com/google/wire"
)

// ProviderSet inventory providers。
var ProviderSet = wire.NewSet(
	NewInventoryUsecase,
	NewCardRepoImpl,
	wire.Bind(new(CardRepo), new(*CardRepoImpl)),
	wire.Bind(new(port.Inventory), new(*CardRepoImpl)),
	// ：供貨交付卡密讀取 + 失敗回滾（supplier 消費，通道 A）
	wire.Bind(new(port.CardContentReader), new(*CardRepoImpl)),
	wire.Bind(new(port.CardReleaser), new(*CardRepoImpl)),
	// 管理列表批量庫存（catalog 消費，通道 A）
	wire.Bind(new(port.StockBatcher), new(*CardRepoImpl)),
	NewAdminInventoryService,
)
