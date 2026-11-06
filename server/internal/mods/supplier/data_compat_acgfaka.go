package supplier

// C：acg-faka 協議兼容層（對外供貨）。
//
// 讓任何 acg-faka 站點用「共享店鋪 → 對接異次元(type=0)」填本站地址 +
// (app_id, app_key) 即可對接——不改對方一行代碼。
//
// 端點（前綴 /shared，全部 POST form-urlencoded、無路徑參數——acg Kernel 按
// "/" 拆段路由）：
// /shared/authentication/connect → {shopName, balance(元)}
// /shared/commodity/items → 兩級分類樹 children=[商品]
// /shared/commodity/item (code) → 單商品
// /shared/commodity/stock (code,race,sku) → {stock}
// /shared/commodity/valuation (code,num,...) → {price(元)}
// /shared/commodity/trade (shared_code,num,request_no,...) → {url,amount,tradeNo,secret}
// /shared/commodity/query (tradeNo) → {secret,widget,status}
//
// 協議事實來源：acg-faka app/Controller/Shared/{Commodity,Authentication}.php +
// app/Service/Bind/Shared.php（字段/簽名/錯誤口徑逐字對齊）：
// - 鑑權：body 內 app_id + app_key + sign（MD5，無時間戳/nonce——協議固有，
// 以 request_no 冪等 + HTTPS + 賬號限流補償，見計劃 C4）
// - 響應：{code:200,msg,data}；失敗 {code:0,msg}（HTTP 200，無 data，字段名 msg）
// - 金額：元（數字）；卡密：secret 純文本 \n 連接
//
// 實現：原生 http.Handler（HandlePrefix 掛載）；核心複用 fulfillOrder。

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	khttp "github.com/go-kratos/kratos/v3/transport/http"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
)

// acgOrderPrefix acg 冪等鍵命名空間（與 zcard/dujiao 下游單號隔離）。
const acgOrderPrefix = "acg:"

// RegisterAcgFakaCompat 掛載 acg-faka 兼容路由（/shared/；須在 SPA 兜底前註冊）。
func RegisterAcgFakaCompat(srv *khttp.Server, svc *SupplyAPIService) {
	srv.HandlePrefix("/shared/", acgMux(svc))
}

// acgMux 路由表（獨立構造便於契約測試）。
func acgMux(svc *SupplyAPIService) *http.ServeMux {
	h := &acgCompat{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("/shared/authentication/connect", h.wrap(h.connect))
	mux.HandleFunc("/shared/commodity/items", h.wrap(h.items))
	mux.HandleFunc("/shared/commodity/item", h.wrap(h.item))
	mux.HandleFunc("/shared/commodity/stock", h.wrap(h.stock))
	mux.HandleFunc("/shared/commodity/valuation", h.wrap(h.valuation))
	mux.HandleFunc("/shared/commodity/trade", h.wrap(h.trade))
	mux.HandleFunc("/shared/commodity/query", h.wrap(h.query))
	return mux
}

type acgCompat struct {
	svc *SupplyAPIService
}

// wrap body MD5 驗籤（app_id → 賬戶 → protocol 校驗 → 解密 app_key 重算比對）。
func (h *acgCompat) wrap(fn func(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, form map[string]string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAcgErr(w, "僅支持 POST")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeAcgErr(w, "表單解析失敗")
			return
		}
		form := map[string]string{}
		for k, v := range r.PostForm {
			if len(v) > 0 {
				form[k] = v[0]
			}
		}
		appID := form["app_id"]
		sign := form["sign"]
		if appID == "" || sign == "" {
			writeAcgErr(w, "商戶ID不存在")
			return
		}
		account, secret, err := h.svc.repo.AccountByKey(r.Context(), appID)
		if err != nil {
			writeAcgErr(w, "商戶ID不存在")
			return
		}
		if string(account.Status) != "approved" {
			writeAcgErr(w, "商戶被禁用")
			return
		}
		// IP 白名單（空名單放行；非空須命中）
		if !ipAllowed(account.IPWhitelist, r) {
			writeAcgErr(w, "請求 IP 不在白名單內")
			return
		}
		if string(account.Protocol) != "acg_faka" {
			writeAcgErr(w, "商戶ID不存在") // 非 acg 賬號不暴露
			return
		}
		if !acgFakaSignVerify(form, secret, sign) {
			writeAcgErr(w, "密鑰錯誤")
			return
		}
		currency, err := data.BaseCurrency(r.Context(), h.svc.repo.data)
		if err != nil || currency != "CNY" {
			writeAcgErr(w, "此協議僅支持人民幣計價")
			return
		}
		fn(w, r, account, form)
	}
}

