package catalog

import (
	"context"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productsku"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/shipping"
)

func (r *ProductRepoImpl) validatePhysicalProduct(ctx context.Context, p *ent.Product, in *port.ProductInput) error {
	shippingChanged := in.GoodsType != nil || in.ProductProperty != nil || in.ShippingMode != nil
	kind := "virtual"
	if p != nil {
		kind = p.GoodsType
	}
	if in.GoodsType != nil {
		kind = *in.GoodsType
	}
	if in.ProductProperty != nil {
		if in.GoodsType != nil && *in.ProductProperty != *in.GoodsType {
			return fmt.Errorf("商品屬性與商品類型不一致")
		}
		kind = *in.ProductProperty
	}
	if kind != "physical" && kind != "virtual" {
		return fmt.Errorf("商品類型無效")
	}
	if p != nil && kind != p.GoodsType {
		return fmt.Errorf("已保存的商品不能改變虛擬或實體屬性，請新建商品")
	}
	in.GoodsType = &kind
	in.ProductProperty = &kind
	if kind != "physical" {
		if in.TrackInventory != nil && !*in.TrackInventory {
			return fmt.Errorf("僅實體商品支持關閉庫存管理")
		}
		return nil
	}
	if p != nil && (p.UpstreamSourceID > 0 || len(p.DirectContent) > 0 || p.FulfillmentMode == "local" || p.FulfillmentMode == "reuse") {
		return fmt.Errorf("該商品已有虛擬交付配置，請新建實體商品")
	}
	if len(in.DirectContent) > 0 || (p != nil && p.PointsRequired > 0 && !in.PointsRequiredSet) {
		return fmt.Errorf("實體商品不能配置虛擬內容或積分兌換")
	}
	if in.PointsRequired > 0 {
		return fmt.Errorf("實體商品暫不支持積分兌換")
	}
	if in.FulfillmentMode != "" && in.FulfillmentMode != "auto" && in.FulfillmentMode != "shipping" {
		return fmt.Errorf("實體商品只支持快遞發貨")
	}
	in.FulfillmentMode = "auto"
	in.StockType = "card"
	in.DeliveryMode = "status"
	in.Dedup = false
	in.ManualStock = nil
	track := p == nil || p.TrackInventory
	if in.TrackInventory != nil {
		track = *in.TrackInventory
	}
	if !track {
		in.PhysicalStock = nil
		in.ExpectedPhysicalStock = nil
	} else if p != nil && !p.TrackInventory {
		hasSKU, err := data.Client(ctx, r.data).ProductSku.Query().Where(productsku.ProductID(p.ID), productsku.SubsiteID(p.SubsiteID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !hasSKU && (in.PhysicalStock == nil || in.ExpectedPhysicalStock == nil) {
			return fmt.Errorf("重新開啓庫存管理前，請核對並填寫可售庫存")
		}
	}
	mode := shipping.ModeFree
	fee := int64(0)
	countries := in.ShippingCountries
	if p != nil {
		mode = p.ShippingMode
		fee = p.ShippingFee
		if !shippingChanged {
			countries = p.ShippingCountries
		}
	}
	if in.ShippingMode != nil {
		mode = *in.ShippingMode
	}
	if in.ShippingFee != nil {
		fee = *in.ShippingFee
	}
	if mode != shipping.ModeFree && mode != shipping.ModeFixed && mode != shipping.ModeCollect {
		return fmt.Errorf("運費方式無效")
	}
	if !money.ValidCents(fee) {
		return fmt.Errorf("運費金額無效")
	}
	if mode != shipping.ModeFixed {
		fee = 0
	}
	if mode == shipping.ModeFixed && fee == 0 {
		return fmt.Errorf("固定運費必須大於0；免運費請選擇包郵")
	}
	if len(countries) == 0 {
		return fmt.Errorf("請至少選擇一個可配送國家或地區")
	}
	seen := map[string]bool{}
	for _, c := range countries {
		if _, ok := shipping.Regions[c]; !ok || seen[c] {
			return fmt.Errorf("配送國家無效或重複")
		}
		seen[c] = true
	}
	if in.PhysicalStock != nil && (*in.PhysicalStock < 0 || *in.PhysicalStock > 100000000) {
		return fmt.Errorf("實物庫存無效")
	}
	in.ShippingMode = &mode
	in.ShippingFee = &fee
	in.ShippingCountries = countries
	return nil
}
