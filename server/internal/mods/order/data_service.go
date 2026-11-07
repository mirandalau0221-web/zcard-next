package order

// 訂單 API（；storefront 下單 + admin 管理，薄 transport）。

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
	couponport "github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/sanitize"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderamountline"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderstatusevent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/payment"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/predicate"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplyconnection"
	"github.com/NovaWorks/zcard-next/server/internal/mods/captcha"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ── storefront ──

// StoreOrderService 顧客下單服務。
type StoreOrderService struct {
	storefrontv1.UnimplementedStoreOrderServiceServer
	uc             *OrderUsecase
	captcha        *captcha.Service // 圖形驗證碼（captcha_order 場景）
	recoverySender notifyport.Sender
}

func (s *StoreOrderService) SetRecoverySender(sender notifyport.Sender) { s.recoverySender = sender }

// NewStoreOrderService 構造。
func NewStoreOrderService(uc *OrderUsecase, cap *captcha.Service) *StoreOrderService {
	return &StoreOrderService{uc: uc, captcha: cap}
}

// CreateOrder 下單。
func (s *StoreOrderService) CreateOrder(ctx context.Context, req *storefrontv1.CreateOrderRequest) (*storefrontv1.CreateOrderReply, error) {
	return s.createOrder(ctx, req, false)
}
func (s *StoreOrderService) QuoteOrder(ctx context.Context, req *storefrontv1.CreateOrderRequest) (*storefrontv1.CreateOrderReply, error) {
	return s.createOrder(ctx, req, true)
}
func (s *StoreOrderService) createOrder(ctx context.Context, req *storefrontv1.CreateOrderRequest, quote bool) (*storefrontv1.CreateOrderReply, error) {
	if len(req.GetItems()) == 0 {
		return nil, errors.BadRequest("order.EMPTY_ITEMS", "訂單項不能為空")
	}
	items := make([]OrderItemInput, 0, len(req.GetItems()))
	for _, it := range req.GetItems() {
		if it.GetProductId() == 0 || it.GetQuantity() <= 0 {
			return nil, errors.BadRequest("order.INVALID_ITEM", "商品 ID 與數量必填")
		}
		items = append(items, OrderItemInput{
			ProductID: it.GetProductId(), SkuID: it.GetSkuId(), Quantity: it.GetQuantity(), ControlAnswers: it.ControlAnswers,
		})
	}
	// 登錄態綁定買家（userAuthMiddleware 注入的 claims；0=遊客單）
	var userID uint64
	claims := identity.ClaimsFromContext(ctx)
	if claims != nil {
		userID = claims.Subject
	}
	input := CreateOrderInput{
		SMSQuoteID: req.SmsQuoteId,
		QuoteOnly:  quote, QuoteKey: req.QuoteKey, ShippingAddress: req.ShippingAddress,
		Items: items, UserID: userID, GuestContact: req.GetGuestContact(),
		QueryPassword: req.GetQueryPassword(), Contact: req.GetContact(),
		CouponCode: req.GetCouponCode(), ControlAnswers: req.GetControlAnswers(),
		UsePoints: req.GetUsePoints(),
		RefCode:   req.GetRefCode(),
		// ：Idempotency-Key 頭（同 key 雙擊返回首單，）
		IdempotencyKey: idempotencyKeyFromContext(ctx),
	}
	if replay, e := s.uc.ReplayOrder(ctx, input); e != nil {
		return nil, mapOrderErr(e)
	} else if replay != nil {
		return &storefrontv1.CreateOrderReply{OrderNo: replay.OrderNo, TotalCents: replay.TotalCents, ShippingCents: replay.ShippingCents, ShippingCollect: replay.ShippingCollect, ExpiresAt: replay.ExpiresAt.Unix(), OrderAccessToken: replay.OrderAccessToken}, nil
	}
	if !quote && s.captcha != nil && claims == nil {
		if err := s.captcha.VerifyScene(ctx, captcha.SceneOrder, req.GetCaptchaId(), req.GetCaptchaCode()); err != nil {
			return nil, err
		}
	}
	res, err := s.uc.CreateOrder(ctx, input)
	if err != nil {
		return nil, mapOrderErr(err)
	}
	return &storefrontv1.CreateOrderReply{
		OrderNo: res.OrderNo, TotalCents: res.TotalCents, ShippingCents: res.ShippingCents, ShippingCollect: res.ShippingCollect, QuoteKey: res.QuoteKey, ExpiresAt: res.ExpiresAt.Unix(), OrderAccessToken: res.OrderAccessToken,
	}, nil
}

