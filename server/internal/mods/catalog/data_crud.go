package catalog

// 商品/分類/標籤管理 CRUD 數據層（；ent import 收口：data 前綴文件）。
// sanitize 在 service 層調用後傳入；slug 唯一校驗在 biz。

import (
	"context"
	"fmt"
	placement "github.com/NovaWorks/zcard-next/server/internal/data/ent/categoryproductplacement"
	"strings"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/category"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productsku"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplierproductprice"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplymapping"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/tag"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	mediamods "github.com/NovaWorks/zcard-next/server/internal/mods/media"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	"github.com/NovaWorks/zcard-next/server/internal/platform/sanitize"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"google.golang.org/protobuf/proto"
)

// ── 商品 ─────────────────────────────────────────────────────

// ListAdmin 管理面商品列表（含下架/隱藏；成本價下發）。
func (r *ProductRepoImpl) ListAdmin(ctx context.Context, f port.AdminFilter) ([]*ent.Product, int64, error) {
	tc := tenancy.FromContext(ctx)
	q := data.Client(ctx, r.data).Product.Query().
		Where(product.SubsiteID(tc.SubsiteID), product.StatusGTE(0)).
		Order(ent.Asc(product.FieldSort), ent.Desc(product.FieldID))
	if f.AutoListing != nil {
		q.Where(product.AutoListing(*f.AutoListing))
	}
	if f.RestockedOnly {
		q.Where(product.Status(0), product.ListingRestocked(true))
	}
	if f.IsLocked != nil {
		q = q.Where(product.IsLocked(*f.IsLocked))
	}
	if f.CategoryID > 0 {
		// 選擇父分類時遞歸包含全部子分類商品
		desc, err := descendantCategoryIDs(ctx, data.Client(ctx, r.data), f.CategoryID)
		if err != nil {
			return nil, 0, err
		}
		ids := make([]uint64, 0, len(desc))
		for id := range desc {
			ids = append(ids, id)
		}
		q = q.Where(product.CategoryIDIn(ids...))
	}
	if f.StockType != "" {
		if f.StockType != "card" && f.StockType != "url" && f.StockType != "code" {
			return nil, 0, fmt.Errorf("商品庫存類型無效")
		}
		q = q.Where(product.StockTypeEQ(product.StockType(f.StockType)))
	}
	if f.Keyword != "" {
		q = q.Where(product.NameContains(f.Keyword)) // 與前台一致：包含匹配（搜名稱中段詞可命中）
	}
	if f.ConnectionID > 0 {
		q = q.Where(product.UpstreamSourceID(f.ConnectionID))
	}
	if f.LocalOnly {
		q = q.Where(product.Or(product.UpstreamSourceIDIsNil(), product.UpstreamSourceID(0)))
	}
	if f.Status != 0 { // 0=全部（proto3 默認值）；1=上架 2=隱藏 -1=僅下架（DB status=0）
		st := f.Status
		if st == -1 {
			st = 0
		}
		q = q.Where(product.Status(st))
	}
	if f.Inventory != "" || f.RestockedOnly {
		return r.listInventory(ctx, q, f)
	}
	// 先按貨源計算庫存，再篩選和分頁；未知/不限庫存不計入缺貨。
	if f.LowStockThreshold > 0 || f.OutOfStockOnly {
		candidates, err := q.Clone().Where(product.Status(1)).All(ctx)
		if err != nil {
			return nil, 0, err
		}
		stocks, err := data.ProductStocks(ctx, r.data, candidates)
		if err != nil {
			return nil, 0, err
		}
		var ids []uint64
		for _, p := range candidates {
			n := stocks[p.ID]
			match := f.OutOfStockOnly && n == 0
			if !f.OutOfStockOnly {
				match, err = data.HasLowStock(ctx, r.data, p, f.LowStockThreshold)
				if err != nil {
					return nil, 0, err
				}
			}
			if match {
				ids = append(ids, p.ID)
			}
		}
		if len(ids) == 0 {
			return nil, 0, nil
		}
		q = q.Where(product.IDIn(ids...))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	if f.Page > 0 && f.PageSize > 0 {
		q = q.Offset((int(f.Page) - 1) * int(f.PageSize)).Limit(int(f.PageSize))
	}
	if f.OptionsOnly {
		q.Select(product.FieldID, product.FieldName, product.FieldCategoryID, product.FieldPrice, product.FieldStockType, product.FieldGoodsType, product.FieldProductProperty, product.FieldSalesVisible, product.FieldTrackInventory, product.FieldSort, product.FieldIsLocked, product.FieldLockVersion, product.FieldStatus, product.FieldCover, product.FieldUpstreamSourceID, product.FieldFulfillmentMode)
	}
	rows, err := q.All(ctx)
	return rows, int64(total), err
}

// StockBatch returns stock from the product's actual fulfillment source.
func (r *ProductRepoImpl) StockBatch(ctx context.Context, productIDs []uint64) (map[uint64]int64, error) {
	snapshots, err := r.StockSnapshotBatch(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	out := map[uint64]int64{}
	for id, snapshot := range snapshots {
		out[id] = snapshot.Available
	}
	return out, nil
}

func (r *ProductRepoImpl) StockSnapshotBatch(ctx context.Context, productIDs []uint64) (map[uint64]port.StockSnapshot, error) {
	return r.stockSnapshotBatch(ctx, productIDs, true)
}

// Admin lists must not wait for external suppliers; display the cached quantity
// and freshness. Stock-only sync and storefront reads continue refreshing it.
func (r *ProductRepoImpl) cachedStockSnapshotBatch(ctx context.Context, productIDs []uint64) (map[uint64]port.StockSnapshot, error) {
	return r.stockSnapshotBatch(ctx, productIDs, false)
}

func (r *ProductRepoImpl) stockSnapshotBatch(ctx context.Context, productIDs []uint64, refresh bool) (map[uint64]port.StockSnapshot, error) {
	out := map[uint64]port.StockSnapshot{}
	for start := 0; start < len(productIDs); start += 500 {
		end := start + 500
		if end > len(productIDs) {
			end = len(productIDs)
		}
		rows, err := data.Client(ctx, r.data).Product.Query().Where(product.IDIn(productIDs[start:end]...), product.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).All(ctx)
		if err != nil {
			return nil, err
		}
		batch, err := data.ProductStockSnapshots(ctx, r.data, rows)
		if err != nil {
			return nil, err
		}
		if refresh {
			r.refreshDisplayStocks(ctx, rows, batch)
		}
		for id, n := range batch {
			out[id] = port.StockSnapshot{Available: n.Available(), Quantity: n.Quantity, CheckedAt: n.CheckedAt, Status: n.Status}
		}
	}
	return out, nil
}

// GetAdmin 管理面商品詳情。
func (r *ProductRepoImpl) GetAdmin(ctx context.Context, subsiteID, id uint64) (*ent.Product, error) {
	return data.Client(ctx, r.data).Product.Query().
		Where(product.ID(id), product.SubsiteID(subsiteID), product.StatusGTE(0)).
		Only(ctx)
}

// CreateProduct 創建商品（description 已 sanitize）。
func (r *ProductRepoImpl) createProduct(ctx context.Context, in port.ProductInput) (*ent.Product, error) {
	if err := r.validatePhysicalProduct(ctx, nil, &in); err != nil {
		return nil, err
	}
	if in.FulfillmentMode == "local" || in.FulfillmentMode == "reuse" {
		return nil, fmt.Errorf("請先創建商品，再配置發貨來源")
	}
	if err := validateServiceConfig(in.FulfillmentMode, in.ManualStock, false); err != nil {
		return nil, err
	}
	tc := tenancy.FromContext(ctx)
	slug, err := r.genUniqueSlug(ctx, tc.SubsiteID, in.Name)
	if err != nil {
		return nil, err
	}
	create := data.Client(ctx, r.data).Product.Create().
		SetSubsiteID(tc.SubsiteID).
		SetName(in.Name).
		SetSlug(slug).
		SetPrice(in.Price).
		SetFactoryPrice(in.FactoryPrice).
		SetStockType(product.StockType(in.StockType))
	create.SetNillableGoodsType(in.GoodsType).SetNillableShippingMode(in.ShippingMode).SetNillableShippingFee(in.ShippingFee).SetNillablePhysicalStock(in.PhysicalStock).SetShippingCountries(in.ShippingCountries)
	create.SetNillableProductProperty(in.ProductProperty).SetNillableSalesVisible(in.SalesVisible).SetNillableTrackInventory(in.TrackInventory)
	if in.FulfillmentMode != "" {
		create.SetFulfillmentMode(in.FulfillmentMode)
	}
	if in.ManualStock != nil {
		create.SetManualStock(*in.ManualStock)
	}
	if in.DeliveryMode != "" {
		create = create.SetDeliveryMode(product.DeliveryMode(in.DeliveryMode))
	}
	create = create.SetStockVisible(in.StockVisible).SetDedup(in.Dedup).SetSort(in.Sort).SetStatus(in.Status).SetIsRecommend(in.IsRecommend)
	if in.CategoryID > 0 {
		create.SetCategoryID(in.CategoryID)
	}
	if in.Description != "" {
		create.SetDescription(in.Description)
	}
	if in.Cover != "" {
		create.SetCover(in.Cover)
	}
	if len(in.Images) > 0 {
		create.SetImages(in.Images)
	}
	if in.PointsRequiredSet {
		create.SetPointsRequired(in.PointsRequired)
	}
	return create.Save(ctx)
}

// SetDirectContent 回填直髮內容密文（創建後由 service 按真實 productID 加密調用）。
func (r *ProductRepoImpl) SetDirectContent(ctx context.Context, id uint64, ciphered []byte) error {
	p, e := data.Client(ctx, r.data).Product.Get(ctx, id)
	if e != nil {
		return e
	}
	if p.DeliveryKind == "social_service" {
		return fmt.Errorf("社交服務使用綁定貨源交付，不能設置直髮內容")
	}
	return data.Client(ctx, r.data).Product.UpdateOneID(id).
		Where(product.GoodsTypeNEQ("physical")).
		SetDirectContent(ciphered).Exec(ctx)
}

// UpdateProduct 更新（nil/零值字段不動）。
func (r *ProductRepoImpl) updateProduct(ctx context.Context, id uint64, in port.ProductInput) (*ent.Product, error) {
	current, err := data.Client(ctx, r.data).Product.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := validateSocialProduct(current, &in); err != nil {
		return nil, err
	}
	if current.ProductKind == "sms_channel" {
		if in.GoodsType != nil && *in.GoodsType != "virtual" || in.StockType != "" && in.StockType != "card" || in.FulfillmentMode != "" && in.FulfillmentMode != "auto" || in.Price > 0 || in.FactoryPrice > 0 || in.PointsRequired > 0 || len(in.DirectContent) > 0 {
			return nil, fmt.Errorf("渠道接碼商品請在貨源導入/映射中設置加價，不能改為普通商品或本地發貨")
		}
	}
	if err := r.validatePhysicalProduct(ctx, current, &in); err != nil {
		return nil, err
	}
	if in.StockType != "" && in.StockType != string(current.StockType) {
		owned, e := data.HasLocalDelivery(ctx, data.Client(ctx, r.data), current)
		if e != nil {
			return nil, e
		}
		if owned {
			return nil, fmt.Errorf("商品已有本地發貨設置，不能直接更改庫存類型")
		}
	}
	if current.FulfillmentMode == "reuse" || current.FulfillmentMode == "local" {
		if in.FulfillmentMode != current.FulfillmentMode && in.FulfillmentMode != "" {
			return nil, fmt.Errorf("請在發貨設置中更改本地發貨來源")
		}
	} else if in.FulfillmentMode == "reuse" || in.FulfillmentMode == "local" {
		return nil, fmt.Errorf("請在發貨設置中配置內容來源")
	}

	if in.FulfillmentMode == "manual" {
		p, err := data.Client(ctx, r.data).Product.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if p.UpstreamSourceID > 0 {
			return nil, fmt.Errorf("上游商品不能改為本地人工交付，請新建本地服務商品")
		}
	}
	q := data.Client(ctx, r.data).Product.UpdateOneID(id).Where(product.StatusGTE(0), product.SubsiteID(tenancy.FromContext(ctx).SubsiteID))
	q.SetNillableGoodsType(in.GoodsType).SetNillableShippingMode(in.ShippingMode).SetNillableShippingFee(in.ShippingFee)
	q.SetNillableProductProperty(in.ProductProperty).SetNillableSalesVisible(in.SalesVisible).SetNillableTrackInventory(in.TrackInventory)
	if current.GoodsType == "physical" {
		q.SetDedup(false)
	}
	if in.GoodsType != nil || in.ShippingMode != nil {
		q.SetShippingCountries(in.ShippingCountries)
	}
	if current.GoodsType == "physical" || current.ProductKind == "sms_channel" {
		q.AddLockVersion(1)
	}
	if in.PhysicalStock != nil && current.GoodsType == "physical" {
		if in.ExpectedPhysicalStock == nil || *in.ExpectedPhysicalStock != current.PhysicalStock {
			return nil, fmt.Errorf("庫存已變化，請刷新後重新調整")
		}
		if err := data.AdjustPhysicalStock(ctx, r.data, current, 0, *in.PhysicalStock); err != nil {
			return nil, err
		}
	} else {
		q.SetNillablePhysicalStock(in.PhysicalStock)
	}
	if in.Name != "" {
		q.SetName(in.Name)
	}
	if in.CategoryID > 0 {
		q.SetCategoryID(in.CategoryID)
		if current.CategoryID != in.CategoryID {
			q.SetCategoryProtected(true)
		}
	}
	if in.DescriptionSet || in.Description != "" {
		q.SetDescription(in.Description)
		if current.UpstreamSourceID > 0 && current.Description != in.Description {
			q.SetDescriptionProtected(true)
		}
	}
	if in.CoverSet || in.Cover != "" {
		q.SetCover(in.Cover)
		if current.UpstreamSourceID > 0 && current.Cover != in.Cover {
			q.SetCoverProtected(true)
		}
	}
	if in.ImagesSet || len(in.Images) > 0 {
		q.SetImages(in.Images)
	}
	if in.Price > 0 {
		q.SetPrice(in.Price)
	}
	if in.FactoryPrice >= 0 {
		q.SetFactoryPrice(in.FactoryPrice)
	}
	if in.StockType != "" {
		q.SetStockType(product.StockType(in.StockType))
		// 保留舊直髮密文供歷史訂單取貨；新訂單按 stock_type 走卡池。
	}
	if err := validateServiceConfig(in.FulfillmentMode, in.ManualStock, false); err != nil {
		return nil, err
	}
	if in.FulfillmentMode != "" {
		q.SetFulfillmentMode(in.FulfillmentMode)
	}
	if in.ManualStock != nil {
		q.SetManualStock(*in.ManualStock)
	}
	if in.DeliveryMode != "" {
		q.SetDeliveryMode(product.DeliveryMode(in.DeliveryMode))
	}
	if in.DirectContent != nil {
		q.SetDirectContent(in.DirectContent)
	}
	if in.StockVisibleSet {
		q.SetStockVisible(in.StockVisible)
	}
	if in.IsRecommendSet {
		q.SetIsRecommend(in.IsRecommend)
	}
	if in.Sort >= 0 {
		q.SetSort(in.Sort)
	}
	if in.Status >= 0 {
		if in.Status != current.Status {
			data.ManualListing(q, in.Status)
		} else {
			q.SetStatus(in.Status)
		}
	}
	if in.PointsRequiredSet {
		q.SetPointsRequired(in.PointsRequired) // 含 0=移出積分商城（PUT 全量語義）
	}
	if current.DeliveryKind == "social_service" {
		q.SetPrice(0).SetFactoryPrice(0).SetPointsRequired(0).AddLockVersion(1)
	}
	if err := q.Exec(ctx); err != nil {
		return nil, err
	}
	return r.GetAdmin(ctx, tenancy.FromContext(ctx).SubsiteID, id)
}

// BatchUpdateStatus 批量上下架（ 列表多選；status 1/0/2）。
func (r *ProductRepoImpl) BatchUpdateStatus(ctx context.Context, ids []uint64, status int8) (int, error) {
	existing, err := data.Client(ctx, r.data).Product.Query().Where(product.IDIn(ids...), product.SubsiteID(tenancy.FromContext(ctx).SubsiteID), product.StatusGTE(0)).IDs(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) == 0 {
		return 0, nil
	}
	n, _, err := r.batchStatus(ctx, existing, status)
	return n, err
}

// DeleteProduct 刪除（軟外鍵約束：有卡密時拒刪）。
func (r *ProductRepoImpl) deleteProduct(ctx context.Context, id uint64) error {
	// 刪除前清理本地採集封面（失敗不阻斷刪除）
	if p, err := data.Client(ctx, r.data).Product.Get(ctx, id); err == nil {
		deleteProductCover(p.Cover)
	}
	n, err := data.Client(ctx, r.data).Product.Delete().Where(product.ID(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("catalog.PRODUCT_NOT_FOUND")
	}
	return nil
}

func (r *ProductRepoImpl) genUniqueSlug(ctx context.Context, subsiteID uint64, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = fmt.Sprintf("p-%d", entutilID())
	}
	slug := base
	for i := 0; i < 100; i++ {
		exists, err := data.Client(ctx, r.data).Product.Query().
			Where(product.SubsiteID(subsiteID), product.Slug(slug)).Exist(ctx)
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i+2)
	}
	return "", fmt.Errorf("catalog.SLUG_EXHAUSTED")
}

func slugify(s string) string {
	var out []rune
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			out = append(out, r)
		} else if r == ' ' || r == '_' {
			out = append(out, '-')
		}
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return string(out)
}

