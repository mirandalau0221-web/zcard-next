package payment

// 渠道管理 + 回調管線 + 退款 v1（ 核心交易支付）。
//
// 渠道憑據 AES-256-GCM 加密存儲（ZCARD_DATA_KEY），解密失敗降級為空——列表絕不 500。
// 回調管線：四重校驗（渠道/單號/金額/幣種）→ 冪等三層 → markPaid → 事務後事件。

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/predicate"
	"sort"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/payment"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/paymentchannel"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/rechargeorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
	orderport "github.com/NovaWorks/zcard-next/server/internal/mods/order/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment/currencyunit"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
	settingsport "github.com/NovaWorks/zcard-next/server/internal/mods/settings/port"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/shopspring/decimal"
)

// PaymentRepoImpl 支付倉儲。
type PaymentRepoImpl struct {
	data   *data.Data
	Cipher *crypto.Box // ZCARD_DATA_KEY
	reg    *Registry   // 渠道 adapter 註冊表
	// 回調管線依賴（wire 注入， 破環點）：
	lifecycle orderport.OrderLifecycle    // 訂單型回調 → 狀態機推進 + order.paid 事件（同事務）
	wallet    walletport.Wallet           // 餘額支付扣款 / 充值到賬入賬（通道 B 同事務）
	points    walletport.Points           // 充值贈送積分（積分賬本；nil = 未裝配跳過）
	outbox    events.Writer               // recharge.succeeded 等事件
	currency  settingsport.CurrencyReader // 幣種快照換算（；nil = 同幣直收）
	settings  settingsport.Provider       // site/url 等（ 回調地址拼接；nil = 相對路徑）
	supplier  port.SupplierRecharger      // target=supply 充值入賬（供貨賬户餘額；nil = 未裝配跳過）
}

// NewPaymentRepoImpl 構造。
func NewPaymentRepoImpl(d *data.Data, box *crypto.Box, reg *Registry, lifecycle orderport.OrderLifecycle, wallet walletport.Wallet, points walletport.Points, outbox events.Writer, currency settingsport.CurrencyReader, settings settingsport.Provider, supplier port.SupplierRecharger) *PaymentRepoImpl {
	return &PaymentRepoImpl{data: d, Cipher: box, reg: reg, lifecycle: lifecycle, wallet: wallet, points: points, outbox: outbox, currency: currency, settings: settings, supplier: supplier}
}

// ChargeSnapshot freezes the exchange rate and provider unit before any gateway request.
// Display precision never participates in charging or callback settlement.
type ChargeSnapshot struct {
	Units     int64
	Currency  string
	Rate      float64
	Precision int32
}

func chargeUnits(amount money.Cents, rate decimal.Decimal, unit currencyunit.ChargeUnit) (int64, error) {
	if amount <= 0 || !rate.IsPositive() || unit.Step <= 0 {
		return 0, fmt.Errorf("payment.INVALID_CHARGE_AMOUNT")
	}
	d := decimal.NewFromInt(int64(amount)).Shift(-2).Mul(rate).Shift(unit.Precision).
		Div(decimal.NewFromInt(unit.Step)).Round(0).Mul(decimal.NewFromInt(unit.Step))
	if !d.IsPositive() || d.GreaterThan(decimal.NewFromInt(1<<63-1)) {
		return 0, fmt.Errorf("payment.INVALID_CHARGE_AMOUNT: 換算金額過小或超出範圍")
	}
	return d.IntPart(), nil
}

func (r *PaymentRepoImpl) computeCharge(ctx context.Context, driver string, cfg json.RawMessage, amount money.Cents) (ChargeSnapshot, error) {
	base, err := data.BaseCurrency(ctx, r.data)
	if err != nil {
		return ChargeSnapshot{}, err
	}
	target, err := chargeCurrency(driver, cfg, base)
	if err != nil {
		return ChargeSnapshot{}, err
	}
	// Preserve the legacy representation for CNY channels and old callbacks.
	if base == "CNY" && target == "CNY" {
		return ChargeSnapshot{}, nil
	}
	unit, err := currencyunit.CurrencyChargeUnit(driver, target)
	if err != nil {
		return ChargeSnapshot{}, err
	}
	rate := decimal.NewFromInt(1)
	if target != base {
		if r.currency == nil {
			return ChargeSnapshot{}, fmt.Errorf("payment.CURRENCY_MISSING: 未配置幣種 %s", target)
		}
		rateStr, _, e := r.currency.CurrencyByCode(ctx, target)
		if e != nil {
			return ChargeSnapshot{}, fmt.Errorf("payment.CURRENCY_MISSING: 未配置幣種 %s", target)
		}
		rate, err = decimal.NewFromString(rateStr)
		if err != nil || !rate.IsPositive() {
			return ChargeSnapshot{}, fmt.Errorf("payment.INVALID_EXCHANGE_RATE")
		}
	}
	rate = rate.Round(8)
	rf, _ := rate.Float64()
	if !rate.IsPositive() || rate.GreaterThan(decimal.RequireFromString("999999999999.99999999")) {
		return ChargeSnapshot{}, fmt.Errorf("payment.INVALID_EXCHANGE_RATE")
	}
	rate = decimal.NewFromFloat(rf).Round(8)
	units, err := chargeUnits(amount, rate, unit)
	if err != nil {
		return ChargeSnapshot{}, err
	}
	return ChargeSnapshot{Units: units, Currency: target, Rate: rf, Precision: unit.Precision}, nil
}

// CNY-only drivers must never interpret an AUD base amount as yuan. Multi-currency
// drivers receive an explicit charge snapshot even for a same-currency AUD charge.
func chargeCurrency(driver string, cfg json.RawMessage, base string) (string, error) {
	var probe struct {
		TargetCurrency string `json:"target_currency"`
		Currency       string `json:"currency"`
	}
	if err := json.Unmarshal(cfg, &probe); err != nil {
		return "", fmt.Errorf("payment.CHANNEL_CONFIG_INVALID: %w", err)
	}
	target := strings.ToUpper(strings.TrimSpace(probe.TargetCurrency))
	switch driver {
	case "wallet":
		return base, nil
	case "stripe", "paypal":
		if target == "" {
			target = base
		}
		if _, err := currencyunit.CurrencyChargeUnit(driver, target); err != nil {
			return "", err
		}
	case "epusdt":
		fiat := strings.ToUpper(strings.TrimSpace(probe.Currency))
		if fiat == "" {
			fiat = "CNY"
		}
		if target != "" && target != fiat {
			return "", fmt.Errorf("payment.CHANNEL_CONFIG_INVALID: GMPay 請使用 currency 配置法幣")
		}
		target = fiat
		if _, err := currencyunit.CurrencyChargeUnit(driver, target); err != nil {
			return "", err
		}
	default:
		if base != "CNY" || (target != "" && target != "CNY") {
			return "", fmt.Errorf("payment.CURRENCY_UNSUPPORTED: %s 不支持基礎貨幣 %s", driver, base)
		}
		target = "CNY"
	}
	return target, nil
}

