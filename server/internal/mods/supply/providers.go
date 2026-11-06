package supply

// wire providers（）。

import (
	dashboardport "github.com/NovaWorks/zcard-next/server/internal/mods/dashboard/port"
	orderport "github.com/NovaWorks/zcard-next/server/internal/mods/order/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"

	"github.com/google/wire"
)

// ProviderSet supply providers。
// 跨模塊綁定（通道 A，裝配集中在 bootstrap）：
// - SyncService.writer ← catalogport.UpstreamProductWriter（catalog.ProductRepoImpl）
// - SyncService.outbox ← events.Writer（data.OutboxWriter）
// - Gateway ← supplyport.UpstreamGateway（ procurement 消費）
// - Gateway ← orderport.UpstreamStockGate（ order 下單前庫存預檢消費）
var ProviderSet = wire.NewSet(
	NewSupplyRepoImpl,
	NewSyncService,
	NewAdminSupplyService,
	NewPacer,
	NewScheduler,
	NewGateway,
	NewStoreSMSChannelService,
	wire.Bind(new(port.UpstreamGateway), new(*Gateway)),
	// ：對賬上游數據源端口（dashboard 消費，通道 A）
	wire.Bind(new(dashboardport.UpstreamOrderSource), new(*Gateway)),
	// 下單前上游庫存預檢閘門（order 消費，通道 A）
	wire.Bind(new(orderport.UpstreamStockGate), new(*Gateway)),
)