var idCounter uint64

func entutilID() uint64 {
	idCounter++
	return idCounter
}

// ── 分類 ─────────────────────────────────────────────────────

// ListCategories 分類列表（含商品計數）。
func (r *ProductRepoImpl) ListCategories(ctx context.Context) ([]*ent.Category, error) {
	tc := tenancy.FromContext(ctx)
	return data.Client(ctx, r.data).Category.Query().
		Where(category.SubsiteID(tc.SubsiteID)).
		Order(ent.Asc(category.FieldSort), ent.Asc(category.FieldID)).
		All(ctx)
}

// CreateCategory 創建分類。
func (r *ProductRepoImpl) CreateCategory(ctx context.Context, name string, parentID uint64, icon string, sort int32) (*ent.Category, error) {
	if err := data.ValidateCategoryName(name); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	tc := tenancy.FromContext(ctx)
	// 環狀校驗
	if parentID > 0 {
		if err := r.checkCategoryTree(ctx, parentID, 0); err != nil {
			return nil, err
		}
	}
	return data.Client(ctx, r.data).Category.Create().
		SetSubsiteID(tc.SubsiteID).
		SetName(name).
		SetNillableParentID(nilOrZero(parentID)).
		SetIcon(icon).
		SetSort(sort).
		Save(ctx)
}

