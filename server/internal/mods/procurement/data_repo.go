package procurement

// 採購單倉儲（/）：兩表 CRUD + 狀態機 CAS（非法遷移拒絕）。
//
// 狀態機（）：
// pending → submitted（受理，記 upstream_order_id）
// pending → fulfilled（同步拿貨成功）
// pending → rejected（永久錯誤：無映射/下架/餘額不足）
// submitted → polling → fulfilled / rejected
// rejected → refunding → refunded（auto_refund 失敗策略）| manual（人工終態）
// polling → manual（24h 卡死轉人工）

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementorder"
)

// 哨兵錯誤。
var (
	ErrNotFound          = errors.New("procurement: 採購單不存在")
	ErrTransitionDenied  = errors.New("procurement: 非法狀態遷移")
	ErrConcurrentUpdate  = errors.New("procurement: 併發更新衝突")
	ErrDuplicatePurchase = errors.New("procurement: 該訂單項已存在採購單")
)

// ProcureRepo 採購倉儲。
type ProcureRepo struct {
	data *data.Data
}

// NewProcureRepo 構造。
func NewProcureRepo(d *data.Data) *ProcureRepo { return &ProcureRepo{data: d} }

// 合法遷移表（併發三通道匯聚同一終態只生效一次：CAS + 遷移表雙保險）。
var allowedTransitions = map[string]map[string]bool{
	"pending":   {"manual": true, "submitted": true, "fulfilled": true, "rejected": true, "polling": true},
	"submitted": {"polling": true, "fulfilled": true, "rejected": true, "refunding": true, "manual": true},
	"polling":   {"fulfilled": true, "rejected": true, "refunding": true, "manual": true},
	"rejected":  {"refunding": true, "manual": true, "refunded": true},
	"refunding": {"refunded": true, "manual": true},
}

// transition 狀態機 CAS（樂觀鎖 + 遷移表；併發到達同一終態只生效一次）。
func (r *ProcureRepo) transition(ctx context.Context, id uint64, from, to string, mutate func(upd *ent.ProcurementOrderUpdateOne) *ent.ProcurementOrderUpdateOne) error {
	cur, err := data.Client(ctx, r.data).ProcurementOrder.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	if string(cur.Status) != from {
		return ErrTransitionDenied
	}
	if !allowedTransitions[from][to] {
		return fmt.Errorf("%w: %s → %s", ErrTransitionDenied, from, to)
	}
	upd := data.Client(ctx, r.data).ProcurementOrder.UpdateOneID(id).
		Where(procurementorder.StatusEQ(procurementorder.Status(from))).
		SetStatus(procurementorder.Status(to))
	if mutate != nil {
		upd = mutate(upd)
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return err
	}
	if saved == nil {
		return ErrConcurrentUpdate // 併發已遷移
	}
	return nil
}

// CreatePending 創建採購單（冪等：order_item_id 唯一 → 重複返回 ErrDuplicatePurchase）。
func (r *ProcureRepo) CreatePending(ctx context.Context, orderItemID, connectionID uint64, productCode string, quantity int32, failStrategy string, traceID string) (*ent.ProcurementOrder, error) {
	var p *ent.ProcurementOrder
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		// ACG checks request_no across ALL downstream accounts and stores CHAR(19).
		// Persist 95 random bits once; local order_item_id uniqueness independently
		// prevents concurrent payment events from creating another purchase.
		dedupe := rand.Text()[:19]
		var err error
		p, err = data.Client(ctx, r.data).ProcurementOrder.Create().
			SetOrderItemID(orderItemID).
			SetConnectionID(connectionID).
			SetStatus(procurementorder.StatusPending).
			SetFailStrategy(procurementorder.FailStrategy(failStrategy)).
			SetDedupeKey(dedupe).
			SetTraceID(traceID).
			Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return ErrDuplicatePurchase
			}
			return err
		}
		// 採購項（sku 快照）
		_, err = data.Client(ctx, r.data).ProcurementItem.Create().
			SetProcurementID(p.ID).
			SetUpstreamSku(productCode).
			SetQuantity(quantity).
			Save(ctx)
		if err != nil {
			return err
		}
		return nil
	})
	return p, err
}

