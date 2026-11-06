package supply

// 定價策略（）：本地價 = 上游價(分) × 匯率 × (1 + 加價%) → 取整模式。
// 純函數，無 IO，供同步服務與單測直接調用。

import "math"

// 取整模式（對齊《數據庫架構設計.md》 price_rounding_mode）。
const (
	RoundingNone      = "none"       // 不取整（四捨五入到分）
	RoundingCeilInt   = "ceil_int"   // 向上取整到整數元（分 → 整百）
	RoundingCeilTenth = "ceil_tenth" // 向上取整到 0.1 元（分 → 整十）
)

// ApplyPricing 按連接定價參數計算本地價（分）——組合加價：
// 本地價 = 上游價 × 匯率 × (1 + markupPercent%) + markupAmountCents → 取整。
// 固定額與百分比可同時配置（其一為 0 即單模式）；返回值恆 >= 0。
func ApplyPricing(upstreamCents int64, rate, markupPercent float64, markupAmountCents int64, mode string) int64 {
	if upstreamCents <= 0 {
		return 0
	}
	if rate <= 0 {
		rate = 1 // 未配置/非法匯率按 1（連接創建缺省值防禦）
	}
	base := float64(upstreamCents) * rate
	raw := base*(1+markupPercent/100) + float64(markupAmountCents)
	result := roundByMode(raw, mode)
	// 低價商品百分比加價被分錢取整吃掉（如 2 分 ×10% = 2.2 → 2 = 成本價）：
	// 存在加價意圖時至少 +1 分，保證渠道加價始終生效
	if (markupPercent > 0 || markupAmountCents > 0) && result <= roundByMode(base, mode) {
		result = roundByMode(base, mode) + 1
	}
	return result
}

// 導入定價模式（ D：交互式導入的策略選擇；對齊 1.x computeInitialPrice）。
const (
	PriceModeChannel = "channel" // 導入和同步統一使用渠道組合加價
	PriceModePercent = "percent" // 本次導入獨立加價 %
	PriceModeFixed   = "fixed"   // 上游價 + 固定金額（markup_amount 分）
	PriceModeEqual   = "equal"   // 原價（不加價）
	PriceModePending = "pending" // 待定價：不算價、導入後不上架（status=0）
)

// ApplyPricingImport 導入定價（先按模式算基數，再走連接取整）。
// markupAmountCents 僅 fixed 模式使用；pending 返回 0（調用方置 status=0 不上架）。
func ApplyPricingImport(upstreamCents int64, rate, markupPercent float64, markupAmountCents int64, mode, rounding string) int64 {
	if mode == PriceModePending {
		return 0
	}
	if rate <= 0 {
		rate = 1
	}
	base := float64(upstreamCents) * rate
	switch mode {
	case PriceModeFixed:
		raw := base + float64(markupAmountCents)
		return roundByMode(raw, rounding)
	case PriceModeEqual:
		return roundByMode(base, rounding)
	default: // percent
		return ApplyPricing(upstreamCents, rate, markupPercent, 0, rounding)
	}
}

func roundByMode(raw float64, mode string) int64 {
	switch mode {
	case RoundingCeilInt:
		return int64(math.Ceil(raw/100)) * 100
	case RoundingCeilTenth:
		return int64(math.Ceil(raw/10)) * 10
	default:
		return int64(math.Round(raw))
	}
}