// UpdateCategory 更新分類。
// 指針語義（缺省不變）：icon nil=不變（空串=清除）；hide nil=不變；
// sort nil=不變；parentId nil=不變（0=置頂級；>0=指定父，防環：
// 不能把分類設為自身或自身的後代，否則樹成環）。
func (r *ProductRepoImpl) updateCategory(ctx context.Context, id uint64, name string, icon *string, hide *bool, sort *int32, parentID *int64) (*ent.Category, error) {
	if name != "" {
		if err := data.ValidateCategoryName(name); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
	}
	client := data.Client(ctx, r.data)
	if _, err := client.Category.Query().Where(category.ID(id), category.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Only(ctx); err != nil {
		return nil, err
	}
	q := client.Category.UpdateOneID(id)
	if name != "" {
		q.SetName(name)
	}
	if icon != nil {
		q.SetIcon(*icon)
	}
	if hide != nil {
		q.SetHide(*hide)
	}
	if sort != nil && *sort >= 0 {
		q.SetSort(*sort)
	}
	if parentID != nil {
		if *parentID > 0 {
			if uint64(*parentID) == id {
				return nil, fmt.Errorf("catalog.CATEGORY_CANNOT_PARENT_SELF")
			}
			// 防環：新父不能是自身後代
			desc, err := descendantCategoryIDs(ctx, client, id)
			if err != nil {
				return nil, err
			}
			if desc[uint64(*parentID)] {
				return nil, fmt.Errorf("catalog.CATEGORY_CYCLE")
			}
			q.SetParentID(uint64(*parentID))
		} else {
			q.ClearParentID() // 置頂級
		}
	}
	return q.Save(ctx)
}

// descendantCategoryIDs 收集分類的全部子孫 id（含自身）——防環判據。
func descendantCategoryIDs(ctx context.Context, client *ent.Client, id uint64) (map[uint64]bool, error) {
	out := map[uint64]bool{id: true}
	frontier := []uint64{id}
	for len(frontier) > 0 {
		kids, err := client.Category.Query().Where(category.ParentIDIn(frontier...)).IDs(ctx)
		if err != nil {
			return nil, err
		}
		next := make([]uint64, 0, len(kids))
		for _, k := range kids {
			if !out[k] {
				out[k] = true
				next = append(next, k)
			}
		}
		frontier = next
	}
	return out, nil
}

// deleteCategory removes the entire tenant-owned subtree and its live products.
// Orders, deliveries and financial records are retained; failure rolls back all.
func (r *ProductRepoImpl) deleteCategory(ctx context.Context, id uint64) error {
	client := data.Client(ctx, r.data)
	tenant := tenancy.FromContext(ctx).SubsiteID
	if err := lockCategoryStructure(ctx, r.data); err != nil {
		return err
	}
	q := client.Category.Query().Where(category.SubsiteID(tenant)).Order(ent.Asc(category.FieldID))
	if r.data.Dialect != db.SQLite {
		q.ForUpdate()
	}
	rows, err := q.All(ctx)
	if err != nil {
		return err
	}
	children := map[uint64][]uint64{}
	found := false
	for _, row := range rows {
		if row.ID == id {
			found = true
		}
		children[row.ParentID] = append(children[row.ParentID], row.ID)
	}
	if !found {
		return fmt.Errorf("catalog.CATEGORY_NOT_FOUND")
	}
	ids := []uint64{id}
	seen := map[uint64]bool{id: true}
	for i := 0; i < len(ids); i++ {
		for _, child := range children[ids[i]] {
			if !seen[child] {
				seen[child] = true
				ids = append(ids, child)
			}
		}
	}
	// Chunk IN predicates for SQLite parameter limits; hold locks until commit.
	for start := 0; start < len(ids); start += 200 {
		end := start + 200
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		pq := client.Product.Query().Where(product.SubsiteID(tenant), product.CategoryIDIn(chunk...), product.StatusGTE(0)).Order(ent.Asc(product.FieldID))
		if r.data.Dialect != db.SQLite {
			pq.ForUpdate()
		}
		products, err := pq.All(ctx)
		if err != nil {
			return err
		}
		for _, p := range products {
			if err = r.archiveProduct(ctx, p.ID); err != nil {
				return err
			}
		}
		if err = client.Product.Update().Where(product.CategoryIDIn(chunk...), product.SubsiteID(tenant), product.StatusLT(0)).ClearCategoryID().Exec(ctx); err != nil {
			return err
		}
		if _, err = client.SupplierProductPrice.Delete().Where(supplierproductprice.CategoryIDIn(chunk...), supplierproductprice.Scope("category")).Exec(ctx); err != nil {
			return err
		}
		if err = client.SupplyMapping.Update().Where(supplymapping.LocalCategoryIDIn(chunk...)).ClearLocalCategoryID().Exec(ctx); err != nil {
			return err
		}
		if _, err = client.CategoryProductPlacement.Delete().Where(placement.CategoryIDIn(chunk...), placement.SubsiteID(tenant)).Exec(ctx); err != nil {
			return err
		}
	}
	for start := 0; start < len(ids); start += 200 {
		end := start + 200
		if end > len(ids) {
			end = len(ids)
		}
		if _, err = client.Category.Delete().Where(category.IDIn(ids[start:end]...), category.SubsiteID(tenant)).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ReorderCategories 分類排序（拖拽重排）：把 parent_id 層級下全部兄弟按 ids 順序
// 重排，sort 歸一化為 0..n-1；跨層級移動時一併改父級。事務原子提交。
func (r *ProductRepoImpl) ReorderCategories(ctx context.Context, parentID uint64, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		if err := lockCategoryStructure(ctx, r.data); err != nil {
			return err
		}
		client := data.Client(ctx, r.data)
		tenant := tenancy.FromContext(ctx).SubsiteID
		if parentID > 0 {
			if _, err := client.Category.Query().Where(category.ID(parentID), category.SubsiteID(tenant)).Only(ctx); err != nil {
				return err
			}
		}
		for i, id := range ids {
			row, err := client.Category.Query().Where(category.ID(id), category.SubsiteID(tenant)).Only(ctx)
			if err != nil {
				return err
			}
			if row.ParentID != parentID {
				if err = r.checkCategoryTree(ctx, id, parentID); err != nil {
					return err
				}
				if err = r.guardCategoryProducts(ctx, []uint64{id}); err != nil {
					return err
				}
			}
			upd := client.Category.UpdateOneID(id).SetSort(int32(i))
			if parentID > 0 {
				upd.SetParentID(parentID)
			} else {
				upd.ClearParentID()
			}
			if err = upd.Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ProductRepoImpl) checkCategoryTree(ctx context.Context, id, parentID uint64) error {
	if id == parentID {
		return fmt.Errorf("catalog.CATEGORY_CYCLE")
	}
	if parentID == 0 {
		return nil
	}
	row, err := data.Client(ctx, r.data).Category.Get(ctx, parentID)
	if ent.IsNotFound(err) {
		return fmt.Errorf("catalog.PARENT_NOT_FOUND")
	}
	if err != nil {
		return err
	}
	return r.checkCategoryTree(ctx, id, row.ParentID)
}

// ── 標籤 ─────────────────────────────────────────────────────

// ListTags 標籤列表。
func (r *ProductRepoImpl) ListTags(ctx context.Context) ([]*ent.Tag, error) {
	return data.Client(ctx, r.data).Tag.Query().Order(ent.Asc(tag.FieldID)).All(ctx)
}

// CreateTag 創建標籤。
func (r *ProductRepoImpl) CreateTag(ctx context.Context, name, slug, icon, color, position string) (*ent.Tag, error) {
	return data.Client(ctx, r.data).Tag.Create().
		SetName(name).
		SetSlug(slug).
		SetIcon(icon).
		SetColor(color).
		SetPosition(tag.Position(position)).
		Save(ctx)
}

// DeleteTag 刪除標籤。
func (r *ProductRepoImpl) DeleteTag(ctx context.Context, id uint64) error {
	n, err := data.Client(ctx, r.data).Tag.Delete().Where(tag.ID(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("catalog.TAG_NOT_FOUND")
	}
	return nil
}

// ── 管理面 DTO 轉換 ──────────────────────────────────────────

// ToAdminPB 轉 admin 協議對象。
func ToAdminPB(p *ent.Product) *adminv1.AdminProduct {
	out := &adminv1.AdminProduct{
		ProductProperty: p.ProductProperty, SalesVisible: &p.SalesVisible, TrackInventory: &p.TrackInventory,
		ProductKind: p.ProductKind, GoodsType: p.GoodsType, ShippingMode: p.ShippingMode, ShippingFeeCents: p.ShippingFee, ShippingCountries: p.ShippingCountries, PhysicalStock: p.PhysicalStock,
		FulfillmentMode: p.FulfillmentMode, ManualStock: &p.ManualStock,
		Id: p.ID, CategoryId: p.CategoryID, Name: p.Name, Slug: p.Slug,
		Description: p.Description, Cover: p.Cover, Images: p.Images,
		CoverProtected: p.CoverProtected, DescriptionProtected: p.DescriptionProtected,
		PriceCents: p.Price, FactoryPriceCents: p.FactoryPrice,
		StockType: string(p.StockType), StockVisible: proto.Bool(p.StockVisible),
		DeliveryMode: string(p.DeliveryMode), Dedup: p.Dedup,
		Sort: p.Sort, Status: int32(p.Status),
		UpstreamSourceId: p.UpstreamSourceID, UpstreamProductCode: p.UpstreamProductCode,
		PointsRequired:   p.PointsRequired,
		HasDirectContent: len(p.DirectContent) > 0,
		IsRecommend:      p.IsRecommend,
		AutoListing:      p.AutoListing, ListingReason: p.ListingReason, ListingMessage: p.ListingMessage, ListingRestocked: p.ListingRestocked, ListingObservedAt: p.ListingObservedAt / 1000,
		IsLocked: p.IsLocked, LockVersion: p.LockVersion, LockedBy: p.LockedBy, LockedAt: productLockedAt(p),
	}
	if !p.CreatedAt.IsZero() {
		out.CreatedAt = p.CreatedAt.Unix()
	}
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = p.UpdatedAt.Unix()
	}
	return out
}

func nilOrZero(v uint64) *uint64 {
	if v == 0 {
		return nil
	}
	return &v
}

// UpsertUpstreamProduct 貨源同步商品 upsert（，supply 模塊經 port 消費）。
// 判據：subsite_id + upstream_source_id + upstream_product_code 冪等。
// Price=-1 保持現有價（價格保護由 supply 側決策後傳入）。
func (r *ProductRepoImpl) UpsertUpstreamProduct(ctx context.Context, in port.UpstreamProductInput) (id uint64, created bool, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		id, created, e = r.upsertUpstreamProduct(ctx, in)
		if e == nil && in.ProductKind == "sms_channel" {
			// Modern channel catalogs supersede the source's legacy offer rows.
			// Preserve IDs for paid orders and in-flight intents. Ordinary goods
			// and operator-locked products are outside this conversion.
			e = data.Client(ctx, r.data).Product.Update().Where(product.SubsiteID(tenancy.FromContext(ctx).SubsiteID), product.UpstreamSourceID(in.ConnectionID), product.DeliveryKind("sms_activation"), product.ProductKindNEQ("sms_channel"), product.StatusGTE(0), product.IsLocked(false)).SetStatus(-1).SetAutoListing(false).AddLockVersion(1).Exec(ctx)
		}
		return e
	})
	return
}

func (r *ProductRepoImpl) upsertUpstreamProduct(ctx context.Context, in port.UpstreamProductInput) (uint64, bool, error) {
	if in.ProductKind == "" {
		in.ProductKind = "standard"
	}
	if in.ProductKind != "standard" && in.ProductKind != "sms_channel" && in.ProductKind != "social_service" {
		return 0, false, fmt.Errorf("商品類型無效")
	}
	if in.ProductKind == "sms_channel" && (in.DeliveryKind != "sms_activation" || len(in.SKUs) > 0) {
		return 0, false, fmt.Errorf("渠道商品類型無效")
	}
	if in.DeliveryKind == "" {
		in.DeliveryKind = "card"
	}
	if in.DeliveryKind != "card" && in.DeliveryKind != "sms_activation" && in.DeliveryKind != "social_service" {
		return 0, false, fmt.Errorf("不支持的上游交付類型")
	}
	if in.DeliveryKind == "sms_activation" && len(in.SKUs) > 0 {
		return 0, false, fmt.Errorf("接碼商品不支持規格")
	}
	if in.ProductKind == "social_service" && in.DeliveryKind != "social_service" {
		return 0, false, fmt.Errorf("社交服務交付類型不匹配")
	}
	if in.DeliveryKind == "social_service" {
		if in.ProductKind != "social_service" || len(in.SocialSchema) == 0 || len(in.SKUs) != 0 {
			return 0, false, fmt.Errorf("社交服務購買規則缺失或商品類型不匹配")
		}
		in.Price, in.FactoryPrice = 0, 0
	}
	tc := tenancy.FromContext(ctx)
	existing, err := data.Client(ctx, r.data).Product.Query().
		Where(
			product.SubsiteID(tc.SubsiteID),
			product.UpstreamSourceID(in.ConnectionID),
			product.UpstreamProductCode(in.UpstreamProductCode),
		).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return 0, false, err
	}

	if err == nil {
		if !existing.IsLocked && existing.Status < 0 {
			// Archived records retain their manual re-import path.
		} else if _, err = data.GuardProductWrite(ctx, r.data, existing.ID); err != nil {
			return 0, false, err
		}
		locked := data.Client(ctx, r.data).Product.Query().Where(product.ID(existing.ID))
		if r.data.Dialect != db.SQLite {
			locked.ForUpdate()
		}
		existing, err = locked.Only(ctx)
		if err != nil {
			return 0, false, err
		}
	}
	in.Description = sanitize.RichHTML(in.Description)
	if ent.IsNotFound(err) {
		// 新建：slug 用上游標識（穩定、冪等）；狀態按 auto_onshelf 開關。
		// 候選 slug（base、base-2 … base-21）一次 IN 查詢判重——替代逐個
		// Exist 最多 100 次點查（大數量同步的熱點）
		slug := slugify(in.UpstreamProductCode)
		if slug == "" {
			slug = fmt.Sprintf("up-%d-%s", in.ConnectionID, in.UpstreamProductCode)
		}
		baseSlug := slug
		candidates := make([]string, 0, 20)
		candidates = append(candidates, baseSlug)
		for i := 2; i <= 21; i++ {
			candidates = append(candidates, fmt.Sprintf("%s-%d", baseSlug, i))
		}
		takenRows, err2 := data.Client(ctx, r.data).Product.Query().
			Where(product.SubsiteID(tc.SubsiteID), product.SlugIn(candidates...)).
			Select(product.FieldSlug).
			Strings(ctx)
		if err2 != nil {
			return 0, false, err2
		}
		taken := make(map[string]bool, len(takenRows))
		for _, s := range takenRows {
			taken[s] = true
		}
		slug = ""
		for _, c := range candidates {
			if !taken[c] {
				slug = c
				break
			}
		}
		if slug == "" {
			// 前 21 個候選全撞（極端）：從 22 起逐個 Exist 兜底
			slug = fmt.Sprintf("%s-%d", baseSlug, 22)
			for i := 22; i < 100; i++ {
				exists, err3 := data.Client(ctx, r.data).Product.Query().
					Where(product.SubsiteID(tc.SubsiteID), product.Slug(slug)).Exist(ctx)
				if err3 != nil {
					return 0, false, err3
				}
				if !exists {
					break
				}
				slug = fmt.Sprintf("%s-%d", baseSlug, i+1)
			}
		}
		// 上游不可售（手動發貨/預選/停售，in.Status=0）恆下架；
		// 在售且允許自動上架才 1（待定價導入 AutoOnshelf=false → 0）
		status := in.Status
		if in.Status == 1 && !in.AutoOnshelf {
			status = 0
		}
		price := in.Price
		if price < 0 {
			price = 0 // 待定價的新商品沒有舊價，不能將“不改價”哨兵存為售價。
		}
		create := data.Client(ctx, r.data).Product.Create().
			SetSubsiteID(tc.SubsiteID).
			SetName(in.Name).
			SetSlug(slug).
			SetPrice(price).
			SetFactoryPrice(max(in.FactoryPrice, 0)).
			SetProductKind(in.ProductKind).SetDeliveryKind(in.DeliveryKind).SetSmsProduct(in.SMSProduct).SetSocialSchema(in.SocialSchema).
			SetStockType(product.StockTypeCard).
			SetStatus(status).
			SetUpstreamSourceID(in.ConnectionID).
			SetUpstreamProductCode(in.UpstreamProductCode).
			SetUpstreamSyncedAt(in.UpstreamSyncedAt).SetCategoryProtected(in.CategoryProtected)
		if in.CategoryID > 0 {
			create.SetCategoryID(in.CategoryID)
		}
		if in.Description != "" {
			create.SetDescription(in.Description)
		}
		if in.Cover != "" {
			create.SetCover(in.Cover)
		}
		created, err := create.Save(ctx)
		if err != nil {
			return 0, false, err
		}
		if err := r.syncUpstreamSkus(ctx, tc.SubsiteID, created.ID, in.SKUs); err != nil {
			return 0, false, err
		}
		if err := data.SyncProductMediaRefs(ctx, r.data, nil, created); err != nil {
			return 0, false, err
		}
		return created.ID, true, nil
	}

	if existing.Status < 0 {
		if !in.ReimportDeleted {
			return 0, false, fmt.Errorf("商品已在本地刪除，自動同步不會恢復；如需重新導入，請在導入上游商品中勾選該商品")
		}
		// 只釋放上游唯一標識；舊商品及其訂單、卡密、SKU 繼續保留歸檔。
		// 釋放與新建必須同一事務，失敗時仍由歸檔記錄阻止自動同步復活。
		var id uint64
		var created bool
		err := data.Tx(ctx, r.data, func(ctx context.Context) error {
			if err := data.Client(ctx, r.data).Product.UpdateOneID(existing.ID).
				Where(product.StatusLT(0), product.UpstreamSourceID(in.ConnectionID), product.UpstreamProductCode(in.UpstreamProductCode)).
				ClearUpstreamProductCode().Exec(ctx); err != nil {
				return err
			}
			in.ReimportDeleted = false
			var err error
			id, created, err = r.UpsertUpstreamProduct(ctx, in)
			return err
		})
		return id, created, err
	}

	// 更新：名稱/描述/封面/分類/狀態/成本價；價格按保護語義（-1 不動）
	if err := data.GuardUpstreamDelivery(ctx, data.Client(ctx, r.data), existing); err != nil {
		return 0, false, err
	}
	upd := data.Client(ctx, r.data).Product.UpdateOneID(existing.ID).Where(product.StatusGTE(0)).
		SetName(in.Name).
		SetUpstreamSyncedAt(in.UpstreamSyncedAt)
	if existing.ProductKind != in.ProductKind {
		return 0, false, fmt.Errorf("商品類型不能變化")
	}
	if existing.DeliveryKind != in.DeliveryKind {
		return 0, false, fmt.Errorf("上游交付類型變化，請新建商品映射")
	}
	upd.SetSmsProduct(in.SMSProduct).SetSocialSchema(in.SocialSchema)
	if in.DeliveryKind == "social_service" && !sameSocialSchema(existing.SocialSchema, in.SocialSchema) {
		upd.AddLockVersion(1)
	}
	if in.FactoryPrice >= 0 {
		upd.SetFactoryPrice(in.FactoryPrice)
	}
	if in.Price >= 0 {
		upd.SetPrice(in.Price)
	}
	// 兩個調用方（collect 同步/交互導入）恆發送顯式狀態：鏡像上游可售性
	if !existing.AutoListing {
		if existing.Status == 2 && in.Status == 1 {
			upd.SetStatus(2)
		} else {
			upd.SetStatus(in.Status)
		}
		if in.Status == 0 && existing.Status > 0 {
			upd.SetListingReason("upstream_unavailable").SetListingRestoreStatus(existing.Status)
		}
	}
	if !existing.CategoryProtected || in.CategoryProtected {
		if in.CategoryID > 0 {
			upd.SetCategoryID(in.CategoryID)
		} else if in.CategorySet {
			upd.ClearCategoryID()
		}
		if in.CategoryProtected {
			upd.SetCategoryProtected(true)
		}
	}
	if !existing.DescriptionProtected && (in.DescriptionSet || in.Description != "") {
		upd.SetDescription(in.Description)
	}
	// cover 恆設（空 = 清空：上游下架/刪圖時鏡像清空，同時調用方已刪本地文件）
	if !existing.CoverProtected {
		upd.SetCover(in.Cover)
	}
	updated, err := upd.Save(ctx)
	if err != nil {
		return 0, false, err
	}
	if err := r.syncUpstreamSkus(ctx, tc.SubsiteID, updated.ID, in.SKUs); err != nil {
		return 0, false, err
	}
	if err := data.SyncProductMediaRefs(ctx, r.data, existing, updated); err != nil {
		return 0, false, err
	}
	return updated.ID, false, nil
}

// syncUpstreamSkus 上游規格組合差量同步到 product_skus（按上游 SKU 標識匹配）：
// 新集合有 → 更新價格/規格/上游標識；沒有 → 刪除（僅刪本同步來源的，
// upstream_sku_id 非空的記錄——本地手建 SKU 不受影響）。nil 入參 = 不動。
// 價格 -1 = 價格保護語義：已有 SKU 跳過改價，新增組合不創建。
func (r *ProductRepoImpl) syncUpstreamSkus(ctx context.Context, subsiteID, productID uint64, in []port.UpstreamSKUInput) error {
	if in == nil {
		return nil
	}
	client := data.Client(ctx, r.data)
	existing, err := client.ProductSku.Query().
		Where(productsku.ProductID(productID)).
		All(ctx)
	if err != nil {
		return err
	}
	type wanted struct {
		id, name string
		price    int64
		spec     map[string]string
	}
	want := make(map[string]wanted, len(in))
	for _, s := range in {
		name := s.Name
		if name == "" {
			name = s.Code
		}
		want[s.Code] = wanted{id: s.Code, name: name, price: s.PriceCents, spec: s.SpecValues}
	}
	for _, e := range existing {
		if e.UpstreamSkuID == "" {
			continue
		}
		w, ok := want[e.UpstreamSkuID]
		if !ok {
			// 上游已無此組合：僅刪同步來源的 SKU（本地手建 upstream_sku_id 為空）
			if e.UpstreamSkuID != "" {
				if err := client.ProductSku.DeleteOneID(e.ID).Exec(ctx); err != nil {
					return err
				}
			}
			continue
		}
		delete(want, e.UpstreamSkuID)
		upd := client.ProductSku.UpdateOneID(e.ID).
			SetName(w.name).
			SetSpecValues(w.spec).
			SetUpstreamSkuID(w.id)
		if w.price >= 0 { // -1 = 價格保護：SKU 保持現價，僅刷新規格與上游標識
			upd = upd.SetPrice(w.price)
		}
		if err := upd.Exec(ctx); err != nil {
			return err
		}
	}
	creates := make([]*ent.ProductSkuCreate, 0, len(want))
	for _, w := range want {
		if w.price < 0 {
			// 價格保護期上游新增的組合：無安全價格可寫，跳過創建，
			// 運營解除保護/強制改價後的下一輪同步補齊
			continue
		}
		creates = append(creates, client.ProductSku.Create().
			SetSubsiteID(subsiteID).
			SetProductID(productID).
			SetName(w.name).
			SetPrice(w.price).
			SetSpecValues(w.spec).
			SetUpstreamSkuID(w.id))
	}
	if len(creates) > 0 {
		return client.ProductSku.CreateBulk(creates...).Exec(ctx)
	}
	return nil
}

// ListSupplyCategories 供貨目錄分類（port.SupplierCatalog；主站一級分類）。
func (r *ProductRepoImpl) ListSupplyCategories(ctx context.Context) ([]port.SupplyCategory, error) {
	tc := tenancy.FromContext(ctx)
	rows, err := data.Client(ctx, r.data).Category.Query().
		Where(category.SubsiteID(tc.SubsiteID), category.Hide(false)).
		Order(ent.Asc(category.FieldSort), ent.Asc(category.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.SupplyCategory, 0, len(rows))
	for _, c := range rows {
		out = append(out, port.SupplyCategory{ID: c.ID, Name: c.Name})
	}
	return out, nil
}

// UpdateUpstreamPrice 僅更新價格（port.UpstreamProductMaintainer；price scope 輕量路徑）。
func (r *ProductRepoImpl) UpdateUpstreamPrice(ctx context.Context, connectionID uint64, productCode string, priceCents int64, skus ...port.UpstreamSKUInput) (bool, error) {
	tc := tenancy.FromContext(ctx)
	found := false
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		client := data.Client(ctx, r.data)
		p, err := client.Product.Query().Where(product.SubsiteID(tc.SubsiteID), product.UpstreamSourceID(connectionID), product.UpstreamProductCode(productCode), product.StatusGTE(0)).Only(ctx)
		if ent.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		p, err = data.GuardProductWrite(ctx, r.data, p.ID)
		if err != nil {
			return err
		}
		if err := data.GuardUpstreamDelivery(ctx, client, p); err != nil {
			return err
		}
		update := client.Product.UpdateOneID(p.ID).SetUpstreamSyncedAt(time.Now().UTC())
		if priceCents >= 0 {
			update.SetPrice(priceCents)
		}
		if err := update.Exec(ctx); err != nil {
			return err
		}
		for _, sk := range skus {
			if sk.Code == "" || sk.PriceCents < 0 {
				continue
			}
			if err := client.ProductSku.Update().Where(productsku.ProductID(p.ID), productsku.SubsiteID(tc.SubsiteID), productsku.UpstreamSkuID(sk.Code)).SetPrice(sk.PriceCents).Exec(ctx); err != nil {
				return err
			}
		}
		found = true
		return nil
	})
	return found, err
}

