package server

// EnsureInstalled 中間件（）：未安裝時除 /install、/health、靜態資源外一律 302 /install。
// i18n 中間件：Accept-Language 解析 → i18n.WithLocale（默認語言 settings.i18n，當前 zh_CN）。

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"strings"

	settingsport "github.com/NovaWorks/zcard-next/server/internal/mods/settings/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/i18n"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

// installGuard 未安裝守門（Filter 層——直接操作 ResponseWriter；
// middleware 返回 nil reply 會被生成的 handler 類型斷言 panic，不可用）。
// 未安裝時業務 API 302 /install（Web 安裝嚮導）；豁免：安裝頁/健康檢查/
// 安裝 API/非 API 靜態資源。已安裝直通（ops.installed_at 點查）。
func installGuard(isInstalled func() bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isInstalled() {
				p := r.URL.Path
				// 安裝頁 / 健康檢查 / 安裝 API：放行
				if p == "/install" || p == "/health" || strings.HasPrefix(p, "/api/v1/admin/install") {
					next.ServeHTTP(w, r)
					return
				}
				// 業務 API：302 安裝嚮導
				if strings.HasPrefix(p, "/api/") {
					w.Header().Set("Location", "/install")
					w.WriteHeader(http.StatusFound)
					return
				}
				// 非 API：靜態資源放行（安裝頁要加載 JS/CSS/字體）；
				// 其餘（/ 首頁、SPA 路由、/admin/ 後台等）一律 302 /install——
				// 客户訪問首頁即見安裝入口，無需知道 /install 路徑
				if isStaticAsset(p) {
					next.ServeHTTP(w, r)
					return
				}
				w.Header().Set("Location", "/install")
				w.WriteHeader(http.StatusFound)
				return
			}
			// 已安裝：/install 返回靜態提示頁（不依賴前端 JS——SPA 未加載
			// 也能看到提示）；其餘路徑直通
			if r.URL.Path == "/install" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(installedPage))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// installedPage 已安裝提示頁（服務端直出靜態頁；樣式與新項目 UI 一致）。
const installedPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>系統已安裝</title>
<style>
  body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
    background:linear-gradient(160deg,#eff6ff 0%,#f8fafc 60%,#eef2ff 100%);
    font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif;padding:24px}
  .card{width:100%;max-width:420px;background:#fff;border-radius:18px;box-shadow:0 24px 64px rgba(15,23,42,.12);
    padding:36px 32px;text-align:center;box-sizing:border-box}
  .icon{font-size:44px;margin-bottom:10px}
  h1{font-size:20px;font-weight:800;color:#0f172a;margin:0 0 8px}
  p{font-size:14px;color:#64748b;line-height:1.7;margin:0 0 22px}
  .links{display:flex;gap:10px;justify-content:center}
  a{display:inline-block;text-decoration:none;font-size:14px;font-weight:600;
    border-radius:10px;padding:10px 20px;transition:background .12s}
  a.primary{background:#2563eb;color:#fff}
  a.primary:hover{background:#1d4ed8}
  a.secondary{border:1px solid #e2e8f0;color:#334155}
  a.secondary:hover{background:#f8fafc}
  details{margin-top:22px;text-align:left;border:1px solid #e2e8f0;border-radius:10px;overflow:hidden}
  summary{cursor:pointer;font-size:13.5px;font-weight:700;color:#334155;padding:10px 14px;background:#f8fafc;list-style:none}
  summary::before{content:"⚙ "}
  details[open] summary{border-bottom:1px solid #e2e8f0}
  details ol{margin:0;padding:12px 14px 12px 34px;font-size:13px;color:#475569;line-height:1.9}
  code{background:#f1f5f9;border-radius:4px;padding:1px 6px;font-size:12px}
  .warn{color:#b45309;font-size:12.5px;padding:0 14px 12px;margin:0}
</style>
</head>
<body>
  <div class="card">
    <div class="icon">🔒</div>
    <h1>系統已安裝</h1>
    <p>安裝已完成，本頁為安裝嚮導入口，無需重複安裝。</p>
    <div class="links">
      <a class="primary" href="/">進入前台</a>
      <a class="secondary" href="/admin/">後台管理</a>
    </div>
    <details>
      <summary>如何重新安裝？</summary>
      <p class="warn">⚠️ 重新安裝會清空當前全部數據（商品/訂單/用户），請先備份數據庫！</p>
      <ol>
        <li><b>SQLite</b>：刪除數據庫文件 <code>data/zcard.db</code></li>
        <li><b>PostgreSQL / MySQL</b>：清空數據庫（重建或執行 <code>DROP DATABASE</code>；安裝標記在庫內，僅刪配置不會回到未安裝）</li>
        <li>若曾用在線嚮導切換過數據庫：刪除 <code>configs/database.yaml</code> 與 <code>data/.install-pending.json</code></li>
        <li>重啓服務後訪問 <code>/install</code> 即回到安裝嚮導</li>
      </ol>
    </details>
  </div>
</body>
</html>`

// isStaticAsset 靜態資源判定：/assets/ 目錄（vite 產物）或常見資源擴展名放行；
// .html 視為 SPA 頁面（vite-ssg 扁平頁），未安裝時也跳安裝嚮導。
func isStaticAsset(p string) bool {
	if strings.HasPrefix(p, "/assets/") {
		return true
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
		".webp", ".woff", ".woff2", ".ttf", ".eot", ".map", ".txt", ".xml", ".json":
		return true
	}
	return false
}

// i18nMiddleware reads the current site language policy on every API request.
func i18nMiddleware(cfg settingsport.Provider) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			defaultLocale, enabledLocales := "zh_TW", []string{"zh_TW"}
			if cfg != nil {
				if raw, err := cfg.GetDefault(ctx, "i18n", "default_locale", nil); err == nil && len(raw) != 0 {
					_ = json.Unmarshal(raw, &defaultLocale)
				}
				if raw, err := cfg.GetDefault(ctx, "i18n", "enabled_locales", nil); err == nil && len(raw) != 0 {
					_ = json.Unmarshal(raw, &enabledLocales)
				}
			}
			policy := i18n.NewPolicy(defaultLocale, enabledLocales)
			locale := policy.Default
			if tr, ok := transport.FromServerContext(ctx); ok {
				locale = policy.AcceptLanguage(tr.RequestHeader().Get("Accept-Language"))
			}
			return handler(i18n.WithLocale(i18n.WithPolicy(ctx, policy), locale), req)
		}
	}
}
