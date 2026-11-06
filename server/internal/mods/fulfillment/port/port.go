// Package port 為 fulfillment 模塊對外契約（零依賴包）。
package port

import "context"

// Fulfiller 履約窄接口（訂閲 order.paid / payment.succeeded 後執行，通道 C 異步）。
type Fulfiller interface {
	// FulfillOrder 履約訂單（本地卡密：reserved → used 標記/即刪兩模式；
	// 交付記錄 = 卡密引用 + 一次性令牌，無明文快照，）。
	FulfillOrder(ctx context.Context, orderNo string) error
}

// UpstreamDeliveryItem 上游交付條目（ 交付出口入參）。
type UpstreamDeliveryItem struct {
	// SealedContent 已加密卡密（procurement_items.received_content 原樣透傳，
	// 交付層不觸碰明文——鐵律 11 的出口側約束）。
	SealedContent []byte
	// ContentHash 卡密 keyed hash（content_hash 列）。
	ContentHash string
}

// AttachUpstreamDelivery 上游採購交付出口（procurement 模塊消費，通道 A）：
// 把已加密的上游卡密寫為 cards（used）+ order_deliveries（一次性令牌/掩碼/三重門），
// 與本地卡密同一交付出口（ 驗收：上游卡密庫內零明文）。
type AttachUpstreamDelivery interface {
	AttachUpstreamDelivery(ctx context.Context, orderID, itemID, productID uint64, items []UpstreamDeliveryItem) error
}

// ReusableDelivery writes a separate delivery record referencing an immutable source.
type ReusableDelivery interface {
	DeliverReusable(context.Context, uint64, uint64, uint64) error
}
