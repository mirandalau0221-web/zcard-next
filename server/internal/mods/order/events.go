package order

// order 模塊事件聲明（附錄 C 事件目錄；經 outbox 發佈，消費方冪等）。
//
// 發佈：
//   - order.created    創建成功（鎖卡完成）後
//   - order.paid       支付成功（payment 回調事務內或餘額支付直髮）
//   - order.delivered  全部交付
//   - order.completed  買家確認/查詢完成
//   - order.canceled   取消（用户/管理員/超時）
//   - order.refunded   退款成功
//
// 訂閲（M1）：payment.succeeded → MarkPaid + 觸發履約。
// payload schema 由 proto 定義（M1 凍結；只加字段不改語義）。

import "github.com/NovaWorks/zcard-next/server/internal/platform/events"

// 本模塊發佈的事件類型（編譯期綁定 platform/events 目錄，防拼寫漂移）。
const (
	EventCreated   = events.OrderCreated
	EventPaid      = events.OrderPaid
	EventDelivered = events.OrderDelivered
	EventCompleted = events.OrderCompleted
	EventCanceled  = events.OrderCanceled
	EventRefunded  = events.OrderRefunded
)

// DedupeKey outbox dedupe_key 構造（唯一索引防重複發佈，§4.8）。
func DedupeKey(orderNo, action string) string { return "order:" + orderNo + ":" + action }
