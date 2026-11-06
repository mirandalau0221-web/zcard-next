package supplier

// B：dujiao-next 協議兼容層（對外供貨）。
//
// 讓任何 dujiao-next 站點在「站點連接」裏填本站地址 + 我方發的
// (api_key, api_secret) 即可對接——不改對方一行代碼。
//
// 端點（前綴 /api/v1/upstream，全部需 3 頭鑑權）：
// POST /ping | GET /categories | GET /products | GET /products/{id}
// POST /orders | GET /orders/{id} | POST /orders/{id}/cancel
//
// 協議事實來源：dujiao-next internal/modules/upstreamapi/transport/http/*（字段/
// 錯誤碼/金額口徑逐字對齊）。金額：內部分 → 字符串元兩位小數；卡密：純文本
// \n 連接（fulfillment.payload）；餘額不足：HTTP 200 + ok=false + payment_failed
// （對方客户端的錢包扣款失敗口徑）。
//
// 實現：原生 http.Handler（HandlePrefix 掛載）——外部協議契約不進我們的 proto/
// OpenAPI；業務核心複用 SupplyAPIService.fulfillOrder。

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	khttp "github.com/go-kratos/kratos/v3/transport/http"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
)

// dujiao 時間窗（對方 MaxTimestampSkew=60；比自家協議 300s 嚴格，按對方來）。
const dujiaoTimeSkew = 60

// RegisterDujiaoCompat 掛載 dujiao 兼容路由（/api/v1/upstream/；須在 SPA 兜底前註冊）。
func RegisterDujiaoCompat(srv *khttp.Server, svc *SupplyAPIService) {
	srv.HandlePrefix("/api/v1/upstream/", dujiaoMux(svc))
}

// dujiaoMux 路由表（獨立構造便於契約測試）。
func dujiaoMux(svc *SupplyAPIService) *http.ServeMux {
	h := &dujiaoCompat{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/upstream/ping", h.wrap(h.ping))
	mux.HandleFunc("/api/v1/upstream/categories", h.wrap(h.categories))
	mux.HandleFunc("/api/v1/upstream/products", h.wrap(h.products))
	mux.HandleFunc("/api/v1/upstream/products/", h.wrap(h.productDetail)) // /{id}
	mux.HandleFunc("/api/v1/upstream/orders", h.wrap(h.createOrder))
	mux.HandleFunc("/api/v1/upstream/orders/", h.wrap(h.orderAction)) // /{id} 與 /{id}/cancel
	return mux
}

// dujiaoCompat 兼容層處理器。
type dujiaoCompat struct {
	svc *SupplyAPIService
}

// wrap 3 頭鑑權 + JSON 錯誤殼（成功殼由各 handler 寫）。
func (h *dujiaoCompat) wrap(fn func(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, err := h.authenticate(r)
		if err != nil {
			var ae *authError
			code, status := "invalid_signature", http.StatusUnauthorized
			if errors.As(err, &ae) {
				code = ae.Code
				if code == errUnknownKey {
					status = http.StatusForbidden
					code = "invalid_api_key"
				}
				if code == errAccountDisabled {
					status = http.StatusForbidden
					code = "user_disabled"
				}
				if code == errTimestampSkew {
					code = "timestamp_expired"
				}
			}
			writeDujiaoErr(w, status, code, err.Error())
			return
		}
		currency, err := data.BaseCurrency(r.Context(), h.svc.repo.data)
		if err != nil || currency != "CNY" {
			writeDujiaoErr(w, http.StatusBadRequest, "currency_unsupported", "此協議僅支持人民幣計價")
			return
		}
		fn(w, r, account)
	}
}

// authenticate 3 頭校驗：解析 → ±60s → key 查賬户 → protocol=dujiao_next → 驗籤。
func (h *dujiaoCompat) authenticate(r *http.Request) (*ent.SupplierAccount, error) {
	key := r.Header.Get("Dujiao-Next-Api-Key")
	tsStr := r.Header.Get("Dujiao-Next-Timestamp")
	sig := r.Header.Get("Dujiao-Next-Signature")
	if key == "" || tsStr == "" || sig == "" {
		return nil, newAuthError(errMissingHeaders, "缺少 Dujiao-Next-Api-Key/Timestamp/Signature 頭")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || abs64(time.Now().Unix()-ts) > dujiaoTimeSkew {
		return nil, newAuthError(errTimestampSkew, "timestamp 非法或超出 ±60s 時間窗")
	}
	account, secret, err := h.svc.repo.AccountByKey(r.Context(), key)
	if err != nil {
		return nil, newAuthError(errUnknownKey, "未知 api_key")
	}
	if string(account.Status) != "approved" {
		return nil, newAuthError(errAccountDisabled, "賬户未審核或已禁用")
	}
	// IP 白名單（空名單放行；非空須命中）
	if !ipAllowed(account.IPWhitelist, r) {
		return nil, newAuthError(errAccountDisabled, "請求 IP 不在白名單內")
	}
	if string(account.Protocol) != "dujiao_next" {
		return nil, newAuthError(errUnknownKey, "該 api_key 不是 dujiao_next 兼容賬號")
	}
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body))) // 恢復（後續 handler 解析需要）
	if subtle.ConstantTimeCompare([]byte(dujiaoSign(secret, r.Method, r.URL.Path, tsStr, body)), []byte(sig)) != 1 {
		return nil, newAuthError(errInvalidSig, "簽名校驗失敗")
	}
	return account, nil
}

