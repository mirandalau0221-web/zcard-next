// Package audit 審計與風控模塊（ 完整落地）：
// - 操作審計（）：變更類 admin 操作（POST/PUT/DELETE）自動落 audit_logs；
// before/after 快照經註冊表採集（各模塊聲明聚合鍵）；異步落庫失敗不阻斷
// - 安全審計（）：port.Auditor 埋點（登錄/解密/導出/權限變更/黑名單變更/
// 取貨）——寫失敗不阻斷業務（1.x 紀律）；取貨審計含誰/何時/IP/訂單（零明文）
// - 風控閘門（）：port.RiskGate——IP 黑名單（精確+CIDR，進程內緩存）→
// pending 訂單閘門（同 IP 超限拒絕，DB 事務內複查防併發穿透）→ 頻率限流
// （每 IP 每分鐘滑動窗口）；取貨連續失敗鎖定（risk_lock_keys TTL，鎖定期內
// 正確密碼也拒絕）；IPv6 /64 聚合（orders.risk_ip 寫入口統一）
// - 訪問統計（）：VisitCounter 進程內聚合（閾值 100 批量落庫 + cron 每分鐘
// 兜底 Flush）——不逐請求寫庫
//
// 表：audit_logs / security_audit_logs / visit_logs / risk_lock_keys。
// 閾值常量：MaxPendingPerIP=3 / FetchFailLockN=5 / FetchLockTTL=30min /
// OrderPerMinPerIP=10（settings.security 讀取側 接線）。
package audit