func (r *PaymentRepoImpl) snapshotCharge(ctx context.Context, paymentID uint64, snap ChargeSnapshot) error {
	if snap.Units == 0 {
		return nil
	}
	_, err := data.Client(ctx, r.data).Payment.UpdateOneID(paymentID).
		SetChargedUnits(snap.Units).SetChargedCurrency(snap.Currency).
		SetExchangeRate(snap.Rate).SetChargedPrecision(snap.Precision).Save(ctx)
	return err
}

// callbackPrecision never consults current display settings. For legacy rows,
// infer only when the original authoritative amount/rate reproduces the exact
// stored provider units; otherwise retain the payment for manual review.
func (r *PaymentRepoImpl) callbackPrecision(ctx context.Context, p *ent.Payment) (int32, bool) {
	if p.ChargedPrecision >= 0 {
		return p.ChargedPrecision, p.ChargedPrecision <= 3
	}
	driver := r.snapshotDriver(ctx, p)
	unit, err := currencyunit.CurrencyChargeUnit(driver, p.ChargedCurrency)
	if err != nil {
		return -1, false
	}
	units, err := chargeUnits(money.Cents(p.Amount), decimal.NewFromFloat(p.ExchangeRate), unit)
	if err == nil && units == p.ChargedUnits {
		return unit.Precision, true
	}
	// Before the fix, PayPal strings/callbacks always used two decimals, even
	// for JPY/HUF/TWD. Recover that legacy scale only when the snapshot matches.
	if driver == "paypal" && unit.Precision == 0 {
		legacy := currencyunit.ChargeUnit{Precision: 2, Step: 100}
		units, err = chargeUnits(money.Cents(p.Amount), decimal.NewFromFloat(p.ExchangeRate), legacy)
		if err == nil && units == p.ChargedUnits {
			return 2, true
		}
	}
	return unit.Precision, false
}

func (r *PaymentRepoImpl) snapshotDriver(ctx context.Context, p *ent.Payment) string {
	if p.DriverSnapshot != "" {
		return p.DriverSnapshot
	}
	if ch, err := r.channelForPayment(ctx, p); err == nil {
		return ch.Driver
	}
	return ""
}

// ── 渠道管理（）────────────────────────────────────────────

// ListChannels 渠道列表（憑據脱敏）。
func (r *PaymentRepoImpl) ListChannels(ctx context.Context) ([]*ent.PaymentChannel, error) {
	return data.Client(ctx, r.data).PaymentChannel.Query().
		Where(paymentchannel.DeletedAtIsNil()).
		Order(ent.Asc(paymentchannel.FieldSort), ent.Asc(paymentchannel.FieldID)).
		All(ctx)
}

// ChannelMethod 支付方式（收銀台顧客看到的選項；params 承載網關路由參數）。
type ChannelMethod struct {
	ChannelUsage
	Code                 string            `json:"code"`
	Name                 string            `json:"name"`
	Icon                 string            `json:"icon,omitempty"`
	Enabled              bool              `json:"enabled"`
	Params               map[string]string `json:"params,omitempty"`
	Recommended          bool              `json:"recommended,omitempty"`
	RecommendLabel       string            `json:"recommend_label,omitempty"`
	RecommendDescription string            `json:"recommend_description,omitempty"`
}

// parseMethods 渠道方式列表解析（methods JSON 空 → nil = 單方式渠道舊語義）。
func parseMethods(ch *ent.PaymentChannel) []ChannelMethod {
	if len(ch.Methods) == 0 {
		return nil
	}
	b, err := json.Marshal(ch.Methods)
	if err != nil {
		return nil
	}
	var out []ChannelMethod
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// methodsJSON 校驗並歸一化方式列表 JSON（空串 → nil）。
func methodsJSON(raw string) ([]map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var ms []ChannelMethod
	if err := json.Unmarshal([]byte(raw), &ms); err != nil {
		return nil, fmt.Errorf("payment.METHODS_INVALID: 支付方式列表格式錯誤: %w", err)
	}
	seen := map[string]bool{}
	for _, m := range ms {
		if err := validateRecommendation(m.RecommendLabel, m.RecommendDescription); err != nil {
			return nil, err
		}
		if seen[m.Code] {
			return nil, fmt.Errorf("payment.METHODS_INVALID: 支付方式標識不能重複")
		}
		seen[m.Code] = true
		if m.Code == "" || m.Name == "" {
			return nil, fmt.Errorf("payment.METHODS_INVALID: 方式的 code/name 必填")
		}
	}
	b, _ := json.Marshal(ms)
	var out []map[string]any
	_ = json.Unmarshal(b, &out)
	return out, nil
}

func (r *PaymentRepoImpl) createChannel(ctx context.Context, name, code, driver, configJSON string, fee int64, feeType string, enabled bool, sort int32, icon string, methods []map[string]any, usage ...ChannelUsage) (*ent.PaymentChannel, error) {
	if isNativeCryptoDriver(driver) && len(methods) > 0 {
		return nil, fmt.Errorf("payment.METHODS_INVALID: 此渠道不支持本地支付方式列表，請在渠道參數中配置幣種與網絡")
	}
	// A deleted channel keeps its code for historical callbacks. A replacement
	// requested from the visible list must receive a fresh code and encryption AAD.
	if isNativeCryptoDriver(driver) {
		c := data.Client(ctx, r.data)
		reserved, err := c.PaymentChannel.Query().Where(paymentchannel.SubsiteID(0), paymentchannel.Code(code), paymentchannel.DeletedAtNotNil()).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if reserved {
			base := code
			for n := 2; ; n++ {
				suffix := fmt.Sprintf("-%d", n)
				code = base[:min(len(base), 30-len(suffix))] + suffix
				exists, err := c.PaymentChannel.Query().Where(paymentchannel.SubsiteID(0), paymentchannel.Code(code)).Exist(ctx)
				if err != nil {
					return nil, err
				}
				if !exists {
					break
				}
			}
		}
	}
	enc, err := r.Cipher.Seal([]byte(configJSON), []byte("payment_channel:"+code))
	if err != nil {
		return nil, fmt.Errorf("payment: 憑據加密失敗: %w", err)
	}
	q := data.Client(ctx, r.data).PaymentChannel.Create().
		SetName(name).
		SetCode(code).
		SetDriver(driver).
		SetConfig(enc).
		SetFee(fee).
		SetFeeType(paymentchannel.FeeType(feeType)).
		SetEnabled(enabled).
		SetSort(sort).
		SetIcon(icon)
	if len(usage) > 0 {
		q.SetNillableAllowPurchase(usage[0].AllowPurchase).SetNillableAllowMemberRecharge(usage[0].AllowMemberRecharge).SetNillableAllowSupplyRecharge(usage[0].AllowSupplyRecharge)
	}
	if methods != nil {
		q = q.SetMethods(methods)
	}
	return q.Save(ctx)
}

// CreateChannel 創建渠道（憑據加密入庫；methodsJSON=支付方式列表）。
func (r *PaymentRepoImpl) CreateChannel(ctx context.Context, name, code, driver, configJSON string, fee int64, feeType string, enabled bool, sort int32, icon string, methods []map[string]any, usage ...ChannelUsage) (out *ent.PaymentChannel, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.createChannel(ctx, name, code, driver, configJSON, fee, feeType, enabled, sort, icon, methods, usage...)
		return e
	})
	return out, err
}

