// Package port 為 settings 模塊對外契約（零依賴包）。
package port

import (
	"context"
	"encoding/json"
)

// Item 設置項。
type Item struct {
	Group string
	Key   string
	Value json.RawMessage
}

// Provider 設置讀取窄接口（各模塊啓動/運行時讀取業務開關的唯一入口，
// 鐵律 7：運行時業務開關在 settings 表，env/config 只作首次部署兜底）。
type Provider interface {
	Get(ctx context.Context, group, key string) (json.RawMessage, error)
	// GetDefault 讀取並綁定到默認值結構體（不存在時返回 def 原值）。
	GetDefault(ctx context.Context, group, key string, def json.RawMessage) (json.RawMessage, error)
}

// CurrencyReader 貨幣讀取（展示換算取數端， exchange 消費；rate 為 decimal 字符串）。
type CurrencyReader interface {
	CurrencyByCode(ctx context.Context, code string) (rate string, precision int32, err error)
}