// GetOrder 查單（單號+密碼）。
// 取貨三重門之一二（ 補全）：查詢密碼 或 登錄態本人——登錄態本人免密碼。
func (s *StoreOrderService) GetOrder(ctx context.Context, req *storefrontv1.GetOrderRequest) (*storefrontv1.GetOrderReply, error) {
	token := req.GetOrderAccessToken()
	if token == "" {
		if tr, ok := transport.FromServerContext(ctx); ok {
			token = tr.RequestHeader().Get("X-Order-Access-Token")
		}
	}
	o, err := s.uc.GetByOrderNo(ctx, req.GetOrderNo())
	if ent.IsNotFound(err) {
		if token != "" {
			return nil, orderaccess.Required(ctx)
		}
		return nil, orderaccess.PublicError(orderaccess.Verify(ctx, s.uc.Gate, req.GetOrderNo(), "", req.GetQueryPassword(), orderClientIP(ctx), ""))
	}
	if err != nil {
		return nil, errors.InternalServer("order.GET_FAILED", "查詢失敗")
	}
	if o.SubsiteID != tenancy.FromContext(ctx).SubsiteID {
		if token != "" {
			return nil, orderaccess.Required(ctx)
		}
		return nil, orderaccess.PublicError(orderaccess.Verify(ctx, s.uc.Gate, req.GetOrderNo(), "", req.GetQueryPassword(), orderClientIP(ctx), ""))
	}
	// 登錄態本人：免查詢密碼（密碼錯與單號不存在對外表現一致的紀律不破壞——
	// 非本人登錄態不泄露訂單存在性，仍走密碼校驗路徑）
	claims := identity.ClaimsFromContext(ctx)
	isOwner := claims != nil && o.UserID != 0 && claims.Subject == o.UserID
	if !isOwner && !orderaccess.VerifyToken(o.OrderAccessTokenHash, o.SubsiteID, o.OrderNo, token) {
		if (o.OrderAccessTokenHash != "" || token != "") && req.GetQueryPassword() == "" {
			return nil, orderaccess.Required(ctx)
		} else {
			// 查詢密碼校驗（三重門之一：設置則必須匹配；錯誤與單號不存在表現一致）。
			// 遊客單另接受下單聯繫郵箱作為憑證，免密碼、免驗證碼直查。
			if err := orderaccess.Verify(ctx, s.uc.Gate, o.OrderNo, o.QueryPasswordHash, req.GetQueryPassword(), orderClientIP(ctx), orderaccess.GuestContactEmail(o.UserID, o.Contact, o.GuestContact)); err != nil {
				return nil, orderaccess.PublicError(err)
			}
		}
	}
	reply := &storefrontv1.GetOrderReply{
		ShippingStatus: o.ShippingStatus, ShippingCents: o.ShippingAmount, ShippingAddress: o.ShippingAddress, CommerceVersion: o.CommerceVersion,
		OrderNo: o.OrderNo, Status: string(o.Status), TotalCents: o.TotalAmount,
		CreatedAt: o.CreatedAt.Unix(),
	}
	paid, payErr := data.Client(ctx, s.uc.Data).Payment.Query().Where(payment.OrderID(o.ID), payment.StatusEQ(payment.StatusSuccess), payment.ReviewReasonEQ("")).Order(ent.Asc(payment.FieldID)).First(ctx)
	if payErr != nil && !ent.IsNotFound(payErr) {
		return nil, payErr
	}
	if payErr == nil {
		reply.PaidTotalCents = paid.Amount
		reply.PaidFeeCents = paid.Fee
	}
	if !o.ExpiredAt.IsZero() {
		reply.ExpiresAt = o.ExpiredAt.Unix()
	}
	reply.ShipmentsJson, err = data.ShipmentsJSON(ctx, s.uc.Data, o.ID)
	if err != nil {
		return nil, err
	}
	// 子項
	items, err := data.Client(ctx, s.uc.Data).OrderItem.Query().Where(orderitem.OrderID(o.ID)).All(ctx)
	if err != nil {
		return nil, errors.InternalServer("order.GET_FAILED", "讀取訂單商品失敗")
	}
	summaries, err := data.OrderProductSummaries(ctx, s.uc.Data, []*ent.Order{o})
	if err != nil {
		return nil, errors.InternalServer("order.GET_FAILED", "讀取訂單商品失敗")
	}
	names := map[uint64]string{}
	for _, item := range summaries[o.ID] {
		names[item.ProductID] = item.Name
	}
	for _, it := range items {
		name := it.ProductName
		if name == "" {
			name = names[it.ProductID]
		}
		if it.DeliveryKind == "sms_activation" {
			name, _ = sanitize.SMSPublicContent("sms_channel", name, "")
		}
		if it.SkuName != "" {
			name += " / " + it.SkuName
		}
		reply.Items = append(reply.Items, &storefrontv1.OrderItemReply{
			ShippingCollect: shippingCollectProducts(o)[strconv.FormatUint(it.ProductID, 10)] == true,
			DeliveryKind:    it.DeliveryKind, SmsProduct: it.SmsProduct, GoodsType: it.GoodsType, PaidCents: it.PaidAmount, ShippingCents: it.ShippingAmount, ShippedQuantity: it.ShippedQuantity, ReceivedQuantity: it.ReceivedQuantity, CanceledQuantity: it.CanceledQuantity, ReturnedQuantity: it.ReturnedQuantity, Id: it.ID, ProductId: it.ProductID, ProductName: name, Quantity: it.Quantity, UnitPriceCents: it.UnitPrice, AmountCents: it.Amount, SkuName: it.SkuName, FulfillmentType: string(it.FulfillmentType), FulfillmentStatus: it.FulfillmentStatus, FormAnswersJson: answersJSON(it.FormAnswers),
		})
	}
	refunds, e := data.Client(ctx, s.uc.Data).RefundOrder.Query().Where(refundorder.OrderID(o.ID), refundorder.StatusEQ(refundorder.StatusSucceeded)).All(ctx)
	if e != nil {
		return nil, e
	}
	for _, rf := range refunds {
		reply.RefundedCents += rf.Amount
		reply.RefundedFeeCents += rf.FeeAmount
	}
	return reply, nil
}