// UpdateChannel 更新渠道（config_json=**** 跳過憑據修改；feeType 空=不修改；
// setIcon/setMethods=false 保持原值——proto optional 語義）。
func (r *PaymentRepoImpl) UpdateChannel(ctx context.Context, id uint64, name, configJSON string, fee int64, feeType string, enabled bool, sort int32, setIcon bool, icon string, setMethods bool, methods []map[string]any, usage ...ChannelUsage) (*ent.PaymentChannel, error) {
	var result *ent.PaymentChannel
	err := data.CurrencyTx(ctx, r.data, func(txCtx context.Context) error {
		ch, err := data.Client(txCtx, r.data).PaymentChannel.UpdateOneID(id).AddSort(0).Save(txCtx)
		if err != nil {
			return err
		}
		if !ch.DeletedAt.IsZero() {
			return fmt.Errorf("payment.CHANNEL_DELETED: 渠道已刪除，請新建渠道")
		}
		result, err = r.updateChannel(txCtx, id, name, configJSON, fee, feeType, enabled, sort, setIcon, icon, setMethods, methods, usage...)
		return err
	})
	return result, err
}

func (r *PaymentRepoImpl) updateChannel(ctx context.Context, id uint64, name, configJSON string, fee int64, feeType string, enabled bool, sort int32, setIcon bool, icon string, setMethods bool, methods []map[string]any, usage ...ChannelUsage) (*ent.PaymentChannel, error) {
	if setMethods && len(methods) > 0 {
		ch, err := data.Client(ctx, r.data).PaymentChannel.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if isNativeCryptoDriver(ch.Driver) {
			return nil, fmt.Errorf("payment.METHODS_INVALID: 此渠道不支持本地支付方式列表，請在渠道參數中配置幣種與網絡")
		}
	}
	q := data.Client(ctx, r.data).PaymentChannel.UpdateOneID(id)
	if len(usage) > 0 {
		q.SetNillableAllowPurchase(usage[0].AllowPurchase).SetNillableAllowMemberRecharge(usage[0].AllowMemberRecharge).SetNillableAllowSupplyRecharge(usage[0].AllowSupplyRecharge)
	}
	if name != "" {
		q.SetName(name)
	}
	if configJSON != "" && configJSON != `"****"` {
		ch, err := data.Client(ctx, r.data).PaymentChannel.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := r.checkBepusdtConfigChange(ctx, ch, configJSON); err != nil {
			return nil, err
		}
		enc, err := r.Cipher.Seal([]byte(configJSON), []byte("payment_channel:"+ch.Code))
		if err != nil {
			return nil, err
		}
		q.SetConfig(enc)
	}
	if fee >= 0 {
		q.SetFee(fee)
	}
	if feeType != "" {
		q.SetFeeType(paymentchannel.FeeType(feeType))
	}
	q.SetEnabled(enabled)
	if sort >= 0 {
		q.SetSort(sort)
	}
	if setIcon {
		q.SetIcon(icon)
	}
	if setMethods {
		if methods != nil {
			q.SetMethods(methods)
		} else {
			q.ClearMethods()
		}
	}
	return q.Save(ctx)
}

// DeleteChannel 刪除渠道。
func (r *PaymentRepoImpl) DeleteChannel(ctx context.Context, id uint64) error {
	return data.Tx(ctx, r.data, func(txCtx context.Context) error {
		c := data.Client(txCtx, r.data)
		ch, err := c.PaymentChannel.UpdateOneID(id).AddSort(0).Save(txCtx)
		if err != nil {
			return err
		}
		if isNativeCryptoDriver(ch.Driver) {
			if !ch.DeletedAt.IsZero() {
				return nil
			}
			if ch.Enabled {
				return fmt.Errorf("payment.CHANNEL_ENABLED: 請先停用渠道再刪除")
			}
			// Keep the original identity and encrypted credentials for late callbacks.
			_, err = c.PaymentChannel.UpdateOneID(id).SetDeletedAt(time.Now().UTC()).Save(txCtx)
			return err
		}
		return c.PaymentChannel.DeleteOneID(id).Exec(txCtx)
	})
}

// DecryptConfig 解密憑據（失敗降級為空，鐵律 5）。
func (r *PaymentRepoImpl) DecryptConfig(ch *ent.PaymentChannel) json.RawMessage {
	if r.Cipher == nil {
		return json.RawMessage("{}")
	}
	plain, err := r.Cipher.Open(ch.Config, []byte("payment_channel:"+ch.Code))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(plain)
}

