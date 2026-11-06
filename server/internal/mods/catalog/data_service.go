package catalog

// 管理面商品目錄 API（；薄 transport——校驗+sanitize+DTO 映射，業務在 data_crud.go）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	authzport "github.com/NovaWorks/zcard-next/server/internal/mods/authz/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	orderport "github.com/NovaWorks/zcard-next/server/internal/mods/order/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/sanitize"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminCatalogService 管理面目錄服務。
type AdminCatalogService struct {
	adminv1.UnimplementedAdminCatalogServiceServer
	repo     *ProductRepoImpl
	sold     orderport.SoldCounter // 批量已售數量（nil 容錯降級 0）
	settings port.SettingsReader   // 低庫存閾值等（通道 A；nil = 默認值）
	cipher   *inventory.CardCipher // 直發內容加密（url/code 商品；nil = 直發不可用）
	authz    authzport.Authorizer
}

// NewAdminCatalogService 構造。
func NewAdminCatalogService(repo *ProductRepoImpl, sold orderport.SoldCounter, settings port.SettingsReader, cipher *inventory.CardCipher, authz authzport.Authorizer) *AdminCatalogService {
	return &AdminCatalogService{repo: repo, sold: sold, settings: settings, cipher: cipher, authz: authz}
}

// sealDirect 直發內容加密（AAD 綁定 product+subsite）。
func (s *AdminCatalogService) sealDirect(ctx context.Context, plain string, productID uint64) ([]byte, error) {
	tc := tenancy.FromContext(ctx)
	return s.cipher.Seal(plain, productID, tc.SubsiteID)
}

// lowStockThresholdFor 低庫存篩選閾值（未開啓篩選返回 0=不過濾）。
func (s *AdminCatalogService) lowStockThresholdFor(ctx context.Context, enabled bool) int {
	if !enabled {
		return 0
	}
	return s.lowStockThreshold(ctx)
}

// lowStockThreshold 庫存預警閾值（settings.supply.low_stock_threshold；默認 5）。
func (s *AdminCatalogService) lowStockThreshold(ctx context.Context) int {
	if s.settings == nil {
		return 5
	}
	raw, err := s.settings.GetJSON(ctx, "supply", "low_stock_threshold")
	if err != nil || len(raw) == 0 {
		return 5
	}
	var v int
	if json.Unmarshal(raw, &v) != nil || v < 1 {
		return 5
	}
	return v
}

// fillStats 批量填充庫存/已售（列表與詳情共用；庫存查詢失敗標記待確認，不冒充缺貨或充足）。
func (s *AdminCatalogService) fillStats(ctx context.Context, items []*adminv1.AdminProduct) {
	if len(items) == 0 {
		return
	}
	ids := make([]uint64, 0, len(items))
	for _, p := range items {
		ids = append(ids, p.Id)
	}
	messages := s.lowStockMessages(ctx, ids)
	snapshots, err := s.repo.cachedStockSnapshotBatch(ctx, ids)
	var solds map[uint64]int64
	if s.sold != nil {
		solds, _ = s.sold.SoldBatch(ctx, ids)
	}
	for _, p := range items {
		p.Stock = -2
		p.StockStatus = "unknown"
		if snapshot, ok := snapshots[p.Id]; err == nil && ok {
			p.Stock = snapshot.Available
			p.StockStatus = snapshot.Status
			p.StockReference = snapshot.Quantity
			if !snapshot.CheckedAt.IsZero() {
				p.StockCheckedAt = snapshot.CheckedAt.Unix()
			}
		}
		p.SoldCount = solds[p.Id]
		p.LowStockMessage = messages[p.Id]
	}
}

// Low-stock badges share the exact SKU-aware criterion with filtering and notifications.
func (s *AdminCatalogService) lowStockMessages(ctx context.Context, ids []uint64) map[uint64]string {
	out := map[uint64]string{}
	if s.settings != nil {
		raw, err := s.settings.GetJSON(ctx, "supply", "low_stock_alert_enabled")
		enabled := true
		if err != nil || len(raw) > 0 && (json.Unmarshal(raw, &enabled) != nil || !enabled) {
			return out
		}
	}
	threshold := s.lowStockThreshold(ctx)
	rows, err := data.Client(ctx, s.repo.data).Product.Query().Where(product.IDIn(ids...), product.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).All(ctx)
	if err != nil {
		return out
	}
	for _, p := range rows {
		slots, err := data.StockSlots(ctx, s.repo.data, p)
		if err != nil {
			continue
		}
		labels := []string{}
		count := 0
		for _, slot := range slots {
			if slot.Quantity < 0 || slot.Quantity >= int64(threshold) {
				continue
			}
			count++
			if len(labels) < 3 {
				name := slot.Name
				if name == "" {
					name = "商品"
				}
				labels = append(labels, fmt.Sprintf("%s：%d 件", name, slot.Quantity))
			}
		}
		if count > 0 {
			if count > len(labels) {
				labels = append(labels, fmt.Sprintf("另有 %d 個規格", count-len(labels)))
			}
			out[p.ID] = strings.Join(labels, "；") + fmt.Sprintf("（低於 %d 件）", threshold)
		}
	}
	return out
}

