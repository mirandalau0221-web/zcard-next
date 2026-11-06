// Package port 為 coupon 模塊對外契約（零依賴包）。
package port

import (
	"context"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// CartItem 管線購物車行（券範圍判定輸入）。
type CartItem struct {
	SkuID      uint64
	ProductID  uint64
	CategoryID uint64
	Quantity   int32
	UnitPrice  money.Cents // 券前折後單價，不含分站加價；調用方剔除禁止用券的行
}

// CouponResolver 優惠券解析（order 價格管線步驟 5 消費）。
type CouponResolver interface {
	// ResolveScoped 校驗券（存在/unused/未過期/用户匹配/範圍矩陣/每人限用）
	// 並返回面額（分；percent 按訂單應付折算，不找零）。
	ResolveScoped(ctx context.Context, code string, userID, levelID uint64, items []CartItem) (value money.Cents, couponID uint64, err error)
	// MarkUsed 核銷券（下單事務內回填 used_order_id/used_at）。
	MarkUsed(ctx context.Context, couponID, orderID uint64) error
	// ReturnByOrder 返還券（取消/退款；過期不返——行恢復 unused 並清訂單回填）。
	ReturnByOrder(ctx context.Context, orderID uint64) error
}

// FlashInfo 生效秒殺（管線步驟 4 輸入）。
type FlashKey struct{ ProductID, SkuID uint64 }

// FlashReader batches display offers without consuming campaign quantities.
type FlashReader interface {
	ActiveBatch(context.Context, []uint64) (map[FlashKey]*FlashInfo, error)
}

type FlashInfo struct {
	EndAt        time.Time
	Remaining    int32
	ID           uint64
	FlashPrice   money.Cents
	StartAt      time.Time // 限購累計窗口起點
	PerUserLimit int32
}

// FlashResolver 秒殺解析（order 價格管線步驟 4 + 同鎖釦減消費）。
type FlashResolver interface {
	// Reserve 僅預佔待付款額度，不計已售；和訂單快照同事務提交。
	Reserve(ctx context.Context, flashID uint64, qty int32) error
	// Confirm 支付成功後將預佔轉為已售，和支付狀態同事務提交。
	Confirm(ctx context.Context, flashID uint64, qty int32) error
	// Release 取消或超時後釋放預佔，不修改已售。
	Release(ctx context.Context, flashID uint64, qty int32) error
	// Active 生效中秒殺（窗口判定無狀態；無則返回 nil）。
	Active(ctx context.Context, productID, skuID uint64) (*FlashInfo, error)
	// Consume 直接扣減（保留兼容；新下單使用 Reserve/Confirm；
	// CAS：sold_qty+qty<=limit_qty，affected==0 → ErrFlashSoldOut）。
	Consume(ctx context.Context, flashID uint64, qty int32) error
	// UserPurchasedCount 用户在秒殺窗口內已購數量（paid+pending 累計，限購判據）。
	UserPurchasedCount(ctx context.Context, productID, userID uint64, since time.Time) (int32, error)
}

// ErrFlashSoldOut 秒殺限量不足（哨兵：事務內回滾）。
var ErrFlashSoldOut = errNew("coupon: 秒殺已搶完")

var ErrFlashReserved = errNew("coupon: 秒殺名額暫被待付款訂單佔用，請稍後重試")

// ErrFlashUserLimit 秒殺限購（哨兵）。
var ErrFlashUserLimit = errNew("coupon: 秒殺限購")

// PromotionInfo 生效促銷（管線：會員折扣後、券前；多促取最優）。
type PromotionInfo struct {
	ID           uint64
	Type         string // fixed | percent | special_price
	Threshold    money.Cents
	Discount     money.Cents // fixed 面額（分）
	DiscountRate int32       // percent 應付比例（萬分比；9500=9.5折）
	SpecialPrice money.Cents
	Name         string
}

// PromotionResolver 促銷解析。
type PromotionResolver interface {
	// BestFor 取商品命中的最優促銷（同時窗多促取折讓最大；無則 nil）。
	BestFor(ctx context.Context, productID, categoryID uint64, unitPrice money.Cents) (*PromotionInfo, error)
}

// DiscountFor 促銷折讓（fixed=面額；percent=價×(1-應付比例)；special_price=價-特價）。
// 純函數——管線與測試共用同一口徑。
func (p *PromotionInfo) DiscountFor(unitPrice money.Cents) money.Cents {
	switch p.Type {
	case "fixed":
		return p.Discount
	case "percent":
		if p.DiscountRate > 0 && p.DiscountRate <= 10000 {
			return money.Cents(int64(unitPrice) * int64(10000-p.DiscountRate) / 10000)
		}
	case "special_price":
		if p.SpecialPrice >= 0 && p.SpecialPrice < unitPrice {
			return unitPrice - p.SpecialPrice
		}
	}
	return 0
}

func errNew(s string) error { return &sentinel{s: s} }

type sentinel struct{ s string }

func (e *sentinel) Error() string { return e.s }