// UpdateUpstreamStatus 僅更新上下架狀態（port.UpstreamProductMaintainer；status scope 輕量路徑）。
func (r *ProductRepoImpl) UpdateUpstreamStatus(ctx context.Context, connectionID uint64, productCode string, status int8) (bool, error) {
	changed := false
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		c := data.Client(ctx, r.data)
		p, e := c.Product.Query().Where(product.SubsiteID(tenancy.FromContext(ctx).SubsiteID), product.UpstreamSourceID(connectionID), product.UpstreamProductCode(productCode), product.StatusGTE(0)).Only(ctx)
		if ent.IsNotFound(e) {
			return nil
		}
		if e != nil {
			return e
		}
		p, e = data.GuardProductWrite(ctx, r.data, p.ID)
		if data.IsProductLocked(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if local, e := data.HasLocalDelivery(ctx, c, p); e != nil {
			return e
		} else if local {
			return nil
		}
		upd := c.Product.UpdateOneID(p.ID).SetStatus(status).SetUpstreamSyncedAt(time.Now().UTC())
		if status == 0 && p.Status > 0 {
			upd.SetListingReason("upstream_unavailable").SetListingRestoreStatus(p.Status).SetListingMessage("上游停售或商品已不存在，需人工核實")
		}
		if e := upd.Exec(ctx); e != nil {
			return e
		}
		changed = true
		return nil
	})
	return changed, err
}