// ── 商品 ──

// ListProducts 管理面商品列表。
func (s *AdminCatalogService) ListProducts(ctx context.Context, req *adminv1.ListProductsRequest) (*adminv1.ListProductsReply, error) {
	page := int32(1)
	size := int32(20)
	if req.GetPage() > 0 {
		page = req.GetPage()
	}
	if req.GetPageSize() > 0 {
		size = req.GetPageSize()
	}
	rows, total, err := s.repo.ListAdmin(ctx, s.adminFilter(ctx, req))
	if err != nil {
		return nil, errors.InternalServer("catalog.LIST_FAILED", "讀取商品失敗")
	}
	reply := &adminv1.ListProductsReply{Products: make([]*adminv1.AdminProduct, 0, len(rows))}
	for _, p := range rows {
		if req.GetOptionsOnly() {
			reply.Products = append(reply.Products, &adminv1.AdminProduct{Id: p.ID, Name: p.Name, CategoryId: p.CategoryID, PriceCents: p.Price, GoodsType: p.GoodsType, ProductProperty: p.ProductProperty, SalesVisible: &p.SalesVisible, TrackInventory: &p.TrackInventory, StockType: string(p.StockType), IsLocked: p.IsLocked, LockVersion: p.LockVersion, Status: int32(p.Status), Cover: p.Cover, UpstreamSourceId: p.UpstreamSourceID, FulfillmentMode: p.FulfillmentMode})
		} else {
			reply.Products = append(reply.Products, ToAdminPB(p))
		}
	}
	reply.Total, reply.Page, reply.PageSize = total, page, size
	if !req.GetOptionsOnly() {
		s.fillStats(ctx, reply.Products)
	}
	return reply, nil
}

func (s *AdminCatalogService) adminFilter(ctx context.Context, req *adminv1.ListProductsRequest) port.AdminFilter {
	page, size := req.GetPage(), req.GetPageSize()
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	return port.AdminFilter{
		CategoryID:        req.GetCategoryId(),
		Keyword:           req.GetKeyword(),
		Status:            int8(req.GetStatus()),
		Page:              page,
		PageSize:          size,
		LowStockThreshold: s.lowStockThresholdFor(ctx, req.GetLowStockOnly() || req.Inventory == "low"),
		Inventory:         req.Inventory, RestockedOnly: req.RestockedOnly, AutoListing: req.AutoListing,
		OutOfStockOnly: req.GetOutOfStockOnly(),
		ConnectionID:   req.GetUpstreamSourceId(),
		LocalOnly:      req.GetLocalOnly(),
		StockType:      req.GetStockType(),
		OptionsOnly:    req.GetOptionsOnly(),
		IsLocked:       req.IsLocked,
	}
}

// GetProduct 商品詳情。
func (s *AdminCatalogService) GetProduct(ctx context.Context, req *adminv1.GetProductRequest) (*adminv1.AdminProduct, error) {
	p, err := s.repo.GetAdmin(ctx, 0, req.GetId()) // TODO: tenant context
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("catalog.PRODUCT_NOT_FOUND", "商品不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("catalog.GET_FAILED", "讀取失敗")
	}
	out := ToAdminPB(p)
	s.fillStats(ctx, []*adminv1.AdminProduct{out})
	return out, nil
}

