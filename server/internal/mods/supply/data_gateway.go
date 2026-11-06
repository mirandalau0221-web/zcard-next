package supply

// 採購網關實現（ 消費方端口）：連接憑據解密 → 適配器裝配 → 提交/查詢/退款。
// 庫存查詢失敗保留錯誤，避免付款前將未知庫存當作充足。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"time"

	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	dashboardport "github.com/NovaWorks/zcard-next/server/internal/mods/dashboard/port"
	orderport "github.com/NovaWorks/zcard-next/server/internal/mods/order/port"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply/adapter"
	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
)

// ErrCooldownActive 渠道熔斷冷卻中（可重試口徑：procurement 退避輪詢自然吸收）。
var ErrCooldownActive = errors.New("supply: rate limit cooldown active")

// Gateway supply 採購網關（實現 supplyport.UpstreamGateway 與 orderport.UpstreamStockGate）。
// S2：出站共享自適應節奏器——熔斷冷卻中直接拒絕（可重試口徑），
// 限流信號反饋降速、成功反饋回升（採購請求量小，主要防持續撞牆）。
type Gateway struct {
	repo   *SupplyRepoImpl
	pacer  *Pacer                    // nil = 不節流（測試）
	reader catalogport.ProductReader // 商品/SKU 上游映射（CheckOrderItems 預檢用）
}

// NewGateway 構造。
func NewGateway(repo *SupplyRepoImpl, pacer *Pacer, reader catalogport.ProductReader) *Gateway {
	return &Gateway{repo: repo, pacer: pacer, reader: reader}
}

// FailStrategyOf 渠道級失敗策略（port 實現；讀連接 settings.failure_action）。
func (g *Gateway) FailStrategyOf(ctx context.Context, connectionID uint64) string {
	conn, err := g.repo.GetConnection(ctx, connectionID)
	if err != nil {
		return "auto_refund"
	}
	if v, _ := conn.Settings["failure_action"].(string); v == "manual" {
		return "manual"
	}
	return "auto_refund"
}

// gate 出站前置：熔斷冷卻判定（冷卻中返回可重試錯誤——procurement 側退避輪詢）。
func (g *Gateway) gate(ctx context.Context, connectionID uint64) (*ent.SupplyConnection, error) {
	conn, err := g.repo.GetConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	if g.pacer != nil && g.pacer.CooldownActive(conn) {
		return nil, fmt.Errorf("supply: 渠道熔斷冷卻中（上游限流），採購稍後自動重試: %w", ErrCooldownActive)
	}
	return conn, nil
}

// feedback 出站結果反饋節奏器（err 分類：限流降速 / 成功回升）。
func (g *Gateway) feedback(ctx context.Context, conn *ent.SupplyConnection, err error) {
	if g.pacer == nil {
		return
	}
	if err != nil && errors.Is(err, adapter.ErrRateLimited) {
		g.pacer.OnRateLimited(ctx, conn, err.Error())
		return
	}
	if err == nil {
		g.pacer.OnSuccess(ctx, conn)
	}
}

// adapterFor 按連接構造適配器（憑據解密失敗 → 明確錯誤）。
func (g *Gateway) adapterFor(ctx context.Context, connectionID uint64) (adapter.Adapter, error) {
	conn, err := g.repo.GetConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	credsJSON, err := g.repo.OpenCredentials(conn)
	if err != nil {
		return nil, err
	}
	var creds adapter.Credentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return nil, errors.New("supply: 憑據結構不合法（請重新配置連接）")
	}
	return adapter.New(conn.Driver, conn.BaseURL, creds, parseRetryIntervals(conn.RetryIntervals))
}

// ListOrders 上游訂單列表（ 對賬數據源；dashboard port 消費——類型轉換收口在此）。
// 協議不支持 → ErrUpstreamListUnsupported（對賬 job failed 可查）。
func (g *Gateway) ListOrders(ctx context.Context, connectionID uint64, start, end time.Time) ([]dashboardport.UpstreamOrder, error) {
	a, err := g.adapterFor(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	lister, ok := a.(adapter.OrderLister)
	if !ok {
		return nil, dashboardport.ErrUpstreamListUnsupported
	}
	rows, err := lister.ListOrders(ctx, start, end)
	if errors.Is(err, adapter.ErrNotSupported) {
		return nil, dashboardport.ErrUpstreamListUnsupported
	}
	if err != nil {
		return nil, err
	}
	out := make([]dashboardport.UpstreamOrder, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboardport.UpstreamOrder{
			UpstreamOrderID: r.UpstreamOrderID, Amount: r.Amount, Status: r.Status,
		})
	}
	return out, nil
}