// Get 採購單詳情。
func (r *ProcureRepo) Get(ctx context.Context, id uint64) (*ent.ProcurementOrder, error) {
	p, err := data.Client(ctx, r.data).ProcurementOrder.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// GetByDownstreamOrderNo 按下游單號查採購單（回調接收定位用； E）。
func (r *ProcureRepo) GetByDownstreamOrderNo(ctx context.Context, downstreamOrderNo string) (*ent.ProcurementOrder, error) {
	po, err := data.Client(ctx, r.data).ProcurementOrder.Query().
		Where(procurementorder.DedupeKeyEQ(downstreamOrderNo)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return po, nil
}

// GetByOrderItem 按訂單項查採購單（冪等判據）。
func (r *ProcureRepo) GetByOrderItem(ctx context.Context, orderItemID uint64) (*ent.ProcurementOrder, error) {
	p, err := data.Client(ctx, r.data).ProcurementOrder.Query().
		Where(procurementorder.OrderItemID(orderItemID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// List 採購單分頁（按狀態過濾）。
func (r *ProcureRepo) List(ctx context.Context, status string, page, pageSize int, orderNo ...string) ([]*ent.ProcurementOrder, int, error) {
	q := data.Client(ctx, r.data).ProcurementOrder.Query().Order(ent.Desc(procurementorder.FieldID))
	if len(orderNo) > 0 && strings.TrimSpace(orderNo[0]) != "" {
		c := data.Client(ctx, r.data)
		orders, err := c.Order.Query().Where(order.OrderNoEQ(strings.TrimSpace(orderNo[0]))).IDs(ctx)
		if err != nil {
			return nil, 0, err
		}
		ids, err := c.OrderItem.Query().Where(orderitem.OrderIDIn(orders...)).IDs(ctx)
		if err != nil {
			return nil, 0, err
		}
		q.Where(procurementorder.OrderItemIDIn(ids...))
	}
	if status != "" {
		q = q.Where(procurementorder.StatusEQ(procurementorder.Status(status)))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	return rows, total, err
}

// ListPollable polling/submitted 單（巡檢拉取；INDEX(status, last_poll_at) 命中）。
func (r *ProcureRepo) ListPollable(ctx context.Context, limit int) ([]*ent.ProcurementOrder, error) {
	return data.Client(ctx, r.data).ProcurementOrder.Query().
		Where(procurementorder.Or(
			procurementorder.StatusIn(procurementorder.StatusSubmitted, procurementorder.StatusPolling),
			procurementorder.And(procurementorder.StatusEQ(procurementorder.StatusPending), procurementorder.CreatedAtLT(time.Now().UTC().Add(-5*time.Minute))),
		)).
		Order(ent.Asc(procurementorder.FieldLastPollAt)).
		Limit(limit).
		All(ctx)
}

// MarkSubmitted 受理（pending → submitted，記 upstream_order_id + 退避）。
func (r *ProcureRepo) MarkSubmitted(ctx context.Context, id uint64, upstreamOrderID string, nextRetryAt time.Time) error {
	return r.transition(ctx, id, "pending", "submitted", func(upd *ent.ProcurementOrderUpdateOne) *ent.ProcurementOrderUpdateOne {
		return upd.SetUpstreamOrderID(upstreamOrderID).SetNextRetryAt(nextRetryAt)
	})
}

// MarkPolling submitted/pending → polling（輪詢前）。
func (r *ProcureRepo) MarkPolling(ctx context.Context, id uint64) error {
	return r.transition(ctx, id, "submitted", "polling", func(upd *ent.ProcurementOrderUpdateOne) *ent.ProcurementOrderUpdateOne {
		return upd.SetLastPollAt(time.Now().UTC())
	})
}

// MarkFulfilled 採購完成（submitted/polling/pending → fulfilled）。
func (r *ProcureRepo) MarkFulfilled(ctx context.Context, id uint64) error {
	// 允許 pending/submitted/polling 三個來源（同步拿貨/輪詢/巡檢匯聚）
	for _, from := range []string{"pending", "submitted", "polling"} {
		err := r.transition(ctx, id, from, "fulfilled", nil)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrTransitionDenied) && !errors.Is(err, ErrConcurrentUpdate) {
			return err
		}
	}
	return ErrTransitionDenied
}

// MarkRejected 永久錯誤（→ 失敗策略分流）。
func (r *ProcureRepo) MarkRejected(ctx context.Context, id uint64, reason string) error {
	for _, from := range []string{"pending", "submitted", "polling"} {
		err := r.transition(ctx, id, from, "rejected", func(upd *ent.ProcurementOrderUpdateOne) *ent.ProcurementOrderUpdateOne {
			return upd.SetLastError(reason)
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrTransitionDenied) && !errors.Is(err, ErrConcurrentUpdate) {
			return err
		}
	}
	return ErrTransitionDenied
}

// MarkRefunding rejected → refunding（auto_refund 分流開始）。
func (r *ProcureRepo) MarkRefunding(ctx context.Context, id uint64) error {
	return r.transition(ctx, id, "rejected", "refunding", nil)
}

// MarkRefunded refunding → refunded（上游退款成功）。
func (r *ProcureRepo) MarkRefunded(ctx context.Context, id uint64, upstreamRefundID string) error {
	return r.transition(ctx, id, "refunding", "refunded", func(upd *ent.ProcurementOrderUpdateOne) *ent.ProcurementOrderUpdateOne {
		return upd.SetUpstreamRefundID(upstreamRefundID)
	})
}

// MarkManual → manual（人工終態：失敗策略分流 / 24h 卡死）。
func (r *ProcureRepo) MarkManual(ctx context.Context, id uint64, reason string) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		client := data.Client(ctx, r.data)
		po, err := client.ProcurementOrder.Get(ctx, id)
		if err != nil {
			return err
		}
		if err := r.lockManualRecovery(ctx, po); err != nil {
			return err
		}
		if po.Status == procurementorder.StatusFulfilled {
			n, err := client.ProcurementOrder.Update().Where(procurementorder.ID(id), procurementorder.StatusEQ(po.Status)).SetStatus(procurementorder.StatusManual).SetLastError(reason).Save(ctx)
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrConcurrentUpdate
			}
		} else if err := r.markManual(ctx, id, reason); err != nil {
			return err
		}
		it, err := client.OrderItem.Get(ctx, po.OrderItemID)
		if ent.IsNotFound(err) {
			return nil
		} // Keep orphaned historic procurements actionable.
		if err != nil {
			return err
		}
		o, err := client.Order.Get(ctx, it.OrderID)
		if err != nil {
			return err
		}
		switch o.Status {
		case order.StatusPaid, order.StatusFulfilling, order.StatusPartiallyDelivered:
		default:
			return nil
		}
		if err := client.OrderItem.UpdateOneID(it.ID).SetFulfillmentStatus("manual").Exec(ctx); err != nil {
			return err
		}
		return client.OrderStatusEvent.Create().SetOrderID(o.ID).SetFromStatus(string(o.Status)).SetToStatus(string(o.Status)).SetEvent("fulfillment_manual").SetOperator("system").SetReason(reason).Exec(ctx)
	})
}

func (r *ProcureRepo) markManual(ctx context.Context, id uint64, reason string) error {
	for _, from := range []string{"pending", "rejected", "submitted", "polling", "refunding"} {
		err := r.transition(ctx, id, from, "manual", func(upd *ent.ProcurementOrderUpdateOne) *ent.ProcurementOrderUpdateOne {
			return upd.SetLastError(reason)
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrTransitionDenied) && !errors.Is(err, ErrConcurrentUpdate) {
			return err
		}
	}
	return ErrTransitionDenied
}

// BumpRetry 退避推進（retry_count+1，next_retry_at 後移）。
func (r *ProcureRepo) BumpRetry(ctx context.Context, id uint64, nextRetryAt time.Time, lastErr string) error {
	p, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	_, err = data.Client(ctx, r.data).ProcurementOrder.UpdateOneID(id).
		SetRetryCount(p.RetryCount + 1).
		SetNextRetryAt(nextRetryAt).
		SetLastError(lastErr).
		Save(ctx)
	return err
}

// AttachReceivedContent 落採購項密文（到手即加密 ：received_content 全密文）。
func (r *ProcureRepo) AttachReceivedContent(ctx context.Context, procurementID uint64, contents [][]byte) error {
	item, err := data.Client(ctx, r.data).ProcurementItem.Query().
		Where(procurementitem.ProcurementID(procurementID)).
		Only(ctx)
	if err != nil {
		return err
	}
	// 單 SKU 採購項：多卡密以行序列化存儲（JSON 數組 of base64）
	rows := make([]string, 0, len(contents))
	for _, c := range contents {
		rows = append(rows, encodeB64(c))
	}
	_, err = data.Client(ctx, r.data).ProcurementItem.UpdateOneID(item.ID).
		SetReceivedContent(rows).
		Save(ctx)
	return err
}

// ReceivedContent 讀採購項密文（[][]byte）。
func (r *ProcureRepo) ReceivedContent(ctx context.Context, procurementID uint64) ([][]byte, error) {
	item, err := data.Client(ctx, r.data).ProcurementItem.Query().
		Where(procurementitem.ProcurementID(procurementID)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(item.ReceivedContent))
	for _, s := range item.ReceivedContent {
		out = append(out, decodeB64(s))
	}
	return out, nil
}

// EnsureTraceID 採購單 trace_id（排障貫穿）。
func (r *ProcureRepo) EnsureTraceID(ctx context.Context, id uint64, traceID string) error {
	if traceID == "" {
		return nil
	}
	_, err := data.Client(ctx, r.data).ProcurementOrder.UpdateOneID(id).SetTraceID(traceID).Save(ctx)
	return err
}

// ItemByProcurement 採購項（sku 快照/成本）。
func (r *ProcureRepo) ItemByProcurement(ctx context.Context, procurementID uint64) (*ent.ProcurementItem, error) {
	item, err := data.Client(ctx, r.data).ProcurementItem.Query().
		Where(procurementitem.ProcurementID(procurementID)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// OrderItemInfo 訂單項上下文（order_id / product_id / subsite_id——交付出口與加密 AAD 需要）。
type OrderItemInfo struct {
	OrderID   uint64
	ProductID uint64
	SubsiteID uint64
	Quantity  int32
}

// OrderItemInfo 按 order_item_id 查訂單項上下文。
func (r *ProcureRepo) OrderItemInfo(ctx context.Context, orderItemID uint64) (*OrderItemInfo, error) {
	it, err := data.Client(ctx, r.data).OrderItem.Get(ctx, orderItemID)
	if err != nil {
		return nil, err
	}
	return &OrderItemInfo{OrderID: it.OrderID, ProductID: it.ProductID, SubsiteID: it.SubsiteID, Quantity: it.Quantity}, nil
}

// OrderIDOf 採購單 → 訂單 ID（失敗策略退款需要；經 order_item 反查）。
func (r *ProcureRepo) OrderIDOf(ctx context.Context, procurementID uint64) (uint64, error) {
	po, err := r.Get(ctx, procurementID)
	if err != nil {
		return 0, err
	}
	it, err := data.Client(ctx, r.data).OrderItem.Get(ctx, po.OrderItemID)
	if err != nil {
		return 0, err
	}
	return it.OrderID, nil
}
