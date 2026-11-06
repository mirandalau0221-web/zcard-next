// Package port 為 catalog 模塊對外契約（零依賴包）。
package port

import (
	"context"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// StockSnapshot carries display history separately from authoritative availability.
type StockSnapshot struct {
	Available int64
	Quantity  int64
	CheckedAt time.Time
	Status    string
}

// Product 商品 DTO（跨模塊快照：order 價格管線消費；管理字段不下發）。
type Product struct {
	ProductKind                  string
	DeliveryKind                 string
	SMSProduct                   map[string]string
	SocialSchema                 map[string]any
	GoodsType, ShippingMode      string
	ProductProperty              string
	SalesVisible, TrackInventory bool
	ShippingFee, PhysicalStock   int64
	ShippingCountries            []string
	FulfillmentMode              string
	ManualStock                  int64
	ID                           uint64
	SubsiteID                    uint64
	Name                         string
	Slug                         string
	Cover                        string
	Description                  string      // 商品詳情（sanitize 後富文本；storefront 詳情頁下發）
	Price                        money.Cents // 售價（分）
	FactoryPrice                 money.Cents // 成本價（分）
	StockType                    string      // card / url / code
	DeliveryMode                 string      // status / delete
	Status                       int8        // 1=上架 0=下架 2=隱藏
	StockVisible                 bool
	// 積分兑換價（0=常規商品；>0=積分商城商品——order 兑換分支判定，）
	PointsRequired int64
	// 貨源信息（ procurement 消費：判定上游項與提交採購）
	UpstreamSourceID    uint64 // 0 = 自營
	UpstreamProductCode string
	// 運營推薦（storefront 首頁推薦位）
	IsRecommend                       bool
	CategoryRecommend, CategoryPinned bool
}

// Control 自定義控件 DTO（下單表單渲染）。
type Control struct {
	Placeholder, Validation string
	MaxLength               int32
	ID                      uint64
	Name                    string
	Type                    string // text | password | select | number | checkbox | radio
	Required                bool
	Options                 []string
	Sort                    int32
}

// Category 前台可見分類（導航/篩選用；ParentID 0=根，多級分類樹形）。
type Category struct {
	ID       uint64
	Name     string
	Icon     string
	ParentID uint64
}

// ReviewItem 前台評價條目（真實 approved + 虛擬合併；Nickname 真實評價為空）。
type ReviewItem struct {
	ID        uint64
	Nickname  string
	Content   string
	Rating    int32
	IsVirtual bool
	Sort      int32
	CreatedAt time.Time
}

// Sku 前台多規格 DTO（只下發 id/名稱/價格）。
type Sku struct {
	Stock           int64
	FulfillmentMode string
	ID              uint64
	Name            string
	Price           money.Cents // 獨立售價（分；0=繼承商品價）
	ProductID       uint64
}

// VisibleFilter 可見商品過濾（storefront 列表 / order 下單校驗共用）。
type VisibleFilter struct {
	SubsiteID  uint64
	CategoryID uint64
	Keyword    string
	Page       int32
	PageSize   int32
	// PointsOnly 積分商城視圖（true=僅 points_required>0 商品；）
	PointsOnly bool
	// RecommendOnly 推薦位視圖（true=僅 is_recommend 商品；首頁推薦區塊）
	RecommendOnly bool
	// Sort 排序：default（綜合）| newest（最新上架）| price_asc | price_desc | sales（空=default）
	Sort string
}

// ProductReader 商品讀取窄接口（storefront service 與 order 模塊消費，通道 A）。
type ProductReader interface {
	// ListVisible 上架商品分頁（隱藏商品對遊客 404 由調用方按 Status 判定）。
	ListVisible(ctx context.Context, f VisibleFilter) (items []Product, total int64, err error)
	// Get 取單個商品（含下架/隱藏，調用方決定可見性語義）。
	Get(ctx context.Context, subsiteID, id uint64) (*Product, error)
	// SkuUpstreamCode 本地 SKU 的上游標識（product_skus.upstream_sku_id；
	// 採購提交時還原規格選擇。不存在/未同步來源 → 空串）。
	SkuUpstreamCode(ctx context.Context, subsiteID, skuID uint64) string
}

// GroupDiscount 商品組優惠及其疊加策略；Rate=0 表示未命中。
type GroupDiscount struct {
	ID          uint64
	Rate        int32 // 應付比例（萬分比；9800=支付98%）
	StackMember bool
	StackCoupon bool
}

// PricingResolver 商品定價解析（order 價格管線消費，通道 A）：
// SKU 價 > 商品價；會員商品組折扣萬分比。
type PricingResolver interface {
	// ResolvePrice 解析商品/SKU 售價（分）。skuID=0 或 SKU 價為空時回落到商品價。
	ResolvePrice(ctx context.Context, productID, skuID uint64) (price money.Cents, err error)
	// ResolveGroupDiscount 多組命中取最小應付比例；同率取最早創建的組，策略隨組返回。
	ResolveGroupDiscount(ctx context.Context, productID uint64) (GroupDiscount, error)
}

// AdminFilter 管理面商品過濾（含下架/隱藏；成本價下發）。
type AdminFilter struct {
	SupportsSocial bool
	Inventory      string
	RestockedOnly  bool
	AutoListing    *bool
	IsLocked       *bool
	OptionsOnly    bool // select lightweight product options without stats
	StockType      string
	SubsiteID      uint64
	CategoryID     uint64
	Keyword        string
	Status         int8 // -1=全部
	Page           int32
	PageSize       int32
	// LowStockThreshold 低庫存過濾閾值（>0 時僅返回卡密類且可用庫存 < 閾值的商品）
	LowStockThreshold int
	OutOfStockOnly    bool // 僅庫存為 0；未知和不限不算缺貨
	// 渠道篩選（商品列表按供貨渠道/自營過濾）
	ConnectionID uint64 // products.upstream_source_id
	LocalOnly    bool   // 僅自營（上游渠道為空）
}

// ProductInput 商品創建/更新輸入（description 已 sanitize）。
type ProductInput struct {
	GoodsType, ShippingMode      *string
	ProductProperty              *string
	SalesVisible, TrackInventory *bool
	ShippingFee, PhysicalStock   *int64
	ExpectedPhysicalStock        *int64
	ShippingCountries            []string
	FulfillmentMode              string
	ManualStock                  *int64
	Name                         string
	CategoryID                   uint64
	Description                  string
	DescriptionSet               bool // distinguish clearing an admin description from omitted upstream fields
	Cover                        string
	CoverSet                     bool // explicit empty cover clears it; omitted cover keeps the saved value
	Images                       []string
	ImagesSet                    bool  // replace the gallery, including an empty gallery
	Price                        int64 // 分
	FactoryPrice                 int64
	StockType                    string
	DeliveryMode                 string
	StockVisible                 bool
	StockVisibleSet              bool // update presence; omitted fields keep the saved display preference
	Dedup                        bool
	Sort                         int32
	Status                       int8
	// 積分兑換價（分單位積分；0=不參與積分商城——PUT 全量語義，）
	PointsRequired    int64
	PointsRequiredSet bool // true = 寫入該值（含 0=移出積分商城）
	// 運營推薦（省略保留原值；false=取消推薦）。
	IsRecommend    bool
	IsRecommendSet bool
	// 直髮內容密文（url/code 商品；nil=不動。service 層已用 CardCipher 加密，
	// AAD=product_id+subsite_id——創建時商品尚無 ID，由 repo 建後回填）
	DirectContent []byte
}

// ProductAdminRepo 管理面倉儲端口（service 層消費）。
type ProductAdminRepo interface {
	ListAdmin(ctx context.Context, f AdminFilter) ([]any, int64, error) // any=*ent.Product，避免 port 依賴 ent
	GetAdmin(ctx context.Context, subsiteID, id uint64) (any, error)
	CreateProduct(ctx context.Context, in ProductInput) (any, error)
	UpdateProduct(ctx context.Context, id uint64, in ProductInput) (any, error)
	SetDirectContent(ctx context.Context, id uint64, ciphered []byte) error
	DeleteProduct(ctx context.Context, id uint64) error
}

// UpstreamProductInput 貨源同步 upsert 輸入（）。
// Price=-1 表示「不更新價格」（價格保護：auto_sync_price=false 或運營已改價）。
type UpstreamProductInput struct {
	ProductKind         string
	DeliveryKind        string
	SMSProduct          map[string]string
	SocialSchema        map[string]any
	ConnectionID        uint64 // products.upstream_source_id
	UpstreamProductCode string // products.upstream_product_code（UNIQUE 判據）
	UpstreamSyncedAt    time.Time
	Name                string
	Description         string
	DescriptionSet      bool // 上游顯式提供空介紹時也更新
	Cover               string
	CategoryID          uint64 // 0 且 CategorySet=true 時清除分類
	CategorySet         bool
	CategoryProtected   bool
	Price               int64              // 分；-1 = 保持現有價
	FactoryPrice        int64              // 本幣分：賬號成本快照；-1=保留已有成本
	Status              int8               // 1=上架 2=隱藏 0=下架
	AutoOnshelf         bool               // 新建商品時是否上架（settings.auto_onshelf）
	ReimportDeleted     bool               // 僅交互導入：歸檔商品保留，釋放上游標識後新建
	SKUs                []UpstreamSKUInput // 上游規格組合（空=不動現有 SKU；顯式空切片語義同空——上游無規格時不清理本地手建 SKU）
}

// UpstreamSKUInput 上游規格組合（acg 笛卡爾積 / dujiao SKU）。
type UpstreamSKUInput struct {
	Code       string            // 上游 SKU 標識（acg 規格選擇編碼；dujiao sku_id）→ product_skus.upstream_sku_id
	Name       string            // 展示名（缺省回退 Code）
	PriceCents int64             // 組合價（已過定價管線）；-1=保留已有價
	SpecValues map[string]string // 結構化規格 {規格: 值}
}

// UpstreamProductWriter 貨源同步商品 upsert 端口（supply 模塊消費，通道 A）。
// 語義：按 upstream_source_id + upstream_product_code 冪等 upsert；
// 返回 (productID, created, error)。
type UpstreamProductWriter interface {
	UpsertUpstreamProduct(ctx context.Context, in UpstreamProductInput) (productID uint64, created bool, err error)
}

// UpstreamProductMaintainer 貨源輕量維護端口（ S1：scope=price/status 同步
// 與刪除對賬消費，通道 A）。定位判據同上；found=false 表示商品未導入（調用方跳過）。
type UpstreamProductMaintainer interface {
	// UpdateUpstreamPrice 僅更新價格（price scope；不動名稱/狀態/庫存）。
	UpdateUpstreamPrice(ctx context.Context, connectionID uint64, productCode string, priceCents int64, skus ...UpstreamSKUInput) (found bool, err error)
	// UpdateUpstreamStatus 僅更新上下架狀態（status scope；1=上架 2=隱藏 0=下架）。
	UpdateUpstreamStatus(ctx context.Context, connectionID uint64, productCode string, status int8) (found bool, err error)
	// ShelveOffMissing 刪除對賬：將連接下 upstream_product_code ∉ seen 的
	// 已導入商品批量下架(0)（上游已刪除推斷；返回下架數）。
	ShelveOffMissing(ctx context.Context, connectionID uint64, seen []string) (shelved int64, err error)
}

// SupplierProduct 供貨目錄商品（ supplier 消費，通道 A）：
// 管理面語義（含下架/隱藏），僅下發可公開字段。
type SupplierProduct struct {
	ID           uint64
	Name         string
	Price        int64 // 分（基礎價；覆蓋價由 supplier 定價表決定）
	FactoryPrice int64 // 分（成本快照）
	CategoryID   uint64
	Description  string
	Cover        string
	Status       int8 // 1=上架 0=下架 2=隱藏
}

// SupplierCatalog 供貨目錄端口（對外供貨 API 消費）。
type SupplierCatalog interface {
	// ListForSupply 目錄分頁（Status=-1 全含）。
	ListForSupply(ctx context.Context, f AdminFilter) ([]SupplierProduct, int64, error)
	// GetForSupply 單品（含下架）。
	GetForSupply(ctx context.Context, productID uint64) (*SupplierProduct, error)
	// ListSupplyCategories 分類列表（acg-faka 兼容層 items 兩級樹用； C）。
	ListSupplyCategories(ctx context.Context) ([]SupplyCategory, error)
}

// SupplyCategory 供貨目錄分類（兼容層樹節點）。
type SupplyCategory struct {
	ID   uint64
	Name string
}

// SettingsReader 系統設置讀取（通道 A：settings.RepoImpl 適配；低庫存閾值等）。
type SettingsReader interface {
	// GetJSON 讀取分組配置（讀取失敗返回 nil, nil，調用方走默認值）。
	GetJSON(ctx context.Context, group, key string) ([]byte, error)
}

// StockLookup refreshes a visible upstream product; implementations coalesce requests.
type StockLookup interface {
	DisplayStock(ctx context.Context, connectionID uint64, productCode string) (int32, error)
}
