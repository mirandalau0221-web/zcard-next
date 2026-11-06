// Package procurement 採購模塊（ 完整落地）：
// - 狀態機：pending → submitted/polling → fulfilled/rejected → refunding → refunded/manual
// （CAS + 遷移表；併發三通道匯聚同一終態只生效一次）
// - 三通道結果獲取：指數退避輪詢（30s×2/1m×2/2m×2/5m×2/10m，耗盡移交巡檢）
// - cron 巡檢（30min，24h 卡死轉人工）+ 上游回調（ 自家協議）
// - 到手即加密（鐵律 11 採購側）：上游卡密內存態 → CardCipher.Seal（AAD 綁定
// 本地商品/租戶）→ 密文落 procurement_items；交付出口複用 fulfillment
// AttachUpstreamDelivery（寫 cards(used) + order_deliveries，與本地卡密同一出口）
// - 失敗策略：auto_refund（payment.OrderRefunder 創建退款單）| manual（人工終態）
// - 事件：procurement.fulfilled / procurement.failed（outbox 發佈）
//
// 表：procurement_orders / procurement_items。
// 訂閲：order.paid（order 模塊發佈，經 outbox；註冊在 cmd/zcard newApp 破環點）。
// 調度：輪詢走 asynq critical 隊列（ProcurementPoll 任務類型），降級模式進程內執行。
package procurement
