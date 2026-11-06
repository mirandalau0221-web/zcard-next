package order

import (
	"context"
	"strings"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderstatusevent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/payment"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// DeleteOrders 只從管理列表移除無用訂單，支付回調、對賬和冪等查詢保留原記錄。
// 整批事務提交，狀態、付款或租户校驗失敗時不刪除任何一條。
func (s *AdminOrderService) DeleteOrders(ctx context.Context, req *adminv1.DeleteOrdersRequest) (*emptypb.Empty, error) {
	if len(req.GetOrderNos()) == 0 || len(req.GetOrderNos()) > 100 {
		return nil, errors.BadRequest("order.INVALID_SELECTION", "每次請選擇 1 至 100 條訂單")
	}
	codes := make([]string, 0, len(req.GetOrderNos()))
	seen := make(map[string]bool)
	for _, code := range req.GetOrderNos() {
		code = strings.TrimSpace(code)
		if code == "" {
			return nil, errors.BadRequest("order.INVALID_SELECTION", "訂單號不能為空")
		}
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	subsiteID := tenancy.FromContext(ctx).SubsiteID
	err := data.Tx(ctx, s.data, func(ctx context.Context) error {
		client := data.Client(ctx, s.data)
		rows, err := client.Order.Query().Where(order.SubsiteID(subsiteID), order.OrderNoIn(codes...)).All(ctx)
		if err != nil {
			return err
		}
		if len(rows) != len(codes) {
			return errors.NotFound("order.NOT_FOUND", "部分訂單不存在，請刷新列表後重試")
		}
		for _, row := range rows {
			if row.AdminDeletedAt != nil {
				continue // 重複請求冪等，不重複創建刪除事件。
			}
			n, err := client.Order.Update().Where(
				order.ID(row.ID), order.SubsiteID(subsiteID), order.Version(row.Version),
				order.AdminDeletedAtIsNil(), order.PaidAtIsNil(),
				order.StatusIn(order.StatusCanceled, order.StatusExpired),
				order.Not(order.HasPaymentsWith(payment.StatusEQ(payment.StatusSuccess))),
				order.Not(order.HasDeliveries()), order.Not(order.HasRefunds()),
			).SetAdminDeletedAt(time.Now().UTC()).AddVersion(1).Save(ctx)
			if err != nil {
				return err
			}
			if n != 1 {
				return errors.BadRequest("order.CANNOT_DELETE", "僅可刪除已取消或已過期且無付款、發貨、退款記錄的訂單；請刷新後重試")
			}
			if _, err := client.OrderStatusEvent.Create().SetOrderID(row.ID).
				SetFromStatus(string(row.Status)).SetToStatus(string(row.Status)).
				SetEvent("admin_deleted").SetOperator(orderstatusevent.OperatorAdmin).
				SetReason("管理員從訂單列表刪除無用訂單，保留關聯記錄").Save(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