// ShelveOffMissing 刪除對賬：連接下未見商品批量下架（port.UpstreamProductMaintainer）。
// 只動 status!=0 的（已下架不重複計數）；seen 為空切片時全量下架（引擎側護欄
// 保證僅在權威快照完整時調用）。
func (r *ProductRepoImpl) ShelveOffMissing(ctx context.Context, connectionID uint64, seen []string) (int64, error) {
	tc := tenancy.FromContext(ctx)
	q := data.Client(ctx, r.data).Product.Query().
		Where(
			product.SubsiteID(tc.SubsiteID),
			product.UpstreamSourceID(connectionID),
			product.StatusGT(0), product.IsLocked(false), product.DeliveryKind("card"),
		)
	if len(seen) > 0 {
		q = q.Where(product.UpstreamProductCodeNotIn(seen...))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, p := range rows {
		changed, e := r.UpdateUpstreamStatus(ctx, connectionID, p.UpstreamProductCode, 0)
		if e != nil {
			return count, e
		}
		if changed {
			count++
		}
	}
	return count, nil
}

// deleteProductCover 刪除本地採集封面文件（/uploads/ 路徑；其餘忽略）。
func deleteProductCover(cover string) {
	if !strings.HasPrefix(cover, "/uploads/") {
		return
	}
	rel := strings.TrimPrefix(cover, "/uploads/")
	rel = strings.ReplaceAll(rel, "\\", "/")
	_ = mediamods.DeleteLocal(rel)
}

// ListForSupply 供貨目錄分頁（ supplier 消費；管理面語義含下架）。
func (r *ProductRepoImpl) ListForSupply(ctx context.Context, f port.AdminFilter) ([]port.SupplierProduct, int64, error) {
	hidden, err := data.HiddenCategoryIDs(ctx, data.Client(ctx, r.data), tenancy.FromContext(ctx).SubsiteID)
	if err != nil {
		return nil, 0, err
	}
	c := data.Client(ctx, r.data)
	controls, e := c.ProductControl.Query().All(ctx)
	if e != nil {
		return nil, 0, e
	}
	var blocked []uint64
	for _, v := range controls {
		blocked = append(blocked, v.ProductID)
	}
	// Legacy supply protocols identify only a product, never a real SKU.
	// Do not advertise a total that their default-pool orders cannot consume.
	q := data.Client(ctx, r.data).Product.Query().Where(product.SubsiteID(tenancy.FromContext(ctx).SubsiteID), product.DeliveryKind("card"), product.GoodsTypeNEQ("physical"), product.StatusGTE(0), data.VisibleProductCategory(hidden)).Where(product.FulfillmentModeNotIn("manual", "local", "reuse"), product.IDNotIn(blocked...), product.Not(product.HasSkusWith(productsku.SubsiteID(tenancy.FromContext(ctx).SubsiteID))))
	if f.Status >= 0 {
		q = q.Where(product.Status(int8(f.Status)))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(ent.Desc(product.FieldID)).
		Offset((int(f.Page) - 1) * int(f.PageSize)).Limit(int(f.PageSize)).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]port.SupplierProduct, 0, len(rows))
	for _, row := range rows {
		out = append(out, toSupplierProduct(row))
	}
	return out, int64(total), nil
}

