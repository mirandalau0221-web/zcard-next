package supply

import (
	"context"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
)

// PriceSocialRetail prices the complete authenticated wholesale quote. The
// product's zero display price is never a unit-price baseline for a service.
func (g *Gateway) PriceSocialRetail(ctx context.Context, connectionID, productID uint64, cost int64) (int64, string, error) {
	conn, err := g.gate(ctx, connectionID)
	if err != nil {
		return 0, "", err
	}
	if conn.Driver != "zcard" || string(conn.Status) != "active" || conn.ExchangeRate != 1 || cost <= 0 {
		return 0, "", fmt.Errorf("社交服務貨源或報價無效")
	}
	p, err := g.repo.entClient(ctx).Product.Get(ctx, productID)
	if err != nil || p.UpstreamSourceID != connectionID || p.DeliveryKind != supplyport.SocialDelivery {
		return 0, "", fmt.Errorf("社交服務商品映射已變化")
	}
	m, err := g.repo.GetMapping(ctx, connectionID, p.UpstreamProductCode, "")
	if err != nil || m.LocalProductID != productID {
		return 0, "", fmt.Errorf("社交服務映射不存在")
	}
	rule, err := readProductRule(m.PricingOverride)
	if err != nil {
		return 0, "", err
	}
	if rule == nil || rule.Mode == PriceModePending {
		return 0, "", fmt.Errorf("社交服務尚未配置定價規則")
	}
	if _, fixed := m.PricingOverride["price"]; fixed {
		return 0, "", fmt.Errorf("動態服務不能使用固定單件售價覆蓋")
	}
	amount := rule.price(conn, cost)
	if amount <= 0 || amount < cost {
		return 0, "", fmt.Errorf("社交服務總價無效或低於供貨報價")
	}
	return amount, data.SocialRetailPricingRevision(conn, m), nil
}

// OpenSocial opens only the ZCard protocol. Provider credentials and raw order
// identifiers remain on the wholesale server's separately configured connection.
func (g *Gateway) OpenSocial(ctx context.Context, connectionID uint64) (supplyport.SocialClient, error) {
	conn, err := g.gate(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	if conn.Driver != "zcard" || conn.ExchangeRate != 1 {
		return nil, fmt.Errorf("社交服務供貨須使用啓用的人民幣 ZCard 連接")
	}
	a, err := g.adapterFor(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	client, ok := a.(supplyport.SocialClient)
	if !ok {
		return nil, fmt.Errorf("貨源不支持社交服務協議")
	}
	return client, nil
}

func (g *Gateway) OpenSocialProvider(ctx context.Context, connectionID uint64) (supplyport.SocialProvider, error) {
	_, err := g.gate(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	a, err := g.adapterFor(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	provider, ok := a.(supplyport.SocialProvider)
	if !ok {
		return nil, fmt.Errorf("貨源不支持社交服務採購")
	}
	return provider, nil
}
