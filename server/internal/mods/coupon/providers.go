package coupon

// wire providers（）。
// 三端口綁定（order 管線消費，通道 A）：
// - CouponResolver（範圍矩陣券）/ FlashResolver（秒殺同鎖）/ PromotionResolver（促銷最優）

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"

	"github.com/google/wire"
)

// ProviderSet coupon providers。
var ProviderSet = wire.NewSet(
	NewCouponRepoImpl,
	wire.Bind(new(port.CouponResolver), new(*CouponRepoImpl)),
	wire.Bind(new(port.FlashResolver), new(*CouponRepoImpl)),
	wire.Bind(new(port.PromotionResolver), new(*CouponRepoImpl)),
	NewAdminCouponService,
	NewStoreCouponService,
)