// ListMyOrders 我的訂單（登錄態； 補全）。
func (s *StoreOrderService) ListMyOrders(ctx context.Context, req *storefrontv1.ListMyOrdersRequest) (*storefrontv1.ListMyOrdersReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	rows, total, err := s.uc.ListUserOrders(ctx, claims.Subject, req.GetStatus(), int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, errors.InternalServer("order.LIST_FAILED", "查詢失敗")
	}
	summaries, err := data.OrderProductSummaries(ctx, s.uc.Data, rows)
	if err != nil {
		return nil, errors.InternalServer("order.LIST_FAILED", "讀取訂單商品失敗")
	}
	reply := &storefrontv1.ListMyOrdersReply{Total: total}
	for _, o := range rows {
		item := &storefrontv1.MyOrderItem{ShippingStatus: o.ShippingStatus,
			OrderNo: o.OrderNo, Status: string(o.Status), TotalCents: o.TotalAmount,
		}
		names := []string{}
		for _, line := range o.Edges.Items {
			item.ItemCount += line.Quantity
			name := line.ProductName
			if name == "" {
				name = "商品"
				for _, summary := range summaries[o.ID] {
					if summary.ProductID == line.ProductID {
						name = summary.Name
						break
					}
				}
			}
			if line.DeliveryKind == "sms_activation" {
				name, _ = sanitize.SMSPublicContent("sms_channel", name, "")
			}
			if line.SkuName != "" {
				name += " · " + line.SkuName
			}
			names = append(names, name)
			if line.FulfillmentType == "manual" && (line.FulfillmentStatus == "pending" || line.FulfillmentStatus == "delivering") {
				item.ManualPendingCount++
			}
		}
		item.ProductSummary = strings.Join(names, "、")
		if !o.CreatedAt.IsZero() {
			item.CreatedAt = o.CreatedAt.Unix()
		}
		if !o.ExpiredAt.IsZero() {
			item.ExpiredAt = o.ExpiredAt.Unix()
		}
		reply.Orders = append(reply.Orders, item)
	}
	return reply, nil
}

