// Package supply 上游貨源模塊（ 完整落地）：
// - 協議適配器（adapter/）：zcard（自家 Supply v2，4 頭 HMAC）/ dujiao_next（3 頭）/
// acg_faka（body MD5），golden vector 契約測試，出站 100% 經 platform/httpx（架構規則 5）
// - 連接管理：CRUD + 憑據 AES-GCM 加密（AAD 綁定 driver+base_url）+ SSRF 校驗
// - 同步服務：拉分類/商品 → upsert 本地（catalog port）→ up_stock 緩存 → 定價重算
// （匯率×加價×取整三模式；價格保護：auto_sync_price 關/固定覆蓋價/運營改價不覆蓋）
// - 任務追蹤：心跳 30s / 統計計數 / 取消標誌；終態發佈 sync.completed
// - 健康度：Ping 探活（cron 5min）+ ping_history 累計（M4 供應商評分基礎數據）
// - 下架語義：上游 inactive → 本地隱藏(2)；deleted 哨兵 → 本地下架(0)
//
// 表：supply_connections / supply_mappings / supply_sync_tasks。
// 1.x 協議知識遷移要點（CLAUDE.md）：acg-faka 路由按 / 拆段（路徑參數走 body）、
// cards 按 PHP_EOL 拆分、tradeNo 口徑、request_no 非冪等窗口；dujiao 分頁 50/頁 +
// includes_inactive 回聲字段防舊版誤判；zcard 簽名「哈希字節 === 發出字節」。
package supply