// CreateProduct 創建商品（description sanitize）。
func (s *AdminCatalogService) CreateProduct(ctx context.Context, req *adminv1.CreateProductRequest) (*adminv1.AdminProduct, error) {
	// 鐵律 16：管理面提交金額必須過服務端邊界校驗（非負 + 上限）——抓包改金額攔截點
	if req.GetName() == "" || !money.ValidCents(req.GetPriceCents()) || !money.ValidCents(req.GetFactoryPriceCents()) {
		return nil, errors.BadRequest("catalog.INVALID_INPUT", "名稱必填、價格與成本須為非負且不超上限")
	}
	in := port.ProductInput{
		ProductProperty: req.ProductProperty, SalesVisible: req.SalesVisible, TrackInventory: req.TrackInventory,
		GoodsType: req.GoodsType, ShippingMode: req.ShippingMode, ShippingFee: req.ShippingFeeCents, PhysicalStock: req.PhysicalStock, ShippingCountries: req.ShippingCountries,
		Name: req.GetName(), CategoryID: req.GetCategoryId(),
		Description:    sanitize.RichHTML(req.GetDescription()),
		DescriptionSet: true,
		Cover:          req.GetCover(), Images: req.GetImages(),
		Price: req.GetPriceCents(), FactoryPrice: req.GetFactoryPriceCents(),
		StockType: req.GetStockType(), DeliveryMode: req.GetDeliveryMode(),
		FulfillmentMode: req.GetFulfillmentMode(), ManualStock: req.ManualStock,
		StockVisible: req.GetStockVisible(), Dedup: req.GetDedup(),
		Sort: req.GetSort(), Status: int8(req.GetStatus()),
		PointsRequired: req.GetPointsRequired(), PointsRequiredSet: true,
		IsRecommend: req.GetIsRecommend(),
	}
	if err := s.repo.validatePhysicalProduct(ctx, nil, &in); err != nil {
		return nil, errors.BadRequest("catalog.INVALID_INPUT", err.Error())
	}
	if *in.GoodsType == "physical" && req.GetDirectContent() != "" {
		return nil, errors.BadRequest("catalog.INVALID_INPUT", "實體商品不能配置虛擬直發內容")
	}
	copyFrom := req.GetCopyFromProductId()
	var copySKUs, copyControls bool
	if copyFrom != 0 {
		claims := identity.ClaimsFromContext(ctx)
		if claims == nil || s.authz == nil || !s.authz.Allowed(ctx, claims.RoleID, "catalog:read_detail") {
			return nil, errors.Forbidden("authz.PERMISSION_DENIED", "沒有讀取原商品的權限")
		}
		copySKUs = s.authz.Allowed(ctx, claims.RoleID, "catalog:sku_read") && s.authz.Allowed(ctx, claims.RoleID, "catalog:sku_write")
		copyControls = s.authz.Allowed(ctx, claims.RoleID, "catalog:control_read") && s.authz.Allowed(ctx, claims.RoleID, "catalog:control_write")
		if !copySKUs && !copyControls {
			return nil, errors.Forbidden("authz.PERMISSION_DENIED", "沒有複製規格或下單控件的權限")
		}
	}

	var p *ent.Product
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		var err error
		p, err = s.repo.CreateProduct(ctx, in)
		if err != nil {
			return errors.InternalServer("catalog.CREATE_FAILED", "創建失敗: "+err.Error())
		}
		if copyFrom != 0 {
			if err := s.repo.copyProductConfiguration(ctx, p, copyFrom, copySKUs, copyControls); err != nil {
				if e := errors.FromError(err); e.Code < 500 {
					return e
				}
				return errors.InternalServer("catalog.COPY_CONFIGURATION_FAILED", "複製規格或下單控件失敗，請重試")
			}
		}
		// 直發內容使用新商品 ID 加密，與商品及複製的配置一起提交。
		if req.GetStockType() != "card" && req.GetDirectContent() != "" {
			if s.cipher == nil {
				return errors.InternalServer("catalog.CIPHER_UNAVAILABLE", "直發加密不可用")
			}
			ciphered, err := s.sealDirect(ctx, req.GetDirectContent(), p.ID)
			if err != nil {
				return errors.InternalServer("catalog.DIRECT_SEAL_FAILED", "直發內容加密失敗")
			}
			if err := s.repo.SetDirectContent(ctx, p.ID, ciphered); err != nil {
				return errors.InternalServer("catalog.DIRECT_SAVE_FAILED", "直發內容保存失敗")
			}
			p.DirectContent = ciphered
		}
		return nil
	})
	if err != nil {
		if e := errors.FromError(err); e.Reason != "" {
			return nil, e
		}
		return nil, errors.InternalServer("catalog.CREATE_FAILED", "創建商品失敗，請重試")
	}
	return ToAdminPB(p), nil
}

