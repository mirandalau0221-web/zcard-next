// Package content 內容模塊（ 完整落地）：輕量 CMS。
// - Banner：橫幅/輪播（position/生效時間窗/跳轉類型/移動端圖回落）
// - Post：文章（blog/notice，多語言 JSON 字段，content 逐語言 sanitize）
// - PostCategory：文章分類（slug 唯一；有文章禁刪）
//
// 發佈狀態機：草稿 → 發佈（首發回填 published_at，取消再發布不覆蓋）。
// 前台讀取：生效橫幅（30s 進程內緩存）、已發佈文章分頁、slug 詳情、分類。
// 多語言回落：locale → zh_CN → 首個非空。
package content
