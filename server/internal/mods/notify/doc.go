// Package notify 通知中心（- 落地）：
// - 通道：Email（SMTP 運行時配置——變更不重啓；未配置/禁用 → skipped 降級不報錯）
// - Inbox（站內信 + 鈴鐺 API）；SMS/Telegram 交付（Channel 接口位已留）
// - 分發器：outbox 事件 → 路由表（事件 → 通道集合）→ 模板渲染（佔位符子集
// {{.key}} 白名單變量 + 值 HTML escape——雙保險防注入）→ 逐通道獨立投遞 →
// 每條落 notification_logs（sent/failed/skipped）
// - 模板：事件 × 通道 × 語言（zh_CN 回落）；後台 CRUD + 樣例預覽 + 白名單校驗
// - 訂閲事件：order.paid/delivered/completed/canceled/refunded、payment.failed、
// recharge.succeeded、user.registered（main.go 註冊）
// - 顯式發送：Dispatcher.Send（notifyport.Sender——業務模塊管理員告警直調）
//
// 表：notifications / notify_templates / notification_logs。
// 冪等：processed_events(event_id, consumer) 由 Dispatcher 統一兜底。
package notify
