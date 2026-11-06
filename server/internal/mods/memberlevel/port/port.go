// Package port 為 memberlevel 模塊對外契約（零依賴包）。
package port

import "context"

// RateResolver 會員等級折扣解析（order 價格管線步驟 2 消費）。
// 返回應付比例萬分比（9800=支付98%；0/10000=不折扣）+ 命中的等級 ID。
// 無等級/用戶不存在返回 0。
type RateResolver interface {
	EffectiveRate(ctx context.Context, userID uint64) (rate int32, levelID uint64, err error)
}

// StateResolver also reports the source selected by the same calculation.
type StateResolver interface {
	EffectiveState(ctx context.Context, userID uint64) (rate int32, levelID uint64, source string, err error)
}