// ── 端點實現 ────────────────────────────────────────────────

func (h *dujiaoCompat) ping(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount) {
	balance, _ := h.svc.repo.BalanceOf(r.Context(), account.ID)
	writeDujiaoOK(w, map[string]any{
		"site_name":        dujiaoSiteName(account),
		"protocol_version": "1.0",
		"user_id":          account.ID,
		"balance":          centsToYuanStr(balance),
		"currency":         "CNY",
		"member_level":     nil,
	})
}

func (h *dujiaoCompat) categories(w http.ResponseWriter, r *http.Request, _ *ent.SupplierAccount) {
	writeDujiaoOK(w, map[string]any{"categories": []any{}})
}

func (h *dujiaoCompat) products(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	includeInactive := q.Get("include_inactive") == "true"
	// updated_after 透傳忽略（全量返回；對方 upsert 冪等無害，v1 從簡）
	status := int8(1)
	if includeInactive {
		status = -1
	}
	items, total, err := h.svc.reader.ListForSupply(r.Context(), catalogport.AdminFilter{Status: status, Page: int32(page), PageSize: int32(pageSize)})
	if err != nil {
		writeDujiaoErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	pricing, err := h.svc.repo.LoadPricing(r.Context(), account.ID)
	if err != nil {
		writeDujiaoErr(w, http.StatusInternalServerError, "pricing_unavailable", "供貨定價讀取失敗")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, p := range items {
		out = append(out, h.dujiaoProduct(r, account, p, pricing))
	}
	writeDujiaoOK(w, map[string]any{
		"items": out, "total": total, "page": page, "page_size": pageSize,
		"includes_inactive": includeInactive,
	})
}

func (h *dujiaoCompat) productDetail(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/upstream/products/")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeDujiaoErr(w, http.StatusBadRequest, "bad_request", "商品 ID 非法")
		return
	}
	p, err := h.svc.productForLegacySupply(r.Context(), id)
	if err != nil {
		writeDujiaoErr(w, http.StatusNotFound, "product_not_found", "商品不存在")
		return
	}
	pricing, err := h.svc.repo.LoadPricing(r.Context(), account.ID)
	if err != nil {
		writeDujiaoErr(w, http.StatusInternalServerError, "pricing_unavailable", "供貨定價讀取失敗")
		return
	}
	writeDujiaoOK(w, map[string]any{"product": h.dujiaoProduct(r, account, *p, pricing)})
}

func (h *dujiaoCompat) createOrder(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount) {
	var req struct {
		SkuID             any    `json:"sku_id"`
		Quantity          int32  `json:"quantity"`
		DownstreamOrderNo string `json:"downstream_order_no"`
		CallbackURL       string `json:"callback_url"`
		TraceID           string `json:"trace_id"`
	}
	body, _ := io.ReadAll(r.Body)
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeDujiaoErr(w, http.StatusBadRequest, "bad_request", "請求體解析失敗")
			return
		}
	}
	if req.Quantity < 1 {
		req.Quantity = 1
	}
	if req.DownstreamOrderNo == "" {
		writeDujiaoErr(w, http.StatusBadRequest, "bad_request", "downstream_order_no 必填（冪等鍵）")
		return
	}
	// sku_id 即我方商品 ID（products 響應裏每個商品單 SKU，SKU id = 商品 id）
	productID, err := parseUintAny(req.SkuID)
	if err != nil || productID == 0 {
		writeDujiaoErr(w, http.StatusBadRequest, "sku_unavailable", "sku_id 非法")
		return
	}
	out, err := h.svc.fulfillOrder(withAccount(r, account.ID), account.ID, productID, req.Quantity, req.DownstreamOrderNo, req.CallbackURL, req.TraceID)
	if err != nil {
		writeDujiaoErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if out.rejected {
		switch out.errCode {
		case "insufficient_balance":
			// dujiao 錢包扣款失敗口徑：HTTP 200 + ok=false + payment_failed
			writeDujiaoErr(w, http.StatusOK, "payment_failed", "供貨餘額不足")
		case "product_unavailable":
			writeDujiaoErr(w, http.StatusBadRequest, "product_unavailable", "商品不可用")
		case "no_stock":
			writeDujiaoErr(w, http.StatusConflict, "insufficient_stock", "庫存不足")
		default:
			writeDujiaoErr(w, http.StatusBadRequest, "bad_request", out.errMsg)
		}
		return
	}
	writeDujiaoOK(w, map[string]any{
		"order_id": out.order.ID, // dujiao 客户端 uint
		"order_no": out.order.DownstreamOrderNo,
		"status":   dujiaoStatus(string(out.order.Status), out.delivered),
		"amount":   centsToYuanStr(out.amount),
		"currency": "CNY",
	})
}