// ── 端點實現 ────────────────────────────────────────────────

func (h *acgCompat) connect(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, _ map[string]string) {
	balance, _ := h.svc.repo.BalanceOf(r.Context(), account.ID)
	name := account.DisplayName
	if name == "" {
		name = "ZCard Supply"
	}
	writeAcgOK(w, map[string]any{"shopName": name, "balance": centsToYuanFloat(balance)})
}

func (h *acgCompat) items(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, _ map[string]string) {
	ctx := withAccount(r, account.ID)
	cats, err := h.svc.reader.ListSupplyCategories(ctx)
	if err != nil {
		writeAcgErr(w, "分類讀取失敗")
		return
	}
	prods, _, err := h.svc.reader.ListForSupply(ctx, catalogport.AdminFilter{Status: -1, Page: 1, PageSize: 1000})
	if err != nil {
		writeAcgErr(w, "商品讀取失敗")
		return
	}
	// 兩級樹：分類 → children 商品；未分類商品歸入「默認分類」（id=0）
	pricing, err := h.svc.repo.LoadPricing(ctx, account.ID)
	if err != nil {
		writeAcgErr(w, "供貨定價讀取失敗")
		return
	}
	byCat := map[uint64][]map[string]any{}
	for _, p := range prods {
		byCat[p.CategoryID] = append(byCat[p.CategoryID], h.acgProduct(ctx, account, p, pricing))
	}
	tree := make([]map[string]any, 0, len(cats)+1)
	if uncategorized := byCat[0]; len(uncategorized) > 0 {
		tree = append(tree, map[string]any{"id": 0, "name": "默認分類", "children": uncategorized})
	}
	for _, c := range cats {
		tree = append(tree, map[string]any{"id": c.ID, "name": c.Name, "children": orEmptyList(byCat[c.ID])})
	}
	writeAcgOK(w, tree)
}

func (h *acgCompat) item(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, form map[string]string) {
	p, ok := h.supplyProduct(r, account, form["code"])
	if !ok {
		writeAcgErr(w, "商品不存在")
		return
	}
	pricing, err := h.svc.repo.LoadPricing(r.Context(), account.ID)
	if err != nil {
		writeAcgErr(w, "供貨定價讀取失敗")
		return
	}
	writeAcgOK(w, h.acgProduct(withAccount(r, account.ID), account, *p, pricing))
}

func (h *acgCompat) stock(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, form map[string]string) {
	p, ok := h.supplyProduct(r, account, form["code"])
	if !ok {
		writeAcgErr(w, "商品不存在")
		return
	}
	ctx := withAccount(r, account.ID)
	stock := -1
	if st, err := h.svc.inv.Stock(ctx, p.ID, 0); err == nil {
		stock = int(st)
	}
	writeAcgOK(w, map[string]any{"stock": stock})
}

func (h *acgCompat) valuation(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, form map[string]string) {
	p, ok := h.supplyProduct(r, account, form["code"])
	if !ok {
		writeAcgErr(w, "商品不存在")
		return
	}
	num := atoiOr(form["num"], 1)
	if num < 1 {
		num = 1
	}
	ctx := withAccount(r, account.ID)
	pricing, err := h.svc.repo.LoadPricing(ctx, account.ID)
	if err != nil {
		writeAcgErr(w, "供貨定價讀取失敗")
		return
	}
	writeAcgOK(w, map[string]any{"price": centsToYuanFloat(pricing.Price(p.ID, 0, p.CategoryID, p.Price) * int64(num))})
}

func (h *acgCompat) trade(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, form map[string]string) {
	code := form["shared_code"]
	quantityText := form["num"]
	if quantityText == "" {
		quantityText = "1"
	}
	num, parseErr := strconv.ParseInt(quantityText, 10, 32)
	if parseErr != nil || num < 1 {
		writeAcgErr(w, "購買數量非法")
		return
	}
	requestNo := form["request_no"]
	if code == "" || requestNo == "" {
		writeAcgErr(w, "參數缺失（shared_code/request_no 必填）")
		return
	}
	productID, parseErr := strconv.ParseUint(code, 10, 64)
	if parseErr != nil || productID == 0 {
		writeAcgErr(w, "商品不存在或已下架")
		return
	}
	// The core checks the catalog only for a new order. A replay must retain
	// the original receipt even if the product later gains SKUs or is hidden.
	out, err := h.svc.fulfillOrder(withAccount(r, account.ID), account.ID, productID, int32(num),
		acgOrderPrefix+requestNo, "", "acg-"+requestNo) // acg 協議無回調
	if err != nil {
		writeAcgErr(w, "下單失敗，請稍後重試")
		return
	}
	if out.rejected {
		switch out.errCode {
		case "insufficient_balance":
			writeAcgErr(w, "餘額不足")
		case "no_stock":
			writeAcgErr(w, "庫存不足")
		default:
			writeAcgErr(w, "商品不可用")
		}
		return
	}
	if !out.delivered {
		// 冪等重放未交付首單（歷史單 pending/paid）——acg 只有同步交付口徑
		writeAcgErr(w, "訂單處理中，請勿重複提交")
		return
	}
	writeAcgOK(w, map[string]any{
		"url":     nil,
		"amount":  centsToYuanFloat(out.amount),
		"tradeNo": strconv.FormatUint(out.order.ID, 10),
		"secret":  strings.Join(out.cards, "\n"),
	})
}

