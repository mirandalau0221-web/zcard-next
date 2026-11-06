package supplier

// HMAC 四頭鑑權中間件（）：
// X-Supply-Key / X-Supply-Timestamp / X-Supply-Nonce / X-Supply-Signature
// 流程：四頭解析 → key 查賬户 → 狀態 approved → ±300s 時間窗 → nonce 防重放
// （DB supply_nonces UNIQUE(key,nonce)）→ 雙口徑驗籤（常數時間）→ 賬户注入 context。
// 掛載：僅 /api/supply/* 路由組（不掛 JWT；匿名 Ping 免簽名）。
//
// 簽名字節不變式：簽名哈希的字節 === 實際發出的字節——直接讀 *http.Request
// （Method/URL.Path/RawQuery/Body），不依賴 Kratos operation 轉寫。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
)

// 鑑權錯誤碼（下游可編程處理）。
const (
	errMissingHeaders  = "missing_headers"
	errUnknownKey      = "unknown_key"
	errAccountDisabled = "account_disabled"
	errTimestampSkew   = "timestamp_skew"
	errNonceReplay     = "nonce_replay"
	errInvalidSig      = "invalid_signature"
)

// 默認時間窗（秒）。
const defaultTimestampSkew = 300

// accountCtxKey 鑑權賬户上下文鍵。
type accountCtxKey struct{}

// SupplyAccountID 從 context 取鑑權賬户（服務實現消費）。
func SupplyAccountID(ctx context.Context) uint64 {
	id, _ := ctx.Value(accountCtxKey{}).(uint64)
	return id
}

// AuthStore 鑑權數據訪問（中間件依賴；由 SupplierRepoImpl 實現）。
type AuthStore interface {
	// AccountByKey 按 api_key 查賬户（返回解密後的 secret）。
	AccountByKey(ctx context.Context, key string) (*ent.SupplierAccount, string, error)
	// ConsumeNonce 消費 nonce（UNIQUE 約束衝突 = 重放）。
	ConsumeNonce(ctx context.Context, key, nonce string, expiresAt time.Time) error
}

// SupplyAuthFilter HMAC 四頭鑑權過濾器（http.Handler 級，能拿到原始 *http.Request
// ——Kratos v3 Transporter 不暴露 method/path/body，簽名不變式要求原始字節）。
// 僅匹配 /api/supply/*（匿名 Ping 免簽名）；賬户注入 request context（Handler 內讀取）。
func SupplyAuthFilter(store AuthStore, skew int64) func(http.Handler) http.Handler {
	if skew <= 0 {
		skew = defaultTimestampSkew
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isSupplyPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// Anonymous ping remains a public connectivity probe. Once any
			// credential header is supplied, require full authentication before
			// reporting the account balance (or a successful credential check).
			if r.URL.Path == "/api/supply/ping" && !hasSupplyCredentials(r) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, err := authenticate(r, store, skew)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticate 執行四頭校驗（分離便於單測）。
func authenticate(r *http.Request, store AuthStore, skew int64) (context.Context, error) {
	key := r.Header.Get("X-Supply-Key")
	tsStr := r.Header.Get("X-Supply-Timestamp")
	nonce := r.Header.Get("X-Supply-Nonce")
	sig := r.Header.Get("X-Supply-Signature")
	if key == "" || tsStr == "" || nonce == "" || sig == "" {
		return nil, newAuthError(errMissingHeaders, "缺少鑑權頭（X-Supply-Key/Timestamp/Nonce/Signature）")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return nil, newAuthError(errTimestampSkew, "timestamp 非法")
	}
	if abs64(time.Now().Unix()-ts) > skew {
		return nil, newAuthError(errTimestampSkew, "timestamp 超出時間窗（±300s）")
	}
	account, secret, err := store.AccountByKey(r.Context(), key)
	if err != nil {
		return nil, newAuthError(errUnknownKey, "未知 api_key")
	}
	if string(account.Status) != "approved" {
		return nil, newAuthError(errAccountDisabled, "賬户未審核或已禁用")
	}
	// IP 白名單（空名單放行；非空須命中——精確 IP 或 CIDR）
	if !ipAllowed(account.IPWhitelist, r) {
		return nil, newAuthError(errAccountDisabled, "請求 IP 不在白名單內")
	}
	// 驗籤（先驗籤後寫 nonce——1.x 教訓：未驗籤請求不污染 nonce 緩存）
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body)) // 恢復 body（後續 Kratos 解碼需要）
	if !verifyDual(secret, r.Method, r.URL.Path, r.URL.RawQuery, tsStr, nonce, body, sig) {
		return nil, newAuthError(errInvalidSig, "簽名校驗失敗")
	}
	if err := store.ConsumeNonce(r.Context(), key, nonce, time.Unix(ts, 0).Add(time.Duration(skew)*time.Second)); err != nil {
		return nil, newAuthError(errNonceReplay, "nonce 已使用（重放拒絕）")
	}
	return context.WithValue(r.Context(), accountCtxKey{}, account.ID), nil
}

func hasSupplyCredentials(r *http.Request) bool {
	for _, name := range []string{"X-Supply-Key", "X-Supply-Timestamp", "X-Supply-Nonce", "X-Supply-Signature"} {
		if r.Header.Get(name) != "" {
			return true
		}
	}
	return false
}

// isSupplyPath 供貨路由判定。
func isSupplyPath(path string) bool {
	if len(path) < len("/api/supply/") || path[:len("/api/supply/")] != "/api/supply/" {
		return false
	}
	return true
}

// writeAuthError 401 響應（錯誤碼供下游程序化處理）。
func writeAuthError(w http.ResponseWriter, err error) {
	code := "invalid_signature"
	msg := err.Error()
	var ae *authError
	if errors.As(err, &ae) {
		code = ae.Code
		msg = ae.Message
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"code":401,"reason":"` + code + `","message":"` + msg + `"}`))
}

// authError 帶錯誤碼的錯誤（HTTP 401；code 供下游程序化處理）。
type authError struct {
	Code    string
	Message string
}

func (e *authError) Error() string { return fmt.Sprintf("supplier.auth.%s: %s", e.Code, e.Message) }

func newAuthError(code, msg string) error { return &authError{Code: code, Message: msg} }

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// 編譯期斷言：AuthStore 由 repo 實現（見 data_repo.go）。
var _ AuthStore = (*SupplierRepoImpl)(nil)