// CallbackURL 渠道回調地址（站點 URL 拼接；site/url 未配置回落相對路徑——
// ：admin 配置面板展示覆制，填到支付平台 webhook/notify 配置）。
func (r *PaymentRepoImpl) CallbackURL(ctx context.Context, code string) string {
	base := ""
	if r.settings != nil {
		if raw, err := r.settings.Get(ctx, "site", "url"); err == nil && len(raw) > 2 {
			var v string
			if json.Unmarshal(raw, &v) == nil {
				base = strings.TrimRight(strings.TrimSpace(v), "/")
				// 站點設置允許裸域名；先補協議，避免 absolutePayURL 將域名
				// 當作相對路徑再次拼接到請求 Host 後。
				if strings.HasPrefix(base, "//") {
					base = "https:" + base
				} else if base != "" && !strings.Contains(base, "://") {
					base = "https://" + base
				}
			}
		}
	}
	return base + "/payments/callback/" + code
}

// ConfiguredFields 已配置字段名列表（解密後統計非空值——僅名不顯值，脱敏；
// admin 前端「已配置」狀態判定 + 敏感字段編輯時留空不覆蓋）。
func (r *PaymentRepoImpl) ConfiguredFields(ch *ent.PaymentChannel) []string {
	cfg := r.DecryptConfig(ch)
	if len(cfg) == 0 || string(cfg) == "{}" {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(cfg, &m) != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		var sv string
		if json.Unmarshal(v, &sv) == nil {
			if strings.TrimSpace(sv) != "" {
				out = append(out, k)
			}
			continue
		}
		// 數組值（多選字段 token/network）：非空數組算已配置
		var arr []json.RawMessage
		if json.Unmarshal(v, &arr) == nil && len(arr) > 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ── 支付單（）────────────────────────────────────────────

// CreatePayment 創建支付單。
func (r *PaymentRepoImpl) CreatePayment(ctx context.Context, orderID uint64, channel string, amount int64, idemKey string, methods ...string) (*ent.Payment, error) {
	if preflight, ok := r.lifecycle.(interface {
		ValidateSMSPayment(context.Context, uint64) error
	}); ok {
		if err := preflight.ValidateSMSPayment(ctx, orderID); err != nil {
			return nil, err
		}
	}
	if preflight, ok := r.lifecycle.(interface {
		ValidateSocialPayment(context.Context, uint64) error
	}); ok {
		if err := preflight.ValidateSocialPayment(ctx, orderID); err != nil {
			return nil, err
		}
	}
	var result *ent.Payment
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		client := data.Client(ctx, r.data)
		o, err := client.Order.Get(ctx, orderID)
		if err != nil {
			return err
		}
		if err := r.validateOrderCurrency(ctx, o); err != nil {
			return err
		}
		now := time.Now().UTC()
		if o.Status != order.StatusPendingPayment || o.ExpiredAt.IsZero() || !now.Before(o.ExpiredAt) || o.ExpiryReview {
			return fmt.Errorf("payment.ORDER_EXPIRED: 訂單已過期或待核對，請重新下單")
		}
		ch, err := resolveChannel(ctx, r.data, o.SubsiteID, channel)
		if err != nil {
			return err
		}
		if e := data.ValidateSMSPayment(ctx, client, o.ID); e != nil {
			return e
		}
		hasSMS, e := data.HasSMSOrder(ctx, client, o.ID)
		if e != nil {
			return e
		}
		if hasSMS && (ch.Driver != "wallet" || !data.SMSSalesEnabled() || o.UserID == 0 || o.BaseCurrency != "CNY" || ch.Fee != 0) {
			return fmt.Errorf("payment.SMS_WALLET_ONLY: 接碼僅支持會員餘額付款，請確認銷售已啓用")
		}
		hasSocial, socialErr := data.HasSocialOrder(ctx, client, o.ID)
		if socialErr != nil {
			return socialErr
		}
		if hasSocial && (ch.Driver != "wallet" || o.UserID == 0 || o.BaseCurrency != "CNY" || ch.Fee != 0) {
			return fmt.Errorf("payment.SOCIAL_WALLET_ONLY: 服務僅支持會員餘額付款")
		}
		if !ch.Enabled {
			return fmt.Errorf("payment.CHANNEL_DISABLED")
		}
		// Serialize attempt creation against cancellation/settlement without keeping a
		// transaction open during the external gateway request.
		n, err := client.Order.Update().Where(order.ID(o.ID), order.StatusEQ(order.StatusPendingPayment), order.VersionEQ(o.Version), order.ExpiredAtGT(now)).SetVersion(o.Version + 1).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("payment.ORDER_CHANGED")
		}
		method := ""
		if len(methods) > 0 {
			method = methods[0]
		}
		price, err := r.price(ctx, ch, o.TotalAmount, method)
		if err != nil {
			return err
		}
		if err = checkQuote(ctx, pricingQuote(price, ch.Code, ch.ID, o.OrderNo, 0)); err != nil {
			return err
		}
		if port.QuoteKey(ctx) != "" {
			candidates, e := client.Payment.Query().Where(payment.OrderID(o.ID), payment.ChannelID(ch.ID), payment.StatusEQ(payment.StatusPending)).Order(ent.Desc(payment.FieldID)).All(ctx)
			if e != nil {
				return e
			}
			for _, prev := range candidates {
				if prev.GatewayOrderRef != "" && len(prev.PricingSnapshot) > 0 && pricingOf(prev) == price {
					result = prev
					return nil
				}
			}
		}
		ref, err := bepusdtNonce()
		if err != nil {
			return err
		}
		result, err = client.Payment.Create().SetPricingSnapshot(pricingJSON(price)).SetFee(price.Fee).SetGatewayOrderRef("ZP" + ref[:30]).SetOrderID(orderID).SetSubsiteID(o.SubsiteID).SetChannel(channel).SetChannelID(ch.ID).SetDriverSnapshot(ch.Driver).SetExpiresAt(o.ExpiredAt).SetAmount(price.Total).SetChargedUnits(price.Charge.Units).SetChargedCurrency(price.Charge.Currency).SetChargedPrecision(price.Charge.Precision).SetExchangeRate(price.Charge.Rate).SetStatus(payment.StatusPending).SetIdempotencyKey(idemKey).Save(ctx)
		return err
	})
	return result, err
}

