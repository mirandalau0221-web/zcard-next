package order

// 下單用例 + 狀態機落地（ 核心交易編排）。
//
// CreateOrder 事務編排（ 時序）：
// 1. 校驗（商品可見 → 控件必填 → 限購）
// 2. inventory.Reserve（鎖卡；不足整批迴滾）
// 3. BindOrder（回填 order_id）
// 4. 管線算價 → 寫 orders + order_items + order_amount_lines
// 5. outbox.Write(order.created)
// 6. 返回訂單號 + 應付金額

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/platform/shipping"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderamountline"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderstatusevent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/predicate"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	auditport "github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	couponport "github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory/port"
	memberlevelport "github.com/NovaWorks/zcard-next/server/internal/mods/memberlevel/port"
	settingsport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	orderport "github.com/NovaWorks/zcard-next/server/internal/mods/order/port"
	paymentport "github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
	resellerport "github.com/NovaWorks/zcard-next/server/internal/mods/reseller/port"
	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/id"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"

	"github.com/shopspring/decimal"
)

// OrderUsecase 擴展（持有依賴）。
type OrderUsecase struct {
	Data         *data.Data
	Inv          port.Inventory
	Gen          *id.Generator
	MemberRate   memberlevelport.RateResolver
	Coupon       couponport.CouponResolver
	Catalog      catalogport.PricingResolver
	Outbox       events.Writer                  // order.* 事件發佈（：procurement 訂閲 order.paid）
	Gate         auditport.RiskGate             // 下單風控閘門（nil = 未裝配跳過）
	Flash        couponport.FlashResolver       // ：秒殺（nil 跳過）
	Promos       couponport.PromotionResolver   // ：促銷（nil 跳過）
	Settings     settingsport.SettingsReader    // ：互斥開關讀取（nil 默認互斥）
	Reseller     resellerport.Pricer            // ：管線步驟 7 分站定價 + 防自購快照（nil 跳過）
	Points       walletport.PointsDebiter       // ：積分兌換下單扣分（nil = 積分單不可用）
	SlowPay      paymentport.SlowPaymentChecker // ：慢通道順延探測（nil = 不順延直接取消；newApp 破環點注入）
	StockGate    orderport.UpstreamStockGate    // ：上游代發項下單前實時庫存預檢（nil = 跳過；newApp 破環點注入）
	AccessCipher *inventory.CardCipher
}

// ttlMinutes 訂單超時分鐘數（settings trade.order_ttl_minutes；缺省/非法回落 30）。
func (uc *OrderUsecase) ttlMinutes(ctx context.Context) int {
	const def = 30
	if uc.Settings == nil {
		return def
	}
	raw, err := uc.Settings.GetJSON(ctx, "trade", "order_ttl_minutes")
	if err != nil || len(raw) == 0 {
		return def
	}
	var v int
	if json.Unmarshal(raw, &v) != nil || v < 1 || v > 1440 {
		return def
	}
	return v
}

// NewOrderUsecaseDep 構造（wire 注入依賴版）。
// SlowPay 經 SetSlowPaymentChecker 由 cmd/zcard newApp 注入（order↔payment wire 環破環點）。
func NewOrderUsecaseDep(d *data.Data, inv port.Inventory, gen *id.Generator, memberRate memberlevelport.RateResolver, coupon couponport.CouponResolver, cat catalogport.PricingResolver, outbox events.Writer, gate auditport.RiskGate, flash couponport.FlashResolver, promos couponport.PromotionResolver, settings settingsport.SettingsReader, reseller resellerport.Pricer, points walletport.PointsDebiter, cipher *inventory.CardCipher) *OrderUsecase {
	return &OrderUsecase{Data: d, Inv: inv, Gen: gen, MemberRate: memberRate, Coupon: coupon, Catalog: cat, Outbox: outbox, Gate: gate, Flash: flash, Promos: promos, Settings: settings, Reseller: reseller, Points: points, AccessCipher: cipher}
}

// SetSlowPaymentChecker 慢通道探測注入（裝配期一次；payment 側實現，
// wire 環：OrderUsecase → payment/port.SlowPaymentChecker → PaymentRepoImpl →
// order/port.OrderLifecycle → OrderUsecase，故走 newApp 手工裝配點）。
func (uc *OrderUsecase) SetSlowPaymentChecker(c paymentport.SlowPaymentChecker) {
	uc.SlowPay = c
}

// SetStockGate 上游庫存預檢閘門注入（裝配期一次；supply 網關實現，
// order → supply 無 wire 依賴，同走 newApp 手工裝配點）。
func (uc *OrderUsecase) SetStockGate(g orderport.UpstreamStockGate) {
	uc.StockGate = g
}

// CreateOrderInput 下單輸入。
type CreateOrderInput struct {
	SocialQuoteID   string
	SMSQuoteID      string
	ShippingAddress map[string]string
	QuoteKey        string
	QuoteOnly       bool
	Items           []OrderItemInput
	UserID          uint64 // 0=遊客
	GuestContact    string
	QueryPassword   string // 明文（bcrypt 哈希後存儲）
	Contact         string
	ClientIP        string
	SubsiteID       uint64
	CouponCode      string            // 優惠券碼（可選）
	ControlAnswers  map[string]string // 兼容舊客戶端的控件答案（新數據落訂單項快照）
	UsePoints       bool              // ：積分兌換下單（全部商品須為積分商品；同事務扣分直落 paid）
	IdempotencyKey  string            // ：下單冪等鍵（頭 Idempotency-Key；同 key 返回首單）
	RefCode         string            // 推廣歸因碼（遊客/無鏈用戶：實時解析推廣者 → 訂單級快照）
}

// OrderItemInput 商品行。
type OrderItemInput struct {
	ControlAnswers map[string]string
	ProductID      uint64
	SkuID          uint64
	Quantity       int32
}

// CreateOrderResult 下單結果。
type CreateOrderResult struct {
	OrderAccessToken string
	ShippingCents    int64
	ShippingCollect  bool
	QuoteKey         string
	OrderNo          string
	TotalCents       int64
	ExpiresAt        time.Time
}