// ListGuestOrders 遊客按下單聯繫方式查訂單列表（郵箱/手機號）。
// 安全口徑：僅遊客單（user_id 空）；精簡信息（單號/狀態/金額/時間——無卡密）；
// 卡密仍需逐單憑證（查詢密碼或下單聯繫郵箱；fetchDelivery 三重門）；查不到返回空列表（防枚舉與
// 查詢不存在表現一致）；限最近 20 條。
func (s *StoreOrderService) ListGuestOrders(ctx context.Context, req *storefrontv1.ListGuestOrdersRequest) (*storefrontv1.ListGuestOrdersReply, error) {
	contact := strings.TrimSpace(req.GetContact())
	if contact == "" || len(contact) > 255 {
		return nil, errors.BadRequest("order.CONTACT_INVALID", "請輸入下單時留的郵箱或手機號")
	}
	rows, err := data.Client(ctx, s.uc.Data).Order.Query().
		Where(
			order.SubsiteID(tenancy.FromContext(ctx).SubsiteID),
			// 遊客單：user_id NULL 或 0（歷史寫入兩種形態並存）
			order.Or(order.UserIDIsNil(), order.UserID(0)),
			order.Or(order.Contact(contact), order.GuestContact(contact)),
		).
		Order(ent.Desc(order.FieldID)).
		Limit(20).
		All(ctx)
	if err != nil {
		return nil, errors.InternalServer("order.LIST_FAILED", "查詢失敗")
	}
	reply := &storefrontv1.ListGuestOrdersReply{}
	for _, o := range rows {
		item := &storefrontv1.GuestOrderItem{
			OrderNo: o.OrderNo, Status: string(o.Status), TotalCents: o.TotalAmount,
			CreatedAt: o.CreatedAt.Unix(), ExpiresAt: o.ExpiredAt.Unix(),
		}
		reply.Orders = append(reply.Orders, item)
	}
	return reply, nil
}

// CancelMyOrder 取消本人待支付訂單（ 補全；鎖卡釋放 + 返券）。
func (s *StoreOrderService) CancelMyOrder(ctx context.Context, req *storefrontv1.CancelMyOrderRequest) (*emptypb.Empty, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	o, err := s.uc.GetByOrderNo(ctx, req.GetOrderNo())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("order.NOT_FOUND", "訂單不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("order.GET_FAILED", "查詢失敗")
	}
	if o.UserID == 0 || o.UserID != claims.Subject {
		return nil, errors.NotFound("order.NOT_FOUND", "訂單不存在") // 非本人不泄露存在性
	}
	if err := s.uc.CancelOrder(ctx, req.GetOrderNo(), "用戶取消", "user", claims.Subject); err != nil {
		return nil, mapOrderErr(err)
	}
	return &emptypb.Empty{}, nil
}

