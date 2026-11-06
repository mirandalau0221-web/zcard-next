// Package bootstrap 模塊裝配（友商模式：模塊互不感知，裝配集中於此）。
//
// 跨模塊綁定（通道 A：消費方接口 ← 被調方 port 實現）只發生在這裏；
// 模塊內部 wire 裝配見各模塊 providers.go。
package bootstrap

import (
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/mods/affiliate"
	"github.com/NovaWorks/zcard-next/server/internal/mods/audit"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
	"github.com/NovaWorks/zcard-next/server/internal/mods/captcha"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog"
	"github.com/NovaWorks/zcard-next/server/internal/mods/content"
	"github.com/NovaWorks/zcard-next/server/internal/mods/coupon"
	"github.com/NovaWorks/zcard-next/server/internal/mods/dashboard"
	"github.com/NovaWorks/zcard-next/server/internal/mods/fulfillment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/mods/license"
	"github.com/NovaWorks/zcard-next/server/internal/mods/lottery"
	"github.com/NovaWorks/zcard-next/server/internal/mods/media"
	"github.com/NovaWorks/zcard-next/server/internal/mods/memberlevel"
	"github.com/NovaWorks/zcard-next/server/internal/mods/notify"
	"github.com/NovaWorks/zcard-next/server/internal/mods/order"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/procurement"
	"github.com/NovaWorks/zcard-next/server/internal/mods/reseller"
	"github.com/NovaWorks/zcard-next/server/internal/mods/seo"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supplier"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply"
	"github.com/NovaWorks/zcard-next/server/internal/mods/ticket"
	"github.com/NovaWorks/zcard-next/server/internal/mods/update"
	"github.com/NovaWorks/zcard-next/server/internal/mods/wallet"

	"github.com/google/wire"
)

// ProviderSet 全量業務裝配（wire.Build 的聚合入口）。
// 里程碑推進時在此追加模塊（procurement/supplier/...），模塊間窄接口綁定也在此登記。
var ProviderSet = wire.NewSet(
	data.ProviderSet,
	securityProviderSet,
	queueProviderSet,
	idProviderSet, // 密鑰解析（env 優先 → conf 兜底 → dev 隨機告警）
	identity.ProviderSet,
	authz.ProviderSet,
	settings.ProviderSet,
	captcha.ProviderSet,
	catalog.ProviderSet,
	inventory.ProviderSet,
	order.ProviderSet,
	payment.ProviderSet,
	wallet.ProviderSet,
	// 佣金提現打通：wallet.CommissionSource ← affiliate.CommissionRepo（通道 A 綁定）
	wire.Bind(new(wallet.CommissionSource), new(*affiliate.CommissionRepo)),
	fulfillment.ProviderSet,
	memberlevel.ProviderSet,
	coupon.ProviderSet,
	lottery.ProviderSet,
	dashboard.ProviderSet,
	supply.ProviderSet,
	procurement.ProviderSet,
	supplier.ProviderSet,
	content.ProviderSet,
	notify.ProviderSet,
	audit.ProviderSet,
	ticket.ProviderSet,
	affiliate.ProviderSet,
	media.ProviderSet,
	license.ProviderSet,
	reseller.ProviderSet,
	seo.ProviderSet,
	update.ProviderSet,
	// M1 預告：order ↔ payment 破環點綁定（payment.OrderLifecycle ← order 實現）
	// M3 預告：affiliate/reseller/ticket/notify/media/audit
)