// CreateRechargePayment 充值支付單（RechargePayer 端口實現）：
// 建單（關聯 recharge_order_id）→ 渠道發起（金額=服務端落庫值）→ 返回跳轉信息。
func (r *PaymentRepoImpl) CreateRechargePayment(ctx context.Context, rechargeOrderID uint64, channel, method string, amount money.Cents) (*port.RechargePaymentInfo, error) {
	client := data.Client(ctx, r.data)
	ro, err := client.RechargeOrder.Get(ctx, rechargeOrderID)
	if err != nil {
		return nil, fmt.Errorf("payment.RECHARGE_NOT_FOUND")
	}
	ch, err := resolveChannel(ctx, r.data, 0, channel)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("payment.CHANNEL_NOT_FOUND")
	}
	if err != nil {
		return nil, err
	}
	if !ch.Enabled {
		return nil, fmt.Errorf("payment.CHANNEL_DISABLED")
	}
	if ch.Driver == "wallet" {
		return nil, fmt.Errorf("payment.RECHARGE_CHANNEL_INVALID: 充值不支持餘額渠道")
	}
	scene := sceneMemberRecharge
	if ro.Target == rechargeorder.TargetSupply {
		scene = sceneSupplyRecharge
	}
	if err := r.checkPaymentUsage(ch, method, scene); err != nil {
		return nil, err
	}
	// 方式級路由（與訂單支付同口徑）：多方式渠道 method 必填且須在啓用列表內
	var methodCode string
	var methodParams map[string]string
	if ms := parseMethods(ch); len(ms) > 0 {
		for _, m := range ms {
			if m.Enabled && m.Code == method {
				methodCode, methodParams = m.Code, m.Params
				break
			}
		}
		if methodCode == "" {
			return nil, fmt.Errorf("payment.METHOD_INVALID: 請選擇該渠道支持的支付方式")
		}
	}
	if isNativeCryptoDriver(ch.Driver) {
		return r.createBepusdtPayment(ctx, ch.ID, 0, ro.ID, method)
	}
	price, err := r.price(ctx, ch, ro.Amount, method)
	if err != nil {
		return nil, err
	}
	if err = checkQuote(ctx, pricingQuote(price, ch.Code, ch.ID, scene, 0)); err != nil {
		return nil, err
	}
	ref, err := bepusdtNonce()
	if err != nil {
		return nil, err
	}
	p, err := client.Payment.Create().SetPricingSnapshot(pricingJSON(price)).SetFee(price.Fee).SetGatewayOrderRef("ZP" + ref[:30]).
		SetChargedUnits(price.Charge.Units).SetChargedCurrency(price.Charge.Currency).SetChargedPrecision(price.Charge.Precision).SetExchangeRate(price.Charge.Rate).
		SetRechargeOrderID(ro.ID).
		SetSubsiteID(0).SetChannelID(ch.ID).SetDriverSnapshot(ch.Driver).
		SetChannel(channel).
		SetAmount(price.Total).
		SetStatus(payment.StatusPending).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	provider, err := r.reg.Provider(ch.Driver)
	if err != nil {
		return nil, fmt.Errorf("payment.CHANNEL_UNSUPPORTED: %w", err)
	}
	cfg := r.DecryptConfig(ch)
	if err := provider.ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("payment.CHANNEL_CONFIG_INVALID: %w", err)
	}
	snap := price.Charge
	// 回跳/回調絕對化（同訂單支付口徑）：return 回充值 tab——曾不傳，
	// 網關回跳空地址即 404；notify 走 site/url 或請求 Host
	info, err := r.dispatchPayment(ctx, p, provider, port.CreatePaymentRequest{
		OrderNo: p.GatewayOrderRef, GatewayOrderRef: p.GatewayOrderRef,
		Channel:      channel,
		Amount:       money.Cents(p.Amount),
		Subject:      "餘額充值",
		ChargedUnits: snap.Units, ChargedCurrency: snap.Currency,
		ReturnURL:     absolutePayURL(ctx, "/member?tab=recharge"),
		NotifyBaseURL: absolutePayURL(ctx, r.callbackURLFor(ctx, ch)),
		Config:        cfg,
		MethodCode:    methodCode, MethodParams: methodParams,
	})
	if err != nil {
		return nil, fmt.Errorf("payment.CREATE_FAILED: %w", err)
	}
	return &port.RechargePaymentInfo{
		PaymentID: p.ID, Type: info.Type, Payload: string(info.Payload), BaseCents: price.Base, FeeCents: price.Fee, TotalCents: price.Total,
	}, nil
}

// GetPayment 按 ID 查支付單。
func (r *PaymentRepoImpl) GetPayment(ctx context.Context, id uint64) (*ent.Payment, error) {
	return data.Client(ctx, r.data).Payment.Get(ctx, id)
}

// ListPayments 支付單列表。
func (r *PaymentRepoImpl) ListPayments(ctx context.Context, status, orderNo string, cursor uint64, limit int32, reviewOnly ...bool) ([]*ent.Payment, error) {
	q := data.Client(ctx, r.data).Payment.Query().
		Where(payment.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).
		Order(ent.Desc(payment.FieldID)).
		Limit(int(limit))
	if len(reviewOnly) > 0 && reviewOnly[0] {
		q = q.Where(payment.ReviewReasonNEQ(""))
	}
	if status != "" {
		q = q.Where(payment.StatusEQ(payment.Status(status)))
	}
	if cursor > 0 {
		q = q.Where(payment.IDLT(cursor))
	}
	if orderNo != "" {
		o, err := data.Client(ctx, r.data).Order.Query().Where(order.OrderNo(orderNo)).Only(ctx)
		if ent.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		q = q.Where(payment.OrderID(o.ID))
	}
	return q.All(ctx)
}

