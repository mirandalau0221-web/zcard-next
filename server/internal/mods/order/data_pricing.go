package order

// PriceCalculator 價格管線（ ，規劃 ）。
//
// 順序釘死：基礎價 → 會員折扣 → 商品組折扣 → 秒殺 → 優惠券 → 積分抵扣 → 分站定價 → 數量×單價 → rounding
// 每一步產出一條 order_amount_lines 行（有符號分；折扣為負、加價為正）。
// 管線為純函數——M1a 中 member/coupon/points/reseller 四 port 返回中性值，/ 接真實現。
//
// 不變量：total == SUM(lines.amount) × quantity；行金額在下單落庫時乘數量。

import (
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// AmountLine 價格管線明細行（對應 ent order_amount_lines）。
type AmountLine struct {
	Type       string // base_price / member_discount / group_discount / promo_discount / coupon_discount / points_discount / subsite_markup / fee / rounding_adjust
	Amount     int64  // 有符號分（折扣為負、加價為正）
	SourceType string // member_level / coupon / flash_sale / points / subsite / manual
	SourceID   uint64
	Seq        int32
	Meta       map[string]any
}

// PriceInput 管線輸入。
type PriceInput struct {
	BasePrice     money.Cents // 基礎價（SKU > 商品）
	Quantity      int32
	MemberRate    int32       // 會員應付比例（萬分比；9800=98%；0/10000=不折扣）
	GroupRate     int32       // 商品組應付比例（萬分比；同會員口徑）
	FlashPrice    money.Cents // 秒殺價（0=無秒殺）
	PromoDiscount money.Cents // 促銷折讓（分；0=無促銷——會員折扣後、券前）
	PromoName     string      // 促銷名（金額行 meta）
	CouponValue   money.Cents // 優惠券面額（分；0=無券）
	PointsValue   money.Cents // 積分抵扣額（分）
	SubsiteMarkup money.Cents // 分站加價（分）
	DisplayRate   float64     // 展示幣匯率（換算 rounding 用， 接入）
}

// PriceResult 管線輸出。
type PriceResult struct {
	Lines []AmountLine
	Total money.Cents // 應付 = SUM(單件 lines) × quantity
}

// PriceCalculator 價格管線（純函數，無副作用）。
func PriceCalculator(in PriceInput) PriceResult {
	var lines []AmountLine
	seq := int32(0)
	current := in.BasePrice

	// 1) 基礎價
	lines = append(lines, AmountLine{
		Type: "base_price", Amount: int64(in.BasePrice), Seq: seq,
	})
	seq++

	// 2) 會員等級折扣
	if in.MemberRate > 0 && in.MemberRate < 10000 {
		// 配置是應付比例，折讓取其補數；保留整數分向下取整的折讓規則。
		discount := int64(in.BasePrice) * int64(10000-in.MemberRate) / 10000
		lines = append(lines, AmountLine{
			Type: "member_discount", Amount: -discount, SourceType: "member_level",
			Seq: seq, Meta: map[string]any{"rate": in.MemberRate},
		})
		current -= money.Cents(discount)
		seq++
	}

	// 3) 會員商品組折扣（疊加規則判定 ）
	if in.GroupRate > 0 && in.GroupRate < 10000 {
		discount := int64(current) * int64(10000-in.GroupRate) / 10000
		lines = append(lines, AmountLine{
			Type: "group_discount", Amount: -discount, SourceType: "member_group",
			Seq: seq, Meta: map[string]any{"rate": in.GroupRate},
		})
		current -= money.Cents(discount)
		seq++
	}

	// 4) 限時秒殺價（時間窗內覆蓋基礎價——折扣 = flash - current）
	if in.FlashPrice > 0 && in.FlashPrice < current {
		diff := int64(current - in.FlashPrice)
		lines = append(lines, AmountLine{
			Type: "promo_discount", Amount: -diff, SourceType: "flash_sale",
			Seq: seq, Meta: map[string]any{"flash_price": int64(in.FlashPrice)},
		})
		current = in.FlashPrice
		seq++
	}

	// 4.5) 通用促銷（會員折扣後、券前；多促最優已由解析器裁定；
	// 秒殺互斥：flash 生效時調用方不填 PromoDiscount——口徑寫死並測試）
	if in.PromoDiscount > 0 && in.PromoDiscount < current {
		lines = append(lines, AmountLine{
			Type: "promo_discount", Amount: -int64(in.PromoDiscount), SourceType: "promotion",
			Seq: seq, Meta: map[string]any{"name": in.PromoName},
		})
		current -= in.PromoDiscount
		seq++
	}

	// 5) 優惠券
	if in.CouponValue > 0 {
		discount := int64(in.CouponValue)
		if discount > int64(current) {
			discount = int64(current) // 券不找零
		}
		lines = append(lines, AmountLine{
			Type: "coupon_discount", Amount: -discount, SourceType: "coupon",
			Seq: seq,
		})
		current -= money.Cents(discount)
		seq++
	}

	// 6) 積分抵扣
	if in.PointsValue > 0 {
		discount := int64(in.PointsValue)
		if discount > int64(current) {
			discount = int64(current) // 不超應付
		}
		lines = append(lines, AmountLine{
			Type: "points_discount", Amount: -discount, SourceType: "points",
			Seq: seq,
		})
		current -= money.Cents(discount)
		seq++
	}

	// 7) 分站定價（加價）
	if in.SubsiteMarkup > 0 {
		lines = append(lines, AmountLine{
			Type: "subsite_markup", Amount: int64(in.SubsiteMarkup), SourceType: "subsite",
			Seq: seq,
		})
		current += in.SubsiteMarkup
		seq++
	}

	// 8) 金額行保持單件口徑，調用方落庫時乘數量；總額包含數量。
	unitTotal := current
	total := unitTotal.Mul(in.Quantity)

	// 9) rounding_adjust（多幣種快照， 接入）
	// M1a：基礎貨幣直通，rounding 恆 0

	return PriceResult{Lines: lines, Total: total}
}

// ValidateTotal 不變量斷言：unit SUM(lines) == unit price（不含數量倍增）。
func ValidateTotal(lines []AmountLine) money.Cents {
	var sum money.Cents
	for _, l := range lines {
		sum += money.Cents(l.Amount)
	}
	return sum
}
