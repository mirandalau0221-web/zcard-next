package procurement

import (
	"context"
	"encoding/json"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/socialoperation"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/socialtask"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"strings"
	"time"
)

type StoreSocialOrderService struct {
	storefrontv1.UnimplementedStoreSocialOrderServiceServer
	svc *ProcureService
}

func NewStoreSocialOrderService(svc *ProcureService) *StoreSocialOrderService {
	return &StoreSocialOrderService{svc: svc}
}
func (s *StoreSocialOrderService) owned(ctx context.Context, no string) (*ent.SocialTask, error) {
	if tr, ok := transport.FromServerContext(ctx); ok {
		tr.ReplyHeader().Set("Cache-Control", "no-store, private")
	}
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil || claims.Subject == 0 || claims.Realm != authn.RealmUser {
		return nil, errors.Unauthorized("social.LOGIN_REQUIRED", "請登錄查看服務訂單")
	}
	c := data.Client(ctx, s.svc.repo.data)
	o, e := c.Order.Query().Where(order.OrderNo(no), order.UserID(claims.Subject), order.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Only(ctx)
	if e != nil {
		return nil, errors.NotFound("social.NOT_FOUND", "服務訂單不存在")
	}
	row, e := c.SocialTask.Query().Where(socialtask.Role("retail"), socialtask.OrderID(o.ID), socialtask.UserID(claims.Subject), socialtask.SubsiteID(o.SubsiteID)).Only(ctx)
	if e != nil {
		return nil, errors.NotFound("social.NOT_FOUND", "服務訂單尚未付款或不存在")
	}
	return row, nil
}
func (s *StoreSocialOrderService) Get(ctx context.Context, req *storefrontv1.SocialRetailOrderRequest) (*storefrontv1.SocialRetailOrderReply, error) {
	row, e := s.owned(ctx, req.OrderNo)
	if e != nil {
		return nil, e
	}
	return s.reply(ctx, row)
}
func (s *StoreSocialOrderService) reply(ctx context.Context, row *ent.SocialTask) (*storefrontv1.SocialRetailOrderReply, error) {
	out := &storefrontv1.SocialRetailOrderReply{State: row.State, Phase: row.Phase, PaidAmountCents: row.RetailPaidAmount, RefundedAmountCents: row.RetailRefundedAmount, RefundStatus: row.RetailRefundState, CanCancel: row.CanCancel, CanRefill: row.CanRefill, Active: !socialTerminal(row.State) && row.Phase != "done" && row.Phase != "review"}
	if len(row.SnapshotCipher) > 0 {
		raw, e := s.svc.cipher.Open(row.SnapshotCipher, row.OrderItemID, row.SubsiteID)
		if e != nil {
			return nil, errors.InternalServer("social.READ_FAILED", "服務進度暫時無法讀取")
		}
		var snapshot supplyport.SocialSnapshot
		if json.Unmarshal([]byte(raw), &snapshot) != nil {
			return nil, errors.InternalServer("social.READ_FAILED", "服務進度暫時無法讀取")
		}
		// Wholesale cost and account ledger references are private to B.
		snapshot.PaidAmountCents = supplyport.Integer(row.RetailPaidAmount)
		snapshot.RefundedAmountCents = supplyport.Integer(row.RetailRefundedAmount)
		snapshot.RefundReference = ""
		snapshot.ErrorCode = ""
		b, _ := json.Marshal(snapshot)
		var obj map[string]any
		_ = json.Unmarshal(b, &obj)
		out.Snapshot, _ = structpb.NewStruct(obj)
	}
	op, e := data.Client(ctx, s.svc.repo.data).SocialOperation.Query().Where(socialoperation.TaskID(row.ID)).Order(ent.Desc(socialoperation.FieldCreatedAt), ent.Desc(socialoperation.FieldID)).First(ctx)
	if e != nil && !ent.IsNotFound(e) {
		return nil, e
	}
	if op != nil {
		out.OperationStatus = op.Status
		out.OperationAction = op.Action
		out.OperationRequestId = strings.TrimPrefix(op.OperationID, "socialop_")
		if op.Status == "pending" || op.Status == "unknown" {
			out.CanCancel = false
			out.CanRefill = false
			out.Active = row.Phase != "review"
			out.Message = "操作已提交，正在確認結果"
		}
		if op.Status == "unknown" {
			out.Message = "操作結果待供貨確認，請勿重複提交"
		}
	}
	if row.Phase == "review" {
		out.Message = "服務結果待核對，請憑訂單號聯繫客服"
		out.CanCancel = false
		out.CanRefill = false
		out.Active = false
	}
	if row.RetailRefundState == "pending" {
		out.Message = "供貨退款已確認，正在退回本次實際支付金額"
		out.Active = true
	}
	return out, nil
}
func (s *StoreSocialOrderService) Act(ctx context.Context, req *storefrontv1.SocialRetailActionRequest) (*storefrontv1.SocialRetailOrderReply, error) {
	row, e := s.owned(ctx, req.OrderNo)
	if e != nil {
		return nil, e
	}
	if req.Action != "cancel" && req.Action != "refill" {
		return nil, errors.BadRequest("social.INVALID_ACTION", "不支持此服務操作")
	}
	if _, e := uuid.Parse(req.RequestId); e != nil {
		return nil, errors.BadRequest("social.REQUEST_ID", "操作請求標識無效")
	}
	operationID := "socialop_" + req.RequestId
	e = data.Tx(ctx, s.svc.repo.data, func(ctx context.Context) error {
		c := data.Client(ctx, s.svc.repo.data)
		n, e := c.SocialTask.Update().Where(socialtask.ID(row.ID), socialtask.Role("retail"), socialtask.UserID(row.UserID), socialtask.LeaseUntilLTE(time.Now().Unix())).AddVersion(0).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.Conflict("social.BUSY", "狀態同步中，請稍後重試")
		}
		current, e := c.SocialTask.Get(ctx, row.ID)
		if e != nil {
			return e
		}
		previous, e := c.SocialOperation.Query().Where(socialoperation.TaskID(row.ID), socialoperation.OperationID(operationID)).Only(ctx)
		if e == nil {
			if previous.Action != req.Action {
				return errors.Conflict("social.IDEMPOTENCY_CONFLICT", "同一操作標識不能修改內容")
			}
			return nil
		}
		if !ent.IsNotFound(e) {
			return e
		}
		if current.Phase == "review" || current.UpstreamOrderID == "" || (req.Action == "cancel" && !current.CanCancel) || (req.Action == "refill" && !current.CanRefill) {
			return errors.BadRequest("social.ACTION_UNAVAILABLE", "當前服務狀態不允許此操作")
		}
		pending, e := c.SocialOperation.Query().Where(socialoperation.TaskID(row.ID), socialoperation.StatusIn("pending", "unknown")).Exist(ctx)
		if e != nil {
			return e
		}
		if pending {
			return errors.Conflict("social.OPERATION_PENDING", "已有操作正在確認，請勿重複提交")
		}
		if e = c.SocialOperation.Create().SetID(uuid.NewString()).SetTaskID(row.ID).SetOperationID(operationID).SetAction(req.Action).SetStatus("pending").Exec(ctx); e != nil {
			return e
		}
		return c.SocialTask.UpdateOneID(row.ID).SetPhase("query").SetNextRunAt(0).AddVersion(1).Exec(ctx)
	})
	if e != nil {
		return nil, e
	}
	row, e = data.Client(ctx, s.svc.repo.data).SocialTask.Get(ctx, row.ID)
	if e != nil {
		return nil, e
	}
	result, e := s.reply(ctx, row)
	if e != nil {
		return nil, e
	}
	requested, e := data.Client(ctx, s.svc.repo.data).SocialOperation.Query().Where(socialoperation.TaskID(row.ID), socialoperation.OperationID(operationID)).Only(ctx)
	if e != nil {
		return nil, e
	}
	result.OperationRequestId = req.RequestId
	result.OperationAction = requested.Action
	result.OperationStatus = requested.Status
	return result, nil
}
