package audit

// 操作審計過濾器（）：變更類 admin 操作（POST/PUT/DELETE）自動落 audit_logs。
//
// 實現為 http.Filter（非 Kratos middleware）——中間件層的 Transporter 是內部
// *khttp.Transport 類型，拿不到原始 *http.Request（method/path/body）；
// Filter 層直接持有請求與響應狀態（supplier HMAC 同款模式）。
//
// before/after 快照：由各模塊註冊的「路由前綴 → 取值函數」註冊表提供；
// 未註冊的路由仍記錄操作元數據（無快照）。敏感字段脱敏（憑據永不入審計）。
// 紀律：審計寫失敗不阻斷業務（異步落庫；失敗僅日誌——1.x 紀律）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
)

// snapshotFn 快照取值函數（聚合鍵 → 變更前數據；由業務模塊註冊）。
type snapshotFn func(ctx context.Context, r *http.Request) (before map[string]any)

// snapshotRegistry 路由前綴 → 快照函數（各模塊 service 聲明自己的審計聚合鍵）。
var snapshotRegistry = map[string]snapshotFn{}

// RegisterSnapshot 註冊快照函數（routePrefix 如 /api/v1/admin/settings）。
func RegisterSnapshot(routePrefix string, fn snapshotFn) {
	snapshotRegistry[routePrefix] = fn
}

// OpAuditFilter 操作審計過濾器工廠。
// signer：admin JWT 校驗器（操作者身份取自令牌——鑑權中間件注入的 claims 只
// 存在於下游 handler ctx，不會寫回 Filter 層的 *http.Request，故此處自行校驗；
// HMAC 校驗代價可忽略）。
// permOf：operation → 權限點（authz 目錄查詢注入，避免循環依賴）。
// 響應狀態：僅 2xx 記錄（錯誤響應未產生變更）。
func OpAuditFilter(repo *AuditRepo, signer *authn.Signer, permOf func(op string) (code string, ok bool), opOf func(r *http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 只審計變更類管理面操作（GET 不落）
			if (r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete) ||
				!strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
				next.ServeHTTP(w, r)
				return
			}
			// Batch previews are read-only; execution writes a single transactional audit.
			if r.URL.Path == "/api/v1/admin/products/batch-content" || r.URL.Path == "/api/v1/admin/products/batch-content/preview" {
				next.ServeHTTP(w, r)
				return
			}
			// 請求載荷讀取（body 恢復供後續解碼）
			bodyAfter := ReadBodyJSON(r)

			rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(rec, r)

			// 非 2xx：未產生變更不落審計
			if rec.code < 200 || rec.code >= 300 {
				return
			}
			in := OpLogInput{
				OperatorType: "admin",
				Action:       r.Method,
				Route:        r.URL.Path,
				IP:           clientIP(r),
				UserAgent:    truncate(r.UserAgent(), 255),
			}
			// 操作者：admin JWT 直接校驗（見函數註釋）；失敗留 0 = 未知
			if signer != nil {
				if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
					if claims, err := signer.Verify(authn.RealmAdmin, strings.TrimSpace(tok)); err == nil {
						in.OperatorID = claims.Subject
					}
				}
			}
			if op := opOf(r); op != "" {
				if code, ok := permOf(op); ok {
					in.PermissionPoint = code
				}
			}
			// before：註冊表命中時查庫取舊值語義（各模塊聲明聚合鍵）
			if fn, ok := matchSnapshot(r.URL.Path); ok {
				in.Before = fn(r.Context(), r)
			}
			in.After = bodyAfter

			// 異步落庫（repo 內部吞錯——審計失敗不影響業務）
			go repo.WriteOpLog(context.WithoutCancel(r.Context()), in)
		})
	}
}