func (h *dujiaoCompat) orderAction(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/upstream/orders/")
	if rest == "" {
		writeDujiaoErr(w, http.StatusBadRequest, "bad_request", "缺少訂單 ID")
		return
	}
	idStr, action, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeDujiaoErr(w, http.StatusBadRequest, "bad_request", "訂單 ID 非法")
		return
	}
	idNum, _ := strconv.ParseUint(idStr, 10, 64)
	ctx := withAccount(r, account.ID)
	o, err := h.svc.repo.GetAccountSupplyOrder(ctx, account.ID, id)
	if err != nil {
		writeDujiaoErr(w, http.StatusNotFound, "order_not_found", "訂單不存在")
		return
	}
	if o.AccountID != account.ID {
		writeDujiaoErr(w, http.StatusNotFound, "order_not_found", "訂單不存在") // 防枚舉
		return
	}
	switch action {
	case "cancel":
		ok, err := h.svc.repo.RefundUndelivered(ctx, account.ID, o.ID)
		if err != nil {
			writeDujiaoErr(w, http.StatusInternalServerError, "cancel_failed", "取消失敗，請稍後重試")
			return
		}
		if !ok {
			writeDujiaoErr(w, http.StatusConflict, "cancel_not_allowed", "交付中或已交付訂單不可取消")
			return
		}
		writeDujiaoOK(w, map[string]any{
			"order_id": idNum, "order_no": o.DownstreamOrderNo, "status": "canceled",
		})
	default: // 查單
		_, refunded, err := h.svc.repo.orderPayments(ctx, account.ID, o.ID)
		if err != nil {
			writeDujiaoErr(w, http.StatusInternalServerError, "order_read_failed", "讀取訂單失敗")
			return
		}
		reply := map[string]any{
			"order_id": idNum, "order_no": o.DownstreamOrderNo,
			"status":          dujiaoStatus(string(o.Status), false),
			"amount":          centsToYuanStr(o.Amount),
			"currency":        "CNY",
			"refunded_amount": centsToYuanStr(refunded),
		}
		if string(o.Status) == "fulfilled" {
			if cards, err := h.svc.cardsPayloadOf(ctx, o); err == nil {
				reply["fulfillment"] = map[string]any{
					"type": "auto", "status": "delivered", "payload": strings.Join(cards, "\n"),
					"delivered_at": o.UpdatedAt.Format(time.RFC3339),
				}
			}
		}
		writeDujiaoOK(w, reply)
	}
}