// UpdateProduct 更新商品。
func (s *AdminCatalogService) UpdateProduct(ctx context.Context, req *adminv1.UpdateProductRequest) (*adminv1.AdminProduct, error) {
	if req.GetPriceCents() > money.MaxCents || req.GetFactoryPriceCents() > money.MaxCents {
		return nil, errors.BadRequest("catalog.INVALID_INPUT", "價格與成本不得超出上限")
	}
	in := port.ProductInput{
		ProductProperty: req.ProductProperty, SalesVisible: req.SalesVisible, TrackInventory: req.TrackInventory,
		GoodsType: req.GoodsType, ShippingMode: req.ShippingMode, ShippingFee: req.ShippingFeeCents, PhysicalStock: req.PhysicalStock, ExpectedPhysicalStock: req.ExpectedPhysicalStock, ShippingCountries: req.ShippingCountries,
		Name: req.GetName(), CategoryID: req.GetCategoryId(),
		Description:    sanitize.RichHTML(req.GetDescription()),
		DescriptionSet: req.Description != nil,
		Cover:          req.GetCover(), CoverSet: req.Cover != nil,
		Images: req.GetImages(), ImagesSet: req.GetImagesSet(),
		Price: req.GetPriceCents(), FactoryPrice: req.GetFactoryPriceCents(),
		StockType: req.GetStockType(), DeliveryMode: req.GetDeliveryMode(),
		FulfillmentMode: req.GetFulfillmentMode(), ManualStock: req.ManualStock,
		StockVisible: req.GetStockVisible(), StockVisibleSet: req.StockVisible != nil,
		Sort: req.GetSort(), Status: int8(req.GetStatus()),
		PointsRequired: req.GetPointsRequired(), PointsRequiredSet: true,
		IsRecommend: req.GetIsRecommend(), IsRecommendSet: req.IsRecommend != nil,
	}
	old, err := s.repo.GetAdmin(ctx, tenancy.FromContext(ctx).SubsiteID, req.GetId())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("catalog.PRODUCT_NOT_FOUND", "商品不存在")
	}
	if err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		return nil, errors.InternalServer("catalog.GET_FAILED", "讀取商品失敗")
	}
	if err := s.repo.validatePhysicalProduct(ctx, old, &in); err != nil {
		return nil, errors.BadRequest("catalog.INVALID_INPUT", err.Error())
	}
	if *in.GoodsType == "physical" && req.GetDirectContent() != "" {
		return nil, errors.BadRequest("catalog.INVALID_INPUT", "實體商品不能配置虛擬直發內容")
	}
	stockType := in.StockType
	if stockType == "" {
		stockType = string(old.StockType)
	}
	if stockType != "card" && stockType != "url" && stockType != "code" {
		return nil, errors.BadRequest("catalog.INVALID_STOCK_TYPE", "請選擇有效的發貨類型")
	}
	// 切換直發類型時必須重新提供內容，防止舊鏈接被當作新兌換碼發送。
	if stockType != "card" && stockType != string(old.StockType) && strings.TrimSpace(req.GetDirectContent()) == "" {
		return nil, errors.BadRequest("catalog.DIRECT_CONTENT_REQUIRED", "切換為鏈接或兌換碼時，請填寫對應的直發內容")
	}
	// 直發內容（url/code）：空=保持不變；非空且商品非卡密類 → 加密更新
	if stockType != "card" && req.GetDirectContent() != "" {
		if s.cipher == nil {
			return nil, errors.InternalServer("catalog.CIPHER_UNAVAILABLE", "直發加密不可用")
		}
		ciphered, serr := s.sealDirect(ctx, req.GetDirectContent(), req.GetId())
		if serr != nil {
			return nil, errors.InternalServer("catalog.DIRECT_SEAL_FAILED", "直發內容加密失敗")
		}
		in.DirectContent = ciphered
	}
	p, err := s.repo.UpdateProduct(ctx, req.GetId(), in)
	if err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		return nil, errors.InternalServer("catalog.UPDATE_FAILED", "更新失敗")
	}
	return ToAdminPB(p), nil
}

// BatchUpdateProductStatus 批量上下架（列表多選）。
func (s *AdminCatalogService) BatchUpdateProductStatus(ctx context.Context, req *adminv1.BatchUpdateProductStatusRequest) (*adminv1.BatchUpdateProductStatusReply, error) {
	if req.Status < 0 || req.Status > 2 {
		return nil, errors.BadRequest("catalog.STATUS_INVALID", "請選擇有效的上下架狀態")
	}
	n, skipped, err := s.repo.batchStatus(ctx, req.GetIds(), int8(req.GetStatus()))
	if err != nil {
		return nil, errors.BadRequest("catalog.BATCH_STATUS_INVALID", err.Error())
	}
	return &adminv1.BatchUpdateProductStatusReply{Updated: int32(n), SkippedLocked: skipped}, nil
}

// ── 分類 ──

// ListCategories 分類列表。
func (s *AdminCatalogService) ListCategories(ctx context.Context, _ *emptypb.Empty) (*adminv1.CategoryList, error) {
	rows, err := s.repo.ListCategories(ctx)
	if err != nil {
		return nil, errors.InternalServer("catalog.CATEGORY_LIST_FAILED", "讀取分類失敗")
	}
	counts, err := s.repo.CategoryProductCounts(ctx, rows)
	if err != nil {
		return nil, errors.InternalServer("catalog.CATEGORY_LIST_FAILED", "讀取分類商品數量失敗")
	}
	reply := &adminv1.CategoryList{}
	for _, c := range rows {
		p := toCategoryPB(c)
		p.ProductCount = counts[c.ID]
		reply.Categories = append(reply.Categories, p)
	}
	return reply, nil
}

