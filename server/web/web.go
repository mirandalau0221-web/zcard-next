//go:build fullstack

// Package web 前端產物嵌入錨點（fullstack 形態，規劃 §10.1）。
//
// go:embed 只能引用包目錄內文件，故錨定本目錄（產物由 make web-dist 從
// monorepo 前端構建拷貝生成，不入庫）。dist 缺失時本 embed 編譯失敗——
// 「dist 缺失即編譯失敗」友商紀律的落地形態。SPA 服務語義見 internal/web。
package web

import "embed"

// DistFS 前端產物（storefront/ 與 admin/ 兩子樹）。
//
//go:embed all:storefront all:admin
var DistFS embed.FS