// CreateOrder 下單（單事務編排）。
func (uc *OrderUsecase) CreateOrder(ctx context.Context, in CreateOrderInput) (*CreateOrderResult, error) {
	if in.SocialQuoteID != "" {
		return uc.createSocialOrder(ctx, in)
	}
	tc := tenancy.FromContext(ctx)
	if in.SubsiteID == 0 {
		in.SubsiteID = tc.SubsiteID
	}

	// Stable lock ordering for carts containing the same products in a different order.
	in.Items = append([]OrderItemInput(nil), in.Items...)
	sort.Slice(in.Items, func(i, j int) bool {
		if in.Items[i].ProductID == in.Items[j].ProductID {
			return in.Items[i].SkuID < in.Items[j].SkuID
		}
		return in.Items[i].ProductID < in.Items[j].ProductID
	})
	if len(in.Items) == 0 || len(in.Items) > 100 {
		return nil, fmt.Errorf("order.FORM_INVALID: 商品數量無效")
	}
	if prev, e := uc.ReplayOrder(ctx, in); e != nil || prev != nil {
		return prev, e
	}
	// 下單風控閘門（事務前快速失敗；事務內 pending 計數複查見 Gate 實現説明）
	if uc.Gate != nil && in.ClientIP != "" {
		if err := uc.Gate.Check(ctx, auditport.GateInput{RiskIP: in.ClientIP, UserID: in.UserID}); err != nil {
			return nil, err
		}
	}

	// Snapshot routing before network checks; a concurrent configuration change must retry.
	revisions := map[uint64]int64{}
	smsQuotes := map[uint64]supplyport.SMSQuote{}
	hasSMS := false
	hasPhysical := false
	hasVirtual := false
	shippingByProduct := map[uint64]int64{}
	collectProducts := map[string]any{}
	var shippingTotal int64
	for _, item := range in.Items {
		p, err := data.ProductForDelivery(ctx, data.Client(ctx, uc.Data), in.SubsiteID, item.ProductID)
		if err != nil {
			return nil, err
		}
		if p.ProductKind != "sms_channel" && (in.SMSQuoteID != "") {
			return nil, fmt.Errorf("order.FORM_INVALID: 報價僅適用於接碼渠道商品")
		}
		revisions[p.ID] = p.LockVersion
		if p.DeliveryKind != "" && p.DeliveryKind != "card" {
			if p.DeliveryKind != supplyport.SMSDelivery {
				return nil, fmt.Errorf("order.FORM_INVALID: 不支持的商品交付類型")
			}
			if !data.SMSSalesEnabled() || in.UserID == 0 || len(in.Items) != 1 || item.Quantity != 1 || item.SkuID != 0 || in.UsePoints || in.CouponCode != "" || p.UpstreamSourceID == 0 || p.GoodsType == "physical" {
				return nil, fmt.Errorf("order.FORM_INVALID: 接碼僅支持登錄會員餘額單件獨立購買，暫不支持優惠券和積分；請確認接碼銷售已啓用")
			}
			currency, e := data.BaseCurrency(ctx, uc.Data)
			if e != nil {
				return nil, e
			}
			if currency != "CNY" {
				return nil, fmt.Errorf("order.FORM_INVALID: 接碼僅支持人民幣基礎幣種")
			}
			if data.FulfillmentMode(p, nil) != "upstream" {
				return nil, fmt.Errorf("order.FORM_INVALID: 接碼須使用上游履約")
			}
			if p.ProductKind == "sms_channel" {
				if in.SubsiteID != 0 || in.QuoteOnly || in.IdempotencyKey == "" {
					return nil, fmt.Errorf("order.FORM_INVALID: 渠道接碼須在主站使用有效報價和請求標識")
				}
				q, e := uc.retailSMSQuote(ctx, p, in, false, "")
				if e != nil {
					return nil, e
				}
				smsQuotes[p.ID] = supplyport.SMSQuote{ProductID: q.UpstreamProductID, ConnectionID: q.ConnectionID, Identity: q.ConnectionIdentity, Amount: q.CostCents}
			} else {
				gateway, ok := uc.StockGate.(supplyport.SMSGateway)
				if !ok {
					return nil, fmt.Errorf("order.FORM_INVALID: 接碼服務未就緒")
				}
				quote, e := gateway.PrepareSMS(ctx, p.UpstreamSourceID, p.UpstreamProductCode)
				if e != nil {
					return nil, fmt.Errorf("order.FORM_INVALID: %w", e)
				}
				smsQuotes[p.ID] = quote
			}
			hasSMS = true
		}
		if p.GoodsType != "physical" {
			hasVirtual = true
		}
		if p.GoodsType == "physical" {
			if in.UsePoints {
				return nil, fmt.Errorf("order.FORM_INVALID: 實體商品暫不支持積分兌換")
			}
			if !hasPhysical {
				a, e := shipping.Validate(in.ShippingAddress)
				if e != nil {
					return nil, fmt.Errorf("order.FORM_INVALID: %w", e)
				}
				in.ShippingAddress = a
			}
			hasPhysical = true
			allowed := false
			for _, c := range p.ShippingCountries {
				if c == in.ShippingAddress["country"] {
					allowed = true
				}
			}
			if !allowed {
				return nil, fmt.Errorf("order.FORM_INVALID: 商品不配送至所選國家")
			}
			if _, ok := shippingByProduct[p.ID]; !ok {
				fee := int64(0)
				switch p.ShippingMode {
				case shipping.ModeFixed:
					fee = p.ShippingFee
				case shipping.ModeCollect:
					collectProducts[strconv.FormatUint(p.ID, 10)] = true
				}
				if !money.ValidCents(fee) {
					return nil, fmt.Errorf("order.FORM_INVALID: 商品運費無效")
				}
				shippingByProduct[p.ID] = fee
				shippingTotal += fee
			}
		}
	}
	purePhysical := hasPhysical && !hasVirtual
	if purePhysical {
		if in.QueryPassword != "" && len(in.QueryPassword) < 4 {
			return nil, fmt.Errorf("order.QUERY_PASSWORD_REQUIRED: 查詢密碼至少4位")
		}
		if in.UserID == 0 && in.QueryPassword == "" {
			if _, err := orderaccess.EmailAddress(in.Contact); err != nil {
				return nil, fmt.Errorf("order.CONTACT_INVALID: 請填寫有效郵箱，用於實體訂單查詢與售後")
			}
			if in.IdempotencyKey != "" && !orderaccess.SecureRequestKey(in.IdempotencyKey) {
				return nil, fmt.Errorf("order.FORM_INVALID: 請使用安全隨機請求標識")
			}
		}
	} else {
		if hasPhysical && in.UserID == 0 && len(in.QueryPassword) < 4 {
			return nil, fmt.Errorf("order.QUERY_PASSWORD_REQUIRED: 混合訂單請設置至少4位查詢密碼")
		}
		if err := uc.validateTradeRequirements(ctx, in); err != nil {
			return nil, err
		}
	}
	// ：上游代發項實時庫存預檢（事務前快速失敗——
	// 上游明確無貨直接拒單，不再讓顧客"下單付款後等採購失敗退款"。
	// 本地卡密項的強校驗在下方事務內鎖卡 Reserve；閘門查詢失敗/庫存未知拒單，僅明確不限庫存放行）
	if uc.StockGate != nil && len(in.Items) > 0 {
		gateItems := make([]orderport.UpstreamStockItem, 0, len(in.Items))
		for _, item := range in.Items {
			if item.Quantity > 0 {
				gateItems = append(gateItems, orderport.UpstreamStockItem{ProductID: item.ProductID, SkuID: item.SkuID, Quantity: item.Quantity})
			}
		}
		if len(gateItems) > 0 {
			if err := uc.StockGate.CheckItems(ctx, in.SubsiteID, gateItems); err != nil {
				return nil, fmt.Errorf("order.INSUFFICIENT_STOCK: %w", err)
			}
		}
	}

	if !hasPhysical {
		in.ShippingAddress = nil
	}
	if in.QuoteOnly {
		in.IdempotencyKey = ""
	}
	requestHash := orderRequestHash(in)
	var result *CreateOrderResult
	err := data.Tx(ctx, uc.Data, func(txCtx context.Context) error {
		client := data.Client(txCtx, uc.Data)
		baseCurrency, err := data.BaseCurrency(txCtx, uc.Data)
		if err != nil {
			return err
		}

		// Idempotency-Key：哈希落庫唯一索引；同 key 雙擊返回首單（）
		var idemHash string
		if in.IdempotencyKey != "" {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s:%s", in.SubsiteID, in.UserID, in.Contact, in.IdempotencyKey)))
			idemHash = "idem-" + hex.EncodeToString(sum[:])
			if prev, err := client.Order.Query().
				Where(order.IdempotencyKey(idemHash)).Only(txCtx); err == nil {
				if prev.RequestHash != "" && prev.RequestHash != requestHash {
					return fmt.Errorf("order.FORM_INVALID: 同一請求標識不能用於不同訂單內容")
				}
				exp := prev.ExpiredAt
				if exp.IsZero() {
					exp = time.Now().Add(time.Duration(uc.ttlMinutes(txCtx)) * time.Minute).UTC()
				}
				result, err = uc.replayOrderResult(prev, in)
				if err != nil {
					return err
				}
				result.ExpiresAt = exp
				return nil // 冪等快路徑：重複請求返回首次結果
			}
		}

		// ：積分兌換前置（登錄 + 引擎裝配；遊客/未裝配直接拒絕不建單）
		if in.UsePoints {
			if in.UserID == 0 {
				return fmt.Errorf("order.POINTS_LOGIN_REQUIRED")
			}
			if uc.Points == nil {
				return fmt.Errorf("order.POINTS_UNAVAILABLE")
			}
		}

		// 1) 鎖卡（庫存不足整批迴滾）。上游代發商品跳過本地鎖卡——卡密在
		// 支付後由 procurement 向上遊採購回填（ 鏈路；本地池無其卡）。
		// url/code 直發商品同樣跳過——直發內容商品級共享（同一鏈接/兌換碼
		// 反覆發貨，無卡池概念），支付後由 fulfillment 直寫交付記錄。
		hiddenCategories, err := data.HiddenCategoryIDs(txCtx, client, in.SubsiteID)
		if err != nil {
			return err
		}
		services, err := uc.prepareServices(txCtx, in, revisions)
		if err != nil {
			return err
		}
		upstreamItem := map[[2]uint64]bool{} // product + SKU fulfillment route
		directItem := map[[2]uint64]bool{}   // product + SKU direct route（url/code）
		var reserveItems []port.ReserveItem
		for _, item := range in.Items {
			if item.Quantity <= 0 {
				continue
			}
			p, err := client.Product.Query().
				Where(product.ID(item.ProductID), product.SubsiteID(in.SubsiteID)).
				Only(txCtx)
			if err != nil {
				continue // 商品校驗在計價循環統一做（PRODUCT_NOT_FOUND）
			}
			if services[itemKey(item.ProductID, item.SkuID)].mode == "manual" || services[itemKey(item.ProductID, item.SkuID)].mode == "shipping" {
				continue
			}
			if services[itemKey(item.ProductID, item.SkuID)].mode == "reuse" {
				directItem[itemKey(item.ProductID, item.SkuID)] = true
				continue
			}
			if services[itemKey(item.ProductID, item.SkuID)].mode == "upstream" {
				upstreamItem[itemKey(item.ProductID, item.SkuID)] = true
				continue
			}
			if p.StockType != product.StockTypeCard {
				directItem[itemKey(item.ProductID, item.SkuID)] = true
				continue // 鏈接/兌換碼直發：不佔卡池
			}
			reserveItems = append(reserveItems, port.ReserveItem{
				ProductID: item.ProductID,
				SkuID:     item.SkuID,
				Quantity:  item.Quantity,
			})
		}
		var reservedIDs []uint64
		if len(reserveItems) > 0 {
			res, err := uc.Inv.Reserve(txCtx, in.SubsiteID, reserveItems)
			if err != nil {
				return fmt.Errorf("order.INSUFFICIENT_STOCK: %w", err)
			}
			if res != nil {
				for _, v := range res.Cards {
					reservedIDs = append(reservedIDs, v.CardID)
				}
			}
		}

		// 2) 生成訂單號（雪花）
		snowflakeID, err := uc.Gen.Next()
		if err != nil {
			return err
		}
		orderNo := id.FormatNo("S", snowflakeID)

		// 3) 會員折扣（萬分比，按用戶累計消費匹配；解析失敗則中止，避免指定等級失效時以錯誤價格成交）
		var memberRate int32
		var memberLevelID uint64
		if uc.MemberRate != nil && in.UserID > 0 {
			r, lvl, err := uc.MemberRate.EffectiveRate(txCtx, in.UserID)
			if err != nil {
				return fmt.Errorf("order.MEMBER_LEVEL_INVALID: %w", err)
			}
			memberRate, memberLevelID = r, lvl
		}

		// 4) 算價管線（每商品行獨立跑管線；會員折扣逐行，優惠券整單後置）
		type itemResult struct {
			input            OrderItemInput
			res              PriceResult
			cost             int64
			productName      string
			inventoryTracked bool
		}
		var results []itemResult
		var totalCents int64
		var pointsTotal int64               // ：積分兌換單合計（積分單位）
		var cartItems []couponport.CartItem // 券範圍判定輸入
		var flashReservations []flashReservation
		flashApplied := false        // 券×秒殺互斥判據
		var totalSubsiteMarkup int64 // 分站加價合計（利潤基數快照）

		for _, item := range in.Items {
			// 共享鎖允許同商品併發下單，並讓下架/刪除等待已開始的下單事務。
			// SQLite 由事務快照及寫入衝突保證一致性，不支持行級鎖。
			q := client.Product.Query().Where(product.ID(item.ProductID), product.SubsiteID(in.SubsiteID))
			if uc.Data.Dialect != db.SQLite {
				q = q.ForShare()
			}
			p, err := q.Only(txCtx)
			if ent.IsNotFound(err) {
				return fmt.Errorf("order.PRODUCT_NOT_FOUND")
			}
			if err != nil {
				return err
			}
			if (p.Status != 1 && !(p.ProductKind == "sms_channel" && p.Status == 2 && in.UserID > 0)) || data.CategoryHidden(hiddenCategories, p.CategoryID) {
				return fmt.Errorf("order.PRODUCT_NOT_AVAILABLE") // 下架/隱藏
			}
			// ：積分兌換單——全部商品須為積分商品（混合單拒絕，口徑清晰）
			if in.UsePoints {
				if p.PointsRequired <= 0 {
					return fmt.Errorf("order.POINTS_MIXED_CART")
				}
				pointsTotal += p.PointsRequired * int64(item.Quantity)
			}

			// Invalid/missing SKUs must not silently fall back to the product price.
			basePrice := money.Cents(p.Price)
			if uc.Catalog != nil && p.ProductKind != "sms_channel" {
				sp, err := uc.Catalog.ResolvePrice(txCtx, item.ProductID, item.SkuID)
				if err != nil {
					return fmt.Errorf("order.SKU_INVALID: %w", err)
				}
				basePrice = sp
			}
			if p.ProductKind == "sms_channel" {
				q, e := uc.retailSMSQuote(txCtx, p, in, false, "")
				if e != nil {
					return e
				}
				basePrice = money.Cents(q.AmountCents)
			}
			// 商品組必須攜帶疊加策略；解析失敗不能繞過禁止疊加的設置。
			var group catalogport.GroupDiscount
			if uc.Catalog != nil {
				group, err = uc.Catalog.ResolveGroupDiscount(txCtx, item.ProductID)
				if err != nil {
					return fmt.Errorf("order.GROUP_LOOKUP_FAILED: %w", err)
				}
			}
			itemMemberRate, groupRate := memberRate, group.Rate
			if !group.StackMember && groupRate > 0 && groupRate < 10000 && memberRate > 0 && memberRate < 10000 {
				// 禁止疊加時取較低應付比例；同價優先會員，不觸發商品組用券限制。
				if memberRate <= groupRate {
					groupRate = 0
				} else {
					itemMemberRate = 0
				}
			}
			if p.ProductKind == "sms_channel" {
				itemMemberRate, groupRate = 0, 0
			}
			priceInput := PriceInput{BasePrice: basePrice, Quantity: 1, MemberRate: itemMemberRate, GroupRate: groupRate}
			discountedUnit := PriceCalculator(priceInput).Total

			// 步驟 4：秒殺（窗口判定 + 限購 + 待付款預佔；正式扣減在 MarkPaid）
			var flashPrice money.Cents
			if uc.Flash != nil && !hasSMS {
				fs, err := uc.Flash.Active(txCtx, item.ProductID, item.SkuID)
				if err != nil {
					return fmt.Errorf("order.FLASH_LOOKUP_FAILED: %w", err)
				}
				if fs != nil {
					// 限購：窗口內 paid+pending 累計
					if fs.PerUserLimit > 0 && in.UserID > 0 {
						bought, err := uc.Flash.UserPurchasedCount(txCtx, item.ProductID, in.UserID, fs.StartAt)
						if err != nil {
							return err
						}
						if bought+item.Quantity > fs.PerUserLimit {
							return couponport.ErrFlashUserLimit
						}
					}
					// 臨時預佔與訂單同事務落庫，未付款不增加 sold_qty。
					if err := uc.Flash.Reserve(txCtx, fs.ID, item.Quantity); err != nil {
						return err
					}
					flashReservations = append(flashReservations, newFlashReservation(fs.ID, item.Quantity))
					flashPrice = fs.FlashPrice
					flashApplied = true
				}
			}

			// 步驟 4.5：促銷（會員折扣後、券前；與秒殺互斥——flash 生效時跳過）
			var promoDiscount money.Cents
			var promoName string
			if uc.Promos != nil && flashPrice == 0 && !hasSMS {
				if pi, err := uc.Promos.BestFor(txCtx, item.ProductID, p.CategoryID, discountedUnit); err == nil && pi != nil {
					if d := pi.DiscountFor(discountedUnit); d > 0 {
						promoDiscount, promoName = d, pi.Name
					}
				}
			}

			// 步驟 7：分站定價（listing 與 checkout 共用同一 ResolveUnitPrice——
			// 1.x 鐵律，分站價只在一處計算）。秒殺價覆蓋分站價（flash 生效時跳過）。
			var subsiteMarkup money.Cents
			if uc.Reseller != nil && in.SubsiteID != tenancy.MainSubsiteID && flashPrice == 0 {
				if sp, err := uc.Reseller.ResolveUnitPrice(txCtx, in.SubsiteID, item.ProductID, item.SkuID, basePrice); err == nil && sp > basePrice {
					subsiteMarkup = sp - basePrice
				}
			}
			totalSubsiteMarkup += int64(subsiteMarkup) * int64(item.Quantity)

			priceInput.Quantity = item.Quantity
			priceInput.FlashPrice = flashPrice
			priceInput.PromoDiscount = promoDiscount
			priceInput.PromoName = promoName
			priceInput.SubsiteMarkup = subsiteMarkup
			pr := PriceCalculator(priceInput)
			cost := p.FactoryPrice
			if p.GoodsType == "physical" && item.SkuID != 0 {
				sk, e := data.DeliverySKU(txCtx, client, p, item.SkuID)
				if e != nil {
					return e
				}
				if sk.Cost > 0 {
					cost = sk.Cost
				}
			}
			results = append(results, itemResult{
				input: item, res: pr, cost: cost,
				productName:      p.Name,
				inventoryTracked: p.GoodsType == "physical" && p.TrackInventory,
			})
			totalCents += int64(pr.Total)
			if groupRate == 0 || group.StackCoupon {
				// 券在會員/商品組/促銷之後計算，不能抵扣分站加價或禁止用券的商品。
				cartItems = append(cartItems, couponport.CartItem{
					ProductID: item.ProductID, SkuID: item.SkuID, CategoryID: p.CategoryID,
					Quantity: item.Quantity, UnitPrice: pr.Total/money.Cents(item.Quantity) - subsiteMarkup,
				})
			}
		}

		// 4.6) 優惠券（整單一次性；範圍矩陣 + 每人限用；券×秒殺互斥默認開）
		// ：積分兌換單不參與券/金額管線——應付恆 0，憑據=積分流水（type=redeem）
		var couponValue int64
		var couponID uint64
		if in.UsePoints {
			totalCents = 0
		} else if uc.Coupon != nil && in.CouponCode != "" && !uc.flashCouponExclusive(txCtx, flashApplied) {
			v, cid, err := uc.Coupon.ResolveScoped(txCtx, in.CouponCode, in.UserID, memberLevelID, cartItems)
			if err != nil {
				return fmt.Errorf("order.COUPON_INVALID: %w", err)
			}
			couponValue = int64(v)
			couponID = cid
		}
		if couponValue > totalCents {
			couponValue = totalCents
		}
		totalCents -= couponValue

		totalCents += shippingTotal
		if !money.ValidCents(totalCents) {
			return fmt.Errorf("order.FORM_INVALID: 訂單金額超出上限")
		}
		quotePrices := []int64{}
		for _, r := range results {
			quotePrices = append(quotePrices, int64(r.res.Total))
		}
		quoteParts := []any{requestHash, totalCents, shippingTotal, couponValue, quotePrices, shippingByProduct, revisions}
		if len(collectProducts) > 0 {
			quoteParts = append(quoteParts, collectProducts)
		}
		quoteBytes, _ := json.Marshal(quoteParts)
		quoteKey := fmt.Sprintf("%x", sha256.Sum256(quoteBytes))
		if hasPhysical && !in.QuoteOnly && in.QuoteKey != quoteKey {
			return fmt.Errorf("order.FORM_INVALID: 請重新確認訂單金額和運費")
		}
		var allocationBefore, allocationBase int64
		couponWeights := map[[2]uint64]int64{}
		if hasPhysical && couponValue > 0 {
			resolver, ok := uc.Coupon.(interface {
				EligibleItems(context.Context, uint64, uint64, []couponport.CartItem) ([]couponport.CartItem, error)
			})
			if !ok {
				return fmt.Errorf("order.COUPON_INVALID: 商品券分攤服務不可用")
			}
			eligible, e := resolver.EligibleItems(txCtx, couponID, memberLevelID, cartItems)
			if e != nil {
				return e
			}
			for _, it := range eligible {
				weight := int64(it.UnitPrice) * int64(it.Quantity)
				couponWeights[itemKey(it.ProductID, it.SkuID)] = weight
				allocationBase += weight
			}
		}
		shippingAssigned := map[uint64]bool{}

		// 4) 查詢密碼哈希
		var queryPwdHash string
		if in.QueryPassword != "" && !in.QuoteOnly {
			// 接 argon2；當前 bcrypt（crypto 包）
			hash, err := hashQueryPassword(in.QueryPassword)
			if err != nil {
				return err
			}
			queryPwdHash = hash
		}

		// 5) 寫 orders（父單）
		ttl := time.Duration(uc.ttlMinutes(txCtx)) * time.Minute
		exp := time.Now().Add(ttl).UTC()
		if in.SMSQuoteID != "" {
			q, e := client.SMSRetailQuote.Get(txCtx, in.SMSQuoteID)
			if e != nil {
				return e
			}
			deadline := time.Unix(q.ExpiresAt, 0).UTC()
			if deadline.Before(exp) {
				exp = deadline
			}
		}
		extra := map[string]any{}
		if len(collectProducts) > 0 {
			extra[shippingCollectProductsKey] = collectProducts
		}
		if len(flashReservations) > 0 {
			extra["flash_reservations"] = flashReservations
		}

		if in.UsePoints {
			extra["points_total"] = pointsTotal // 積分口徑快照（退款/審計讀此處）
		}
		// ：三級分銷歸因鏈快照（下單瞬間鎖定）。優先級：
		// 1) 登錄用戶讀自身邀請鏈（註冊時綁定）；
		// 2) 鏈為空且帶 ref_code（推廣鏈接進站）→ 實時解析推廣者訂單級歸因
		// （不改用戶鏈——一次性快照；遊客/無鏈老用戶下單均發傭）
		var invL1, invL2, invL3 uint64
		if in.UserID > 0 {
			if u, err := client.User.Get(txCtx, in.UserID); err == nil {
				invL1, invL2, invL3 = u.InviteL1, u.InviteL2, u.InviteL3
			}
		}
		if invL1 == 0 && in.RefCode != "" {
			if inviter := uc.resolveRefCode(txCtx, in.RefCode); inviter != nil && inviter.ID != in.UserID {
				invL1, invL2, invL3 = inviter.ID, inviter.InviteL1, inviter.InviteL2
			}
		}
		// ：分站快照（下單瞬間鎖定——分賬與退款逆向只認快照，不回溯）
		profitEligible := true
		if uc.Reseller != nil && in.SubsiteID != tenancy.MainSubsiteID {
			profitEligible = uc.Reseller.ProfitEligible(txCtx, in.SubsiteID, in.UserID)
		}
		var subsiteDomain *string
		if in.SubsiteID != tenancy.MainSubsiteID && tc.Host != "" {
			host := tc.Host
			subsiteDomain = &host
		}
		if hasSMS {
			if totalCents <= 0 {
				return fmt.Errorf("order.FORM_INVALID: 接碼實付必須大於零")
			}
			invL1, invL2, invL3 = 0, 0, 0
		}
		create := client.Order.Create().
			SetOrderNo(orderNo).
			SetSubsiteID(in.SubsiteID).
			SetSubsiteProfit(totalSubsiteMarkup).
			SetProfitEligible(profitEligible).
			SetNillableSubsiteDomain(subsiteDomain).
			SetNillableInviteL1(nilOrZero(invL1)).
			SetNillableInviteL2(nilOrZero(invL2)).
			SetNillableInviteL3(nilOrZero(invL3)).
			SetUserID(in.UserID).
			SetNillableUserID(nilOrZero(in.UserID)).
			SetGuestContact(in.GuestContact).
			SetQueryPasswordHash(queryPwdHash).
			SetStatus(order.StatusPendingPayment).
			SetTotalAmount(totalCents).
			SetRequestHash(requestHash).
			SetShippingAmount(shippingTotal).
			SetShippingAddress(in.ShippingAddress).
			SetBaseCurrency(baseCurrency).
			SetContact(in.Contact).
			SetClientIP(in.ClientIP).
			SetRiskIP(auditport.NormalizeIP(in.ClientIP)).
			SetExpiredAt(exp).
			SetVersion(0).
			SetExtra(extra)
		if hasPhysical {
			create.SetCommerceVersion(1).SetShippingStatus("pending_payment")
		}
		var accessToken string
		if purePhysical && in.UserID == 0 && in.QueryPassword == "" && !in.QuoteOnly {
			if uc.AccessCipher == nil {
				return fmt.Errorf("order.CREATE_FAILED: 訂單授權服務未就緒")
			}
			accessToken, err = orderaccess.NewToken()
			if err != nil {
				return err
			}
			secret, e := uc.AccessCipher.SealOrderAccess(accessToken, orderNo, in.SubsiteID)
			if e != nil {
				return e
			}
			create.SetOrderAccessTokenHash(orderaccess.TokenHash(in.SubsiteID, orderNo, accessToken)).SetOrderAccessTokenSecret(secret)
		}
		if idemHash != "" {
			create.SetIdempotencyKey(idemHash)
		}
		o, err := create.Save(txCtx)

		if err != nil {
			return fmt.Errorf("order.CREATE_FAILED: %w", err)
		}

		// 6) 綁定訂單到卡（Reserve 後回填 order_id；上游/直發項無本地卡可綁）
		if binder, ok := uc.Inv.(interface {
			BindReserved(context.Context, uint64, []uint64) error
		}); ok {
			if err := binder.BindReserved(txCtx, o.ID, reservedIDs); err != nil {
				return err
			}
		} else {
			for _, item := range in.Items {
				if services[itemKey(item.ProductID, item.SkuID)].mode == "manual" || services[itemKey(item.ProductID, item.SkuID)].mode == "shipping" {
					continue
				}
				if upstreamItem[itemKey(item.ProductID, item.SkuID)] {
					continue
				}
				if directItem[itemKey(item.ProductID, item.SkuID)] {
					continue
				}
				if err := uc.Inv.BindOrder(txCtx, in.SubsiteID, item.ProductID, o.ID, item.Quantity); err != nil {
					return err
				}
			}
		}

		// 7) 寫 order_items + order_amount_lines
		var amountSeq int32
		for _, r := range results {
			paid := int64(r.res.Total)
			fee := int64(0)
			kind := "virtual"
			if hasPhysical {
				weight := couponWeights[itemKey(r.input.ProductID, r.input.SkuID)]
				paid -= allocateCents(couponValue, allocationBefore, allocationBefore+weight, allocationBase)
				allocationBefore += weight
			}
			if services[itemKey(r.input.ProductID, r.input.SkuID)].mode == "shipping" {
				kind = "physical"
				if !shippingAssigned[r.input.ProductID] {
					fee = shippingByProduct[r.input.ProductID]
					shippingAssigned[r.input.ProductID] = true
				}
			}
			orderItem, err := client.OrderItem.Create().
				SetOrderID(o.ID).
				SetSubsiteID(in.SubsiteID).
				SetProductID(r.input.ProductID).
				SetSkuID(r.input.SkuID).
				SetUnitPrice(int64(r.res.Lines[0].Amount)).
				SetQuantity(r.input.Quantity).
				SetAmount(int64(r.res.Total)).
				SetGoodsType(kind).SetInventoryTracked(r.inventoryTracked).SetPaidAmount(paid).SetShippingAmount(fee).
				SetProfitSnapshot(func() map[string]any {
					var markup int64
					for _, line := range r.res.Lines {
						if line.Type == "subsite_markup" {
							markup += line.Amount * int64(r.input.Quantity)
						}
					}
					return map[string]any{"markup": markup}
				}()).
				SetCost(func() int64 {
					if services[itemKey(r.input.ProductID, r.input.SkuID)].mode == "reuse" {
						return 0
					}
					return r.cost
				}()).
				SetFulfillmentType(orderitem.FulfillmentType(services[itemKey(r.input.ProductID, r.input.SkuID)].mode)).
				SetDeliverySourceID(services[itemKey(r.input.ProductID, r.input.SkuID)].sourceID).
				SetProductName(services[itemKey(r.input.ProductID, r.input.SkuID)].name).
				SetSkuName(services[itemKey(r.input.ProductID, r.input.SkuID)].skuName).
				SetFormAnswers(services[itemKey(r.input.ProductID, r.input.SkuID)].answers).
				SetFulfillmentStatus("pending").
				Save(txCtx)
			if err != nil {
				return fmt.Errorf("order.ITEM_CREATE_FAILED: %w", err)
			}

			if hasSMS {
				if err := uc.freezeSMSSelection(txCtx, orderItem, smsQuotes[r.input.ProductID], in, orderNo); err != nil {
					return err
				}
			}
			if kind == "physical" && orderItem.InventoryTracked {
				if err := data.MovePhysicalStock(txCtx, uc.Data, o.SubsiteID, r.input.ProductID, r.input.SkuID, o.ID, -int64(r.input.Quantity), fmt.Sprintf("reserve:%d", orderItem.ID), "下單預佔"); err != nil {
					return err
				}
			}
			if fee > 0 {
				if err := client.OrderAmountLine.Create().SetOrderID(o.ID).SetItemID(orderItem.ID).SetType("shipping").SetAmount(fee).SetSeq(amountSeq).Exec(txCtx); err != nil {
					return err
				}
				amountSeq++
			}
			for _, line := range r.res.Lines {
				_, err := client.OrderAmountLine.Create().
					SetOrderID(o.ID).
					SetItemID(orderItem.ID).
					SetType(orderamountline.Type(line.Type)).
					SetAmount(line.Amount * int64(r.input.Quantity)).
					SetSourceType(line.SourceType).
					SetSourceID(line.SourceID).
					SetSeq(amountSeq).
					SetMeta(line.Meta).
					Save(txCtx)
				if err != nil {
					return fmt.Errorf("order.AMOUNT_LINE_FAILED: %w", err)
				}
				amountSeq++
			}
		}

		// 7.5) 優惠券金額行 + 核銷
		if couponValue > 0 {
			_, err := client.OrderAmountLine.Create().
				SetOrderID(o.ID).
				SetType(orderamountline.TypeCouponDiscount).
				SetAmount(-couponValue).
				SetSourceType("coupon").
				SetSourceID(couponID).
				SetSeq(amountSeq).
				Save(txCtx)
			if err != nil {
				return fmt.Errorf("order.COUPON_LINE_FAILED: %w", err)
			}
			if uc.Coupon != nil {
				if err := uc.Coupon.MarkUsed(txCtx, couponID, o.ID); err != nil {
					return fmt.Errorf("order.COUPON_MARK_FAILED: %w", err)
				}
			}
		}

		// 8) 狀態事件溯源（初始 created）
		_, _ = client.OrderStatusEvent.Create().
			SetOrderID(o.ID).
			SetFromStatus("").
			SetToStatus(string(order.StatusPendingPayment)).
			SetEvent("created").
			SetOperator(orderstatusevent.OperatorSystem).
			SetClientIP(in.ClientIP).
			Save(txCtx)

		if in.QuoteOnly {
			result = &CreateOrderResult{TotalCents: totalCents, ShippingCents: shippingTotal, ShippingCollect: len(collectProducts) > 0, QuoteKey: quoteKey}
			return errQuoteRollback
		}
		if hasPhysical && totalCents == 0 {
			if err := uc.MarkPaid(txCtx, orderNo); err != nil {
				return err
			}
		}
		// 9) 積分兌換收尾：同事務扣分（冪等鍵 points_pay:<orderNo>；
		// 不足整單回滾）→ 直落 paid（狀態機 CAS + order.paid 事件 → 自動交付）。
		if in.UsePoints {
			if err := uc.Points.PointDebitInTx(txCtx, walletport.PointEntry{
				UserID:    in.UserID,
				Direction: "out",
				Type:      "redeem",
				Amount:    pointsTotal,
				Reference: "points_pay:" + orderNo,
				OrderID:   o.ID,
				Remark:    "積分商城兌換",
			}); err != nil {
				return fmt.Errorf("order.POINTS_INSUFFICIENT: %w", err)
			}
			if err := uc.MarkPaid(txCtx, orderNo); err != nil {
				return fmt.Errorf("order.POINTS_MARK_PAID_FAILED: %w", err)
			}
		}

		result = &CreateOrderResult{
			OrderAccessToken: accessToken,
			OrderNo:          orderNo,
			ShippingCollect:  len(collectProducts) > 0,
			ShippingCents:    shippingTotal, QuoteKey: quoteKey,
			TotalCents: totalCents,
			ExpiresAt:  exp,
		}
		return nil
	})
	if errors.Is(err, errQuoteRollback) {
		return result, nil
	}
	// A unique-key race must roll back all reservations before returning the first order.
	if ent.IsConstraintError(err) && in.IdempotencyKey != "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s:%s", in.SubsiteID, in.UserID, in.Contact, in.IdempotencyKey)))
		if prev, qerr := data.Client(ctx, uc.Data).Order.Query().Where(order.IdempotencyKey("idem-" + hex.EncodeToString(sum[:]))).Only(ctx); qerr == nil {
			if prev.RequestHash != "" && prev.RequestHash != requestHash {
				return nil, fmt.Errorf("order.FORM_INVALID: 請求內容已變化")
			}
			return uc.replayOrderResult(prev, in)
		}
	}
	return result, err
}