// HandleCallback 回調處理管線（ 核心——四重校驗+冪等+業務推進）。
// 事務內：行鎖 payment → 冪等三層 → 按支付單類型分流：
//
//	訂單型（order_id>0）：餘額渠道先扣款（wallet.DebitInTx，同事務）→
//
// OrderLifecycle.MarkPaid（狀態機 CAS + 狀態事件 + outbox order.paid， 破環點）
//
//	充值型（recharge_order_id>0）：充值單 pending→success → 餘額入賬
//
// （amount+gift，reference=recharge:<paymentID> 冪等）→ outbox recharge.succeeded
//
// 支付確認前零入賬（鐵律 16）；事件與入賬同事務，回滾不殘留。
func (r *PaymentRepoImpl) HandleCallback(ctx context.Context, paymentID uint64, fact CallbackFact) error {
	if !fact.Success {
		return fmt.Errorf("payment.NOT_SUCCESSFUL")
	}
	return data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)

		// 1) 行鎖支付單
		p, err := client.Payment.Query().Where(payment.ID(paymentID)).Only(txCtx)
		if ent.IsNotFound(err) {
			return fmt.Errorf("payment.NOT_FOUND")
		}
		if err != nil {
			return err
		}

		// Native crypto gateways must bind the exact attempt even for duplicate callbacks.
		if isNativeCryptoDriver(p.DriverSnapshot) {
			p, err = r.lockBepusdtPayment(txCtx, p.ID)
			if err != nil {
				return err
			}
			if fact.GatewayOrderRef == "" || fact.GatewayOrderRef != p.GatewayOrderRef || fact.ChannelOrderNo == "" || (p.ChannelOrderNo != "" && p.ChannelOrderNo != fact.ChannelOrderNo) {
				return fmt.Errorf("payment.GATEWAY_ORDER_MISMATCH")
			}
			if fact.Channel != p.Channel || fact.Amount != p.Amount || fact.Currency != "CNY" {
				return fmt.Errorf("payment.AMOUNT_MISMATCH")
			}
		}

		// 2) 冪等第一層：已 success 直接 ACK
		if p.Status == payment.StatusSuccess {
			return nil
		}

		// 3) 四重校驗（渠道/單號/金額/幣種——金額永遠對服務端權威值）
		// 跨幣路徑（ 快照）：回調金額=渠道幣種最小單位，對 charged_units
		// 精確核對（網關回顯下單金額，零二次換算）；實收換算回基礎貨幣分落 charged_amount。
		// 同幣路徑（charged_units=0）：舊口徑 fact.Amount==amount + CNY。
		if fact.Channel != p.Channel {
			return fmt.Errorf("payment.CHANNEL_MISMATCH")
		}
		chargedBase := fact.Amount
		reviewReason := ""
		precision := p.ChargedPrecision
		if p.ChargedUnits > 0 {
			// Old PayPal zero-decimal callback parsing used x100 units. Preserve
			// the historical snapshot representation; new payments use protocol units.
			if p.ChargedPrecision < 0 && r.snapshotDriver(txCtx, p) == "paypal" {
				unit, err := currencyunit.CurrencyChargeUnit("paypal", p.ChargedCurrency)
				if err == nil && unit.Precision == 0 && fact.Amount > 0 && fact.Amount <= (1<<63-1)/100 && fact.Amount*100 == p.ChargedUnits {
					fact.Amount *= 100
				}
			}
			if fact.Amount != p.ChargedUnits {
				return fmt.Errorf("payment.AMOUNT_MISMATCH: want units %d got %d", p.ChargedUnits, fact.Amount)
			}
			if !strings.EqualFold(fact.Currency, p.ChargedCurrency) {
				return fmt.Errorf("payment.CURRENCY_MISMATCH: want %s got %s", p.ChargedCurrency, fact.Currency)
			}
			var trustworthy bool
			precision, trustworthy = r.callbackPrecision(txCtx, p)
			rate := decimal.NewFromFloat(p.ExchangeRate)
			if !trustworthy || !rate.IsPositive() {
				// Record receipt without crediting wallet or fulfilling an ambiguous payment.
				reviewReason = "歷史跨幣支付金額單位無法確認，請核對網關實收後補單或退款"
				chargedBase = 0
			} else {
				base, _ := money.FromDisplay(fact.Amount, rate, precision)
				chargedBase = int64(base)
			}
		} else {
			if fact.Amount != p.Amount {
				return fmt.Errorf("payment.AMOUNT_MISMATCH: want %d got %d", p.Amount, fact.Amount)
			}
			base, err := r.paymentBaseCurrency(txCtx, p)
			if err != nil {
				return err
			}
			if !strings.EqualFold(fact.Currency, base) {
				return fmt.Errorf("payment.CURRENCY_MISMATCH")
			}
		}

		if p.OrderID > 0 {
			o, err := client.Order.Get(txCtx, p.OrderID)
			if err != nil {
				return err
			}
			if fact.OrderNo != "" && fact.OrderNo != o.OrderNo && fact.OrderNo != p.GatewayOrderRef {
				return fmt.Errorf("payment.ORDER_MISMATCH")
			}
			fact.OrderNo = o.OrderNo
		}
		// 4) 冪等第二層 + 分流推進（支付單 CAS pending→success）
		now := time.Now().UTC()
		affected, err := client.Payment.Update().
			Where(payment.ID(p.ID), payment.StatusNEQ(payment.StatusSuccess)).
			SetStatus(payment.StatusSuccess).
			SetChargedAmount(chargedBase).
			SetChargedPrecision(precision).
			SetReviewReason(reviewReason).
			SetChannelOrderNo(fact.ChannelOrderNo).
			SetPaidAt(now).
			SetRaw(fact.Raw).
			Save(txCtx)
		if err != nil {
			return err
		}

		if affected != 1 {
			return fmt.Errorf("payment.CONCURRENT_UPDATE")
		}
		if reviewReason != "" {
			return nil
		}
		if p.OrderID > 0 {
			o, err := client.Order.Get(txCtx, p.OrderID)
			if err != nil {
				return err
			}
			if o.Status != order.StatusPendingPayment {
				ch, err := r.channelForPayment(txCtx, p)
				if err != nil {
					return err
				}
				if ch.Driver == "wallet" {
					return fmt.Errorf("payment.ORDER_NOT_PENDING")
				}
				reason := "訂單已關閉或已有付款，到賬待核對，請處理補單或退款"
				if _, err = client.Payment.UpdateOneID(p.ID).SetReviewReason(reason).Save(txCtx); err != nil {
					return err
				}
				_, err = client.OrderStatusEvent.Create().SetOrderID(o.ID).SetFromStatus(string(o.Status)).SetToStatus(string(o.Status)).SetEvent("payment_review").SetOperator("system").SetReason(reason).Save(txCtx)
				return err
			}
		}
		if p.RechargeOrderID > 0 {
			return r.settleRecharge(txCtx, p, fact)
		}
		if p.OrderID > 0 {
			// 錢包直付等內部路徑 fact 不帶單號——按支付單回填（MarkPaid 判據）
			if o, err := client.Order.Get(txCtx, p.OrderID); err == nil {
				fact.OrderNo = o.OrderNo
			}
		}
		return r.settleOrder(txCtx, p, fact)
	})
}

