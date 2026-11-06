package ticket

// 客服工作台（ admin）：urgent_paid 置頂、回覆/內部備註/解決/關閉、篩選。

import (
	"context"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"

	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminTicketService 客服工作台。
type AdminTicketService struct {
	adminv1.UnimplementedAdminTicketServiceServer
	repo   *TicketRepo
	outbox events.Writer
}

// NewAdminTicketService 構造。
func NewAdminTicketService(repo *TicketRepo, outbox events.Writer) *AdminTicketService {
	return &AdminTicketService{repo: repo, outbox: outbox}
}

// ListTickets 工作台列表（urgent_paid 置頂）。
func (s *AdminTicketService) ListTickets(ctx context.Context, req *adminv1.ListTicketsRequest) (*adminv1.ListTicketsReply, error) {
	page, size := ticketPage(req.GetPage(), req.GetPageSize())
	var orderID uint64
	if req.GetOrderNo() != "" {
		orderID = orderIDOf(ctx, s, req.GetOrderNo())
		if orderID == 0 {
			return &adminv1.ListTicketsReply{Total: 0, Page: int32(page), PageSize: int32(size)}, nil
		}
	}
	rows, total, err := s.repo.ListWorkbench(ctx, req.GetStatus(), req.GetType(), orderID, page, size)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListTicketsReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, t := range rows {
		reply.Tickets = append(reply.Tickets, toAdminPB(t))
	}
	return reply, nil
}

// GetTicket 詳情（含內部備註）。
func (s *AdminTicketService) GetTicket(ctx context.Context, req *adminv1.GetTicketRequest) (*adminv1.GetTicketReply, error) {
	t, err := s.repo.GetByNo(ctx, req.GetTicketNo())
	if err != nil {
		return nil, err
	}
	msgs, err := s.repo.MessagesAll(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.GetTicketReply{Ticket: toAdminPB(t)}
	for _, m := range msgs {
		reply.Messages = append(reply.Messages, toAdminMsgPB(m))
	}
	return reply, nil
}

// ReplyTicket 客服回覆（is_internal 內部備註不觸發狀態機；正常回復 open→processing）。
func (s *AdminTicketService) ReplyTicket(ctx context.Context, req *adminv1.AdminReplyRequest) (*emptypb.Empty, error) {
	t, err := s.repo.GetByNo(ctx, req.GetTicketNo())
	if err != nil {
		return nil, err
	}
	adminID := adminIDOf(ctx)
	if _, err := s.repo.CreateMessage(ctx, t.ID, "admin", adminID, req.GetContent(), nil, req.GetIsInternal()); err != nil {
		return nil, err
	}
	if !req.GetIsInternal() {
		// 首次客服回覆：open → processing（首響回填）
		if err := s.repo.MarkProcessingOnReply(ctx, t.ID); err != nil {
			return nil, err
		}
		s.publishReply(ctx, t, adminID)
	}
	return &emptypb.Empty{}, nil
}

// ResolveTicket 解決。
func (s *AdminTicketService) ResolveTicket(ctx context.Context, req *adminv1.TicketNoRequest) (*emptypb.Empty, error) {
	t, err := s.repo.GetByNo(ctx, req.GetTicketNo())
	if err != nil {
		return nil, err
	}
	if err := s.repo.Transition(ctx, t.ID, "resolved"); err != nil {
		return nil, err
	}
	s.publishReply(ctx, t, adminIDOf(ctx))
	return &emptypb.Empty{}, nil
}

// CloseTicket 關閉。
func (s *AdminTicketService) CloseTicket(ctx context.Context, req *adminv1.TicketNoRequest) (*emptypb.Empty, error) {
	t, err := s.repo.GetByNo(ctx, req.GetTicketNo())
	if err != nil {
		return nil, err
	}
	if err := s.repo.Transition(ctx, t.ID, "closed"); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// AutoCloseResolved resolved 超 7 天自動關閉（cron）。
func (s *AdminTicketService) AutoCloseResolved(ctx context.Context) {
	older := time.Now().UTC().Add(-7 * 24 * time.Hour)
	rows, err := s.repo.ListAutoCloseable(ctx, older, 100)
	if err != nil {
		return
	}
	for _, t := range rows {
		_ = s.repo.Transition(ctx, t.ID, "closed")
	}
}

func (s *AdminTicketService) publishReply(ctx context.Context, t *ent.Ticket, adminID uint64) {
	if s.outbox == nil {
		return
	}
	payload := map[string]any{"ticket_no": t.TicketNo, "by": "admin", "admin_id": adminID}
	raw, err := jsonMarshalTicket(payload)
	if err != nil {
		return
	}
	agg := "ticket:" + t.TicketNo
	_ = s.outbox.Write(ctx, "ticket", events.TicketReplied, agg, agg+":replied:"+adminIDKey(adminID), raw)
}

func toAdminPB(t *ent.Ticket) *adminv1.AdminTicket {
	p := &adminv1.AdminTicket{
		Id: t.ID, TicketNo: t.TicketNo, UserId: t.UserID,
		GuestContact: t.GuestContact, Type: string(t.Type),
		Priority: string(t.Priority), Status: string(t.Status),
		OrderId: t.OrderID, ProductId: t.ProductID,
		Satisfaction: int32(t.Satisfaction), CreatedAt: t.CreatedAt.Unix(),
	}
	if !t.FirstReplyAt.IsZero() {
		p.FirstReplyAt = t.FirstReplyAt.Unix()
	}
	if !t.SLADueAt.IsZero() {
		p.SlaDueAt = t.SLADueAt.Unix()
	}
	return p
}

func toAdminMsgPB(m *ent.TicketMessage) *adminv1.AdminTicketMessage {
	return &adminv1.AdminTicketMessage{
		Id: m.ID, SenderType: string(m.SenderType), SenderId: m.SenderID,
		Content: m.Content, Attachments: m.Attachments,
		IsInternal: m.IsInternal, CreatedAt: m.CreatedAt.Unix(),
	}
}

func adminIDOf(ctx context.Context) uint64 {
	if claims := identity.ClaimsFromContext(ctx); claims != nil {
		return claims.Subject
	}
	return 0
}

func adminIDKey(id uint64) string {
	if id == 0 {
		return "0"
	}
	return itoaU64(id)
}

func itoaU64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// orderIDOf 按訂單號反查 ID（工作台篩選；order_no → orders.id，直查 ent——ticket 無 order port 依賴，
// 屬跨模塊讀約束豁免？否——經 data 層直查同庫 orders 表為既定模式（supply 同款），保留。
func orderIDOf(ctx context.Context, s *AdminTicketService, orderNo string) uint64 {
	return s.repo.OrderIDByNo(ctx, orderNo)
}