// MarkPaid 支付回調事務內調用（ payment 消費）。
func (uc *OrderUsecase) MarkPaid(ctx context.Context, orderNo string) error {
	return data.Tx(ctx, uc.Data, func(ctx context.Context) error { return uc.markPaid(ctx, orderNo) })
}

func (uc *OrderUsecase) markPaid(ctx context.Context, orderNo string) error {
	client := data.Client(ctx, uc.Data)
	o, err := client.Order.Query().Where(order.OrderNo(orderNo)).Only(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("order.NOT_FOUND")
	}
	if err != nil {
		return err
	}
	// 冪等：已 paid 直接成功
	if o.Status == order.StatusPaid {
		return nil
	}
	if err := uc.validateSocialPayment(ctx, o.ID); err != nil {
		return err
	}
	if !Allow(string(o.Status), string(order.StatusPaid)) {
		return fmt.Errorf("order.TRANSITION_NOT_ALLOWED: %s → paid", o.Status)
	}
	// 樂觀鎖 CAS
	affected, err := client.Order.Update().
		Where(order.ID(o.ID), order.StatusEQ(o.Status), order.VersionEQ(o.Version)).
		SetStatus(order.StatusPaid).
		SetPaidAt(time.Now().UTC()).
		SetVersion(o.Version + 1).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("order.CONCURRENT_UPDATE")
	}
	if err := uc.settleFlashReservations(ctx, o, true); err != nil {
		return err
	}
	if o.CommerceVersion > 0 {
		if err = client.Order.UpdateOneID(o.ID).SetShippingStatus("pending").Exec(ctx); err != nil {
			return err
		}
	}
	// 狀態事件溯源
	_, err = client.OrderStatusEvent.Create().
		SetOrderID(o.ID).
		SetFromStatus(string(o.Status)).
		SetToStatus(string(order.StatusPaid)).
		SetEvent("paid").
		SetOperator(orderstatusevent.OperatorSystem).
		Save(ctx)
	if err != nil {
		return err
	}
	if err := uc.createSMSIntents(ctx, o); err != nil {
		return err
	}
	if err := uc.createSocialTasks(ctx, o); err != nil {
		return err
	}
	return uc.publishPaid(ctx, client, o)
}