// CreateCategory 創建分類。
func (s *AdminCatalogService) CreateCategory(ctx context.Context, req *adminv1.CreateCategoryRequest) (*adminv1.Category, error) {
	c, err := s.repo.CreateCategory(ctx, req.GetName(), req.GetParentId(), req.GetIcon(), req.GetSort())
	if err != nil {
		return nil, errors.BadRequest("catalog.CATEGORY_CREATE_FAILED", "創建失敗（可能存在環）")
	}
	return toCategoryPB(c), nil
}

// UpdateCategory 更新分類。
func (s *AdminCatalogService) UpdateCategory(ctx context.Context, req *adminv1.UpdateCategoryRequest) (*adminv1.Category, error) {
	c, err := s.repo.UpdateCategory(ctx, req.GetId(), req.GetName(), req.Icon, req.Hide, req.Sort, req.ParentId)
	if err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		if strings.Contains(err.Error(), "CATEGORY_CYCLE") {
			return nil, errors.BadRequest("catalog.CATEGORY_CYCLE", "不能把分類移到自身或它的子分類下")
		}
		if strings.Contains(err.Error(), "CATEGORY_CANNOT_PARENT_SELF") {
			return nil, errors.BadRequest("catalog.CATEGORY_CANNOT_PARENT_SELF", "分類不能設為自身的子級")
		}
		return nil, errors.InternalServer("catalog.CATEGORY_UPDATE_FAILED", "更新失敗")
	}
	return toCategoryPB(c), nil
}

// ReorderCategories 分類排序（拖拽重排：某層級兄弟按 ids 順序歸一化 sort）。
func (s *AdminCatalogService) ReorderCategories(ctx context.Context, req *adminv1.ReorderCategoriesRequest) (*emptypb.Empty, error) {
	if err := s.repo.ReorderCategories(ctx, req.GetParentId(), req.GetIds()); err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		if strings.Contains(err.Error(), "CATEGORY_CYCLE") {
			return nil, errors.BadRequest("catalog.CATEGORY_CYCLE", "不能把分類移到自身或它的子分類下")
		}
		return nil, errors.InternalServer("catalog.CATEGORY_REORDER_FAILED", "排序失敗")
	}
	return &emptypb.Empty{}, nil
}

// DeleteCategory 刪除分類。
func (s *AdminCatalogService) DeleteCategory(ctx context.Context, req *adminv1.DeleteCategoryRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteCategory(ctx, req.GetId()); err != nil {
		switch err.Error() {
		case "catalog.CATEGORY_NOT_FOUND":
			return nil, errors.NotFound("catalog.CATEGORY_NOT_FOUND", "分類不存在或已刪除")
		default:
			return nil, errors.InternalServer("catalog.CATEGORY_DELETE_FAILED", "刪除分類失敗，請稍後重試")
		}
	}
	return &emptypb.Empty{}, nil
}

// ── 標籤 ──

// ListTags 標籤列表。
func (s *AdminCatalogService) ListTags(ctx context.Context, _ *emptypb.Empty) (*adminv1.TagList, error) {
	rows, err := s.repo.ListTags(ctx)
	if err != nil {
		return nil, errors.InternalServer("catalog.TAG_LIST_FAILED", "讀取標籤失敗")
	}
	reply := &adminv1.TagList{}
	for _, t := range rows {
		reply.Tags = append(reply.Tags, &adminv1.Tag{
			Id: t.ID, Name: t.Name, Slug: t.Slug, Icon: t.Icon,
			Color: t.Color, Position: string(t.Position), Hide: t.Hide,
		})
	}
	return reply, nil
}

// CreateTag 創建標籤。
func (s *AdminCatalogService) CreateTag(ctx context.Context, req *adminv1.CreateTagRequest) (*adminv1.Tag, error) {
	t, err := s.repo.CreateTag(ctx, req.GetName(), req.GetSlug(), req.GetIcon(), req.GetColor(), req.GetPosition())
	if err != nil {
		return nil, errors.InternalServer("catalog.TAG_CREATE_FAILED", "創建失敗（slug 可能重複）")
	}
	return &adminv1.Tag{Id: t.ID, Name: t.Name, Slug: t.Slug}, nil
}

