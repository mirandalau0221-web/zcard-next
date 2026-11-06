// Package seo 搜索引擎優化（SEO）：robots.txt/sitemap + 爬蟲動態渲染。
//
//   - robots.txt：私有頁禁抓 + 自定義規則（site.robots_custom）+ Sitemap 指向
//   - sitemap.xml：首頁/列表/上架商品/已發佈文章（lastmod），URL 基準 site.url
//   - 動態渲染（Dynamic Rendering，Google 認可模式）：爬蟲 UA 請求商品/文章
//     詳情時實時從 DB 渲染完整 SEO HTML（title/canonical/og/JSON-LD + 正文）——
//     內容永遠新鮮、刪除即真 404，零重建零重啓；真人請求走靜態頁/SPA 不變
//
// 頁面級 SEO（canonical/meta/JSON-LD）由 storefront 消費公開配置生成，本模塊不涉頁面。
package seo
