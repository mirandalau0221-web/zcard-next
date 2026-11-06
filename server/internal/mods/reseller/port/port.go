// Package port 為 reseller 模塊對外契約（零依賴包）。
package port

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// Pricer 分站定價窄接口（order 價格管線步驟 7 消費，通道 A）。
// listing 與 checkout 共用同一 ResolveUnitPrice（1.x 鐵律——分站價只在一處計算）；
// ProfitEligible 為下單時的防自購快照判定（落 orders.profit_eligible）。
type Pricer interface {
	// ResolveUnitPrice 分站單價：SKU 規則 > 商品規則 > 分站默認加價率 > 繼承主站價；
	// 4 模式換算 + 下限保護（不得低於主站基礎價）。
	ResolveUnitPrice(ctx context.Context, subsiteID, productID, skuID uint64, basePrice money.Cents) (money.Cents, error)
	// ProfitEligible 防自購三查：買家==分站主 / 買家∈分站主上級鏈 / 反向命中 → false。
	ProfitEligible(ctx context.Context, subsiteID, buyerID uint64) bool
}