// DeleteTag 刪除標籤。
func (s *AdminCatalogService) DeleteTag(ctx context.Context, req *adminv1.DeleteTagRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteTag(ctx, req.GetId()); err != nil {
		return nil, errors.NotFound("catalog.TAG_NOT_FOUND", "標籤不存在")
	}
	return &emptypb.Empty{}, nil
}

// ── 自定義控件 ──

// ListControls 控件列表。
func (s *AdminCatalogService) ListControls(ctx context.Context, req *adminv1.ListControlsRequest) (*adminv1.ControlList, error) {
	rows, err := s.repo.ListProductControls(ctx, req.GetProductId())
	if err != nil {
		return nil, errors.InternalServer("catalog.CONTROL_LIST_FAILED", "讀取控件失敗")
	}
	reply := &adminv1.ControlList{}
	for _, c := range rows {
		reply.Controls = append(reply.Controls, toControlPB(c))
	}
	return reply, nil
}

// CreateControl 創建控件。
func (s *AdminCatalogService) CreateControl(ctx context.Context, req *adminv1.CreateControlRequest) (*adminv1.AdminControl, error) {
	if req.GetProductId() == 0 || req.GetName() == "" || req.GetType() == "" {
		return nil, errors.BadRequest("catalog.CONTROL_INVALID", "商品/名稱/類型必填")
	}
	c, err := s.repo.CreateProductControl(ctx, req.GetProductId(), 0, req.GetName(), req.GetType(), req.GetRequired(), req.GetOptions(), req.GetSort(), ControlSettings{Placeholder: req.GetPlaceholder(), Validation: req.GetValidation(), MaxLength: req.MaxLength})
	if err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		return nil, errors.InternalServer("catalog.CONTROL_CREATE_FAILED", "創建失敗")
	}
	return toControlPB(c), nil
}

// UpdateControl 更新控件。
func (s *AdminCatalogService) UpdateControl(ctx context.Context, req *adminv1.UpdateControlRequest) (*adminv1.AdminControl, error) {
	c, err := s.repo.UpdateProductControl(ctx, req.GetId(), req.GetName(), req.GetType(), req.GetRequired(), req.GetOptions(), req.GetSort(), ControlSettings{Placeholder: req.GetPlaceholder(), Validation: req.GetValidation(), MaxLength: req.MaxLength})
	if err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		return nil, errors.NotFound("catalog.CONTROL_NOT_FOUND", "控件不存在")
	}
	return toControlPB(c), nil
}

// DeleteControl 刪除控件。
func (s *AdminCatalogService) DeleteControl(ctx context.Context, req *adminv1.DeleteControlRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteProductControl(ctx, req.GetId()); err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		return nil, errors.NotFound("catalog.CONTROL_NOT_FOUND", "控件不存在")
	}
	return &emptypb.Empty{}, nil
}

// ── 評價 ──

// ListReviews 評價列表。
func (s *AdminCatalogService) ListReviews(ctx context.Context, req *adminv1.ListReviewsRequest) (*adminv1.ListReviewsReply, error) {
	rows, total, err := s.repo.ListReviews(ctx, req.GetStatus(), req.GetPage(), req.GetPageSize())
	if err != nil {
		return nil, errors.InternalServer("catalog.REVIEW_LIST_FAILED", "讀取評價失敗")
	}
	reply := &adminv1.ListReviewsReply{Total: total}
	for _, v := range rows {
		reply.Reviews = append(reply.Reviews, toReviewPB(v))
	}
	return reply, nil
}

// ApproveReview 審核通過。
func (s *AdminCatalogService) ApproveReview(ctx context.Context, req *adminv1.ApproveReviewRequest) (*adminv1.ReviewItem, error) {
	v, err := s.repo.ApproveReview(ctx, req.GetId())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("catalog.REVIEW_NOT_FOUND", "評價不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("catalog.REVIEW_APPROVE_FAILED", "審核失敗")
	}
	return toReviewPB(v), nil
}

// RejectReview 審核拒絕。
func (s *AdminCatalogService) RejectReview(ctx context.Context, req *adminv1.RejectReviewRequest) (*adminv1.ReviewItem, error) {
	v, err := s.repo.RejectReview(ctx, req.GetId())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("catalog.REVIEW_NOT_FOUND", "評價不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("catalog.REVIEW_REJECT_FAILED", "審核失敗")
	}
	return toReviewPB(v), nil
}

