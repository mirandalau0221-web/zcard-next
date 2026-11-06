package order

// 購物車（ 銷賬）：登錄態 CRUD + 商品快照聯查。
// 結算複用 CreateOrder（前端組裝 items/coupon/control_answers）——本文件無下單邏輯。
// 語義：同 (user, product, sku) 合併數量（唯一索引 upsert）；下架/隱藏商品 valid=false 打標。

import (
	"context"
	"encoding/json"
	"errors"
	couponport "github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"
	settingsport "github.com/NovaWorks/zcard-next/server/internal/mods/settings/port"
	kerrors "github.com/go-kratos/kratos/v3/errors"

	"entgo.io/ent/dialect/sql"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/cartitem"
	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	invport "github.com/NovaWorks/zcard-next/server/internal/mods/inventory/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"

	"google.golang.org/protobuf/types/known/emptypb"
)

// StoreCartService 購物車服務。
type StoreCartService struct {
	storefrontv1.UnimplementedStoreCartServiceServer
	settings settingsport.Provider
	flash    couponport.FlashResolver
	data     *data.Data
	pricing  catalogport.PricingResolver
	inv      invport.Inventory // 庫存快照（Stock 單查；批量留優化）
}

// NewStoreCartService 構造（wire；Pricing/Inventory 複用 order 依賴注入鏈）。
func NewStoreCartService(d *data.Data, pricing catalogport.PricingResolver, inv invport.Inventory, settings settingsport.Provider, flash couponport.FlashResolver) *StoreCartService {
	return &StoreCartService{data: d, pricing: pricing, inv: inv, settings: settings, flash: flash}
}

func mustUserClaims(ctx context.Context) (uint64, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return 0, kerrors.Unauthorized("identity.UNAUTHORIZED", "登錄狀態已失效，請重新登錄")
	}
	return claims.Subject, nil
}

// AddCartItem 加購（合併語義；商品須存在，價格/庫存快照在列表聯查）。
func (s *StoreCartService) AddCartItem(ctx context.Context, req *storefrontv1.AddCartItemRequest) (*storefrontv1.CartItem, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	userID, err := mustUserClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetQuantity() <= 0 || req.GetQuantity() > 99 {
		return nil, errors.New("cart.QUANTITY_INVALID: 1-99")
	}
	client := data.Client(ctx, s.data)
	// 商品存在性（上架校驗留給列表打標——允許下架商品留在車內可見）
	p, err := client.Product.Get(ctx, req.GetProductId())
	if err != nil {
		return nil, errors.New("cart.PRODUCT_NOT_FOUND")
	}
	if p.DeliveryKind != "card" {
		return nil, errors.New("接碼商品須獨立單件購買，不能加入購物車")
	}
	hidden, err := data.HiddenCategoryIDs(ctx, client, p.SubsiteID)
	if err != nil {
		return nil, err
	}
	if data.CategoryHidden(hidden, p.CategoryID) {
		return nil, errors.New("cart.PRODUCT_NOT_AVAILABLE: 商品已下架")
	}
	sku, err := data.DeliverySKU(ctx, client, p, req.GetSkuId())
	if err != nil {
		return nil, errors.New("cart.SKU_INVALID")
	}
	reuse := data.StockSource(p, sku) == "reuse"
	if reuse && req.GetQuantity() != 1 {
		return nil, errors.New("該規格每次只需購買一份")
	}
	// 合併：唯一索引 (user_id, product_id, sku_id) 衝突時數量累加（AddQuantity）
	if err := client.CartItem.Create().
		SetUserID(userID).
		SetProductID(req.GetProductId()).
		SetSkuID(req.GetSkuId()).
		SetQuantity(req.GetQuantity()).
		OnConflict(
			sql.ConflictColumns(cartitem.FieldUserID, cartitem.FieldProductID, cartitem.FieldSkuID),
		).Update(func(u *ent.CartItemUpsert) {
		if reuse {
			u.SetQuantity(1)
		} else {
			u.AddQuantity(req.GetQuantity())
		}
	}).Exec(ctx); err != nil {
		return nil, err
	}
	// 回讀合併後的行（upsert 無 returning）
	row, err := s.myItem(ctx, userID, req.GetProductId(), req.GetSkuId())
	if err != nil {
		return nil, err
	}
	return s.toItemPB(ctx, row)
}

