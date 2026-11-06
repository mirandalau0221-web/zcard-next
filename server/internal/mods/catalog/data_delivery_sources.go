package catalog

import (
	"context"
	"encoding/base64"
	"fmt"
	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productcontrol"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productsku"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/google/uuid"
	"strings"
	"time"
)

func (s *AdminCatalogService) GetDeliverySources(ctx context.Context, req *adminv1.DeliverySourcesRequest) (*adminv1.DeliverySourcesReply, error) {
	c := data.Client(ctx, s.repo.data)
	p, err := data.ProductForDelivery(ctx, c, tenancy.FromContext(ctx).SubsiteID, req.ProductId)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.DeliverySourcesReply{Revision: p.LockVersion}
	skus, err := c.ProductSku.Query().Where(productsku.ProductID(p.ID)).Order(ent.Asc(productsku.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	var slots []*ent.ProductSku
	if len(skus) == 0 {
		slots = append(slots, nil)
	} else {
		slots = skus
	}
	for _, sk := range slots {
		id := uint64(0)
		name := "默認規格"
		if sk != nil {
			id = sk.ID
			name = sk.Name
		}
		info := &adminv1.DeliverySourceInfo{SkuId: id, SkuName: name, Mode: data.StockSource(p, sk), Status: "unconfigured"}
		src, e := data.CurrentDeliverySource(ctx, c, p, id)
		if e != nil && !ent.IsNotFound(e) {
			return nil, e
		}
		if src != nil {
			info.Id = src.ID
			info.Status = src.Status
			info.HasContent = len(src.Content) > 0
			info.ExpiresAt = src.ExpiresAt
			info.MaxDeliveries = src.MaxDeliveries
			info.Error = src.LastError
			info.OriginProcurementId = src.OriginProcurementID
			info.CostCents = src.CostCents
			info.UpstreamOrderId = src.UpstreamOrderID
			info.Deliveries = src.DeliveredCount
			if (src.Status == "ready" || src.Status == "paused") && src.ExpiresAt > 0 && src.ExpiresAt <= time.Now().Unix() {
				info.Status = "expired"
			}
		}
		reply.Sources = append(reply.Sources, info)
	}
	// Only original successful upstream receipts of this exact product are eligible.
	items, err := c.OrderItem.Query().Where(orderitem.ProductID(p.ID), orderitem.SubsiteID(p.SubsiteID), orderitem.FulfillmentTypeEQ(orderitem.FulfillmentTypeUpstream), orderitem.HasOrderWith(order.StatusNotIn(order.StatusRefunded, order.StatusCanceled, order.StatusExpired, order.StatusRefundPending))).Order(ent.Desc(orderitem.FieldID)).Limit(100).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if len(it.FormAnswers) > 0 || it.FulfillmentStatus == "refunded" {
			continue
		}
		po, e := c.ProcurementOrder.Query().Where(procurementorder.OrderItemID(it.ID), procurementorder.StatusEQ(procurementorder.StatusFulfilled)).Only(ctx)
		if ent.IsNotFound(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		rows, e := c.ProcurementItem.Query().Where(procurementitem.ProcurementID(po.ID)).All(ctx)
		if e != nil {
			return nil, e
		}
		if len(rows) != 1 {
			continue
		}
		for i := range rows[0].ReceivedContent {
			reply.Receipts = append(reply.Receipts, &adminv1.DeliveryReceiptOption{ProcurementId: po.ID, ContentIndex: int32(i), SkuId: it.SkuID, PurchasedAt: po.CreatedAt.Unix(), Label: fmt.Sprintf("採購 #%d · 第 %d 份 · %s", po.ID, i+1, po.CreatedAt.Format("2006-01-02 15:04"))})
		}
	}
	return reply, nil
}
func (s *AdminCatalogService) SetDeliverySource(ctx context.Context, req *adminv1.SetDeliverySourceRequest) (*adminv1.DeliverySourcesReply, error) {
	if req.ExpiresAt < 0 || req.MaxDeliveries < 0 || req.MaxDeliveries > 100000000 || len(req.Content) > 60000 {
		return nil, fmt.Errorf("有效期、發放次數或內容長度無效")
	}
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		c := data.Client(ctx, s.repo.data)
		p, e := data.GuardProductWrite(ctx, s.repo.data, req.ProductId)
		if e != nil {
			return e
		}
		if p.ProductKind == "sms_channel" || p.DeliveryKind == "social_service" {
			return fmt.Errorf("渠道接碼不能改為本地發貨")
		}
		if p.GoodsType == "physical" {
			return fmt.Errorf("實體商品只支持快遞發貨")
		}
		if p.LockVersion != req.ExpectedRevision {
			return fmt.Errorf("商品設置已變化，請刷新後重試")
		}
		sk, e := data.DeliverySKU(ctx, c, p, req.SkuId)
		if e != nil {
			return e
		}
		if req.SkuId == 0 {
			n, e := c.ProductSku.Query().Where(productsku.ProductID(p.ID)).Count(ctx)
			if e != nil {
				return e
			}
			if n > 0 {
				return fmt.Errorf("請為每個規格分別設置發貨來源")
			}
		}
		src, e := data.CurrentDeliverySource(ctx, c, p, req.SkuId)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		if req.Action != "" && req.Action != "configure" {
			if src == nil {
				return fmt.Errorf("沒有已配置的複用內容")
			}
			switch req.Action {
			case "pause":
				if src.Status == "submitting" || src.Status == "polling" || src.Status == "uncertain" {
					return fmt.Errorf("首次採購結果尚未核實，請先核實原採購單；可先下架商品停止新訂單")
				}
				if e := c.ProductDeliverySource.UpdateOneID(src.ID).SetStatus("paused").Exec(ctx); e != nil {
					return e
				}
			case "resume":
				if src.Status != "paused" || len(src.Content) == 0 {
					return fmt.Errorf("僅能恢復已有有效內容；未完成採購請先核實")
				}
				if src.ExpiresAt > 0 && src.ExpiresAt <= time.Now().Unix() {
					return fmt.Errorf("內容已過期，請更換內容")
				}
				if e := c.ProductDeliverySource.UpdateOneID(src.ID).SetStatus("ready").Exec(ctx); e != nil {
					return e
				}
			case "reconcile":
				if src.Status != "uncertain" || strings.TrimSpace(req.UpstreamOrderId) == "" || len(req.UpstreamOrderId) > 128 {
					return fmt.Errorf("請填寫原上游訂單號，核查不會重新採購")
				}
				if src.UpstreamOrderID != "" && src.UpstreamOrderID != strings.TrimSpace(req.UpstreamOrderId) {
					return fmt.Errorf("已記錄原採購訂單號，不能替換為其他訂單")
				}
				conn, e := c.SupplyConnection.Get(ctx, src.ConnectionID)
				if e != nil {
					return e
				}
				if data.ConnectionRevision(conn) != src.ConnectionRevision {
					return fmt.Errorf("貨源賬號配置與首次採購時不同，請恢復原貨源賬號後核查原單")
				}
				if e := c.ProductDeliverySource.UpdateOneID(src.ID).SetUpstreamOrderID(strings.TrimSpace(req.UpstreamOrderId)).SetStatus("polling").SetNextCheckAt(0).SetLastError("").Exec(ctx); e != nil {
					return e
				}
			default:
				return fmt.Errorf("無效的操作")
			}
			return c.Product.UpdateOneID(p.ID).AddLockVersion(1).Exec(ctx)
		}
		if req.Mode != "upstream" && req.Mode != "local" && req.Mode != "reuse" {
			return fmt.Errorf("無效的發貨來源")
		}
		if req.Mode == "upstream" && p.UpstreamSourceID == 0 {
			return fmt.Errorf("該商品沒有上游綁定")
		}
		if req.Mode == "local" && p.StockType != "card" {
			return fmt.Errorf("一次性卡密需要卡密庫存類型")
		}
		if src != nil && (src.Status == "submitting" || src.Status == "polling" || src.Status == "uncertain") {
			return fmt.Errorf("請先核實正在處理的首次採購，不能更換來源後重復採購")
		}
		if src != nil && src.Status == "empty" {
			n, e := data.SourceAllocations(ctx, c, src.ID, s.repo.data.Dialect.Capabilities().SupportsSkipLocked)
			if e != nil {
				return e
			}
			if n > 0 {
				return fmt.Errorf("已有訂單等待首次採購，請先處理這些訂單")
			}
		}
		if req.Mode == "reuse" && src != nil && len(src.Content) > 0 && !req.FirstPurchase && req.Content == "" && req.ProcurementId == 0 {
			if req.ExpiresAt > 0 && req.ExpiresAt <= time.Now().Unix() {
				return fmt.Errorf("有效期必須晚於當前時間")
			}
			n, e := data.SourceAllocations(ctx, c, src.ID, s.repo.data.Dialect.Capabilities().SupportsSkipLocked)
			if e != nil {
				return e
			}
			if req.MaxDeliveries > 0 && req.MaxDeliveries < int64(n) {
				return fmt.Errorf("發放次數不能少於已發放及預佔數量")
			}
			if e := c.ProductDeliverySource.UpdateOneID(src.ID).SetExpiresAt(req.ExpiresAt).SetMaxDeliveries(req.MaxDeliveries).Exec(ctx); e != nil {
				return e
			}
			return c.Product.UpdateOneID(p.ID).AddLockVersion(1).Exec(ctx)
		}
		if req.Mode == "reuse" {
			if req.ExpiresAt > 0 && req.ExpiresAt <= time.Now().Unix() {
				return fmt.Errorf("有效期必須晚於當前時間")
			}
			options := 0
			if req.Content != "" {
				options++
			}
			if req.ProcurementId > 0 {
				options++
			}
			if req.FirstPurchase {
				options++
			}
			if options != 1 {
				return fmt.Errorf("請選擇一種內容來源：填寫、歷史採購或首次自動採購")
			}
			controls, e := c.ProductControl.Query().Where(productcontrol.ProductID(p.ID)).Count(ctx)
			if e != nil {
				return e
			}
			if controls > 0 {
				return fmt.Errorf("包含客户填寫字段的商品不能自動複用，請先確認並移除專屬字段")
			}
			create := c.ProductDeliverySource.Create().SetProductID(p.ID).SetSkuID(req.SkuId).SetSubsiteID(p.SubsiteID).SetExpiresAt(req.ExpiresAt).SetMaxDeliveries(req.MaxDeliveries)
			if req.FirstPurchase {
				if p.UpstreamSourceID == 0 || p.UpstreamProductCode == "" {
					return fmt.Errorf("缺少上游商品綁定")
				}
				conn, e := c.SupplyConnection.Get(ctx, p.UpstreamSourceID)
				if e != nil {
					return e
				}
				if conn.Status != "active" {
					return fmt.Errorf("請先啓用貨源")
				}
				upstreamSKU := ""
				if sk != nil {
					upstreamSKU = sk.UpstreamSkuID
					if upstreamSKU == "" {
						return fmt.Errorf("缺少上游規格綁定")
					}
				}
				create.SetPurchaseKey("reuse:" + uuid.NewString()).SetExchangeRate(conn.ExchangeRate).SetConnectionID(conn.ID).SetConnectionRevision(data.ConnectionRevision(conn)).SetUpstreamProduct(p.UpstreamProductCode).SetUpstreamSku(upstreamSKU).SetStatus("empty")
			} else {
				if s.cipher == nil {
					return fmt.Errorf("內容加密組件未配置")
				}
				plain := req.Content
				if req.ProcurementId > 0 {
					po, e := c.ProcurementOrder.Get(ctx, req.ProcurementId)
					if e != nil {
						return e
					}
					it, e := c.OrderItem.Get(ctx, po.OrderItemID)
					if e != nil {
						return e
					}
					o, e := c.Order.Get(ctx, it.OrderID)
					if e != nil {
						return e
					}
					if po.Status != "fulfilled" || it.SubsiteID != p.SubsiteID || it.ProductID != p.ID || it.SkuID != req.SkuId || it.FulfillmentType != "upstream" || len(it.FormAnswers) > 0 || o.Status == "refund_pending" || o.Status == "refunded" || o.Status == "canceled" || o.Status == "expired" || it.FulfillmentStatus == "refunded" {
						return fmt.Errorf("只能複用本商品同一規格未退款的成功採購內容")
					}
					receipt, e := c.ProcurementItem.Query().Where(procurementitem.ProcurementID(po.ID)).Only(ctx)
					if e != nil {
						return e
					}
					i := int(req.ContentIndex)
					if i < 0 || i >= len(receipt.ReceivedContent) {
						return fmt.Errorf("請選擇有效的採購內容")
					}
					ciphertext, e := base64.StdEncoding.DecodeString(receipt.ReceivedContent[i])
					if e != nil {
						return fmt.Errorf("原採購內容不可讀取")
					}
					plaintext, e := s.cipher.Open(ciphertext, p.ID, p.SubsiteID)
					if e != nil {
						return fmt.Errorf("原採購內容不可解密")
					}
					plain = string(plaintext)
					create.SetOriginProcurementID(po.ID)
				}
				if strings.TrimSpace(plain) == "" {
					return fmt.Errorf("發貨內容不能為空")
				}
				sealed, e := s.cipher.Seal(plain, p.ID, p.SubsiteID)
				if e != nil {
					return e
				}
				create.SetContent(sealed).SetStatus("ready")
			}
			if src != nil {
				if e := c.ProductDeliverySource.UpdateOneID(src.ID).ClearCurrentKey().Exec(ctx); e != nil {
					return e
				}
			}
			if _, e := create.SetCurrentKey(data.DeliverySourceKey(p.SubsiteID, p.ID, req.SkuId)).Save(ctx); e != nil {
				return e
			}
		} else if src != nil {
			if e := c.ProductDeliverySource.UpdateOneID(src.ID).ClearCurrentKey().Exec(ctx); e != nil {
				return e
			}
		}
		mode := req.Mode
		if mode == "upstream" {
			mode = "auto"
		}
		if sk != nil {
			if e := c.ProductSku.UpdateOneID(sk.ID).SetFulfillmentMode(mode).Exec(ctx); e != nil {
				return e
			}
		} else {
			if e := c.Product.UpdateOneID(p.ID).SetFulfillmentMode(mode).Exec(ctx); e != nil {
				return e
			}
		}
		update := c.Product.UpdateOneID(p.ID).AddLockVersion(1)
		if mode == "local" || mode == "reuse" {
			update.SetAutoListing(false).SetListingChangedAt(time.Now().UnixMilli()).SetListingZeroSince(0).SetListingRestocked(false).SetListingMessage("已切換本地發貨，自動上下架已暫停")
		}
		return update.Exec(ctx)
	})
	if err != nil {
		return nil, err
	}
	return s.GetDeliverySources(ctx, &adminv1.DeliverySourcesRequest{ProductId: req.ProductId})
}
