package fulfillment

// 履約 API（；storefront 取貨 + admin 手動發貨/列表）。

import (
	"context"
	"encoding/json"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
	"github.com/go-kratos/kratos/v3/transport"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderdelivery"
	auditport "github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"

	"github.com/go-kratos/kratos/v3/errors"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ── Storefront ──

// StoreDeliveryService 顧客取貨服務。
type StoreDeliveryService struct {
	storefrontv1.UnimplementedStoreDeliveryServiceServer
	repo *DeliveryRepoImpl
}

// NewStoreDeliveryService 構造。
func NewStoreDeliveryService(repo *DeliveryRepoImpl) *StoreDeliveryService {
	return &StoreDeliveryService{repo: repo}
}

// FetchDelivery 取貨（三重門——密碼錯誤與單號不存在響應一致）。
func (s *StoreDeliveryService) FetchDelivery(ctx context.Context, req *storefrontv1.FetchDeliveryRequest) (*storefrontv1.FetchDeliveryReply, error) {
	token := req.GetOrderAccessToken()
	if token == "" {
		if tr, ok := transport.FromServerContext(ctx); ok {
			token = tr.RequestHeader().Get("X-Order-Access-Token")
		}
	}
	if req.GetOrderNo() == "" || (req.GetQueryPassword() == "" && token == "" && identity.ClaimsFromContext(ctx) == nil) {
		return nil, errors.BadRequest("delivery.INVALID_INPUT", "訂單號與查詢密碼必填")
	}
	res, err := s.repo.FetchDelivery(ctx, req.GetOrderNo(), req.GetQueryPassword(), clientIP(ctx), token)
	if err != nil {
		return nil, orderaccess.PublicError(err)
	}
	reply := &storefrontv1.FetchDeliveryReply{
		OrderNo: res.OrderNo, Status: res.Status, FetchCount: res.FetchCnt,
	}
	for _, item := range res.Items {
		reply.Items = append(reply.Items, &storefrontv1.DeliveryItem{
			Content: item.Content, Masked: item.Masked, ItemId: item.ItemID, DeliveryId: item.DeliveryID, Kind: item.Kind, ProductName: item.ProductName, SkuName: item.SkuName, DeliveredAt: item.DeliveredAt,
		})
	}
	return reply, nil
}

// MaskedDeliveries 我的訂單掩碼列表（登錄用戶）。
func (s *StoreDeliveryService) MaskedDeliveries(ctx context.Context, req *storefrontv1.MaskedDeliveriesRequest) (*storefrontv1.MaskedDeliveriesReply, error) {
	return &storefrontv1.MaskedDeliveriesReply{}, nil // ：接用戶訂單列表
}

// ── Admin ──

// AdminFulfillmentService 履約管理服務。
type AdminFulfillmentService struct {
	adminv1.UnimplementedAdminFulfillmentServiceServer
	repo *DeliveryRepoImpl
	data *data.Data
}

// NewAdminFulfillmentService 構造。
func NewAdminFulfillmentService(repo *DeliveryRepoImpl, d *data.Data) *AdminFulfillmentService {
	return &AdminFulfillmentService{repo: repo, data: d}
}

// ListPending 待快遞發貨及人工交付列表。
func (s *AdminFulfillmentService) ListPending(ctx context.Context, req *adminv1.ListPendingRequest) (*adminv1.ListPendingReply, error) {
	page := int(req.GetPage())
	size := int(req.GetPageSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, hasMore, err := s.repo.ListPending(ctx, page, size)
	if err != nil {
		return nil, errors.InternalServer("fulfillment.LIST_FAILED", "讀取待發貨失敗")
	}
	// 商品名批查（item 僅存 product_id 快照）
	ids := make([]uint64, 0, len(rows)*2)
	for _, o := range rows {
		for _, it := range o.Edges.Items {
			ids = append(ids, it.ProductID)
		}
	}
	names, _ := s.repo.ProductNames(ctx, ids)
	reply := &adminv1.ListPendingReply{HasMore: hasMore}
	for _, o := range rows {
		for _, it := range o.Edges.Items {
			if it.GoodsType != "physical" && (it.FulfillmentStatus == "delivered" || it.FulfillmentStatus == "refunded") {
				continue
			}

			name := it.ProductName
			if name == "" {
				name = names[it.ProductID]
			}
			raw, _ := json.Marshal(it.FormAnswers)
			quantity := it.Quantity - it.CanceledQuantity
			if it.GoodsType == "physical" {
				quantity -= it.ShippedQuantity
			}
			reply.Orders = append(reply.Orders, &adminv1.PendingOrder{OrderNo: o.OrderNo, ProductId: it.ProductID, ProductName: name, Quantity: quantity, CreatedAt: o.CreatedAt.Unix(), OrderItemId: it.ID, SkuName: it.SkuName, FormAnswersJson: string(raw), FulfillmentStatus: it.FulfillmentStatus, AssignedAdminId: it.AssignedAdminID, FulfillmentType: string(it.FulfillmentType), GoodsType: string(it.GoodsType)})
		}
	}
	return reply, nil
}

// ManualDeliver 手動交付。
func (s *AdminFulfillmentService) ManualDeliver(ctx context.Context, req *adminv1.ManualDeliverRequest) (*emptypb.Empty, error) {
	if req.GetOrderNo() == "" || (req.GetContent() == "" && req.GetLogisticsNo() == "" && req.GetServiceContent() == "") {
		return nil, errors.BadRequest("fulfillment.INVALID_INPUT", "訂單號與交付內容必填")
	}
	claims := identity.ClaimsFromContext(ctx)
	var adminID uint64
	if claims != nil {
		adminID = claims.Subject
	}
	if req.GetServiceContent() != "" {
		if req.GetContent() != "" || req.GetLogisticsNo() != "" {
			return nil, errors.BadRequest("fulfillment.INVALID_INPUT", "請選擇一種交付內容")
		}
		if err := s.repo.CompleteService(ctx, req.OrderNo, req.OrderItemId, req.ServiceContent, req.Remark, adminID); err != nil {
			return nil, errors.BadRequest("fulfillment.DELIVER_FAILED", err.Error())
		}
		return &emptypb.Empty{}, nil
	}
	if err := s.repo.ManualDeliver(ctx, req.GetOrderNo(), req.GetContent(), req.GetLogisticsNo(), req.GetRemark(), adminID, req.GetOrderItemId()); err != nil {
		return nil, errors.InternalServer("fulfillment.DELIVER_FAILED", "交付失敗: "+err.Error())
	}
	return &emptypb.Empty{}, nil
}

// ListDeliveries 交付記錄列表（含完整卡密——現場解密不落庫；路由受
// order:view_delivery 權限門控，查看行為審計留痕）。
func (s *AdminFulfillmentService) ListDeliveries(ctx context.Context, req *adminv1.ListDeliveriesRequest) (*adminv1.ListDeliveriesReply, error) {
	page := int(req.GetPage())
	size := int(req.GetPageSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, total, err := s.repo.ListDeliveries(ctx, req.GetOrderNo(), page, size, req.GetOrderItemId())
	if err != nil {
		return nil, errors.InternalServer("fulfillment.LIST_FAILED", "讀取交付失敗")
	}
	// 明文查看審計（管理端看卡密是敏感操作——誰/何時看了哪單）
	if s.repo.auditor != nil && len(rows) > 0 {
		adminID := uint64(0)
		if claims := identity.ClaimsFromContext(ctx); claims != nil {
			adminID = claims.Subject
		}
		s.repo.auditor.Security(ctx, auditport.SecurityEntry{
			ActorType: "admin", ActorID: adminID,
			Action:   "delivery.view_content",
			Metadata: map[string]any{"order_no": req.GetOrderNo(), "count": len(rows)},
		})
	}
	reply := &adminv1.ListDeliveriesReply{Total: total}
	for _, d := range rows {
		orderNo := s.getOrderNo(ctx, d.OrderID)
		content := s.decryptDelivery(ctx, d)
		masked := "****"
		if len(content) > 4 {
			masked = "****" + content[len(content)-4:]
		}
		reply.Deliveries = append(reply.Deliveries, &adminv1.DeliveryRecord{
			Id: d.ID, OrderNo: orderNo, CardId: d.CardID, OrderItemId: d.ItemID, Kind: deliveryKind(d),
			Content: content, ContentMasked: masked,
			DeliveredMode: string(d.DeliveredMode), DeliveredBy: d.DeliveredBy,
			FetchCount: d.FetchCount, FetchedIp: d.FetchedIP,
		})
		if !d.DeliveredAt.IsZero() {
			reply.Deliveries[len(reply.Deliveries)-1].DeliveredAt = d.DeliveredAt.Unix()
		}
	}
	return reply, nil
}

// decryptDelivery 交付記錄明文（卡密現場解密；直發取商品 direct_content；
// 即刪模式卡密已物理刪除）。
func (s *AdminFulfillmentService) decryptDelivery(ctx context.Context, d *ent.OrderDelivery) string {
	client := data.Client(ctx, s.data)
	if len(d.ServiceContent) > 0 {
		return s.repo.serviceText(ctx, d)
	}
	if tracking, ok := d.Logistics["tracking_no"].(string); ok && tracking != "" {
		return "物流單號：" + tracking
	}
	if d.DeliveredMode == orderdelivery.DeliveredModeDirect && d.CardID == 0 {
		var productID uint64
		if d.ItemID > 0 {
			if it, e := client.OrderItem.Get(ctx, d.ItemID); e == nil {
				productID = it.ProductID
				if it.FulfillmentType == "reuse" {
					src, err := client.ProductDeliverySource.Get(ctx, it.DeliverySourceID)
					if err != nil || src.ProductID != it.ProductID || src.SkuID != it.SkuID || src.SubsiteID != it.SubsiteID {
						return "（共享交付來源不可用）"
					}
					plain, err := s.repo.cipher.Open(src.Content, src.ProductID, src.SubsiteID)
					if err != nil {
						return "（解密失敗）"
					}
					return string(plain)
				}
			}
		}
		p, err := client.Product.Get(ctx, productID)
		if err != nil || len(p.DirectContent) == 0 {
			return "（直發內容未配置）"
		}
		plain, err := s.repo.cipher.Open(p.DirectContent, p.ID, p.SubsiteID)
		if err != nil {
			return "（解密失敗）"
		}
		return string(plain)
	}
	c, err := client.Card.Get(ctx, d.CardID)
	if ent.IsNotFound(err) {
		return "（卡密已刪除）"
	}
	if err != nil {
		return ""
	}
	plain, err := s.repo.cipher.Open(c.Content, c.ProductID, c.SubsiteID)
	if err != nil {
		return "（解密失敗）"
	}
	return string(plain)
}

func (s *AdminFulfillmentService) getOrderNo(ctx context.Context, orderID uint64) string {
	o, err := data.Client(ctx, s.data).Order.Get(ctx, orderID)
	if err != nil {
		return ""
	}
	return o.OrderNo
}

func clientIP(ctx context.Context) string {
	if r, ok := khttp.RequestFromServerContext(ctx); ok {
		return r.RemoteAddr
	}
	return ""
}
