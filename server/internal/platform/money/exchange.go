package money

// 展示層與下單快照的跨幣種換算（規劃 §5.1 / §3.4）：
// 記賬永遠是基礎貨幣 Cents（默認 CNY，最小單位=分，即 2 位小數）；
// 換算只發生在展示與快照，中間量用 decimal 後取整，
// 展示價與記賬價的差由調用方記入 rounding_adjust（禁止 float 中轉）。

import (
	"github.com/shopspring/decimal"
)

// DefaultDisplayPrecision CNY/USD 等常用貨幣的小數位。
const DefaultDisplayPrecision = 2

// baseScale 基礎貨幣最小單位（分 = 10^-2 元）。
var baseScale = decimal.NewFromInt(100)

// Exchange 單筆換算結果（快照用）。
type Exchange struct {
	// Rate 換算匯率快照（1 基礎貨幣 = Rate 目標幣；與 currencies.rate 同義）
	Rate decimal.Decimal
	// DisplayAmount 目標幣金額（以該幣最小單位計的整數，如 USD cent）
	DisplayAmount int64
	// Rounded 往返取捨量（基礎貨幣分；記入 order_amount_lines 的 rounding_adjust）
	Rounded Cents
}

// displayUnits 計算目標幣最小單位數：amount(分) ÷ 100 × rate × 10^precision，四捨五入。
func displayUnits(amount int64, rate decimal.Decimal, precision int32) int64 {
	scale := decimal.New(1, precision)
	return decimal.NewFromInt(amount).
		Div(baseScale).
		Mul(rate).
		Mul(scale).
		Round(0).
		IntPart()
}

// baseFromUnits 反算基礎貨幣分：units ÷ 10^precision ÷ rate × 100，四捨五入。
func baseFromUnits(units int64, rate decimal.Decimal, precision int32) int64 {
	scale := decimal.New(1, precision)
	return decimal.NewFromInt(units).
		Div(scale).
		Div(rate).
		Mul(baseScale).
		Round(0).
		IntPart()
}

// safeRate 匯率缺失/非法時 1:1 直通（寧可不錯換，調用方以基礎貨幣口徑繼續）。
func safeRate(rate decimal.Decimal) decimal.Decimal {
	if rate.IsZero() || rate.IsNegative() {
		return decimal.NewFromInt(1)
	}
	return rate
}

// ToDisplay 基礎貨幣分 → 展示幣金額：
// display = (amount ÷ 100) × rate，四捨五入到 precision 位小數（最小單位整數）；
// Rounded = 回算(round(display ÷ rate × 100)) − amount（對賬容差，§5.1 回調核對 ± rounding）。
func ToDisplay(amount Cents, rate decimal.Decimal, precision int32) Exchange {
	if precision < 0 {
		precision = DefaultDisplayPrecision
	}
	rate = safeRate(rate)
	units := displayUnits(int64(amount), rate, precision)
	return Exchange{
		Rate:          rate,
		DisplayAmount: units,
		Rounded:       Cents(baseFromUnits(units, rate, precision)) - amount,
	}
}

// FromDisplay 展示幣金額 → 基礎貨幣分（下單快照反向）。
// 返回 (基礎分, rounding)——rounding = 取整值 − 精確值（記 amount_lines）。
func FromDisplay(units int64, rate decimal.Decimal, precision int32) (Cents, Cents) {
	if precision < 0 {
		precision = DefaultDisplayPrecision
	}
	rate = safeRate(rate)
	base := baseFromUnits(units, rate, precision)
	exact := baseFromUnitsExact(units, rate, precision)
	return Cents(base), Cents(base) - Cents(exact)
}

func baseFromUnitsExact(units int64, rate decimal.Decimal, precision int32) int64 {
	scale := decimal.New(1, precision)
	return decimal.NewFromInt(units).
		Div(scale).
		Div(rate).
		Mul(baseScale).
		Truncate(0).
		IntPart()
}
