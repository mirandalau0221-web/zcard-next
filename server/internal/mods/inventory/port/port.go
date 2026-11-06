// Package port 為 inventory 模塊對外契約（零依賴包）。
package port

import (
	"context"
	"time"
)

// ReserveItem 鎖卡請求項。
type ReserveItem struct {
	ProductID uint64
	SkuID     uint64 // 0 = 商品級
	Quantity  int32
	// 靚號自選卡號歸一化 hash（非靚號場景為空）
	NumberHash string
}

// ReservedCard 已鎖定卡密（不攜帶明文/密文內容——內容只在交付時現場解密）。
type ReservedCard struct {
	CardID uint64
	Locked bool
}

// Reservation 鎖定結果。
type Reservation struct {
	// ReservationID 預留批次標識（TTL 釋放的掃描錨點）
	ReservationID string
	Cards         []ReservedCard
	ExpiresAt     time.Time
}

// Inventory 庫存窄接口（order 下單事務內鎖卡消費，通道 B：同事務工作單元）。
// 交付；實現約束：MySQL/PG 事務內 FOR UPDATE 鎖可用行，SQLite 走
// BEGIN IMMEDIATE + UPDATE...WHERE status='available' 校驗 affected rows（）。
type Inventory interface {
	Reserve(ctx context.Context, subsiteID uint64, items []ReserveItem) (*Reservation, error)
	// Release 釋放預留（訂單取消/超時，TTL 兜底由週期任務二次釋放）。
	Release(ctx context.Context, orderID uint64) error
	// BindOrder 鎖卡後綁定訂單（Reserve 成功 → caller 回填 order_id）。
	BindOrder(ctx context.Context, subsiteID, productID, orderID uint64, quantity int32) error
	// MarkUsed 售出標記（校驗 affected rows 防併發重發，友商紀律）。
	MarkUsed(ctx context.Context, cardIDs []uint64, orderID uint64) error
	// Stock 可用庫存數（-1 = 無限，鏈接類商品）。
	Stock(ctx context.Context, productID, skuID uint64) (int64, error)
}

// CardContentReader 交付卡密讀取（ 供貨交付消費，通道 A）：
// 按卡 ID 批量讀取密文並現場解密返回明文（明文僅內存態，鐵律 11 出口約束）。
type CardContentReader interface {
	Contents(ctx context.Context, cardIDs []uint64, productID, subsiteID uint64) ([]string, error)
}

// CardReleaser 鎖卡回滾（ 供貨交付失敗時釋放；reserved → available）。
type CardReleaser interface {
	ReleaseCards(ctx context.Context, cardIDs []uint64) error
}

// StockBatcher 批量可用庫存（管理列表展示消費，通道 A——單商品 N+1 防護）。
type StockBatcher interface {
	// StockBatch 各商品可用卡密數（一條 GROUP BY；鏈接/兌換碼類不入卡池=無條目）。
	StockBatch(ctx context.Context, productIDs []uint64) (map[uint64]int64, error)
}
