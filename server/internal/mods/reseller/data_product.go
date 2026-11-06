package reseller

// 分站自營商品上架鏈路（ 驗收旅程出單段）：
// 分站主自服務面創建商品（subsite_id = 本人 profile.ID）→ 分站域名下單
// → 管線步驟 7 接 ResolveUnitPrice（分站價）→ order.paid → SettleService 利潤入賬。
// 商品行數據隔離與主站同構：products.subsite_id 行級隔離（鐵律 14）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
)

// OwnProductInput 分站自營商品上架輸入（description 已 sanitize）。
type OwnProductInput struct {
	Name         string
	CategoryID   uint64
	Description  string
	Cover        string
	Price        int64 // 分（主站基礎價口徑；分站價由定價引擎疊加）
	FactoryPrice int64
	StockType    string // card | url | code
	DeliveryMode string
	StockVisible bool
	Sort         int32
	Status       int8 // 1=上架 0=下架 2=隱藏
}

// CreateOwnProduct 分站自營商品創建（subsite_id 顯式傳入——分站主自服務面，
// 不經租戶上下文；slug 在分站內唯一，分站間同 slug 不衝突）。
func (r *ResellerRepo) createOwnProduct(ctx context.Context, subsiteID uint64, in OwnProductInput) (*ent.Product, error) {
	client := data.Client(ctx, r.data)
	slug, err := r.ownProductSlug(ctx, subsiteID, in.Name)
	if err != nil {
		return nil, err
	}
	create := client.Product.Create().
		SetSubsiteID(subsiteID).
		SetName(in.Name).
		SetSlug(slug).
		SetPrice(in.Price).
		SetFactoryPrice(in.FactoryPrice)
	if in.StockType != "" {
		create.SetStockType(product.StockType(in.StockType))
	} else {
		create.SetStockType(product.StockTypeCard)
	}
	if in.DeliveryMode != "" {
		create.SetDeliveryMode(product.DeliveryMode(in.DeliveryMode))
	}
	create = create.SetStockVisible(in.StockVisible).SetSort(in.Sort).SetStatus(in.Status)
	if in.CategoryID > 0 {
		create.SetCategoryID(in.CategoryID)
	}
	if in.Description != "" {
		create.SetDescription(in.Description)
	}
	if in.Cover != "" {
		create.SetCover(in.Cover)
	}
	return create.Save(ctx)
}

// ownProductSlug 分站內唯一 slug（與 catalog 同構的 slugify 簡化版；
// 判據 products.subsite_id + slug——分站間同名商品不衝突）。
func (r *ResellerRepo) ownProductSlug(ctx context.Context, subsiteID uint64, name string) (string, error) {
	base := slugifyOwn(name)
	if base == "" {
		base = fmt.Sprintf("rs-%d", subsiteID)
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
	return "", fmt.Errorf("reseller.SLUG_EXHAUSTED")
}

func slugifyOwn(s string) string {
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
	return strings.ToLower(string(out))
}

func (r *ResellerRepo) CreateOwnProduct(ctx context.Context, subsiteID uint64, in OwnProductInput) (out *ent.Product, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.createOwnProduct(ctx, subsiteID, in)
		return e
	})
	return out, err
}
