// Package port 為 order 模塊對外契約（零依賴包）。
package port

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// UpstreamStockGate 上游代發商品實時庫存閘門（ 落地；supply 網關實現，
// newApp 破環點注入）。本地商品不走上游；對接商品缺貨/查詢失敗/庫存未知
// 返回錯誤，僅上游明確不限(-1)可放行。nil 閘門 = 不啓用預檢。

type UpstreamStockGate interface {
	CheckItems(ctx context.Context, subsiteID uint64, items []UpstreamStockItem) error
}

// UpstreamStockItem 預檢行（與下單行同構；非上游項由實現方忽略）。
type UpstreamStockItem struct {
	ProductID uint64
	SkuID     uint64
	Quantity  int32
}

// PaidFact 支付成功事實（payment 回調事務內經 OrderLifecycle 推進訂單，
// 破環點：payment → order 窄接口回調，bootstrap 注入）。
type PaidFact struct {
	OrderNo        string
	PaymentID      uint64
	Channel        string
	Amount         money.Cents // 實收（分，基礎貨幣口徑核對）
	ChannelOrderNo string
}

// OrderLifecycle 訂單生命週期窄接口（payment 消費，通道 B 同事務）。
type OrderLifecycle interface {
	// MarkPaid 訂單置 paid：狀態機校驗 → 鎖卡轉售出預留 → 落 order_status_events
	// → 寫 outbox(order.paid)。冪等：已 paid 直接返回成功。
	MarkPaid(ctx context.Context, fact PaidFact) error
	// Cancel 取消（用戶/管理員/超時 TTL 任務）；paid 之後禁止回 canceled（）。
	Cancel(ctx context.Context, orderNo, reason string, operator Operator) error
}

// QueryResult 取貨結果（取貨三重門通過後返回，鐵律 12）。
type QueryResult struct {
	OrderNo  string
	Status   string
	Total    money.Cents
	Items    []DeliveryItem
	FetchCnt int32 // 已取貨次數（>1 走重新顯示流程 + 審計）
}

// DeliveryItem 交付項（內容為一次性解密明文——僅此內存態，永不落庫/日誌）。
type DeliveryItem struct {
	ItemID      uint64
	ProductName string
	Content     string
	Masked      bool // true = 掩碼展示（尾 4 位），需重新顯示流程
}

// Operator 操作者（狀態事件溯源 operator 字段）。
type Operator struct {
	Type string // system / user / admin / worker
	ID   uint64
	IP   string
}

// OrderQuery 取貨查詢窄接口（storefront 消費）。
type OrderQuery interface {
	// QueryByNo 單號 + 查詢密碼取貨（constant-time 比對；密碼錯誤與單號錯誤對外表現一致）。
	QueryByNo(ctx context.Context, orderNo, queryPassword, clientIP string) (*QueryResult, error)
}

// SoldCounter 批量已售數量（管理列表展示消費，通道 A）。
type SoldCounter interface {
	// SoldBatch 各商品已售數量（paid 及之後狀態訂單的 order_items.quantity 聚合）。
	SoldBatch(ctx context.Context, productIDs []uint64) (map[uint64]int64, error)
}
