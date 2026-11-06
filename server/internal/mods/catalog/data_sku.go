package catalog

// SKU 多規格數據層（M1b）：admin CRUD + 前台列表 + 訂單取價（SKU 價 > 商品價）。
// ent import 收口：data 前綴文件（架構測試規則 3b）。

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/lotteryprize"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productdeliverysource"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productsku"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
)

// SkuInput SKU 創建/更新輸入（price_cents=0 表示繼承商品價）。
type SkuInput struct {
	PhysicalStock                     *int64
	ExpectedPhysicalStock             *int64
	FulfillmentMode                   string
	ProductID                         uint64
	Name                              string
	SpecValues                        map[string]string
	PriceCents                        int64
	CostCents                         int64
	StockOffset                       int32
	UpstreamSkuID                     string
	SetPrice, SetCost, SetStockOffset bool
}

// ── admin CRUD ───────────────────────────────────────────────

// ListProductSkus 管理面 SKU 列表。
func (r *ProductRepoImpl) ListProductSkus(ctx context.Context, productID uint64) ([]*ent.ProductSku, error) {
	return data.Client(ctx, r.data).ProductSku.Query().
		Where(productsku.ProductID(productID)).
		Order(ent.Asc(productsku.FieldID)).
		All(ctx)
}

// CreateSku 創建 SKU。
func (r *ProductRepoImpl) createSku(ctx context.Context, in SkuInput) (*ent.ProductSku, error) {
	parent, e := data.Client(ctx, r.data).Product.Get(ctx, in.ProductID)
	if e != nil {
		return nil, e
	}
	if parent.ProductKind == "sms_channel" || parent.DeliveryKind == "social_service" {
		return nil, fmt.Errorf("渠道接碼不生成 SKU，請在商品頁面選擇選項")
	}
	if parent.GoodsType == "physical" {
		in.StockOffset = 0
		in.UpstreamSkuID = ""
		if !parent.TrackInventory {
			in.PhysicalStock = nil
		}
		c := data.Client(ctx, r.data)
		hasSKU, err := c.ProductSku.Query().Where(productsku.ProductID(parent.ID)).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !hasSKU {
			used, err := c.OrderItem.Query().Where(orderitem.ProductID(parent.ID)).Exist(ctx)
			if err != nil {
				return nil, err
			}
			if used {
				return nil, errors.Conflict("catalog.SKU_BASE_ORDERS", "該實體商品已有無規格訂單，請複製為新商品後添加規格")
			}
			if parent.PhysicalStock != 0 {
				if !parent.TrackInventory {
					return nil, errors.Conflict("catalog.SKU_BASE_STOCK", "請先開啓庫存管理，將商品級可售庫存設為 0，再添加規格")
				}
				return nil, errors.Conflict("catalog.SKU_BASE_STOCK", "請先將商品級可售庫存設為 0，再添加規格並分配庫存")
			}
		}

		in.FulfillmentMode = "follow"
		if in.PhysicalStock != nil && (*in.PhysicalStock < 0 || *in.PhysicalStock > 100000000) {
			return nil, errors.BadRequest("catalog.SKU_INVALID", "規格庫存須在 0 至 100000000 之間")
		}
	}
	if parent.FulfillmentMode == "reuse" || parent.FulfillmentMode == "local" {
		return nil, errors.Conflict("catalog.SKU_DELIVERY_CONFLICT", "默認規格已配置本地發貨，請先切換髮貨來源，再新增規格")
	}
	if in.FulfillmentMode == "local" || in.FulfillmentMode == "reuse" {
		return nil, errors.BadRequest("catalog.SKU_INVALID", "請先保存規格，再在發貨設置中配置內容來源")
	}
	if err := validateServiceConfig(in.FulfillmentMode, nil, true); err != nil {
		return nil, errors.BadRequest("catalog.SKU_INVALID", err.Error())
	}
	if in.FulfillmentMode == "manual" {
		p, err := data.Client(ctx, r.data).Product.Get(ctx, in.ProductID)
		if err != nil {
			return nil, err
		}
		if p.UpstreamSourceID > 0 {
			return nil, errors.BadRequest("catalog.SKU_INVALID", "上游規格不能改為本地人工交付")
		}
	}
	tc := tenancy.FromContext(ctx)
	if in.SpecValues == nil {
		in.SpecValues = map[string]string{}
	}
	create := data.Client(ctx, r.data).ProductSku.Create().
		SetSubsiteID(tc.SubsiteID).
		SetProductID(in.ProductID).
		SetName(in.Name).
		SetSpecValues(in.SpecValues).
		SetStockOffset(in.StockOffset)
	create.SetNillablePhysicalStock(in.PhysicalStock)
	if in.FulfillmentMode != "" {
		create.SetFulfillmentMode(in.FulfillmentMode)
	}
	if in.PriceCents > 0 {
		create.SetPrice(in.PriceCents)
	}
	if in.CostCents > 0 {
		create.SetCost(in.CostCents)
	}
	if in.UpstreamSkuID != "" {
		create.SetUpstreamSkuID(in.UpstreamSkuID)
	}
	row, err := create.Save(ctx)
	if sqlgraph.IsUniqueConstraintError(err) {
		return nil, errors.Conflict("catalog.SKU_NAME_DUPLICATE", "該商品已有同名規格，請修改規格名")
	}
	return row, err
}

