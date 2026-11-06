package fulfillment

// fulfillment 模塊事件聲明（附錄 C）。
//
// 發佈：
//   - order.delivered  全部交付完成（觸發通知/完成態流轉）
//
// 訂閲：
//   - payment.succeeded → 本地卡密項履約（upstream 項由 procurement 接管，M2）
//   - order.paid        → 履約編排入口

import "github.com/NovaWorks/zcard-next/server/internal/platform/events"

const EventDelivered = events.OrderDelivered