// statusRecorder 捕獲響應狀態碼。
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// matchSnapshot 路由前綴匹配快照函數（最長前綴優先）。
func matchSnapshot(path string) (snapshotFn, bool) {
	best := ""
	var fn snapshotFn
	for prefix, f := range snapshotRegistry {
		if strings.HasPrefix(path, prefix) && len(prefix) > len(best) {
			best, fn = prefix, f
		}
	}
	return fn, best != ""
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	return truncate(r.RemoteAddr, 64)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ReadBodyJSON 讀請求體為 map（body 恢復供後續解碼；敏感字段脱敏）。
func ReadBodyJSON(r *http.Request) map[string]any {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if len(body) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/admin/inventory/") {
		// Import lines contain saleable credentials. Keep only inventory
		// identifiers and counters; the restored body still reaches the handler.
		metadata := map[string]any{}
		for key, value := range m {
			switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
			case "productid", "skuid", "importid", "id", "expectedrevision", "expectedversion":
				if inventoryAuditID(value) {
					metadata[key] = value
				}
			case "cardids":
				if ids, ok := value.([]any); ok {
					valid := true
					for _, id := range ids {
						valid = valid && inventoryAuditID(id)
					}
					if valid {
						metadata[key] = value
					}
				}
			case "dedup", "enable":
				if _, ok := value.(bool); ok {
					metadata[key] = value
				}
			case "lines":
				if lines, ok := value.([]any); ok {
					metadata["line_count"] = len(lines)
				}
			}
		}
		m = metadata
	}
	// 抽獎配置的 content 包含私密獎品；僅保留規則元數據，原請求體已恢復。
	if strings.HasPrefix(r.URL.Path, "/api/v1/admin/lottery/") {
		if prizes, ok := m["prizes"].([]any); ok {
			for _, raw := range prizes {
				if prize, ok := raw.(map[string]any); ok {
					delete(prize, "content")
				}
			}
		}
	}
	// Shared delivery content contains credentials; only configuration metadata is auditable.
	if strings.HasSuffix(r.URL.Path, "/delivery-sources") && strings.HasPrefix(r.URL.Path, "/api/v1/admin/products/") {
		if _, ok := m["content"]; ok {
			m["content"] = "****"
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/admin/fulfillment/") && strings.HasSuffix(r.URL.Path, "/shipping") {
		if _, ok := m["address"]; ok {
			m["address"] = "****"
		}
	}
	// Settings wrap secrets inside value_json, so field-name redaction alone is insufficient.
	if r.URL.Path == "/api/v1/admin/settings/notify/smtp_password" || r.URL.Path == "/api/v1/admin/settings/notify/telegram_bot_token" || r.URL.Path == "/api/v1/admin/settings/notify/telegram" {
		for _, key := range []string{"value_json", "valueJson"} {
			if _, ok := m[key]; ok {
				m[key] = "****"
			}
		}
	}
	return redact(m)
}

// Protobuf JSON accepts decimal strings for uint64 IDs. Unknown aliases may
// also reach the filter, so never copy arbitrary strings or nested objects.
func inventoryAuditID(value any) bool {
	switch v := value.(type) {
	case float64:
		return v >= 0
	case string:
		if v == "" || len(v) > 20 {
			return false
		}
		for _, digit := range v {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// redact 敏感字段脱敏（憑據/密碼/密鑰永不入審計快照）。
func redact(m map[string]any) map[string]any {
	sensitive := map[string]bool{
		"code": true, "totp_code": true, "captcha_code": true, "challenge": true, "recovery_ticket": true, "recovery_code": true, "recovery_codes": true, "otpauth_url": true, "refresh_token": true, "access_token": true, "new_password": true,
		"current_password": true, "confirm_password": true, "currentpassword": true, "newpassword": true, "confirmpassword": true,
		"admin_password": true, "adminpassword": true, "query_password": true, "querypassword": true, "order_access_token": true, "orderaccesstoken": true,
		"bot_token": true, "telegram_bot_token": true, "smtp_password": true, "password": true, "secret": true, "credentials": true, "api_secret": true,
		"app_key": true, "config_json": true, "new_secret": true, "private_key": true,
	}
	out := make(map[string]any, len(m))
	secretSetting := m["group"] == "notify" && (m["key"] == "smtp_password" || m["key"] == "telegram_bot_token" || m["key"] == "telegram")
	for k, v := range m {
		if secretSetting && (k == "value_json" || k == "valueJson") {
			out[k] = "****"
			continue
		}
		if sensitive[strings.ToLower(k)] {
			out[k] = "****"
			continue
		}
		switch value := v.(type) {
		case map[string]any:
			out[k] = redact(value)
		case []any:
			items := make([]any, len(value))
			for i, item := range value {
				if child, ok := item.(map[string]any); ok {
					items[i] = redact(child)
				} else {
					items[i] = item
				}
			}
			out[k] = items
		default:
			out[k] = v
		}
	}
	return out
}
