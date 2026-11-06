package catalog

// 前台商品目錄 API（遊客可訪問；薄 transport：校驗 + 裝配 + DTO 映射）。

import (
	"context"
	stderrors "errors"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	couponport "github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"
	"sort"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	resellerport "github.com/NovaWorks/zcard-next/server/internal/mods/reseller/port"

	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	orderport "github.com/NovaWorks/zcard-next/server/internal/mods/order/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/sanitize"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// StoreCatalogService 前台目錄服務（實現 storefrontv1.StoreCatalogService）。
type StoreCatalogService struct {
	stockLookup port.StockLookup
	flash       couponport.FlashReader
	storefrontv1.UnimplementedStoreCatalogServiceServer
	uc *CatalogUsecase
	// pricer 分站定價（：listing 與 checkout 共用同一 ResolveUnitPrice——1.x 鐵律；
	// nil = 主站直營形態不解析分站價）。
	pricer resellerport.Pricer
	// sold 批量已售數量（列表/詳情展示 + sales 排序；nil 容錯降級 0）
	sold orderport.SoldCounter
}

// NewStoreCatalogService 構造。
func NewStoreCatalogService(uc *CatalogUsecase, pricer resellerport.Pricer, sold orderport.SoldCounter) *StoreCatalogService {
	return &StoreCatalogService{uc: uc, pricer: pricer, sold: sold}
}

// ListProducts 商品列表（遊客可訪問；隱藏商品不出現在列表）。
// 分頁參數缺省：page=1 / page_size=20（嵌套 message 的 query 綁定為 page.page=1 形式）。
// 排序：default（綜合）/ newest / price_asc / price_desc 走 SQL；sales 全量拉取後按銷量內存排序分頁。
func (s *StoreCatalogService) ListProducts(ctx context.Context, req *storefrontv1.ListProductsRequest) (*storefrontv1.ListProductsReply, error) {
	tc := tenancy.FromContext(ctx)
	page := req.GetPage()
	if page <= 0 {
		page = 1
	}
	pageSize := req.GetPageSize()
	if pageSize <= 0 {
		pageSize = 20
	}
	filter := port.VisibleFilter{
		SubsiteID:     tc.SubsiteID,
		CategoryID:    req.GetCategoryId(),
		Keyword:       req.GetKeyword(),
		Page:          page,
		PageSize:      pageSize,
		PointsOnly:    req.GetPointsOnly(),
		RecommendOnly: req.GetRecommendOnly(),
		Sort:          req.GetSort(),
	}
	salesSort := req.GetSort() == "sales"
	var items []port.Product
	var total int64
	if salesSort {
		// sales 排序：全量拉取（上架商品量可控）→ 內存按銷量排序 → 手動分頁
		all, err := s.uc.ListAllVisible(ctx, filter)
		if err != nil {
			return nil, errors.InternalServer("catalog.LIST_FAILED", "讀取商品列表失敗")
		}
		items, total = all, int64(len(all))
	} else {
		var err error
		items, total, err = s.uc.ListVisible(ctx, filter)
		if err != nil {
			return nil, errors.InternalServer("catalog.LIST_FAILED", "讀取商品列表失敗")
		}
	}
	// 批量銷量（列表展示 + sales 排序）
	var solds map[uint64]int64
	if s.sold != nil {
		ids := make([]uint64, 0, len(items))
		for i := range items {
			ids = append(ids, items[i].ID)
		}
		solds, _ = s.sold.SoldBatch(ctx, ids)
	}
	if salesSort {
		sort.SliceStable(items, func(i, j int) bool {
			return solds[items[i].ID] > solds[items[j].ID]
		})
		start := int((page - 1) * pageSize)
		end := start + int(pageSize)
		if start > len(items) {
			items = nil
		} else if end > len(items) {
			items = items[start:]
		} else {
			items = items[start:end]
		}
	}
	reply := &storefrontv1.ListProductsReply{
		Items:    make([]*storefrontv1.Product, 0, len(items)),
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	ids := make([]uint64, 0, len(items))
	for _, p := range items {
		ids = append(ids, p.ID)
	}
	offers, err := s.flashOffers(ctx, ids)
	if err != nil {
		return nil, errors.InternalServer("catalog.FLASH_FAILED", "讀取活動價格失敗，請重試")
	}
	snapshots, _ := s.uc.repo.StockSnapshotBatch(ctx, ids)
	stocks := map[uint64]int64{}
	for id, snapshot := range snapshots {
		stocks[id] = snapshot.Available
	}

	for i := range items {
		p := &items[i]
		if tc.SubsiteID != tenancy.MainSubsiteID && s.pricer != nil {
			if sp, err := s.pricer.ResolveUnitPrice(ctx, tc.SubsiteID, p.ID, 0, p.Price); err == nil {
				p.Price = sp // 分站價（下限保護在 ResolveUnitPrice 內）
			}
		}
		item := toStorefrontProduct(p, stocks, solds[p.ID])
		item.FlashSale = toFlashOffer(offers[couponport.FlashKey{ProductID: p.ID}])
		snapshot, ok := snapshots[p.ID]
		item.StockStatus = "unknown"
		if ok {
			item.StockStatus = snapshot.Status
			item.StockReference = snapshot.Quantity
			if !snapshot.CheckedAt.IsZero() {
				item.StockCheckedAt = snapshot.CheckedAt.Unix()
			}
		}
		reply.Items = append(reply.Items, item)
	}
	return reply, nil
}

// ListCategories 可見分類列表（hide=false；分站白名單；按 sort 排序）。
func (s *StoreCatalogService) ListCategories(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.ListCategoriesReply, error) {
	rows, err := s.uc.ListVisibleCategories(ctx, tenancy.FromContext(ctx).SubsiteID)
	if err != nil {
		return nil, errors.InternalServer("catalog.CATEGORY_FAILED", "讀取分類失敗")
	}
	reply := &storefrontv1.ListCategoriesReply{}
	for _, c := range rows {
		reply.Categories = append(reply.Categories, &storefrontv1.CategoryItem{
			Id: c.ID, Name: c.Name, Icon: c.Icon, ParentId: c.ParentID,
		})
	}
	return reply, nil
}

// GetProduct 商品詳情（下架/隱藏商品對遊客返回 404，不暴露存在性， 必測項）。
func (s *StoreCatalogService) GetProduct(ctx context.Context, req *storefrontv1.GetProductRequest) (*storefrontv1.Product, error) {
	tc := tenancy.FromContext(ctx)
	p, err := s.uc.GetVisible(ctx, tc.SubsiteID, req.GetId())
	if err != nil {
		if stderrors.Is(err, ErrProductNotFound) {
			return nil, errors.NotFound("catalog.PRODUCT_NOT_FOUND", "商品不存在")
		}
		return nil, errors.InternalServer("catalog.GET_FAILED", "讀取商品失敗")
	}
	if p.Status == 2 {
		if tr, ok := transport.FromServerContext(ctx); ok {
			tr.ReplyHeader().Set("Cache-Control", "no-store, private")
		}
	}
	// ：分站單價（listing=checkout 同源；SKU 規則優先於商品規則由定價引擎裁定）
	base := p.Price
	if tc.SubsiteID != tenancy.MainSubsiteID && s.pricer != nil {
		if sp, err := s.pricer.ResolveUnitPrice(ctx, tc.SubsiteID, p.ID, 0, base); err == nil {
			p.Price = sp
		}
	}
	snapshots, _ := s.uc.repo.StockSnapshotBatch(ctx, []uint64{p.ID})
	snapshot := snapshots[p.ID]
	var solds map[uint64]int64
	if s.sold != nil {
		solds, _ = s.sold.SoldBatch(ctx, []uint64{p.ID})
	}
	out := toStorefrontProduct(p, map[uint64]int64{p.ID: snapshot.Available}, solds[p.ID])
	out.StockStatus = snapshot.Status
	out.StockReference = snapshot.Quantity
	if !snapshot.CheckedAt.IsZero() {
		out.StockCheckedAt = snapshot.CheckedAt.Unix()
	}
	offers, err := s.flashOffers(ctx, []uint64{p.ID})
	if err != nil {
		return nil, errors.InternalServer("catalog.FLASH_FAILED", "讀取活動價格失敗，請重試")
	}
	out.FlashSale = toFlashOffer(offers[couponport.FlashKey{ProductID: p.ID}])
	if p.DeliveryKind != "social_service" && p.ProductKind != "sms_channel" && p.UpstreamSourceID != 0 && s.stockLookup != nil {
		n, err := s.stockLookup.DisplayStock(ctx, p.UpstreamSourceID, p.UpstreamProductCode)
		if err != nil || n < -1 {
			out.Stock = -2
			out.StockStatus = "unknown"
			if snapshot.Quantity >= -1 && !snapshot.CheckedAt.IsZero() {
				out.StockStatus = "stale"
			}
		} else {
			out.Stock = int64(n)
			out.StockStatus = "current"
			out.StockReference = int64(n)
		}
	}
	controls, err := s.uc.ListControls(ctx, req.GetId())
	if err != nil {
		return nil, errors.InternalServer("catalog.CONTROL_FAILED", "讀取控件失敗")
	}
	for _, c := range controls {
		out.Controls = append(out.Controls, &storefrontv1.ProductControl{
			Id: c.ID, Name: c.Name, Type: string(c.Type),
			Required: c.Required, Options: c.Options, Sort: c.Sort,
			Placeholder: c.Placeholder, Validation: c.Validation, MaxLength: c.MaxLength,
		})
	}
	reviews, err := s.uc.ListProductReviews(ctx, req.GetId())
	if err != nil {
		return nil, errors.InternalServer("catalog.REVIEW_FAILED", "讀取評價失敗")
	}
	for _, rv := range reviews {
		item := &storefrontv1.ReviewItem{
			Id: rv.ID, Nickname: rv.Nickname, Content: rv.Content,
			Rating: rv.Rating, IsVirtual: rv.IsVirtual,
		}
		if !rv.CreatedAt.IsZero() {
			item.CreatedAt = rv.CreatedAt.Unix()
		}
		out.Reviews = append(out.Reviews, item)
	}
	skus, err := s.uc.ListSkus(ctx, req.GetId())
	if err != nil {
		return nil, errors.InternalServer("catalog.SKU_FAILED", "讀取規格失敗")
	}
	for _, sku := range skus {
		skuBase := sku.Price
		if skuBase == 0 {
			skuBase = base // 繼承商品基礎價
		}
		skuPrice := skuBase
		if tc.SubsiteID != tenancy.MainSubsiteID && s.pricer != nil {
			if sp, err := s.pricer.ResolveUnitPrice(ctx, tc.SubsiteID, p.ID, sku.ID, skuBase); err == nil {
				skuPrice = sp
			}
		}
		offer := offers[couponport.FlashKey{ProductID: p.ID, SkuID: sku.ID}]
		if offer == nil {
			offer = offers[couponport.FlashKey{ProductID: p.ID}]
		}
		out.Skus = append(out.Skus, &storefrontv1.Sku{
			Stock: sku.Stock, FulfillmentMode: sku.FulfillmentMode, Id: sku.ID, Name: sku.Name, PriceCents: int64(skuPrice), FlashSale: toFlashOffer(offer),
		})
	}
	return out, nil
}

// toStorefrontProduct preserves source-aware stock: >=0 finite, -1 unlimited,
// -2 unknown. Stock visibility only controls display, never purchase validation.
func toStorefrontProduct(p *port.Product, stocks map[uint64]int64, soldCount int64) *storefrontv1.Product {
	name, description := sanitize.SMSPublicContent(p.ProductKind, p.Name, p.Description)
	stock, ok := stocks[p.ID]
	if !ok {
		stock = -2
	}

	return &storefrontv1.Product{
		ProductProperty: p.ProductProperty, SalesVisible: &p.SalesVisible, TrackInventory: &p.TrackInventory,
		SocialSchema: socialStruct(p.SocialSchema), ProductKind: p.ProductKind, DeliveryKind: p.DeliveryKind, SmsProduct: p.SMSProduct, SmsSalesEnabled: data.SMSSalesEnabled(),
		GoodsType: p.GoodsType, ShippingMode: p.ShippingMode, ShippingFeeCents: p.ShippingFee, ShippingCountries: p.ShippingCountries,
		Id:              p.ID,
		Name:            name,
		Slug:            p.Slug,
		Cover:           p.Cover,
		Description:     description, // 商品詳情（上游採集/後台編輯； 漏映射導致前台全部無描述）
		PriceCents:      int64(p.Price),
		StockType:       p.StockType,
		FulfillmentMode: p.FulfillmentMode, ManualStock: p.ManualStock,
		Stock:             stock,
		StockVisible:      proto.Bool(p.StockVisible),
		PointsRequired:    p.PointsRequired, // 積分商城（；0=常規商品）
		SalesCount:        soldCount,
		CategoryRecommend: p.CategoryRecommend, CategoryPinned: p.CategoryPinned,
		IsRecommend: p.IsRecommend, // 運營推薦（首頁推薦位）
	}
}

func (s *StoreCatalogService) SetStockLookup(lookup port.StockLookup) {
	s.stockLookup = lookup
	// Admin and storefront share this repository; both lists refresh stale stock.
	if repo, ok := s.uc.repo.(interface{ SetStockLookup(port.StockLookup) }); ok {
		repo.SetStockLookup(lookup)
	}
}

func (s *StoreCatalogService) SetFlashReader(reader couponport.FlashReader) { s.flash = reader }
func (s *StoreCatalogService) flashOffers(ctx context.Context, ids []uint64) (map[couponport.FlashKey]*couponport.FlashInfo, error) {
	if s.flash == nil {
		return nil, nil
	}
	return s.flash.ActiveBatch(ctx, ids)
}
func toFlashOffer(f *couponport.FlashInfo) *storefrontv1.FlashOffer {
	if f == nil {
		return nil
	}
	return &storefrontv1.FlashOffer{PriceCents: int64(f.FlashPrice), EndAt: f.EndAt.Unix(), Remaining: f.Remaining, PerUserLimit: f.PerUserLimit}
}