// dujiaoProduct 商品行（單 SKU：SKU id = 商品 id；金額字符串元）。
func (h *dujiaoCompat) dujiaoProduct(r *http.Request, account *ent.SupplierAccount, p catalogport.SupplierProduct, pricing *supplierPricing) map[string]any {
	ctx := withAccount(r, account.ID)
	price := pricing.Price(p.ID, 0, p.CategoryID, p.Price)
	stock := -1
	if st, err := h.svc.inv.Stock(ctx, p.ID, 0); err == nil {
		stock = int(st)
	}
	stockStatus := "in_stock"
	if stock == 0 {
		stockStatus = "out_of_stock"
	}
	return map[string]any{
		"id":               p.ID, // dujiao 客户端 uint（字符串 unmarshal 失敗）
		"slug":             fmt.Sprintf("p-%d", p.ID),
		"seo_meta":         map[string]any{},
		"title":            map[string]any{"zh-CN": p.Name}, // dujiao 多語言對象（jsonmap.JSON）
		"description":      map[string]any{"zh-CN": p.Description},
		"content":          map[string]any{"zh-CN": p.Description},
		"images":           imgList(p.Cover),
		"price_amount":     centsToYuanStr(price),
		"currency":         "CNY",
		"fulfillment_type": "auto",
		"is_active":        p.Status == 1,
		"category_id":      p.CategoryID,
		"updated_at":       time.Now().UTC().Format(time.RFC3339), // 客户端不消費（增量信任上游過濾）；對齊協議字段
		"skus": []map[string]any{{
			"id": p.ID, "sku_code": "default",
			"price_amount": centsToYuanStr(price),
			"stock_status": stockStatus, "stock_quantity": stock,
			"is_active": p.Status == 1,
		}},
	}
}

// ── 工具 ───────────────────────────────────────────────────

// dujiaoStatus 我方狀態 → dujiao 口徑。
func dujiaoStatus(status string, deliveredNow bool) string {
	if deliveredNow {
		return "delivered"
	}
	switch status {
	case "fulfilled":
		return "delivered"
	case "rejected":
		return "canceled"
	case "refunded", "refunding":
		return "refunded"
	default:
		return "paid"
	}
}

// centsToYuanStr 分 → "12.34"（字符串元兩位小數；dujiao 全端點口徑）。
func centsToYuanStr(cents int64) string {
	neg := ""
	if cents < 0 {
		neg, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", neg, cents/100, cents%100)
}

// dujiaoSiteName connect/ping 回顯店鋪名。
func dujiaoSiteName(account *ent.SupplierAccount) string {
	if account.DisplayName != "" {
		return account.DisplayName
	}
	return "ZCard Supply"
}

func writeDujiaoOK(w http.ResponseWriter, data map[string]any) {
	data["ok"] = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func writeDujiaoErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": code, "error_message": msg})
}

// withAccount 注入賬户上下文（fulfillOrder/倉庫層消費 SupplyAccountID）。
func withAccount(r *http.Request, accountID uint64) context.Context {
	return context.WithValue(r.Context(), accountCtxKey{}, accountID)
}

// parseUintAny JSON 數值/字符串 → uint64。
func parseUintAny(v any) (uint64, error) {
	switch x := v.(type) {
	case float64:
		return uint64(x), nil
	case string:
		return strconv.ParseUint(x, 10, 64)
	case json.Number:
		return strconv.ParseUint(x.String(), 10, 64)
	case uint64:
		return x, nil
	case int:
		if x < 0 {
			return 0, fmt.Errorf("負數")
		}
		return uint64(x), nil
	}
	return 0, fmt.Errorf("無法解析 %T", v)
}

// imgList 封面 → images 數組（dujiao 字段形態）。
func imgList(cover string) []string {
	if cover == "" {
		return []string{}
	}
	return []string{cover}
}