// idempotencyKeyFromContext 讀 Idempotency-Key 頭（）。
func idempotencyKeyFromContext(ctx context.Context) string {
	if tr, ok := transport.FromServerContext(ctx); ok {
		return tr.RequestHeader().Get("Idempotency-Key")
	}
	return ""
}

// ── admin ──

// AdminOrderService 訂單管理服務。
type AdminOrderService struct {
	adminv1.UnimplementedAdminOrderServiceServer
	uc   *OrderUsecase
	data *data.Data
}

// NewAdminOrderService 構造。
func NewAdminOrderService(uc *OrderUsecase, d *data.Data) *AdminOrderService {
	return &AdminOrderService{uc: uc, data: d}
}

// ListOrders 訂單列表。
func (s *AdminOrderService) ListOrders(ctx context.Context, req *adminv1.ListOrdersRequest) (*adminv1.ListOrdersReply, error) {
	tc := tenancy.FromContext(ctx)
	limit := req.GetLimit()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	keyword := strings.TrimSpace(req.GetKeyword())
	if utf8.RuneCountInString(keyword) > 150 {
		return nil, errors.BadRequest("order.INVALID_KEYWORD", "搜索關鍵詞不能超過 150 個字符")
	}
	var filters []predicate.Order
	if req.ShippingStatus != "" {
		filters = append(filters, order.ShippingStatus(req.ShippingStatus))
	}
	if req.GetProductId() > 0 {
		filters = append(filters, order.HasItemsWith(orderitem.ProductID(req.GetProductId())))
	}
	if req.GetStartTime() != 0 || req.GetEndTime() != 0 {
		if req.GetStartTime() <= 0 || req.GetEndTime() <= req.GetStartTime() || req.GetEndTime()-req.GetStartTime() > 366*86400 {
			return nil, errors.BadRequest("order.INVALID_RANGE", "時間範圍須在 1 至 366 天內")
		}
		start, end := time.Unix(req.GetStartTime(), 0).UTC(), time.Unix(req.GetEndTime(), 0).UTC()
		switch req.GetTimeField() {
		case "", "created":
			filters = append(filters, order.CreatedAtGTE(start), order.CreatedAtLT(end))
		case "paid":
			filters = append(filters, order.PaidAtGTE(start), order.PaidAtLT(end))
		default:
			return nil, errors.BadRequest("order.INVALID_RANGE", "不支持的時間類型")
		}
	}
	if req.GetChannelId() > 0 || req.GetChannelCode() != "" {
		ps := []predicate.Payment{payment.StatusEQ(payment.StatusSuccess), payment.ReviewReasonEQ(""), payment.SubsiteID(tc.SubsiteID)}
		if req.GetChannelId() > 0 {
			ps = append(ps, payment.ChannelID(req.GetChannelId()))
		} else {
			ps = append(ps, payment.ChannelID(0), payment.Channel(req.GetChannelCode()))
		}
		filters = append(filters, order.HasPaymentsWith(ps...))
	}
	rows, err := s.uc.ListOrders(ctx, tc.SubsiteID, req.GetStatus(), req.GetCursor(), limit, keyword, filters...)
	if err != nil {
		return nil, errors.InternalServer("order.LIST_FAILED", "讀取訂單失敗")
	}
	summaries, err := data.OrderProductSummaries(ctx, s.uc.Data, rows)
	if err != nil {
		return nil, errors.InternalServer("order.LIST_FAILED", "讀取訂單商品失敗")
	}
	reply := &adminv1.ListOrdersReply{}
	for _, o := range rows {
		pb := toAdminOrderPB(o, nil, nil, nil, nil, nil)
		for _, it := range summaries[o.ID] {
			pb.Items = append(pb.Items, &adminv1.AdminOrderItem{ProductId: it.ProductID, SkuId: it.SkuID, Name: it.Name, SkuName: it.SkuName, Quantity: it.Quantity})
		}
		reply.Orders = append(reply.Orders, pb)
	}
	if len(rows) == int(limit) {
		reply.NextCursor = rows[len(rows)-1].ID
	}
	return reply, nil
}

