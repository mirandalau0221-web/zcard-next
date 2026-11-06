package procurement

// 管理面 API（）：採購單列表/詳情/手動重試/手動標記完成。

import (
	"context"
	"errors"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementorder"

	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminProcurementService 管理面採購服務。
type AdminProcurementService struct {
	adminv1.UnimplementedAdminProcurementServiceServer
	repo *ProcureRepo
	svc  *ProcureService
}

// NewAdminProcurementService 構造。
func NewAdminProcurementService(repo *ProcureRepo, svc *ProcureService) *AdminProcurementService {
	return &AdminProcurementService{repo: repo, svc: svc}
}

// ListProcurements 列表。
func (s *AdminProcurementService) ListProcurements(ctx context.Context, req *adminv1.ListProcurementsRequest) (*adminv1.ListProcurementsReply, error) {
	page, pageSize := procurePageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.List(ctx, req.GetStatus(), page, pageSize, req.GetOrderNo())
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListProcurementsReply{Total: int64(total), Page: int32(page), PageSize: int32(pageSize)}
	for _, po := range rows {
		reply.Procurements = append(reply.Procurements, s.toProto(ctx, po))
	}
	return reply, nil
}

// GetProcurement 詳情。
func (s *AdminProcurementService) GetProcurement(ctx context.Context, req *adminv1.GetProcurementRequest) (*adminv1.ProcurementOrder, error) {
	po, err := s.repo.Get(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, po)
}

// RetryProcurement 恢復本地交付或查詢已有上游訂單。
func (s *AdminProcurementService) RetryProcurement(ctx context.Context, req *adminv1.RetryProcurementRequest) (*adminv1.ProcurementOrder, error) {
	po, err := s.repo.Get(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	switch string(po.Status) {
	case "rejected", "refunding", "refunded", "manual":
		return nil, errors.New("procurement.ALREADY_TERMINAL: 採購單已終態")
	}
	// 有采購回執時只恢復本地交付；沒有回執時僅查詢上游，絕不重新購買。
	if err := s.svc.PollOne(ctx, req.GetId()); err != nil {
		return nil, err
	}
	po, err = s.repo.Get(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return s.toProto(ctx, po), nil
}

// MarkProcurementManual 停止自動採購並轉人工，後續通過訂單人工補發完成交付。
func (s *AdminProcurementService) MarkProcurementManual(ctx context.Context, req *adminv1.MarkProcurementManualRequest) (*adminv1.ProcurementOrder, error) {
	po, err := s.repo.Get(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if string(po.Status) == "manual" {
		return s.toProto(ctx, po), nil
	}
	if err := s.repo.MarkManual(ctx, req.GetId(), orEmpty(req.GetRemark(), "管理員手動轉人工")); err != nil {
		return nil, err
	}
	po, _ = s.repo.Get(ctx, req.GetId())
	return s.toProto(ctx, po), nil
}

// toProto 轉協議（含採購項信息）。
func (s *AdminProcurementService) toProto(ctx context.Context, po *ent.ProcurementOrder) *adminv1.ProcurementOrder {
	out := &adminv1.ProcurementOrder{
		Id:               po.ID,
		OrderItemId:      po.OrderItemID,
		ConnectionId:     po.ConnectionID,
		UpstreamOrderId:  po.UpstreamOrderID,
		Status:           string(po.Status),
		FailStrategy:     string(po.FailStrategy),
		RetryCount:       po.RetryCount,
		DedupeKey:        po.DedupeKey,
		TraceId:          po.TraceID,
		LastError:        po.LastError,
		UpstreamRefundId: po.UpstreamRefundID,
		CreatedAt:        po.CreatedAt.Unix(),
		UpdatedAt:        po.UpdatedAt.Unix(),
	}
	if !po.NextRetryAt.IsZero() {
		out.NextRetryAt = po.NextRetryAt.Unix()
	}
	if !po.LastPollAt.IsZero() {
		out.LastPollAt = po.LastPollAt.Unix()
	}
	if oi, err := s.repo.OrderItemInfo(ctx, po.OrderItemID); err == nil {
		if o, e := data.Client(ctx, s.repo.data).Order.Get(ctx, oi.OrderID); e == nil {
			out.OrderNo = o.OrderNo
			out.OrderStatus = string(o.Status)
			if count, e := s.repo.deliveredCount(ctx, po.OrderItemID, oi.Quantity); e == nil {
				out.DeliveredQuantity = int32(count)
				out.DeliveryIncomplete = count < int(oi.Quantity) && (out.OrderStatus == "paid" || out.OrderStatus == "fulfilling" || out.OrderStatus == "partially_delivered")
			}
		}
	}
	// 採購項（數量/成本/到手卡密行數）
	if item, err := s.repo.ItemByProcurement(ctx, po.ID); err == nil {
		out.ItemQuantity = item.Quantity
		out.ItemUnitCost = item.UnitCost
		out.ReceivedCards = int32(len(item.ReceivedContent))
	}
	return out
}

func procurePageParams(page, pageSize int32) (int, int) {
	p := int(page)
	if p < 1 {
		p = 1
	}
	ps := int(pageSize)
	if ps < 1 {
		ps = 20
	}
	if ps > 100 {
		ps = 100
	}
	return p, ps
}

func orEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// 編譯期保證：procurementorder 包被引用（狀態過濾語義）。
var _ = procurementorder.StatusPending
var _ = emptypb.Empty{}