// GetForSupply 供貨單品（含下架）。
func (r *ProductRepoImpl) GetForSupply(ctx context.Context, productID uint64) (*port.SupplierProduct, error) {
	row, err := data.Client(ctx, r.data).Product.Query().Where(product.ID(productID), product.SubsiteID(tenancy.FromContext(ctx).SubsiteID), product.StatusGTE(0)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.DeliveryKind != "card" {
		return nil, fmt.Errorf("接碼商品不支持二級供貨")
	}
	if hasSKU, e := data.Client(ctx, r.data).ProductSku.Query().Where(productsku.ProductID(row.ID), productsku.SubsiteID(row.SubsiteID)).Exist(ctx); e != nil {
		return nil, e
	} else if hasSKU {
		return nil, fmt.Errorf("多規格商品不支持舊版供貨接口")
	}
	if yes, e := data.HasLocalDelivery(ctx, data.Client(ctx, r.data), row); e != nil {
		return nil, e
	} else if yes {
		return nil, fmt.Errorf("該商品發貨方式不支持舊版供貨接口")
	}
	hidden, err := data.HiddenCategoryIDs(ctx, data.Client(ctx, r.data), row.SubsiteID)
	if err != nil {
		return nil, err
	}
	if data.CategoryHidden(hidden, row.CategoryID) {
		return nil, ErrProductNotFound
	}
	if err := data.RejectServiceProduct(ctx, data.Client(ctx, r.data), row); err != nil {
		return nil, err
	}
	p := toSupplierProduct(row)
	return &p, nil
}

func toSupplierProduct(row *ent.Product) port.SupplierProduct {
	name, description := sanitize.SMSPublicContent(row.ProductKind, row.Name, row.Description)
	return port.SupplierProduct{
		ID:           row.ID,
		Name:         name,
		Price:        row.Price,
		FactoryPrice: row.FactoryPrice,
		CategoryID:   row.CategoryID,
		Description:  description,
		Cover:        row.Cover,
		Status:       row.Status,
	}
}

func (r *ProductRepoImpl) CreateProduct(ctx context.Context, in port.ProductInput) (out *ent.Product, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.createProduct(ctx, in)
		if e != nil {
			return e
		}
		return data.SyncProductMediaRefs(ctx, r.data, nil, out)
	})
	return
}
func (r *ProductRepoImpl) UpdateProduct(ctx context.Context, id uint64, in port.ProductInput) (out *ent.Product, err error) {
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {
		if _, e := data.GuardProductWrite(ctx, r.data, id); e != nil {
			return e
		}
		old, e := data.Client(ctx, r.data).Product.Get(ctx, id)
		if e != nil {
			return e
		}
		out, e = r.updateProduct(ctx, id, in)
		if e != nil {
			return e
		}
		return data.SyncProductMediaRefs(ctx, r.data, old, out)
	})
	return
}
func (r *ProductRepoImpl) DeleteProduct(ctx context.Context, id uint64) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error { return r.archiveProduct(ctx, id) })
}