// GetOrder 訂單詳情（含金額行+狀態事件）。
func (s *AdminOrderService) GetOrder(ctx context.Context, req *adminv1.GetAdminOrderRequest) (*adminv1.AdminOrder, error) {
	o, err := s.uc.GetByOrderNo(ctx, req.GetOrderNo())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("order.NOT_FOUND", "訂單不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("order.GET_FAILED", "查詢失敗")
	}
	client := data.Client(ctx, s.data)
	items, _ := client.OrderItem.Query().Where(orderitem.OrderID(o.ID)).All(ctx)
	lines, _ := client.OrderAmountLine.Query().Where(orderamountline.OrderID(o.ID)).
		Order(ent.Asc(orderamountline.FieldSeq)).All(ctx)
	events, _ := client.OrderStatusEvent.Query().Where(orderstatusevent.OrderID(o.ID)).
		Order(ent.Asc(orderstatusevent.FieldCreatedAt), ent.Asc(orderstatusevent.FieldID)).All(ctx)
	// 商品/上游聯查（ 修復：訂單詳情展示自營/上游渠道/鏈接/成本——老項目同款信息區）
	products, connections := s.loadItemUpstream(ctx, items)
	out := toAdminOrderPB(o, items, lines, events, products, connections)
	out.ShipmentsJson, err = data.ShipmentsJSON(ctx, s.data, o.ID)
	if err != nil {
		return nil, err
	}
	refunds, err := client.RefundOrder.Query().Where(refundorder.OrderID(o.ID), refundorder.StatusEQ(refundorder.StatusSucceeded)).All(ctx)
	if err != nil {
		return nil, errors.InternalServer("order.REFUND_QUERY_FAILED", "讀取退款金額失敗")
	}
	for _, r := range refunds {
		out.RefundedCents += r.Amount
		out.RefundedFeeCents += r.FeeAmount
	}
	paid, payErr := client.Payment.Query().Where(payment.OrderID(o.ID), payment.StatusEQ(payment.StatusSuccess), payment.ReviewReasonEQ("")).Order(ent.Asc(payment.FieldID)).First(ctx)
	if payErr != nil && !ent.IsNotFound(payErr) {
		return nil, payErr
	}
	if payErr == nil {
		out.PaidTotalCents = paid.Amount
		out.PaidFeeCents = paid.Fee
	}
	return out, nil
}

// loadItemUpstream 批量聯查訂單項的商品與上游貨源連接（軟外鍵——無 ent edge）。
func (s *AdminOrderService) loadItemUpstream(ctx context.Context, items []*ent.OrderItem) (map[uint64]*ent.Product, map[uint64]*ent.SupplyConnection) {
	products := map[uint64]*ent.Product{}
	connIDs := map[uint64]bool{}
	if len(items) > 0 {
		ids := make([]uint64, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ProductID)
		}
		rows, _ := data.Client(ctx, s.data).Product.Query().
			Where(product.IDIn(ids...)).All(ctx)
		for _, pr := range rows {
			products[pr.ID] = pr
			if pr.UpstreamSourceID != 0 {
				connIDs[pr.UpstreamSourceID] = true
			}
		}
	}
	connections := map[uint64]*ent.SupplyConnection{}
	if len(connIDs) > 0 {
		ids := make([]uint64, 0, len(connIDs))
		for id := range connIDs {
			ids = append(ids, id)
		}
		rows, _ := data.Client(ctx, s.data).SupplyConnection.Query().
			Where(supplyconnection.IDIn(ids...)).All(ctx)
		for _, conn := range rows {
			connections[conn.ID] = conn
		}
	}
	return products, connections
}

