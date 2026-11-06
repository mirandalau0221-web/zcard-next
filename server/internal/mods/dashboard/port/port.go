// Package port 為 dashboard 模塊對外契約（零依賴包）。
package port

import (
	"context"
	"errors"
	"time"
)

// ErrUpstreamListUnsupported 上游不支持訂單列表（對賬 job 置 failed 的可查原因）。
var ErrUpstreamListUnsupported = errors.New("上游協議不支持訂單列表對賬")

// UpstreamOrder 上游訂單（對賬比對側數據；金額分）。
type UpstreamOrder struct {
	UpstreamOrderID string
	Amount          int64
	Status          string
}

// UpstreamOrderSource 上游訂單數據源（對賬引擎消費，通道 A）。
// supply.Gateway 實現（按連接構建 adapter 並轉換為本類型）；測試注入假實現覆蓋四態。
type UpstreamOrderSource interface {
	// ListOrders 時間窗內上游訂單（不支持返回 ErrUpstreamListUnsupported）。
	ListOrders(ctx context.Context, connectionID uint64, start, end time.Time) ([]UpstreamOrder, error)
}

// SettingsReader 系統設置讀取（通道 A：settings.RepoImpl 適配；工作台低庫存閾值等）。
type SettingsReader interface {
	// GetJSON 讀取分組配置（讀取失敗返回 nil, nil，調用方走默認值）。
	GetJSON(ctx context.Context, group, key string) ([]byte, error)
}