// UpdateSku applies explicitly supplied zero values (price 0 inherits product price).
func (r *ProductRepoImpl) updateSku(ctx context.Context, id uint64, in SkuInput) (*ent.ProductSku, error) {
	current, e := data.Client(ctx, r.data).ProductSku.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	parent, e := data.Client(ctx, r.data).Product.Get(ctx, current.ProductID)
	if e != nil {
		return nil, e
	}
	if parent.GoodsType == "physical" {
		in.StockOffset = 0
		in.SetStockOffset = true
		in.UpstreamSkuID = ""
		if !parent.TrackInventory {
			in.PhysicalStock = nil
			in.ExpectedPhysicalStock = nil
		}
		if in.FulfillmentMode != "" && in.FulfillmentMode != "follow" && in.FulfillmentMode != "auto" {
			return nil, fmt.Errorf("實體規格僅支持快遞配送")
		}
		in.FulfillmentMode = "follow"
	}
	if in.FulfillmentMode != "" && in.FulfillmentMode != current.FulfillmentMode && (in.FulfillmentMode == "local" || in.FulfillmentMode == "reuse" || current.FulfillmentMode == "local" || current.FulfillmentMode == "reuse") {
		return nil, fmt.Errorf("請在發貨設置中更改規格的內容來源")
	}
	if in.UpstreamSkuID != "" && in.UpstreamSkuID != current.UpstreamSkuID && (current.FulfillmentMode == "local" || current.FulfillmentMode == "reuse") {
		return nil, fmt.Errorf("本地發貨規格不能更換上游綁定")
	}
	if err := validateServiceConfig(in.FulfillmentMode, nil, true); err != nil {
		return nil, err
	}
	if in.FulfillmentMode == "manual" {
		sku, err := data.Client(ctx, r.data).ProductSku.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		p, err := data.Client(ctx, r.data).Product.Get(ctx, sku.ProductID)
		if err != nil {
			return nil, err
		}
		if p.UpstreamSourceID > 0 {
			return nil, fmt.Errorf("上游規格不能改為本地人工交付")
		}
	}
	q := data.Client(ctx, r.data).ProductSku.UpdateOneID(id)
	if parent.GoodsType == "physical" {
		q.ClearUpstreamSkuID()
	}
	if in.FulfillmentMode != "" {
		q.SetFulfillmentMode(in.FulfillmentMode)
	}
	if in.Name != "" {
		q.SetName(in.Name)
	}
	if in.PhysicalStock != nil {
		if in.ExpectedPhysicalStock == nil || *in.ExpectedPhysicalStock != current.PhysicalStock {
			return nil, fmt.Errorf("規格庫存已變化，請刷新後調整")
		}
		p, e := data.Client(ctx, r.data).Product.Get(ctx, current.ProductID)
		if e != nil {
			return nil, e
		}
		if p.GoodsType != "physical" {
			return nil, fmt.Errorf("非實體商品不支持實物庫存")
		}
		if e = data.AdjustPhysicalStock(ctx, r.data, p, id, *in.PhysicalStock); e != nil {
			return nil, e
		}
	}
	if in.SpecValues != nil {
		q.SetSpecValues(in.SpecValues)
	}
	if in.SetPrice || in.PriceCents > 0 {
		q.SetPrice(in.PriceCents)
	}
	if in.SetCost || in.CostCents > 0 {
		q.SetCost(in.CostCents)
	}
	if in.SetStockOffset || in.StockOffset != 0 {
		q.SetStockOffset(in.StockOffset)
	}
	if in.UpstreamSkuID != "" {
		q.SetUpstreamSkuID(in.UpstreamSkuID)
	}
	if err := q.Exec(ctx); err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, errors.Conflict("catalog.SKU_NAME_DUPLICATE", "該商品已有同名規格，請修改規格名")
		}
		return nil, err
	}
	return data.Client(ctx, r.data).ProductSku.Get(ctx, id)
}