// Submit 提交採購。永久錯誤歸一化（哨兵 → procurement 狀態機判 rejected）。
func (g *Gateway) Submit(ctx context.Context, req supplyport.PurchaseRequest) (*supplyport.PurchaseResult, error) {
	conn, err := g.gate(ctx, req.ConnectionID)
	if err != nil {
		return nil, err
	}
	a, err := g.adapterFor(ctx, req.ConnectionID)
	if err != nil {
		return nil, err
	}
	res, err := a.CreateOrder(ctx, adapter.CreateOrderReq{
		ProductCode:       req.ProductCode,
		UpstreamSKU:       req.UpstreamSKU,
		Quantity:          req.Quantity,
		DownstreamOrderNo: req.DownstreamOrderNo,
		TraceID:           req.TraceID,
		CallbackURL:       g.submitCallbackURL(ctx, req.ConnectionID, req.CallbackURL),
	})
	g.feedback(ctx, conn, err)
	if err != nil {
		switch {
		case errors.Is(err, adapter.ErrProductDeleted):
			return nil, supplyport.ErrUpstreamDeleted
		case errors.Is(err, adapter.ErrProductUnavailable):
			return nil, supplyport.ErrUpstreamUnavailable
		case errors.Is(err, adapter.ErrInsufficientBalance):
			return nil, supplyport.ErrUpstreamBalance
		case errors.Is(err, adapter.ErrNoStock):
			return nil, supplyport.ErrUpstreamNoStock
		case errors.Is(err, adapter.ErrDuplicateSubmit):
			return nil, supplyport.ErrUpstreamDuplicate
		}
		return nil, err
	}
	return &supplyport.PurchaseResult{
		UpstreamOrderID: res.UpstreamOrderID,
		Status:          res.Status,
		Amount:          res.Amount,
		Cards:           res.Cards,
	}, nil
}