// CancelOrder 取消。
func (s *AdminOrderService) CancelOrder(ctx context.Context, req *adminv1.CancelOrderRequest) (*emptypb.Empty, error) {
	if err := s.uc.CancelOrder(ctx, req.GetOrderNo(), req.GetReason(), "admin", 0); err != nil {
		return nil, mapOrderErr(err)
	}
	return &emptypb.Empty{}, nil
}

// ── 映射 ──

func toAdminOrderPB(o *ent.Order, items []*ent.OrderItem, lines []*ent.OrderAmountLine, events []*ent.OrderStatusEvent,
	products map[uint64]*ent.Product, connections map[uint64]*ent.SupplyConnection) *adminv1.AdminOrder {
	out := &adminv1.AdminOrder{ShippingStatus: o.ShippingStatus, ShippingCents: o.ShippingAmount, ShippingAddress: o.ShippingAddress, CommerceVersion: o.CommerceVersion,
		Id: o.ID, OrderNo: o.OrderNo, Status: string(o.Status),
		TotalCents: o.TotalAmount, CostCents: o.Cost,
		UserId: o.UserID, GuestContact: o.GuestContact, Contact: o.Contact,
		ClientIp:  o.ClientIP,
		CreatedAt: o.CreatedAt.Unix(),
	}
	if !o.PaidAt.IsZero() {
		out.PaidAt = o.PaidAt.Unix()
	}
	if !o.ExpiredAt.IsZero() {
		out.ExpiredAt = o.ExpiredAt.Unix()
	}
	out.ExpiryReview = o.ExpiryReview
	out.ExpiryReason = o.ExpiryReason
	out.ExpiryAttempts = o.ExpiryAttempts
	if !o.ExpiryRetryAt.IsZero() {
		out.ExpiryRetryAt = o.ExpiryRetryAt.Unix()
	}
	for _, it := range items {
		pb := &adminv1.AdminOrderItem{InventoryTracked: proto.Bool(it.InventoryTracked), GoodsType: it.GoodsType, PaidCents: it.PaidAmount, ShippingCents: it.ShippingAmount, RefundedCents: it.RefundedAmount, RefundedShippingCents: it.RefundedShipping, ShippedQuantity: it.ShippedQuantity, ReceivedQuantity: it.ReceivedQuantity, CanceledQuantity: it.CanceledQuantity, ReturnedQuantity: it.ReturnedQuantity,
			ShippingCollect: shippingCollectProducts(o)[strconv.FormatUint(it.ProductID, 10)] == true,
			FormAnswersJson: answersJSON(it.FormAnswers), AssignedAdminId: it.AssignedAdminID,
			ProductId: it.ProductID, SkuId: it.SkuID, Quantity: it.Quantity,
			UnitPriceCents: it.UnitPrice, AmountCents: it.Amount,
			Id: it.ID, FulfillmentType: string(it.FulfillmentType), FulfillmentStatus: it.FulfillmentStatus,
			SkuName: it.SkuName, CostCents: it.Cost,
		}
		// 自營/上游信息（商品快照缺失時回落商品當前態——訂單詳情以商品為準）
		if pr := products[it.ProductID]; pr != nil {
			pb.Name = pr.Name
			pb.IsSelf = pr.UpstreamSourceID == 0 // 0=自營
			if pr.UpstreamSourceID != 0 {
				pb.UpstreamSourceId = pr.UpstreamSourceID
				pb.UpstreamProductCode = pr.UpstreamProductCode
				if conn := connections[pr.UpstreamSourceID]; conn != nil {
					pb.UpstreamSourceName = conn.Name
					pb.UpstreamDriver = conn.Driver
					pb.UpstreamUrl = conn.BaseURL
				}
			}
		}
		if it.ProductName != "" {
			pb.Name = it.ProductName
		}
		out.Items = append(out.Items, pb)
	}
	for _, l := range lines {
		out.AmountLines = append(out.AmountLines, &adminv1.AmountLine{
			Type: string(l.Type), AmountCents: l.Amount,
			SourceType: l.SourceType, SourceId: l.SourceID, Seq: l.Seq,
		})
	}
	for _, e := range events {
		out.StatusEvents = append(out.StatusEvents, &adminv1.StatusEvent{
			FromStatus: e.FromStatus, ToStatus: e.ToStatus, Event: e.Event,
			Operator: string(e.Operator), Reason: e.Reason,
			CreatedAt: e.CreatedAt.Unix(),
		})
	}
	return out
}