// settleOrder 訂單型推進：餘額渠道扣款 → OrderLifecycle.MarkPaid（同事務）。
func (r *PaymentRepoImpl) settleOrder(ctx context.Context, p *ent.Payment, fact CallbackFact) error {
	// 餘額支付：先扣款（冪等鍵 order_pay:<orderID>；餘額不足整事務回滾）
	ch, err := r.channelForPayment(ctx, p)
	if err != nil {
		return err
	}
	if r.wallet != nil && ch.Driver == "wallet" && p.OrderID > 0 {
		o, err := data.Client(ctx, r.data).Order.Get(ctx, p.OrderID)
		if err != nil {
			return err
		}
		if err := r.wallet.DebitInTx(ctx, walletport.Entry{
			UserID: o.UserID, Direction: walletport.DirectionOut,
			Type: "order_pay", Amount: money.Cents(p.Amount),
			Reference: fmt.Sprintf("order_pay:%d", p.OrderID),
			OrderID:   p.OrderID,
		}); err != nil {
			return fmt.Errorf("payment.WALLET_DEBIT_FAILED: %w", err)
		}
	}
	// 訂單置 paid（狀態機 CAS + 事件 + outbox order.paid；冪等：已 paid 直接成功）
	if r.lifecycle == nil || p.OrderID == 0 {
		return fmt.Errorf("payment.ORDER_LIFECYCLE_UNBOUND")
	}
	return r.lifecycle.MarkPaid(ctx, orderport.PaidFact{
		OrderNo:        fact.OrderNo,
		PaymentID:      p.ID,
		Channel:        p.Channel,
		Amount:         money.Cents(p.Amount),
		ChannelOrderNo: fact.ChannelOrderNo,
	})
}

// settleRecharge 充值型推進：充值單 success → 餘額入賬（冪等鍵 recharge:<paymentID>）
// → outbox recharge.succeeded。金額與贈送全部取服務端落庫值（鐵律 16）。
func (r *PaymentRepoImpl) settleRecharge(ctx context.Context, p *ent.Payment, fact CallbackFact) error {
	base, err := data.BaseCurrency(ctx, r.data)
	if err != nil {
		return err
	}
	paymentCurrency, err := r.paymentBaseCurrency(ctx, p)
	if err != nil {
		return err
	}
	if paymentCurrency != base {
		return fmt.Errorf("payment.CURRENCY_MISMATCH: 充值單幣種與基礎貨幣不一致，需要遷移")
	}
	client := data.Client(ctx, r.data)
	var ro *ent.RechargeOrder
	if isNativeCryptoDriver(p.DriverSnapshot) {
		if r.data.Dialect != db.SQLite {
			ro, err = client.RechargeOrder.Query().Where(rechargeorder.ID(p.RechargeOrderID)).ForUpdate().Only(ctx)
		} else {
			ro, err = client.RechargeOrder.UpdateOneID(p.RechargeOrderID).AddAmount(0).Save(ctx)
		}
	} else {
		ro, err = client.RechargeOrder.Get(ctx, p.RechargeOrderID)
	}
	if err != nil {
		return err
	}
	reviewOrReject := func() error {
		if isNativeCryptoDriver(p.DriverSnapshot) {
			_, err := client.Payment.UpdateOneID(p.ID).SetReviewReason("充值單已處理，本次到賬待核對，請處理退款").Save(ctx)
			return err
		}
		return fmt.Errorf("payment.RECHARGE_NOT_PENDING")
	}
	if ro.Status != rechargeorder.StatusPending {
		return reviewOrReject()
	}
	// A second channel can pay the same recharge concurrently. The status CAS
	// claims settlement across channels before either wallet or supplier credit.
	affected, err := client.RechargeOrder.Update().
		Where(rechargeorder.ID(ro.ID), rechargeorder.StatusEQ(rechargeorder.StatusPending)).
		SetStatus(rechargeorder.StatusSuccess).SetPaymentID(p.ID).
		SetPaidAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		return err
	}
	if affected != 1 {
		return reviewOrReject()
	}
	// 入賬分支（target 由建單時定；金額全部取服務端落庫值，鐵律 16）：
	// balance → 用户錢包餘額（本金+贈送）+ 贈送積分；
	// supply → 對接賬户供貨餘額（本金；供貨預存無贈送）。
	total := ro.Amount + ro.GiftAmount
	if ro.Target == rechargeorder.TargetSupply {
		if ro.SupplierAccountID > 0 && r.supplier != nil {
			if err := r.supplier.Recharge(ctx, ro.SupplierAccountID, total,
				fmt.Sprintf("recharge:%d", p.ID),
				fmt.Sprintf("對接賬户自助充值到賬（用户 #%d，本金 %d 分 + 贈送 %d 分）", ro.UserID, ro.Amount, ro.GiftAmount)); err != nil {
				return fmt.Errorf("payment.SUPPLY_RECHARGE_FAILED: %w", err)
			}
		}
	} else if total > 0 && r.wallet != nil {
		if err := r.wallet.CreditInTx(ctx, walletport.Entry{
			UserID: ro.UserID, Direction: walletport.DirectionIn,
			Type: "recharge", Amount: money.Cents(total),
			Reference: fmt.Sprintf("recharge:%d", p.ID),
			OrderID:   0,
			Remark:    fmt.Sprintf("充值到賬（本金 %d 分 + 贈送 %d 分）", ro.Amount, ro.GiftAmount),
		}); err != nil {
			return fmt.Errorf("payment.RECHARGE_CREDIT_FAILED: %w", err)
		}
	}
	// 贈送積分（冪等鍵 points:recharge:<paymentID>；積分賬本 ）
	if ro.GiftPoints > 0 && r.points != nil {
		if err := r.points.PointCreditInTx(ctx, walletport.PointEntry{
			UserID: ro.UserID, Direction: "in", Type: "earn_recharge",
			Amount: int64(ro.GiftPoints), Reference: fmt.Sprintf("points:recharge:%d", p.ID),
			Remark: fmt.Sprintf("充值贈送積分 %d", ro.GiftPoints),
		}); err != nil {
			return fmt.Errorf("payment.RECHARGE_POINTS_FAILED: %w", err)
		}
	}
	// 充值成功事件（notify 消費）
	if r.outbox != nil {
		payload, _ := json.Marshal(map[string]any{
			"recharge_id": ro.ID, "payment_id": p.ID, "user_id": ro.UserID,
			"amount": ro.Amount, "gift_amount": ro.GiftAmount, "total": total,
		})
		_ = r.outbox.Write(ctx, "payment", events.RechargeSucceeded,
			fmt.Sprintf("recharge:%d", ro.ID), fmt.Sprintf("recharge:%d:succeeded", ro.ID), payload)
	}
	return nil
}