func (h *acgCompat) query(w http.ResponseWriter, r *http.Request, account *ent.SupplierAccount, form map[string]string) {
	id, err := strconv.ParseUint(form["tradeNo"], 10, 64)
	if err != nil {
		writeAcgErr(w, "訂單號非法")
		return
	}
	ctx := withAccount(r, account.ID)
	o, err := h.svc.repo.GetAccountSupplyOrder(ctx, account.ID, id)
	if err != nil || o.AccountID != account.ID {
		writeAcgErr(w, "訂單不存在")
		return
	}
	status := 0 // 0=未完成 1=已支付
	if string(o.Status) == "paid" || string(o.Status) == "fulfilling" || string(o.Status) == "fulfilled" {
		status = 1
	}
	secret := ""
	if string(o.Status) == "fulfilled" {
		if cards, err := h.svc.cardsPayloadOf(ctx, o); err == nil {
			secret = strings.Join(cards, "\n")
		}
	}
	writeAcgOK(w, map[string]any{"secret": secret, "widget": nil, "status": status})
}

// ── 工具 ───────────────────────────────────────────────────

// supplyProduct code（商品 ID）→ 供貨商品。
func (h *acgCompat) supplyProduct(r *http.Request, account *ent.SupplierAccount, code string) (*catalogport.SupplierProduct, bool) {
	id, err := strconv.ParseUint(code, 10, 64)
	if err != nil || id == 0 {
		return nil, false
	}
	p, err := h.svc.productForLegacySupply(withAccount(r, account.ID), id)
	if err != nil {
		return nil, false
	}
	return p, true
}

// acgProduct 商品行（字段對齊 acg commodity 語義；金額元字符串）。
// 字段集合覆蓋 acg 3.5.7+ 客戶端 sync() 的讀取全集——draft/seckill/contact
// 為常量語義：本站不支持預選卡與秒殺，與 draft_status/seckill_status=0 一致；
// contact_type=0（任意）避免對接站把 null 寫進 NOT NULL 列而同步失敗。
func (h *acgCompat) acgProduct(ctx context.Context, account *ent.SupplierAccount, p catalogport.SupplierProduct, pricing *supplierPricing) map[string]any {
	price := pricing.Price(p.ID, 0, p.CategoryID, p.Price)
	stock := -1
	if st, err := h.svc.inv.Stock(ctx, p.ID, 0); err == nil {
		stock = int(st)
	}
	return map[string]any{
		"code":               strconv.FormatUint(p.ID, 10),
		"name":               p.Name,
		"price":              centsToYuanStr(price),
		"user_price":         centsToYuanStr(price),
		"factory_price":      centsToYuanStr(p.FactoryPrice),
		"description":        p.Description,
		"introduce":          p.Description,
		"cover":              p.Cover,
		"status":             boolToInt(p.Status == 1),
		"delivery_way":       1, // 自動發貨
		"draft_status":       0,
		"draft_premium":      "0",
		"minimum":            1,
		"maximum":            99,
		"stock":              stock,
		"config":             "",
		"widget":             nil,
		"category_id":        p.CategoryID,
		"seckill_status":     0,
		"seckill_start_time": nil,
		"seckill_end_time":   nil,
		"contact_type":       0,
	}
}

func writeAcgOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": data})
}

func writeAcgErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": msg})
}

// centsToYuanFloat 分 → 元數字（acg connect/valuation/trade amount 口徑）。
func centsToYuanFloat(cents int64) float64 {
	return float64(cents) / 100
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

func orEmptyList(list []map[string]any) []map[string]any {
	if list == nil {
		return []map[string]any{}
	}
	return list
}