// CreateVirtualReview 創建虛擬評價（content sanitize）。
func (s *AdminCatalogService) CreateVirtualReview(ctx context.Context, req *adminv1.CreateVirtualReviewRequest) (*adminv1.VirtualReviewItem, error) {
	if req.GetProductId() == 0 || req.GetContent() == "" {
		return nil, errors.BadRequest("catalog.VIRTUAL_REVIEW_INVALID", "商品 ID 與內容必填")
	}
	v, err := s.repo.CreateVirtualReview(ctx, req.GetProductId(),
		sanitize.Text(req.GetNickname()), sanitize.HTML(req.GetContent()),
		int8(req.GetRating()), req.GetSort())
	if err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		return nil, errors.InternalServer("catalog.VIRTUAL_REVIEW_CREATE_FAILED", "創建失敗")
	}
	return toVirtualReviewPB(v), nil
}

// ── SKU ──

// ListSkus SKU 列表。
func (s *AdminCatalogService) ListSkus(ctx context.Context, req *adminv1.ListSkusRequest) (*adminv1.SkuList, error) {
	rows, err := s.repo.ListProductSkus(ctx, req.GetProductId())
	if err != nil {
		return nil, errors.InternalServer("catalog.SKU_LIST_FAILED", "讀取 SKU 失敗")
	}
	reply := &adminv1.SkuList{}
	for _, sku := range rows {
		reply.Skus = append(reply.Skus, toSkuPB(sku))
	}
	return reply, nil
}

// CreateSku 創建 SKU。
func (s *AdminCatalogService) CreateSku(ctx context.Context, req *adminv1.CreateSkuRequest) (*adminv1.Sku, error) {
	if req.GetProductId() == 0 || req.GetName() == "" {
		return nil, errors.BadRequest("catalog.SKU_INVALID", "商品 ID 與規格名必填")
	}
	sku, err := s.repo.CreateSku(ctx, SkuInput{
		PhysicalStock: req.PhysicalStock, ProductID: req.GetProductId(), Name: req.GetName(), SpecValues: req.GetSpecValues(),
		PriceCents: req.GetPriceCents(), CostCents: req.GetCostCents(),
		StockOffset: req.GetStockOffset(), UpstreamSkuID: req.GetUpstreamSkuId(), FulfillmentMode: req.GetFulfillmentMode(),
	})
	if err != nil {
		if e := errors.FromError(err); e.Code < 500 {
			return nil, e
		}
		if ent.IsValidationError(err) {
			return nil, errors.BadRequest("catalog.SKU_INVALID", "規格名稱或設置無效")
		}
		return nil, errors.InternalServer("catalog.SKU_CREATE_FAILED", "創建規格失敗，請重試")
	}
	return toSkuPB(sku), nil
}

// UpdateSku 更新 SKU。
func (s *AdminCatalogService) UpdateSku(ctx context.Context, req *adminv1.UpdateSkuRequest) (*adminv1.Sku, error) {
	sku, err := s.repo.UpdateSku(ctx, req.GetId(), SkuInput{PhysicalStock: req.PhysicalStock, ExpectedPhysicalStock: req.ExpectedPhysicalStock,
		SetPrice: req.PriceCents != nil, SetCost: req.CostCents != nil, SetStockOffset: req.StockOffset != nil,
		Name: req.GetName(), SpecValues: req.GetSpecValues(),
		PriceCents: req.GetPriceCents(), CostCents: req.GetCostCents(),
		StockOffset: req.GetStockOffset(), UpstreamSkuID: req.GetUpstreamSkuId(), FulfillmentMode: req.GetFulfillmentMode(),
	})
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("catalog.SKU_NOT_FOUND", "SKU 不存在")
	}
	if err != nil {
		if e := errors.FromError(err); e.Code < 500 {
			return nil, e
		}
		return nil, errors.InternalServer("catalog.SKU_UPDATE_FAILED", "更新失敗")
	}
	return toSkuPB(sku), nil
}

// DeleteSku 刪除 SKU。
func (s *AdminCatalogService) DeleteSku(ctx context.Context, req *adminv1.DeleteSkuRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteSku(ctx, req.GetId()); err != nil {
		if data.IsProductLocked(err) {
			return nil, err
		}
		return nil, errors.NotFound("catalog.SKU_NOT_FOUND", "SKU 不存在")
	}
	return &emptypb.Empty{}, nil
}

// ── 會員商品組 ──

// ListMemberGroups 商品組列表。
func (s *AdminCatalogService) ListMemberGroups(ctx context.Context, _ *emptypb.Empty) (*adminv1.MemberGroupList, error) {
	rows, err := s.repo.ListMemberGroups(ctx)
	if err != nil {
		return nil, errors.InternalServer("catalog.GROUP_LIST_FAILED", "讀取商品組失敗")
	}
	reply := &adminv1.MemberGroupList{}
	for _, g := range rows {
		reply.Groups = append(reply.Groups, toMemberGroupPB(g))
	}
	return reply, nil
}

