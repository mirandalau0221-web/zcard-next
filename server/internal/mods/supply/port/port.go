// Package port 為 supply 模塊對外契約（零依賴包）。
package port

import "context"

// PurchaseRequest 採購提交請求（ procurement 消費；DownstreamOrderNo 冪等鍵）。
type PurchaseRequest struct {
	ConnectionID      uint64
	ProductCode       string // 上游商品標識（映射後的上游鍵）
	UpstreamSKU       string // 規格 SKU 上游標識（本地 product_skus.upstream_sku_id：acg=規格選擇編碼、dujiao=sku_id；空=無規格）
	Quantity          int
	DownstreamOrderNo string // 冪等鍵（防重複下單）
	TraceID           string
	CallbackURL       string // 空 = 輪詢/巡檢兜底
}

// PurchaseResult 採購提交結果。
type PurchaseResult struct {
	UpstreamOrderID string   // 查單依據
	Status          string   // delivered | pending
	Amount          int64    // 分
	Cards           []string // delivered 時上游卡密（內存明文，調用方必須立即加密）
}

// PurchaseOrderInfo 上游訂單查詢結果。
type PurchaseOrderInfo struct {
	Status string
	Amount int64
	Cards  []string // delivered 時卡密（內存明文）
}

// UpstreamCallbackAuth 上游回調驗簽入參（HTTP 原語；port 零依賴故用 map 傳頭）。
type UpstreamCallbackAuth struct {
	Method    string
	Path      string // 實際接收路徑（zcard 驗籤用）
	RawQuery  string
	Headers   map[string]string
	Body      []byte
}

// UpstreamCallbackResult 回調載荷解析結果。
type UpstreamCallbackResult struct {
	DownstreamOrderNo string
	Status            string // delivered | canceled | ...
	Cards             []string
	Amount            int64 // 分（0 = 載荷未攜帶）
}

// UpstreamGateway 上游採購網關（procurement 模塊消費，通道 A）。
// 實現位於 supply 模塊（連接憑據解密 + 適配器裝配 + 實時庫存校驗）。
type UpstreamGateway interface {
	// Submit 提交採購（冪等鍵隨請求；永久錯誤歸一化為哨兵錯誤語義，見實現）。
	Submit(ctx context.Context, req PurchaseRequest) (*PurchaseResult, error)
	// Query 查詢上游訂單（三通道結果匯聚共用）。
	Query(ctx context.Context, connectionID uint64, upstreamOrderID string) (*PurchaseOrderInfo, error)
	// CheckStock 實時庫存校驗（-1=上游明確不限；查詢失敗保留 error；
	// skuCode 為上游規格標識，可空——商品級口徑）。
	CheckStock(ctx context.Context, connectionID uint64, productCode, skuCode string) (int32, error)
	// Refund 向上遊傳導退款（可選能力；不支持返回 ErrRefundNotSupported）。
	Refund(ctx context.Context, connectionID uint64, upstreamOrderID string) error
	// FailStrategyOf 渠道級失敗策略（settings.failure_action：auto_refund 默認 |
	// manual 轉人工；採購建單時讀取， 對齊 1.x failure_action）。
	FailStrategyOf(ctx context.Context, connectionID uint64) string
	// VerifyUpstreamCallback 校驗並解析上游回調（ E：按連接驅動驗籤——
	// zcard 四頭雙口徑 / dujiao 三頭固定 path；acg 無回調返回 ErrCallbackNotSupported）。
	// 冪等由採購狀態機 CAS 兜底（ts+簽名摘要 防重放語義見實現）。
	VerifyUpstreamCallback(ctx context.Context, connectionID uint64, auth *UpstreamCallbackAuth) (*UpstreamCallbackResult, error)
}

// 哨兵錯誤（適配器層歸一化，procurement 狀態機判據）。
var (
	// ErrUpstreamDeleted 上游商品已刪除（永久錯誤 → rejected）。
	ErrUpstreamDeleted = errorsNew("supply: upstream product deleted")
	// ErrUpstreamUnavailable 上游商品下架（永久錯誤 → rejected）。
	ErrUpstreamUnavailable = errorsNew("supply: upstream product unavailable")
	// ErrUpstreamBalance 上游餘額不足（永久錯誤 → rejected）。
	ErrUpstreamBalance = errorsNew("supply: upstream balance insufficient")
	// ErrUpstreamDuplicate 防重鍵衝突（acg-faka request_no 重複即報錯：上游可能
	// 已受理首請求——禁止重試與自動退款，轉人工核對）。
	ErrUpstreamDuplicate = errorsNew("supply: upstream duplicate submit")
	// ErrUpstreamNoStock 上游無庫存（快速拒絕）。
	ErrUpstreamNoStock = errorsNew("supply: upstream no stock")
	// ErrRefundNotSupported 上游不支持退款。
	ErrRefundNotSupported = errorsNew("supply: refund not supported by upstream")
	// ErrCallbackNotSupported 上游協議無回調（acg-faka；走輪詢/巡檢兜底）。
	ErrCallbackNotSupported = errorsNew("supply: callback not supported by upstream")
	// ErrCallbackVerifyFailed 回調驗籤失敗（統一 401 防枚舉口徑）。
	ErrCallbackVerifyFailed = errorsNew("supply: callback verify failed")
)

func errorsNew(s string) error { return &sentinelError{s} }

type sentinelError struct{ s string }

func (e *sentinelError) Error() string { return e.s }
