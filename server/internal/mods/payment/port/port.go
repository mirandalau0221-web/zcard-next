// Package port 為 payment 模塊對外契約（零依賴包）。
//
// 渠道能力接口拆分（，採納友商形態，替代 1.x 大 PaymentDriver）：
// 按 (provider, channel) 註冊表路由；新渠道 = 新增 adapter 文件 + 註冊，不改核心代碼。
package port

import (
	"context"
	"encoding/json"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// CreatePaymentRequest 創建支付請求。
type CreatePaymentRequest struct {
	OrderNo         string
	GatewayOrderRef string // Stable reference persisted before the gateway request.
	Deadline        time.Time
	Channel         string      // 渠道碼
	Amount          money.Cents // 應收（基礎貨幣分；記賬與核對權威值）
	Subject         string
	ReturnURL       string
	NotifyBaseURL   string
	IdempotencyKey  string          // 寫接口冪等（）
	Config          json.RawMessage // 解密後的渠道憑據 JSON（每渠道獨立，adapter 無狀態）
	// ── 幣種快照（：服務端按 currency 表換算後的渠道金額）──
	// ChargedUnits==0 即同幣直收（CNY）——適配器用 Amount（向後兼容 alipay/wechat/epay）；
	// 非 0 時適配器以 ChargedUnits/ChargedCurrency 構造協議金額，回調亦以此口徑核對。
	ChargedUnits    int64
	ChargedCurrency string // ISO 碼（USD/EUR…；空=CNY）
	// ── 方式級參數（收銀台顧客選擇的支付方式；空 = 渠道默認/舊單方式語義）──
	// MethodCode 方式標識（alipay/wxpay/usdt-trc20…，透傳網關時按 params 展開）；
	// MethodParams 網關參數：易支付 {"type":"wxpay"}、USDT {"network":"tron","token":"USDT"}。
	MethodCode   string
	MethodParams map[string]string
}

// RedirectInfo 支付發起結果（收銀台/二維碼/參數包）。
type RedirectInfo struct {
	ChannelOrderNo string
	Deadline       time.Time
	Type           string // redirect / qrcode / params
	Payload        json.RawMessage
}

// RechargePaymentInfo 充值支付單發起結果（wallet 模塊消費）。
type RechargePaymentInfo struct {
	BaseCents  int64
	FeeCents   int64
	TotalCents int64
	PaymentID  uint64
	Type       string // redirect / qrcode / params
	Payload    string
}

// RechargePayer 充值支付單創建端口（wallet 模塊消費，通道 A）：
// 充值單落 pending 後創建支付單（關聯 recharge_order_id）+ 渠道發起，
// 餘額入賬只發生在回調成功後（鐵律 16）。
type RechargePayer interface {
	CreateRechargePayment(ctx context.Context, rechargeOrderID uint64, channel, method string, amount money.Cents) (*RechargePaymentInfo, error)
}

// SupplierRecharger 供貨賬戶餘額入賬端口（payment 回調消費；wire 綁定 supplier repo）：
// target=supply 的充值單支付成功後，將本金入賬到對接賬戶供貨餘額（冪等鍵由調用方保證）。
type SupplierRecharger interface {
	Recharge(ctx context.Context, accountID uint64, amount int64, reference, remark string) error
}

// CallbackFact 回調事實（VerifyCallback/ParseWebhook 的統一產出，四重校驗的輸入）。
type CallbackFact struct {
	GatewayOrderRef string
	Provider        string
	ChannelOrderNo  string // 網關單號（獨立字段隔離下游回傳格式）
	OrderNo         string // 業務單號
	Amount          money.Cents
	Currency        string
	Success         bool
	Raw             json.RawMessage // 回調原文（審計）
}

// Provider 渠道基礎能力（所有渠道必須實現）。
type Provider interface {
	Type() string
	ValidateConfig(cfg json.RawMessage) error
	CreatePayment(ctx context.Context, req CreatePaymentRequest) (*RedirectInfo, error)
}

// Webhooker JSON webhook 解析（stripe/paypal 類；簽名驗證需渠道憑據——cfg 隨調用傳入）。
type Webhooker interface {
	ParseWebhook(headers map[string]string, body []byte, cfg json.RawMessage) (*CallbackFact, error)
}

// CallbackVerifier 表單同步回調驗籤（epay/alipay 類）。
type CallbackVerifier interface {
	VerifyCallback(form map[string]string, cfg json.RawMessage) (*CallbackFact, error)
}

// Capturer 主動查單補單（1.x「手動補單」的規範化）。
type Capturer interface {
	QueryPayment(ctx context.Context, gatewayOrderNo string, cfg json.RawMessage) (*CallbackFact, error)
}

// OrderCapturer 在未收到回調、尚無網關交易號時按商戶訂單號查單。
type OrderCapturer interface {
	QueryPaymentByOrderNo(ctx context.Context, orderNo string, cfg json.RawMessage) (*CallbackFact, error)
}

// Acker 回調成功應答體（渠道感知，可選能力位）：
// 未實現則回調管線默認 JSON {"status":"ok"}；epusdt 類網關要求純文本 "ok"。
type Acker interface {
	SuccessAck() string
}

// ConfigField 渠道配置字段 schema（：admin 配置面動態表單渲染）。
type ConfigField struct {
	Key         string
	Label       string
	Type        string // text | password | textarea | select | number | switch
	Required    bool
	Placeholder string
	Help        string
	Sensitive   bool // 敏感字段：編輯時留空=保持原值，不回顯
	Dynamic     bool // 選項動態加載（OptionProvider；加載失敗回落 Options）
	Multiple    bool // 多選（保存為數組；epusdt token/network——佔位訂單模式）
	Options     []ConfigOption
	Default     string
}

// FieldOptionsResult 動態選項結果。
type FieldOptionsResult struct {
	Options  []ConfigOption
	Fallback bool // 上游不可達回落靜態矩陣（前端提示「無法連接網關」）
}

// ConfigOption 下拉選項。
type ConfigOption struct {
	Label string
	Value string
}

// FieldProvider 配置字段 schema 聲明（可選能力位；未實現則默認單 JSON 文本框——
// 向後兼容，保證自定義驅動可用）。
type FieldProvider interface {
	ConfigFields() []ConfigField
}

// DriverMeta 驅動元數據（admin 配置面展示）。
type DriverMeta struct {
	Name        string
	Icon        string // 品牌圖標標識（前端圖標庫映射）
	Description string
}

// MetaProvider 驅動元數據聲明（可選能力位；未實現則回落 Type() 名）。
type MetaProvider interface {
	Meta() DriverMeta
}

// OptionProvider 字段選項動態加載（ 修復）：epusdt network/token 的
// 可選值以網關 GET /payments/gmpay/v1/config 的 supported_assets 為準（官方文檔
// 明示——每個商戶實例啓用的鏈/代幣不同）；實現方負責上游不可達回落靜態矩陣。
type OptionProvider interface {
	FieldOptions(ctx context.Context, field string, partial json.RawMessage) (FieldOptionsResult, error)
}

// Refunder 原路退款（2.0 新增能力位，退款編排三通道之一）。
type Refunder interface {
	Refund(ctx context.Context, gatewayOrderNo string, amount money.Cents, reason string, cfg json.RawMessage) error
}

// OrderRefunder 訂單退款入口（ procurement 失敗策略消費，通道 A）：
// 按訂單創建退款單（channel=upstream：貨源採購失敗自動退款），
// 由 payment 模塊驅動訂單 refund 流轉。
type OrderRefunder interface {
	RefundOrder(ctx context.Context, orderID uint64, amount money.Cents, reason string) error
}

// SlowPaymentGracePeriod caps confirmation waiting after the original order deadline.
const SlowPaymentGracePeriod = 15 * time.Minute

// SlowPaymentChecker reports unconfirmed slow payments still within that grace period.
// A local pending attempt alone is not evidence of an on-chain transfer.
type SlowPaymentChecker interface {
	HasPendingSlowPayment(ctx context.Context, orderID uint64) (bool, error)
}

// Registry 渠道註冊表（按 (provider, channel) 路由；支付渠道配置存 payment_channels）。
type Registry interface {
	Register(p Provider)
	Provider(provider string) (Provider, error)
}
