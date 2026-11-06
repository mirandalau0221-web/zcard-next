package affiliate

// wire providers（）。
//
// 跨模塊綁定（通道 A）：
// - engine.wallet ← walletport.Wallet（凍結確認入賬/逆向扣回）
// - engine.settings ← notifyport.SettingsReader（affiliate.affiliate 配置組）
// - CommissionRepo 實現 port.CommissionReader（dashboard 統計）

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/affiliate/port"

	"github.com/google/wire"
)

// ProviderSet affiliate providers。
var ProviderSet = wire.NewSet(
	NewCommissionRepo,
	NewAffiliateService,
	NewStoreAffiliateService,
	wire.Bind(new(port.CommissionReader), new(*CommissionRepo)),
)