func mapOrderErr(err error) error {
	msg := err.Error()
	switch {
	case contains(msg, "FORM_INVALID"):
		return errors.BadRequest("order.FORM_INVALID", strings.TrimPrefix(msg, "order.FORM_INVALID: "))
	case stderrors.Is(err, couponport.ErrFlashReserved):
		return errors.BadRequest("order.FLASH_RESERVED", "秒殺名額暫被待付款訂單佔用，請稍後重試")
	case stderrors.Is(err, couponport.ErrFlashSoldOut):
		return errors.BadRequest("order.FLASH_SOLD_OUT", "秒殺已搶完")
	case stderrors.Is(err, couponport.ErrFlashUserLimit):
		return errors.BadRequest("order.FLASH_USER_LIMIT", "已達到本次秒殺限購數量")
	case contains(msg, "STOCK_UNAVAILABLE"):
		return errors.BadRequest("order.STOCK_UNAVAILABLE", "暫時無法確認庫存，請稍後重試")
	case contains(msg, "INSUFFICIENT"):
		return errors.BadRequest("order.INSUFFICIENT_STOCK", "庫存不足")
	case contains(msg, "PRODUCT_NOT"):
		return errors.NotFound("order.PRODUCT_NOT_FOUND", "商品不存在或不可購買")
	case contains(msg, "EMPTY"):
		return errors.BadRequest("order.EMPTY_ITEMS", "訂單項不能為空")
	case contains(msg, "COUPON"):
		return errors.BadRequest("order.COUPON_INVALID", "優惠券無效或不可用")
	case contains(msg, "POINTS_LOGIN"):
		return errors.Unauthorized("order.POINTS_LOGIN_REQUIRED", "積分兌換需登錄")
	case contains(msg, "POINTS_MIXED"):
		return errors.BadRequest("order.POINTS_MIXED_CART", "積分兌換訂單須全部為積分商品")
	case contains(msg, "POINTS"):
		return errors.BadRequest("order.POINTS_INSUFFICIENT", "積分不足")
	case contains(msg, "CANNOT_CANCEL"):
		return errors.BadRequest("order.CANNOT_CANCEL", "訂單當前狀態不可取消")
	case contains(msg, "TRANSITION"):
		return errors.BadRequest("order.TRANSITION_NOT_ALLOWED", "狀態遷移不允許")
	case contains(msg, "NOT_FOUND"):
		return errors.NotFound("order.NOT_FOUND", "訂單不存在")
	case contains(msg, "QUERY_PASSWORD_REQUIRED"):
		return errors.BadRequest("order.QUERY_PASSWORD_REQUIRED", "請設置查詢密碼（至少 4 位，取貨時使用）")
	case contains(msg, "CONTACT_REQUIRED"), contains(msg, "CONTACT_INVALID"):
		// 透出 usecase 校驗文案（含聯繫方式模式説明）
		if idx := strings.Index(msg, ": "); idx >= 0 && idx+2 < len(msg) {
			return errors.BadRequest("order.CONTACT_INVALID", msg[idx+2:])
		}
		return errors.BadRequest("order.CONTACT_INVALID", "請填寫有效的聯繫方式")
	default:
		return errors.InternalServer("order.CREATE_FAILED", "下單失敗")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || searchString(s, sub))
}

func searchString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func answersJSON(v []map[string]string) string { b, _ := json.Marshal(v); return string(b) }

func orderClientIP(ctx context.Context) string {
	if r, ok := khttp.RequestFromServerContext(ctx); ok {
		return r.RemoteAddr
	}
	return ""
}
