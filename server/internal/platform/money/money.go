// Package money 定義全倉唯一金額類型（鐵律 1）：
// 金額一律 int64「分」，基於基礎貨幣（默認 CNY）；存儲與傳遞永不出現浮點。
// 跨幣種換算僅發生在展示層與下單快照，中間過程用 decimal 庫（shopspring）後取整。
package money

import (
	"errors"
	"fmt"
	"strings"
)

// Cents 金額（分，基礎貨幣）。禁止定義第二個金額類型（架構測試 §4.10-6）。
type Cents int64

// Zero 零元。
const Zero Cents = 0

// ErrInvalidAmount 金額非法（解析失敗/精度越界/負數越界場景）。
var ErrInvalidAmount = errors.New("money: invalid amount")

// MaxCents 單筆金額上限（1 億元 = 10^10 分）。管理面提交金額的服務端邊界
// 校驗口徑（鐵律 16）：客戶端可提交的金額必須落在 [0, MaxCents]（有符號口徑
// 為 ±MaxCents），超限即拒絕——抓包改金額只能在允許區間內取值。
const MaxCents int64 = 10_000_000_00

// ValidCents 非負且不超上限（價格/充值等非負金額口徑）。
func ValidCents(v int64) bool { return v >= 0 && v <= MaxCents }

// ValidSignedCents 有符號金額（調賬/賬本口徑；絕對值不超上限）。
func ValidSignedCents(v int64) bool { return v >= -MaxCents && v <= MaxCents }

// Add 返回 a+b（不改變原值；金額是不可變語義）。
func (c Cents) Add(other Cents) Cents { return c + other }

// Sub 返回 a-b。
func (c Cents) Sub(other Cents) Cents { return c - other }

// Mul 返回 a*n（數量乘單價；n 為非負數量）。
func (c Cents) Mul(n int32) Cents { return c * Cents(n) }

// Neg 返回 -a（行式金額模型中折扣為負值的構造入口）。
func (c Cents) Neg() Cents { return -c }

// IsNegative 是否為負。
func (c Cents) IsNegative() bool { return c < 0 }

// IsZero 是否為零。
func (c Cents) IsZero() bool { return c == 0 }

// String 按「分」輸出（不做幣種格式化；展示層用 Format）。
func (c Cents) String() string { return fmt.Sprintf("%d", int64(c)) }

// Format 按指定小數位輸出十進制字符串（純整數運算，無浮點參與）。
// precision 為貨幣小數位（CNY=2、JPY=0）；四捨五入模式由調用方在換算入口決定。
func (c Cents) Format(precision int) string {
	if precision < 0 {
		precision = 0
	}
	neg := c < 0
	if neg {
		c = -c
	}
	base := int64(1)
	for i := 0; i < precision; i++ {
		base *= 10
	}
	whole := int64(c) / base
	frac := int64(c) % base
	s := fmt.Sprintf("%d", whole)
	if precision > 0 {
		s += "." + fmt.Sprintf("%0*d", precision, frac)
	}
	if neg {
		s = "-" + s
	}
	return s
}

// ParseDecimalStr 從十進制字符串解析為分（如 "12.34" → 1234）。
// 禁止 float 中轉；precision 為字符串允許的小數位上限（越界報錯，不靜默截斷）。
func ParseDecimalStr(s string, precision int) (Cents, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: empty string", ErrInvalidAmount)
	}
	neg := false
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if intPart == "" {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}
	if hasFrac && (fracPart == "" || len(fracPart) > precision) {
		return 0, fmt.Errorf("%w: fraction %q exceeds precision %d", ErrInvalidAmount, fracPart, precision)
	}
	var cents int64
	for _, ch := range []byte(intPart) {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
		}
		cents = cents*10 + int64(ch-'0')
	}
	for i := 0; i < precision; i++ {
		cents *= 10
	}
	if hasFrac {
		for i, ch := range []byte(fracPart) {
			digit := int64(ch - '0')
			// 逐位放到對應精度位（如 precision=2："34" → 3*10+4）
			cents += digit * pow10(int64(precision-i-1))
		}
	}
	if neg {
		cents = -cents
	}
	return Cents(cents), nil
}

func pow10(n int64) int64 {
	v := int64(1)
	for i := int64(0); i < n; i++ {
		v *= 10
	}
	return v
}