// isWalletChannel 渠道驅動是否為餘額支付（wallet driver）。
func (r *PaymentRepoImpl) isWalletChannel(ctx context.Context, code string) bool {
	ch, err := data.Client(ctx, r.data).PaymentChannel.Query().
		Where(paymentchannel.Code(code)).Only(ctx)
	return err == nil && ch.Driver == "wallet"
}

// CallbackFact 回調事實（適配器產出）。
type CallbackFact struct {
	GatewayOrderRef string
	Channel         string
	ChannelOrderNo  string
	OrderNo         string
	Amount          int64 // 分（基礎貨幣）
	Currency        string
	Success         bool
	Raw             json.RawMessage
}

// ── 退款（）───────────────────────────────────────────────

// CreateRefund 創建退款單。
func (r *PaymentRepoImpl) CreateRefund(ctx context.Context, orderID uint64, amount int64, channel, reason string) (*ent.RefundOrder, error) {
	return data.Client(ctx, r.data).RefundOrder.Create().
		SetOrderID(orderID).
		SetAmount(amount).
		SetChannel(refundorder.Channel(channel)).
		SetReason(reason).
		SetStatus(refundorder.StatusCreated).
		Save(ctx)
}

// ListRefunds 退款單列表。
func (r *PaymentRepoImpl) ListRefunds(ctx context.Context, status string, filters ...*adminv1.ListRefundsRequest) ([]*ent.RefundOrder, error) {
	orderFilter := []predicate.Order{order.SubsiteID(tenancy.FromContext(ctx).SubsiteID)}
	var before uint64
	if len(filters) > 0 && filters[0] != nil {
		if filters[0].OrderNo != "" {
			orderFilter = append(orderFilter, order.OrderNo(filters[0].OrderNo))
		}
		before = filters[0].BeforeId
	}
	q := data.Client(ctx, r.data).RefundOrder.Query().Where(refundorder.HasOrderWith(orderFilter...)).Order(ent.Desc(refundorder.FieldID)).Limit(50)
	if status != "" {
		q.Where(refundorder.StatusEQ(refundorder.Status(status)))
	}
	if before > 0 {
		q.Where(refundorder.IDLT(before))
	}
	return q.All(ctx)
}

// ── DTO 轉換 ────────────────────────────────────────────────

// ToChannelPB 轉 admin 協議（憑據脱敏；icon/methods_json 原樣下發）。
func ToChannelPB(ch *ent.PaymentChannel) *adminv1.Channel {
	pb := &adminv1.Channel{
		Id: ch.ID, Name: paymentChannelName(ch), Code: ch.Code, Driver: ch.Driver,
		ConfigJson: `"****"`, // 憑據永不明文下發
		Fee:        ch.Fee, FeeType: string(ch.FeeType), FeeBearer: string(ch.FeeBearer), Recommended: ch.Recommended, RecommendLabel: ch.RecommendLabel, RecommendDescription: ch.RecommendDescription,
		Enabled: ch.Enabled, Sort: ch.Sort,
		Icon:          ch.Icon,
		AllowPurchase: &ch.AllowPurchase, AllowMemberRecharge: &ch.AllowMemberRecharge, AllowSupplyRecharge: &ch.AllowSupplyRecharge,
	}
	if ms := parseMethods(ch); len(ms) > 0 {
		if b, err := json.Marshal(ms); err == nil {
			pb.MethodsJson = string(b)
		}
	}
	return pb
}

// ToPaymentPB 轉支付單協議。
func ToPaymentPB(p *ent.Payment, orderNo string) *adminv1.Payment {
	out := &adminv1.Payment{
		Id: p.ID, OrderId: p.OrderID, OrderNo: orderNo,
		Channel: p.Channel, ChannelOrderNo: p.ChannelOrderNo,
		AmountCents: p.Amount, ChargedCents: p.ChargedAmount, FeeCents: p.Fee,
		Status: string(p.Status),
	}
	out.ReviewReason = p.ReviewReason
	out.DriverSnapshot = p.DriverSnapshot
	if !p.ExpiresAt.IsZero() {
		out.ExpiresAt = p.ExpiresAt.Unix()
	}
	if !p.PaidAt.IsZero() {
		out.PaidAt = p.PaidAt.Unix()
	}
	if !p.CreatedAt.IsZero() {
		out.CreatedAt = p.CreatedAt.Unix()
	}
	return out
}

// ToRefundPB 轉退款單協議。
func ToRefundPB(rf *ent.RefundOrder, orderNo string) *adminv1.RefundOrder {
	allocations, _ := json.Marshal(rf.ItemAllocations)
	return &adminv1.RefundOrder{
		Id: rf.ID, OrderId: rf.OrderID, OrderNo: orderNo,
		AmountCents: rf.Amount, FeeCents: rf.FeeAmount, Channel: string(rf.Channel),
		Status: string(rf.Status), Reason: rf.Reason,
		UpstreamRefundId: rf.UpstreamRefundID, CreatedAt: rf.CreatedAt.Unix(), ShippingCents: rf.ShippingAmount, ItemAllocationsJson: string(allocations), OperatorId: rf.OperatorID,
	}
}

// RefundOrder is used by procurement. There is no automatic gateway refund executor.
// Return an error so procurement transfers the order to manual review instead of
// creating a receipt that can never settle (especially for guest and mixed orders).
func (r *PaymentRepoImpl) RefundOrder(ctx context.Context, orderID uint64, amount money.Cents, reason string) error {
	return fmt.Errorf("payment: 自動退款尚未執行，請在訂單詳情核實後退款或補發")
}

// Normalize only generated legacy names; preserve operator names and all identities.
func paymentChannelName(ch *ent.PaymentChannel) string {
	if ch.Driver != "epusdt" {
		return ch.Name
	}
	const old = "EPUSDT / GM Pay（多鏈多幣種）"
	if ch.Name == old {
		return "GM Pay"
	}
	if suffix, ok := strings.CutPrefix(ch.Name, old+" "); ok {
		if n, err := strconv.Atoi(suffix); err == nil && n >= 2 && strconv.Itoa(n) == suffix {
			return "GM Pay " + suffix
		}
	}
	return ch.Name
}
