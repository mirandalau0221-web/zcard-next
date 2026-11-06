package catalog

// 商品倉儲 Ent 實現（租户過濾經 tenancy.Context； 交付 Ent interceptor 後
// 本處顯式條件作為雙保險保留——interceptor 負責注入，業務不依賴「忘了寫」）。

import (
	mediaport "github.com/NovaWorks/zcard-next/server/internal/mods/media/port"

	"context"
	"entgo.io/ent/dialect/sql"
	"errors"
	placement "github.com/NovaWorks/zcard-next/server/internal/data/ent/categoryproductplacement"
	"sync"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/category"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productcontrol"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/sanitize"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

// ErrProductNotFound 商品不存在或不可見。
var ErrProductNotFound = errors.New("catalog.PRODUCT_NOT_FOUND")

// ProductRepoImpl 商品倉儲實現。
type ProductRepoImpl struct {
	stockLookup     port.StockLookup
	stockRefreshing sync.Map
	data            *data.Data
}

// NewProductRepoImpl keeps the existing constructor contract; product media refs use the product transaction.
func NewProductRepoImpl(d *data.Data, _ mediaport.Referencer) *ProductRepoImpl {
	return &ProductRepoImpl{data: d}
}

// ListVisible 上架商品分頁（INDEX(subsite_id, status) 命中；只取列表所需列避免回表）。
func (r *ProductRepoImpl) ListVisible(ctx context.Context, f port.VisibleFilter) ([]port.Product, int64, error) {
	client := data.Client(ctx, r.data)
	hidden, err := data.HiddenCategoryIDs(ctx, client, f.SubsiteID)
	if err != nil {
		return nil, 0, err
	}
	q := client.Product.Query().Where(data.VisibleProductCategory(hidden)).
		Where(
			product.SubsiteID(f.SubsiteID),
			product.Status(1),
		)
	// 排序（sales 由 service 層全量排序，此處不處理）
	switch f.Sort {
	case "price_asc":
		q = q.Order(ent.Asc(product.FieldPrice))
	case "price_desc":
		q = q.Order(ent.Desc(product.FieldPrice))
	case "newest":
		// 最新上架：創建先後降序（區別於綜合排序的運營權重 FieldSort 優先）
		q = q.Order(ent.Desc(product.FieldID))
	default:
		// 綜合排序（default）：運營權重優先，同權重內新商品在前
		if f.CategoryID > 0 {
			q = q.Order(func(s *sql.Selector) {
				t := sql.Table(placement.Table).As("category_placement")
				s.LeftJoin(t).On(s.C(product.FieldID), t.C(placement.FieldProductID)).OnP(sql.And(sql.EQ(t.C(placement.FieldCategoryID), f.CategoryID), sql.EQ(t.C(placement.FieldSubsiteID), f.SubsiteID)))
				s.OrderExpr(sql.ExprFunc(func(b *sql.Builder) {
					b.WriteString("CASE WHEN ").Ident(t.C(placement.FieldIsPinned)).WriteString(" = ").Arg(true).WriteString(" THEN 0 ELSE 1 END")
				}))
				s.OrderExpr(sql.ExprFunc(func(b *sql.Builder) {
					b.WriteString("CASE WHEN ").Ident(t.C(placement.FieldIsPinned)).WriteString(" = ").Arg(true).WriteString(" THEN ").Ident(t.C(placement.FieldPosition)).WriteString(" ELSE 0 END")
				}))
			})
		}
		q = q.Order(ent.Asc(product.FieldSort), ent.Desc(product.FieldID))
	}
	if f.CategoryID > 0 {
		// 選擇父分類時遞歸包含全部子分類商品（與後台列表同口徑）
		desc, err := descendantCategoryIDs(ctx, client, f.CategoryID)
		if err != nil {
			return nil, 0, err
		}
		ids := make([]uint64, 0, len(desc))
		for id := range desc {
			ids = append(ids, id)
		}
		q = q.Where(product.CategoryIDIn(ids...))
	}
	if f.Keyword != "" {
		// 商品名搜索用包含匹配（用户預期：名稱中段詞也要能命中； 禁 %xx% 針對的是
		// order_no/username 等等值索引列，商品名模糊檢索不在此列，當前量級 LIKE 可接受）
		q = q.Where(product.NameContains(f.Keyword))
	}
	if f.PointsOnly {
		q = q.Where(product.PointsRequiredGT(0)) // 積分商城視圖（）
	}
	if f.RecommendOnly {
		q = q.Where(product.IsRecommend(true)) // 推薦位視圖（首頁推薦區塊）
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	query := q.Clone()
	// 分頁（Page=0 = 全量拉取，sales 排序用）
	if f.Page > 0 && f.PageSize > 0 {
		query = query.Offset((int(f.Page) - 1) * int(f.PageSize)).Limit(int(f.PageSize))
	}
	rows, err := query.All(ctx)
	if err != nil {
		return nil, 0, err
	}
	items := make([]port.Product, 0, len(rows))
	for _, row := range rows {
		items = append(items, toPortProduct(row))
	}
	if f.CategoryID > 0 && len(items) > 0 {
		ids := make([]uint64, 0, len(items))
		for _, p := range items {
			ids = append(ids, p.ID)
		}
		placements, e := client.CategoryProductPlacement.Query().Where(placement.SubsiteID(f.SubsiteID), placement.CategoryID(f.CategoryID), placement.ProductIDIn(ids...)).All(ctx)
		if e != nil {
			return nil, 0, e
		}
		byID := map[uint64]*ent.CategoryProductPlacement{}
		for _, p := range placements {
			byID[p.ProductID] = p
		}
		for i := range items {
			if p := byID[items[i].ID]; p != nil {
				items[i].CategoryRecommend = p.IsRecommended
				items[i].CategoryPinned = p.IsPinned
			}
		}
	}
	return items, int64(total), nil
}

// Get 單個商品。
// SkuUpstreamCode 本地 SKU 的上游標識（procurement 規格採購還原；不存在 → 空串）。
func (r *ProductRepoImpl) SkuUpstreamCode(ctx context.Context, subsiteID, skuID uint64) string {
	sku, err := data.Client(ctx, r.data).ProductSku.Get(ctx, skuID)
	if err != nil || sku.SubsiteID != subsiteID {
		return ""
	}
	return sku.UpstreamSkuID
}

func (r *ProductRepoImpl) Get(ctx context.Context, subsiteID, id uint64) (*port.Product, error) {
	row, err := data.Client(ctx, r.data).Product.Query().
		Where(product.ID(id), product.SubsiteID(subsiteID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, err
	}
	p := toPortProduct(row)
	p.ManualStock, err = data.ManualAvailable(ctx, data.Client(ctx, r.data), row)
	if err != nil {
		return nil, err
	}
	hidden, err := data.HiddenCategoryIDs(ctx, data.Client(ctx, r.data), subsiteID)
	if err != nil {
		return nil, err
	}
	if data.CategoryHidden(hidden, row.CategoryID) {
		p.Status = 0
	}
	return &p, nil
}

func toPortProduct(row *ent.Product) port.Product {
	name, description := sanitize.SMSPublicContent(row.ProductKind, row.Name, row.Description)
	return port.Product{
		ProductKind: row.ProductKind, DeliveryKind: row.DeliveryKind, SMSProduct: row.SmsProduct, SocialSchema: row.SocialSchema,
		GoodsType: row.GoodsType, ProductProperty: row.ProductProperty, SalesVisible: row.SalesVisible, TrackInventory: row.TrackInventory, ShippingMode: row.ShippingMode, ShippingFee: row.ShippingFee, ShippingCountries: row.ShippingCountries, PhysicalStock: row.PhysicalStock,
		ID:              row.ID,
		SubsiteID:       row.SubsiteID,
		Name:            name,
		Slug:            row.Slug,
		Cover:           row.Cover,
		Description:     description,
		Price:           money.Cents(row.Price),
		FactoryPrice:    money.Cents(row.FactoryPrice),
		StockType:       string(row.StockType),
		FulfillmentMode: row.FulfillmentMode, ManualStock: row.ManualStock,
		DeliveryMode: string(row.DeliveryMode),
		Status:       row.Status,
		StockVisible: row.StockVisible,
		// ：積分兑換價（積分商城商品判定）
		PointsRequired: row.PointsRequired,
		// ：貨源信息（procurement 判定上游項）
		UpstreamSourceID:    row.UpstreamSourceID,
		UpstreamProductCode: row.UpstreamProductCode,
		IsRecommend:         row.IsRecommend,
	}
}

// ListVisibleCategories 可見分類（hide=false + 分站白名單；導航/篩選用）。
func (r *ProductRepoImpl) ListVisibleCategories(ctx context.Context, subsiteID uint64) ([]port.Category, error) {
	rows, err := data.Client(ctx, r.data).Category.Query().
		Where(
			category.SubsiteID(subsiteID),
			category.Hide(false),
		).
		Order(ent.Asc(category.FieldSort), ent.Asc(category.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	hidden, err := data.HiddenCategoryIDs(ctx, data.Client(ctx, r.data), subsiteID)
	if err != nil {
		return nil, err
	}
	out := make([]port.Category, 0, len(rows))
	for _, c := range rows {
		if data.CategoryHidden(hidden, c.ID) {
			continue
		}
		// 分站可見性白名單（空=全部可見；非空且不含本站 → 跳過）
		if len(c.VisibleSubsites) > 0 && !containsUint64(c.VisibleSubsites, subsiteID) {
			continue
		}
		out = append(out, port.Category{ID: c.ID, Name: c.Name, Icon: c.Icon, ParentID: c.ParentID})
	}
	return out, nil
}

func containsUint64(list []uint64, v uint64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ListControls 商品自定義控件（按 sort 升序）。
func (r *ProductRepoImpl) ListControls(ctx context.Context, productID uint64) ([]port.Control, error) {
	rows, err := data.Client(ctx, r.data).ProductControl.Query().
		Where(productcontrol.ProductID(productID)).
		Order(ent.Asc(productcontrol.FieldSort)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.Control, 0, len(rows))
	for _, c := range rows {
		out = append(out, port.Control{
			ID: c.ID, Name: c.Name, Type: string(c.Type),
			Required: c.Required, Options: c.Options, Sort: c.Sort,
			Placeholder: c.Placeholder, Validation: c.Validation, MaxLength: c.MaxLength,
		})
	}
	return out, nil
}

var _ = tenancy.Main // interceptor 接入後移除佔位引用