// Query 查詢上游訂單。
func (g *Gateway) Query(ctx context.Context, connectionID uint64, upstreamOrderID string) (*supplyport.PurchaseOrderInfo, error) {
	conn, err := g.gate(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	a, err := g.adapterFor(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	detail, err := a.GetOrder(ctx, upstreamOrderID)
	g.feedback(ctx, conn, err)
	if err != nil {
		return nil, err
	}
	return &supplyport.PurchaseOrderInfo{
		Status: detail.Status,
		Amount: detail.Amount,
		Cards:  detail.Cards,
	}, nil
}

// CheckStock 實時庫存：-1 僅表示上游明確不限；錯誤不可偽裝成不限。
// skuCode 為上游規格標識（acg=race|k=v 編碼 / dujiao=sku_id），可空走商品級口徑。
func (g *Gateway) CheckStock(ctx context.Context, connectionID uint64, productCode, skuCode string) (int32, error) {
	started := time.Now().UTC()
	a, err := g.adapterFor(ctx, connectionID)
	if err != nil {
		return 0, err
	}
	stock, err := a.GetStock(ctx, productCode, skuCode)
	if skuCode == "" {
		g.cacheStockAt(ctx, connectionID, productCode, stock, err, started)
	} else {
		observed := stock
		if err != nil {
			observed = -2
		}
		cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_ = g.repo.recordSKUStock(cacheCtx, connectionID, productCode, skuCode, observed, started)
		cancel()
	}
	if err != nil {
		return 0, err
	}
	return stock, nil
}

// CheckOrderItems 下單前上游代發項實時庫存預檢（實現 orderport.UpstreamStockGate 接口，
// order 創建訂單消費）：把上游缺貨或查詢失敗的單擋在付款前。
// 本地商品由本地鎖卡校驗；對接商品查庫存失敗拒單，明確不限(-1)可下單。
func (g *Gateway) CheckItems(ctx context.Context, subsiteID uint64, items []orderport.UpstreamStockItem) error {
	filtered := make([]orderport.UpstreamStockItem, 0, len(items))
	for _, it := range items {
		p, e := data.ProductForDelivery(ctx, data.Client(ctx, g.repo.data), subsiteID, it.ProductID)
		if e != nil {
			return e
		}
		if p.ProductKind == "sms_channel" {
			continue
		}
		sku, e := data.DeliverySKU(ctx, data.Client(ctx, g.repo.data), p, it.SkuID)
		if e != nil {
			return e
		}
		switch data.StockSource(p, sku) {
		case "local", "manual":
			continue
		case "reuse":
			src, e := data.CurrentDeliverySource(ctx, data.Client(ctx, g.repo.data), p, it.SkuID)
			if e != nil || !data.SourceAdmissible(src) {
				return fmt.Errorf("該規格複用內容暫不可售")
			}
			if src.Status == "ready" || src.Status == "submitting" || src.Status == "polling" {
				continue
			}
		}
		filtered = append(filtered, it)
	}
	return checkOrderItems(ctx, g.reader, func(connectionID uint64, productCode, skuCode string) (int32, error) {
		return g.CheckStock(ctx, connectionID, productCode, skuCode)
	}, subsiteID, filtered)
}

// checkOrderItems 預檢純邏輯（reader/stock 可注入——單測覆蓋決策矩陣）。
func checkOrderItems(ctx context.Context, reader catalogport.ProductReader,
	stock func(connectionID uint64, productCode, skuCode string) (int32, error),
	subsiteID uint64, items []orderport.UpstreamStockItem) error {
	if reader == nil {
		return nil
	}
	for _, it := range items {
		p, err := reader.Get(ctx, subsiteID, it.ProductID)
		if err != nil {
			continue // 商品不可讀：交由訂單主流程判定（PRODUCT_NOT_FOUND）
		}
		if p.UpstreamSourceID == 0 {
			continue // 非上游項：本地卡密走事務內鎖卡，直髮無庫存概念
		}
		if p.UpstreamProductCode == "" {
			return fmt.Errorf("supply.STOCK_UNAVAILABLE: 商品「%s」缺少上游商品標識", p.Name)
		}
		skuCode := ""
		if it.SkuID > 0 {
			skuCode = reader.SkuUpstreamCode(ctx, subsiteID, it.SkuID)
			if skuCode == "" {
				return fmt.Errorf("supply.STOCK_UNAVAILABLE: 商品「%s」缺少上游規格標識", p.Name)
			}
		}
		n, err := stock(p.UpstreamSourceID, p.UpstreamProductCode, skuCode)
		if err != nil || n < -1 {
			return fmt.Errorf("supply.STOCK_UNAVAILABLE: 商品「%s」暫時無法確認庫存，請稍後重試", p.Name)
		}
		if n == -1 {
			continue // 上游明確返回無限庫存。
		}

		if n < int32(it.Quantity) {
			return fmt.Errorf("supply: 商品「%s」上游庫存不足（餘 %d 需 %d）: %w", p.Name, n, it.Quantity, supplyport.ErrUpstreamNoStock)
		}
	}
	return nil
}

// Refund 向上遊傳導退款（acg_faka 等不支持 → ErrRefundNotSupported）。
func (g *Gateway) Refund(ctx context.Context, connectionID uint64, upstreamOrderID string) error {
	a, err := g.adapterFor(ctx, connectionID)
	if err != nil {
		return err
	}
	err = a.RefundOrder(ctx, upstreamOrderID)
	if errors.Is(err, adapter.ErrNotSupported) {
		return supplyport.ErrRefundNotSupported
	}
	return err
}

// submitCallbackURL 回調地址決策：請求顯式指定 > 連接配置 callback_url（運營在
// 渠道管理裏登記的本站公網回調地址； E 提交時自動攜帶給上游）。
func (g *Gateway) submitCallbackURL(ctx context.Context, connectionID uint64, explicit string) string {
	if explicit != "" {
		return explicit
	}
	conn, err := g.repo.GetConnection(ctx, connectionID)
	if err != nil {
		return ""
	}
	return conn.CallbackURL
}