// CreateMemberGroup 創建商品組。
func (s *AdminCatalogService) CreateMemberGroup(ctx context.Context, req *adminv1.CreateMemberGroupRequest) (*adminv1.MemberGroup, error) {
	if req.GetName() == "" || req.GetDiscount() <= 0 || req.GetDiscount() >= 10000 {
		return nil, errors.BadRequest("catalog.GROUP_INVALID", "名稱必填，折扣需在 (0,10000) 萬分比區間")
	}
	g, err := s.repo.CreateMemberGroup(ctx, GroupInput{
		Name: req.GetName(), ProductIDs: req.GetProductIds(), Discount: req.GetDiscount(),
		StackMember: req.GetStackMember(), StackCoupon: req.GetStackCoupon(),
		BadgeStyle: req.GetBadgeStyle(),
	})
	if err != nil {
		return nil, errors.InternalServer("catalog.GROUP_CREATE_FAILED", "創建失敗")
	}
	return toMemberGroupPB(g), nil
}

// UpdateMemberGroup 更新商品組。
func (s *AdminCatalogService) UpdateMemberGroup(ctx context.Context, req *adminv1.UpdateMemberGroupRequest) (*adminv1.MemberGroup, error) {
	g, err := s.repo.UpdateMemberGroup(ctx, req.GetId(), GroupInput{
		Name: req.GetName(), ProductIDs: req.GetProductIds(), Discount: req.GetDiscount(),
		StackMember: req.GetStackMember(), StackCoupon: req.GetStackCoupon(),
		BadgeStyle: req.GetBadgeStyle(),
	})
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("catalog.GROUP_NOT_FOUND", "商品組不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("catalog.GROUP_UPDATE_FAILED", "更新失敗")
	}
	return toMemberGroupPB(g), nil
}

// DeleteMemberGroup 刪除商品組。
func (s *AdminCatalogService) DeleteMemberGroup(ctx context.Context, req *adminv1.DeleteMemberGroupRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteMemberGroup(ctx, req.GetId()); err != nil {
		return nil, errors.NotFound("catalog.GROUP_NOT_FOUND", "商品組不存在")
	}
	return &emptypb.Empty{}, nil
}

// ── 轉換 ──

func toMemberGroupPB(g *ent.MemberProductGroup) *adminv1.MemberGroup {
	return &adminv1.MemberGroup{
		Id: g.ID, Name: g.Name, ProductIds: g.ProductIds, Discount: g.Discount,
		StackMember: g.StackMember, StackCoupon: g.StackCoupon, BadgeStyle: g.BadgeStyle,
	}
}

func toSkuPB(sku *ent.ProductSku) *adminv1.Sku {
	return &adminv1.Sku{PhysicalStock: &sku.PhysicalStock,
		Id: sku.ID, ProductId: sku.ProductID, Name: sku.Name,
		SpecValues: sku.SpecValues, PriceCents: sku.Price, CostCents: sku.Cost,
		StockOffset: sku.StockOffset, UpstreamSkuId: sku.UpstreamSkuID, FulfillmentMode: sku.FulfillmentMode,
	}
}

func toReviewPB(v *ent.Review) *adminv1.ReviewItem {
	out := &adminv1.ReviewItem{
		Id: v.ID, ProductId: v.ProductID, UserId: v.UserID, OrderId: v.OrderID,
		Rating: int32(v.Rating), Content: v.Content, Status: string(v.Status),
	}
	if !v.CreatedAt.IsZero() {
		out.CreatedAt = v.CreatedAt.Unix()
	}
	return out
}

func toVirtualReviewPB(v *ent.VirtualReview) *adminv1.VirtualReviewItem {
	out := &adminv1.VirtualReviewItem{
		Id: v.ID, ProductId: v.ProductID, Nickname: v.Nickname,
		Content: v.Content, Rating: int32(v.Rating), Sort: v.Sort,
	}
	if !v.CreatedAt.IsZero() {
		out.CreatedAt = v.CreatedAt.Unix()
	}
	return out
}

func toControlPB(c *ent.ProductControl) *adminv1.AdminControl {
	return &adminv1.AdminControl{
		Id: c.ID, ProductId: c.ProductID, Name: c.Name,
		Type: string(c.Type), Required: c.Required, Options: c.Options, Sort: c.Sort,
		Placeholder: c.Placeholder, Validation: c.Validation, MaxLength: &c.MaxLength,
	}
}

func toCategoryPB(c *ent.Category) *adminv1.Category {
	return &adminv1.Category{
		Id: c.ID, ParentId: c.ParentID, Name: c.Name, Icon: c.Icon,
		Hide: c.Hide, Sort: c.Sort,
	}
}