// DeleteSku 刪除 SKU。
func (r *ProductRepoImpl) deleteSku(ctx context.Context, id uint64) error {
	c := data.Client(ctx, r.data)
	query := c.ProductSku.Query().Where(productsku.ID(id))
	if r.data.Dialect.Capabilities().SupportsForUpdate {
		query = query.ForUpdate()
	}
	sk, e := query.Only(ctx)
	if e != nil {
		return e
	}
	p, e := c.Product.Get(ctx, sk.ProductID)
	if e != nil {
		return e
	}
	if p.GoodsType == "physical" && sk.PhysicalStock != 0 {
		return fmt.Errorf("規格仍有可售庫存，請先調整庫存為零再刪除")
	}
	// Use current locking reads after the product lock. A plain Exists can see a
	// snapshot from before waiting for a concurrent importer under MySQL RR.
	cards := c.Card.Query().Where(card.ProductID(p.ID), card.SubsiteID(p.SubsiteID), card.SkuID(id), card.StatusIn(card.StatusAvailable, card.StatusReserved, card.StatusDisabled)).Limit(1)
	orders := c.OrderItem.Query().Where(orderitem.ProductID(p.ID), orderitem.SubsiteID(p.SubsiteID), orderitem.SkuID(id)).Limit(1)
	prizes := c.LotteryPrize.Query().Where(lotteryprize.ProductID(p.ID), lotteryprize.SubsiteID(p.SubsiteID), lotteryprize.SkuID(id), lotteryprize.Enabled(true)).Limit(1)
	sources := c.ProductDeliverySource.Query().Where(productdeliverysource.ProductID(p.ID), productdeliverysource.SubsiteID(p.SubsiteID), productdeliverysource.SkuID(id), productdeliverysource.Or(productdeliverysource.CurrentKeyNotNil(), productdeliverysource.StatusIn("submitting", "polling", "uncertain"))).Limit(1)
	if r.data.Dialect.Capabilities().SupportsForUpdate {
		cards, orders, prizes, sources = cards.ForUpdate(), orders.ForUpdate(), prizes.ForUpdate(), sources.ForUpdate()
	}
	if rows, err := cards.All(ctx); err != nil {
		return err
	} else if len(rows) > 0 {
		return fmt.Errorf("規格仍有未售或預佔卡密，不能刪除")
	}
	if rows, err := orders.All(ctx); err != nil {
		return err
	} else if len(rows) > 0 {
		return fmt.Errorf("規格已有訂單引用，不能刪除")
	}
	if rows, err := prizes.All(ctx); err != nil {
		return err
	} else if len(rows) > 0 {
		return fmt.Errorf("該規格用於抽獎獎品，請先更換或移除獎品來源")
	}
	if rows, err := sources.All(ctx); err != nil {
		return err
	} else if len(rows) > 0 {
		return fmt.Errorf("規格仍有發貨來源記錄，不能刪除")
	}

	n, err := data.Client(ctx, r.data).ProductSku.Delete().Where(productsku.ID(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("catalog.SKU_NOT_FOUND")
	}
	return nil
}

// ── 前台/訂單取價 ────────────────────────────────────────────

// ListSkus 前台 SKU 列表（只下發 id/名稱/價格）。
func (r *ProductRepoImpl) ListSkus(ctx context.Context, productID uint64) ([]port.Sku, error) {
	rows, err := data.Client(ctx, r.data).ProductSku.Query().
		Where(productsku.ProductID(productID)).
		Order(ent.Asc(productsku.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	p, err := data.Client(ctx, r.data).Product.Get(ctx, productID)
	if err != nil {
		return nil, err
	}
	out := make([]port.Sku, 0, len(rows))
	for _, s := range rows {
		mode := data.FulfillmentMode(p, s)
		stock := int64(-2)
		switch mode {
		case "shipping":
			stock = s.PhysicalStock
			if !p.TrackInventory {
				stock = -1
			}
		case "reuse":
			stock, err = data.LocalSKUStock(ctx, r.data, p, s)
		case "manual":
			stock, err = data.ManualAvailable(ctx, data.Client(ctx, r.data), p)
		case "auto":
			if p.StockType != "card" {
				stock = -1
			} else {
				n, e := data.Client(ctx, r.data).Card.Query().Where(card.ProductID(p.ID), card.SkuID(s.ID), card.StatusEQ(card.StatusAvailable)).Count(ctx)
				stock = int64(n)
				err = e
			}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, port.Sku{Stock: stock,
			FulfillmentMode: s.FulfillmentMode, ID: s.ID, Name: s.Name, Price: money.Cents(s.Price), ProductID: s.ProductID,
		})
	}
	return out, nil
}

// ResolvePrice validates the SKU belongs to this product; zero price inherits the base price.
func (r *ProductRepoImpl) ResolvePrice(ctx context.Context, productID, skuID uint64) (money.Cents, error) {
	client := data.Client(ctx, r.data)
	if skuID > 0 {
		sku, err := client.ProductSku.Query().Where(productsku.ID(skuID), productsku.ProductID(productID)).Only(ctx)
		if err != nil {
			return 0, fmt.Errorf("catalog.SKU_NOT_FOUND: 規格不存在或不屬於該商品: %w", err)
		}
		if sku.Price > 0 {
			return money.Cents(sku.Price), nil
		}
	} else {
		exists, err := client.ProductSku.Query().Where(productsku.ProductID(productID)).Exist(ctx)
		if err != nil {
			return 0, err
		}
		if exists {
			return 0, fmt.Errorf("catalog.SKU_REQUIRED: 請選擇商品規格")
		}
	}
	p, err := client.Product.Get(ctx, productID)
	if err != nil {
		return 0, err
	}
	return money.Cents(p.Price), nil
}

var _ port.PricingResolver = (*ProductRepoImpl)(nil)

func validateServiceConfig(mode string, stock *int64, sku bool) error {
	if mode != "" && mode != "auto" && mode != "manual" && mode != "local" && mode != "reuse" && !(sku && mode == "follow") {
		return fmt.Errorf("交付方式無效")
	}
	if stock != nil && *stock < -1 {
		return fmt.Errorf("人工可售總量須為-1或非負整數")
	}
	return nil
}

func (r *ProductRepoImpl) CreateSku(ctx context.Context, in SkuInput) (out *ent.ProductSku, err error) {
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {

		if _, e := data.GuardProductWrite(ctx, r.data, in.ProductID); e != nil {
			return e
		}

		var inner error
		out, inner = r.createSku(ctx, in)
		if inner != nil {
			return inner
		}
		return data.Client(ctx, r.data).Product.UpdateOneID(in.ProductID).AddLockVersion(1).Exec(ctx)
	})
	return
}

func (r *ProductRepoImpl) UpdateSku(ctx context.Context, id uint64, in SkuInput) (out *ent.ProductSku, err error) {
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {
		row, e := data.Client(ctx, r.data).ProductSku.Get(ctx, id)
		if e != nil {
			return e
		}
		if _, e := data.GuardProductWrite(ctx, r.data, row.ProductID); e != nil {
			return e
		}

		var inner error
		out, inner = r.updateSku(ctx, id, in)
		if inner != nil {
			return inner
		}
		return data.Client(ctx, r.data).Product.UpdateOneID(row.ProductID).AddLockVersion(1).Exec(ctx)
	})
	return
}

func (r *ProductRepoImpl) DeleteSku(ctx context.Context, id uint64) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		row, e := data.Client(ctx, r.data).ProductSku.Get(ctx, id)
		if e != nil {
			return e
		}
		if _, e := data.GuardProductWrite(ctx, r.data, row.ProductID); e != nil {
			return e
		}

		if e := r.deleteSku(ctx, id); e != nil {
			return e
		}
		return data.Client(ctx, r.data).Product.UpdateOneID(row.ProductID).AddLockVersion(1).Exec(ctx)
	})
}