// ListCart 我的購物車（快照聯查 + 失效打標）。
func (s *StoreCartService) ListCart(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.ListCartReply, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	userID, err := mustUserClaims(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := data.Client(ctx, s.data).CartItem.Query().
		Where(cartitem.UserID(userID)).
		Order(ent.Desc(cartitem.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	reply := &storefrontv1.ListCartReply{}
	for _, row := range rows {
		item, err := s.toItemPB(ctx, row)
		if err != nil {
			return nil, err
		}
		reply.Items = append(reply.Items, item)
		if item.GetValid() {
			reply.Total++
		}
	}
	return reply, nil
}

// UpdateCartItem 改量（0=刪除；屬主校驗）。
func (s *StoreCartService) UpdateCartItem(ctx context.Context, req *storefrontv1.UpdateCartItemRequest) (*storefrontv1.CartItem, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	userID, err := mustUserClaims(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.myItemByID(ctx, userID, req.GetId())
	if err != nil {
		return nil, errors.New("cart.ITEM_NOT_FOUND")
	}
	if req.GetQuantity() < 0 || req.GetQuantity() > 99 {
		return nil, errors.New("cart.QUANTITY_INVALID: 0-99")
	}
	client := data.Client(ctx, s.data)
	if req.GetQuantity() == 0 {
		if err := client.CartItem.DeleteOneID(row.ID).Exec(ctx); err != nil {
			return nil, err
		}
		return &storefrontv1.CartItem{Id: row.ID}, nil
	}
	p, err := client.Product.Get(ctx, row.ProductID)
	if err != nil {
		return nil, err
	}
	sku, err := data.DeliverySKU(ctx, client, p, row.SkuID)
	if err != nil {
		return nil, err
	}
	if data.StockSource(p, sku) == "reuse" && req.GetQuantity() != 1 {
		return nil, errors.New("該規格每次只需購買一份")
	}
	if err := client.CartItem.UpdateOne(row).SetQuantity(req.GetQuantity()).Exec(ctx); err != nil {
		return nil, err
	}
	row.Quantity = req.GetQuantity()
	return s.toItemPB(ctx, row)
}

// RemoveCartItem 移除（屬主校驗）。
func (s *StoreCartService) RemoveCartItem(ctx context.Context, req *storefrontv1.RemoveCartItemRequest) (*emptypb.Empty, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	userID, err := mustUserClaims(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.myItemByID(ctx, userID, req.GetId())
	if err != nil {
		return nil, errors.New("cart.ITEM_NOT_FOUND")
	}
	if err := data.Client(ctx, s.data).CartItem.DeleteOne(row).Exec(ctx); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ── 內部 ────────────────────────────────────────────────────────

func (s *StoreCartService) myItem(ctx context.Context, userID, productID, skuID uint64) (*ent.CartItem, error) {
	return data.Client(ctx, s.data).CartItem.Query().
		Where(
			cartitem.UserID(userID),
			cartitem.ProductID(productID),
			cartitem.SkuID(skuID),
		).Only(ctx)
}

func (s *StoreCartService) myItemByID(ctx context.Context, userID, id uint64) (*ent.CartItem, error) {
	return data.Client(ctx, s.data).CartItem.Query().
		Where(cartitem.UserID(userID), cartitem.ID(id)).Only(ctx)
}

// toItemPB 行 → PB（聯查商品快照；商品缺失/下架 → valid=false）。
func (s *StoreCartService) toItemPB(ctx context.Context, row *ent.CartItem) (*storefrontv1.CartItem, error) {
	client := data.Client(ctx, s.data)
	p, err := client.Product.Get(ctx, row.ProductID)
	item := &storefrontv1.CartItem{
		Id: row.ID, ProductId: row.ProductID, SkuId: row.SkuID,
		Quantity: row.Quantity,
		AddedAt:  row.CreatedAt.Unix(),
	}
	if ent.IsNotFound(err) {
		item.Valid = false // 商品已刪
		return item, nil
	}
	if err != nil {
		return nil, err
	}
	item.ProductName = p.Name
	item.ProductCover = p.Cover
	item.PointsRequired = p.PointsRequired
	item.PointsOnly = p.PointsRequired > 0
	if p.DeliveryKind != "card" {
		return nil, errors.New("接碼商品須獨立單件購買，不能加入購物車")
	}
	hidden, err := data.HiddenCategoryIDs(ctx, client, p.SubsiteID)
	if err != nil {
		return nil, err
	}
	item.Valid = p.Status == 1 && !data.CategoryHidden(hidden, p.CategoryID) // 上架 on_sale（隱藏/下架打標失效） // 上架才可選（隱藏/下架打標）
	// 現價（SKU 解析；失敗回落商品價）
	var price money.Cents = money.Cents(p.Price)
	if row.SkuID > 0 && s.pricing != nil {
		if sp, err := s.pricing.ResolvePrice(ctx, row.ProductID, row.SkuID); err == nil {
			price = sp
		}
	}
	item.PriceCents = int64(price)
	if s.flash != nil {
		f, err := s.flash.Active(ctx, row.ProductID, row.SkuID)
		if err != nil {
			return nil, err
		}
		if f != nil {
			item.FlashSale = &storefrontv1.FlashOffer{PriceCents: int64(f.FlashPrice), EndAt: f.EndAt.Unix(), Remaining: f.Remaining, PerUserLimit: f.PerUserLimit}
		}
	}
	// 庫存（inventory port 單查；失敗降級 0——寧顯無貨不顯假庫存）
	sku, skuErr := data.DeliverySKU(ctx, client, p, row.SkuID)
	if skuErr != nil {
		item.Valid = false
		item.Stock = 0
		return item, nil
	}
	if data.StockSource(p, sku) == "reuse" {
		item.MaxQuantity = 1
	}
	if data.StockSource(p, sku) != "upstream" {
		stock, e := data.LocalSKUStock(ctx, s.data, p, sku)
		if e != nil {
			return nil, e
		}
		item.Stock = stock
	} else if p.UpstreamSourceID > 0 {
		item.Stock = -2
		if stocks, err := data.ProductStocks(ctx, s.data, []*ent.Product{p}); err == nil {
			item.Stock = stocks[p.ID]
		}
	} else if s.inv != nil {
		if stock, err := s.inv.Stock(ctx, row.ProductID, row.SkuID); err == nil {
			item.Stock = stock
		}
	} else {
		item.Stock = -1 // 鏈接/兑換碼類不限
	}
	return item, nil
}

// 運行時讀取開關；關閉只阻止操作，不刪除已有購物車。
func (s *StoreCartService) requireEnabled(ctx context.Context) error {
	if s.settings == nil {
		return kerrors.ServiceUnavailable("cart.CONFIG_UNAVAILABLE", "購物車配置暫時無法讀取")
	}
	raw, err := s.settings.GetDefault(ctx, "trade", "cart_enabled", json.RawMessage(`true`))
	if err != nil {
		return kerrors.ServiceUnavailable("cart.CONFIG_UNAVAILABLE", "購物車配置暫時無法讀取")
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return kerrors.ServiceUnavailable("cart.CONFIG_INVALID", "購物車配置無效")
	}
	if !enabled {
		return kerrors.Forbidden("cart.DISABLED", "購物車已關閉，請在商品詳情頁直接購買")
	}
	return nil
}