// publishPaid 發佈 order.paid（ procurement 訂閲；payload 含 upstream 項，
// 消費方無需回查訂單——跨模塊查詢受限）。
func (uc *OrderUsecase) publishPaid(ctx context.Context, client *ent.Client, o *ent.Order) error {
	if uc.Outbox == nil {
		return nil
	}
	items, err := client.OrderItem.Query().
		Where(orderitem.OrderID(o.ID)).
		All(ctx)
	if err != nil {
		return err
	}
	type paidItem struct {
		OrderItemID     uint64 `json:"order_item_id"`
		ProductID       uint64 `json:"product_id"`
		SkuID           uint64 `json:"sku_id"`
		Quantity        int32  `json:"quantity"`
		FulfillmentType string `json:"fulfillment_type"`
	}
	rows := []paidItem{}
	for _, it := range items {
		rows = append(rows, paidItem{
			OrderItemID: it.ID,
			ProductID:   it.ProductID,
			SkuID:       it.SkuID,
			Quantity:    it.Quantity,
			FulfillmentType: func() string {
				if it.DeliveryKind == "social_service" {
					return "social_service"
				}
				if it.DeliveryKind == "sms_activation" {
					return "sms_activation"
				}
				return string(it.FulfillmentType)
			}(),
		})
	}
	payload := map[string]any{
		"order_no":   o.OrderNo,
		"order_id":   o.ID,
		"subsite_id": o.SubsiteID,
		"user_id":    o.UserID,
		// ：歸因鏈快照（affiliate 消費；事件自帶免回查）
		"invite_l1": o.InviteL1, "invite_l2": o.InviteL2, "invite_l3": o.InviteL3,
		"total_cents": o.TotalAmount - o.ShippingAmount,
		// 毛利口徑基數（amount − cost；affiliate BaseScope=profit 消費）
		"profit_cents": o.TotalAmount - o.ShippingAmount - orderCostOf(items),
		// ：分站快照（reseller 分賬消費；自購快照不產生利潤）
		"subsite_profit":  o.SubsiteProfit,
		"profit_eligible": o.ProfitEligible,
		"items":           rows,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return uc.Outbox.Write(ctx, "order", events.OrderPaid, o.OrderNo, DedupeKey(o.OrderNo, "paid"), raw)
}

// ListUserOrders 用戶訂單列表（登錄態「我的訂單」；offset 分頁——單用戶量級安全）。
func (uc *OrderUsecase) ListUserOrders(ctx context.Context, userID uint64, status string, page, size int) ([]*ent.Order, int64, error) {
	if userID == 0 {
		return nil, 0, fmt.Errorf("order.USER_REQUIRED")
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 50 {
		size = 20
	}
	q := data.Client(ctx, uc.Data).Order.Query().
		Where(order.UserID(userID)).
		Order(ent.Desc(order.FieldID))
	if status != "" {
		q = q.Where(order.StatusEQ(order.Status(status)))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Clone().WithItems().Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, int64(total), err
}

// GetByOrderNo 按單號查訂單。
func (uc *OrderUsecase) GetByOrderNo(ctx context.Context, orderNo string) (*ent.Order, error) {
	return data.Client(ctx, uc.Data).Order.Query().Where(order.OrderNo(orderNo)).Only(ctx)
}

// ListOrders 訂單列表（遊標分頁）。
func (uc *OrderUsecase) ListOrders(ctx context.Context, subsiteID uint64, status string, cursor uint64, limit int32, keyword string, filters ...predicate.Order) ([]*ent.Order, error) {
	q := data.Client(ctx, uc.Data).Order.Query().
		Where(order.SubsiteID(subsiteID), order.AdminDeletedAtIsNil()).
		Order(ent.Desc(order.FieldID)).
		Limit(int(limit))
	q = q.Where(filters...)
	switch status {
	case "needs_delivery":
		q = q.Where(order.StatusIn(order.StatusPaid, order.StatusFulfilling, order.StatusPartiallyDelivered))
	case "needs_refund":
		q = q.Where(order.Or(order.StatusEQ(order.StatusRefundPending), order.HasRefundsWith(refundorder.StatusIn(refundorder.StatusCreated, refundorder.StatusProcessing))))
	case "":
	default:
		q = q.Where(order.StatusEQ(order.Status(status)))
	}
	if cursor > 0 {
		q = q.Where(order.IDLT(cursor))
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q = q.Where(order.Or(order.OrderNoContains(keyword), order.ContactContains(keyword), order.GuestContactContains(keyword)))
	}
	return q.All(ctx)
}

// ── 工具 ──

func hashQueryPassword(plain string) (string, error) {
	// M1a 用 bcrypt（與 admin 登錄同庫）； 換 argon2
	return crypto.HashPassword(plain)
}

func nilOrZero(v uint64) *uint64 {
	if v == 0 {
		return nil
	}
	return &v
}

var _ = decimal.NewFromInt // 保持 decimal 引用（rounding 接入）

// flashCouponExclusive 券×秒殺互斥判定（settings.trade.flash_coupon_exclusive；
// 讀取失敗/未配置默認互斥——保守口徑）。
func (uc *OrderUsecase) flashCouponExclusive(ctx context.Context, flashApplied bool) bool {
	if !flashApplied {
		return false
	}
	if uc.Settings == nil {
		return true
	}
	raw, err := uc.Settings.GetJSON(ctx, "trade", "flash_coupon_exclusive")
	if err != nil || len(raw) == 0 {
		return true
	}
	var v struct {
		Exclusive *bool `json:"flash_coupon_exclusive"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Exclusive == nil {
		return true
	}
	return *v.Exclusive
}

// resolveRefCode 推廣碼解析（雙格式：8 位隨機碼 promo_code 匹配 / 舊數字 user_id）。
// 與 identity.ResolvePromoCode 同口徑；模塊不互相依賴（ent 直查，通道 A 免疫）。
func (uc *OrderUsecase) resolveRefCode(ctx context.Context, code string) *ent.User {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil
	}
	client := data.Client(ctx, uc.Data)
	if u, err := client.User.Query().Where(user.PromoCode(code)).Only(ctx); err == nil {
		return u
	}
	// 舊數字 user_id 兼容（存量推廣鏈接）
	if isDigitStr(code) {
		var id uint64
		for _, c := range code {
			id = id*10 + uint64(c-'0')
		}
		if id > 0 {
			if u, err := client.User.Get(ctx, id); err == nil {
				return u
			}
		}
	}
	return nil
}

func isDigitStr(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validateTradeRequirements 交易設置下單校驗（settings.trade）：
// - query_password（默認 true）：下單必須設置查詢密碼（≥4 位）——否則客戶忘記
// 密碼且無聯繫方式時訂單無法找回
// - contact_required（默認 any）：none|phone|email|qq|any——遊客必須留對應
// 格式聯繫方式作為查詢標識；登錄用戶已有賬戶可追溯，不強制
//
// 讀取失敗走保守默認（強制密碼 + any）。
func (uc *OrderUsecase) validateTradeRequirements(ctx context.Context, in CreateOrderInput) error {
	if in.SMSQuoteID != "" && in.UserID > 0 {
		return nil
	}
	// 查詢密碼強制（所有下單者——含登錄用戶：取貨三重門的核心憑證）
	if uc.queryPasswordRequired(ctx) {
		if len(in.QueryPassword) < 4 {
			return fmt.Errorf("order.QUERY_PASSWORD_REQUIRED: 請設置查詢密碼（至少 4 位，取貨時使用）")
		}
	}
	// 聯繫方式（僅遊客；登錄用戶有賬戶可追溯；積分兌換遊客在事務內 POINTS_LOGIN 拒絕，
	// 此處跳過避免攔截語義——聯繫方式校驗只對常規遊客單生效）
	if (in.UserID == 0 || uc.contactScope(ctx) == "all") && !in.UsePoints {
		mode := uc.contactRequired(ctx)
		contact := strings.TrimSpace(in.Contact)
		if contact == "" {
			contact = strings.TrimSpace(in.GuestContact)
		}
		if mode != "none" {
			if contact == "" {
				return fmt.Errorf("order.CONTACT_REQUIRED: 請填寫聯繫方式（%s），用於訂單查詢與售後", contactModeLabel(mode))
			}
			if !contactMatchesMode(contact, mode) {
				return fmt.Errorf("order.CONTACT_INVALID: 聯繫方式格式不符（需要%s）", contactModeLabel(mode))
			}
		}
	}
	return nil
}

// queryPasswordRequired 查詢密碼強制開關（settings.trade.query_password；默認 true）。
func (uc *OrderUsecase) queryPasswordRequired(ctx context.Context) bool {
	if uc.Settings == nil {
		return true
	}
	raw, err := uc.Settings.GetJSON(ctx, "trade", "query_password")
	if err != nil || len(raw) == 0 {
		return true
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return true
	}
	return v
}

// contactRequired 聯繫方式要求（settings.trade.contact_required；默認 any）。
func (uc *OrderUsecase) contactRequired(ctx context.Context) string {
	if uc.Settings == nil {
		return "any"
	}
	raw, err := uc.Settings.GetJSON(ctx, "trade", "contact_required")
	if err != nil || len(raw) == 0 {
		return "any"
	}
	var v string
	if json.Unmarshal(raw, &v) != nil || v == "" {
		return "any"
	}
	return v
}

// contactModeLabel 聯繫方式要求顯示名。
func contactModeLabel(mode string) string {
	switch mode {
	case "phone":
		return "手機號"
	case "email":
		return "郵箱"
	case "qq":
		return "QQ 號"
	default:
		return "郵箱或手機號等任一聯繫方式"
	}
}

// contactMatchesMode 聯繫方式格式校驗（寬鬆口徑：any=郵箱/手機/QQ 任一）。
func contactMatchesMode(contact, mode string) bool {
	isEmail := strings.Contains(contact, "@") && strings.Contains(contact, ".")
	isPhone := len(contact) >= 7 && len(contact) <= 15 && isDigitsOrPhone(contact)
	isQQ := isDigits(contact) && len(contact) >= 5 && len(contact) <= 12
	switch mode {
	case "phone":
		return isPhone
	case "email":
		return isEmail
	case "qq":
		return isQQ
	default: // any
		return isEmail || isPhone || isQQ
	}
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func isDigitsOrPhone(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && r != '+' && r != '-' && r != ' ' {
			return false
		}
	}
	return true
}

// orderCostOf 訂單成本合計（order_items 無成本列——按商品 factory_price 近似；
// 精確成本隨採購聯動， affiliate 毛利口徑以事件快照為準）。
func orderCostOf(items []*ent.OrderItem) int64 {
	return 0 // 事件側精確毛利 M4 採購成本回填後啓用；當前毛利口徑 = 金額（BaseScope=amount 默認）
}

// fulfillmentTypeOf 上游代發項 → upstream（procurement 訂閲 order.paid 後向上游
// 採購）；本地商品 → auto（本地卡密履約）。
func fulfillmentTypeOf(isUpstream bool) orderitem.FulfillmentType {
	if isUpstream {
		return orderitem.FulfillmentTypeUpstream
	}
	return orderitem.FulfillmentTypeAuto
}

// contactScope preserves existing sites; operators can require every buyer.
func (uc *OrderUsecase) contactScope(ctx context.Context) string {
	if uc.Settings != nil {
		raw, err := uc.Settings.GetJSON(ctx, "trade", "contact_scope")
		var scope string
		if err == nil && json.Unmarshal(raw, &scope) == nil && scope == "all" {
			return "all"
		}
	}
	return "guest"
}