func (r *ProductRepoImpl) UpdateCategory(ctx context.Context, id uint64, name string, icon *string, hide *bool, sort *int32, parentID *int64) (out *ent.Category, err error) {
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {
		if e := lockCategoryStructure(ctx, r.data); e != nil {
			return e
		}
		if parentID != nil {
			if e := r.guardCategoryProducts(ctx, []uint64{id}); e != nil {
				return e
			}
		}
		var e error
		out, e = r.updateCategory(ctx, id, name, icon, hide, sort, parentID)
		return e
	})
	return
}
func (r *ProductRepoImpl) DeleteCategory(ctx context.Context, id uint64) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error { return r.deleteCategory(ctx, id) })
}
func (r *ProductRepoImpl) guardCategoryProducts(ctx context.Context, ids []uint64) error {
	c := data.Client(ctx, r.data)
	all := []uint64{}
	for _, id := range ids {
		desc, e := descendantCategoryIDs(ctx, c, id)
		if e != nil {
			return e
		}
		for child := range desc {
			all = append(all, child)
		}
	}
	products, e := c.Product.Query().Where(product.SubsiteID(tenancy.FromContext(ctx).SubsiteID), product.CategoryIDIn(all...), product.StatusGTE(0)).Order(ent.Asc(product.FieldID)).IDs(ctx)
	if e != nil {
		return e
	}
	for _, id := range products {
		if _, e = data.GuardProductWrite(ctx, r.data, id); e != nil {
			return e
		}
	}
	return nil
}
