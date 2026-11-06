// Package media 素材庫模塊（M1）：分類樹、上傳（本地 uploads/ 或 S3 兼容）、
// 改名、批量移動/刪除、外鏈導入、圖片引用計數。
//
// 表：media / media_categories。上傳安全：類型白名單 + 圖片重編碼（防圖片馬）+ 大小限制。
package media
