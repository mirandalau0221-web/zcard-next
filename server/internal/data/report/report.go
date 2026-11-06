// Package report 報表裸 SQL 唯一收口（規劃 §3.5 硬原則 3 / 架構測試 §4.10-5）。
//
// 規則：
//   - 只有本包可寫裸 SQL，且必須經 platform/db.SQL 原語構造（禁止字符串拼接方言 SQL）；
//   - 查詢帶 subsite_id 的表時 SQL 必須包含租户條件佔位（架構測試 §4.10-12 靜態掃描）；
//   - 報表聚合走 daily_stats 日結，不掃大表（§5.18）。
//
// M1b 隨 dashboard v1 交付首批查詢；當前為收口占位。
package report
