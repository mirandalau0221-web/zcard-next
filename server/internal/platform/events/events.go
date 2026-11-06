// Package events 事務性 Outbox 事件契約（規劃 §4.8 / 附錄 C）。
//
// 通道 C：業務事務內寫 outbox_events（與業務同事務提交），relay（500ms 掃描）
// 投遞 asynq 或進程內分發；dedupe_key 唯一索引防重複發佈；
// 消費方經 processed_events(event_id, consumer) 冪等。
package events

import (
	"context"
	"encoding/json"
)

// 事件目錄 v1（附錄 C，M1 凍結；向後兼容規則：只加字段不改語義）。
const (
	AdminMFAChanged      = "identity.admin_mfa_changed"
	OrderCreated         = "order.created"
	OrderPaid            = "order.paid"
	OrderDelivered       = "order.delivered"
	OrderCompleted       = "order.completed"
	OrderCanceled        = "order.canceled"
	OrderRefunded        = "order.refunded"
	PaymentSucceeded     = "payment.succeeded"
	PaymentFailed        = "payment.failed"
	RefundRequested      = "refund.requested"
	RefundSucceeded      = "refund.succeeded"
	WithdrawalReviewed   = "withdrawal.reviewed"
	TicketCreated        = "ticket.created"
	TicketReplied        = "ticket.replied"
	UserRegistered       = "user.registered"
	RechargeSucceeded    = "recharge.succeeded"
	SyncCompleted        = "sync.completed"
	SupplyRateLimited    = "supply.rate_limited"
	ProcurementFulfilled = "procurement.fulfilled"
	ProcurementFailed    = "procurement.failed"
)

// Envelope outbox 事件信封（隊列任務載荷結構；task type = "event:"+Type）。
type Envelope struct {
	// EventID outbox_events.id（消費冪等錨點）
	EventID uint64 `json:"event_id"`
	// Type 事件類型（如 order.paid）
	Type string `json:"type"`
	// AggregateID 聚合根標識（訂單號/支付單號）
	AggregateID string `json:"aggregate_id"`
	// SubsiteID 租户（worker 消費側恢復租户上下文用）
	SubsiteID uint64 `json:"subsite_id"`
	// Payload 事件載荷（各模塊 proto 定義）
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Writer outbox 寫入端口：**必須在業務事務內調用**（通道 C 的關鍵約束）——
// 實現位於 internal/data（經 data.Tx 攜帶的 *ent.Tx 落庫，事務回滾事件不殘留）。
// dedupe_key 重複時返回 nil（冪等：視為已發佈，不報錯）。
type Writer interface {
	Write(ctx context.Context, module, typ, aggregateID, dedupeKey string, payload json.RawMessage) error
}

// Handler 事件處理器（消費方註冊；實現必須冪等——processed_events 已兜底，
// 但處理器自身不應假設恰好一次）。
type Handler func(ctx context.Context, env Envelope) error

// All 事件目錄全集（worker mux 註冊與 CI 目錄校驗用；新增事件必須同步此處）。
func All() []string {
	return []string{
		AdminMFAChanged, OrderCreated, OrderPaid, OrderDelivered, OrderCompleted, OrderCanceled, OrderRefunded,
		PaymentSucceeded, PaymentFailed, RefundRequested, RefundSucceeded,
		WithdrawalReviewed, TicketCreated, TicketReplied,
		UserRegistered, RechargeSucceeded, SyncCompleted, SupplyRateLimited,
		ProcurementFulfilled, ProcurementFailed,
	}
}
