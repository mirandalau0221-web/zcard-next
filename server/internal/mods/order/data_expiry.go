package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderstatusevent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/predicate"
	paymentport "github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
)

const slowPaymentWaitingReason = "慢支付確認等待，支付截止後最多順延15分鐘"
const legacySlowPaymentReviewReason = "慢支付尚未確認，請核對到賬狀態"

// Recover only the old pending-slow-payment hold. Other review causes remain manual.
func (uc *OrderUsecase) expiryReviewFilter(now time.Time) predicate.Order {
	if uc.SlowPay == nil {
		return order.ExpiryReviewEQ(false)
	}
	return order.Or(order.ExpiryReviewEQ(false), order.And(
		order.ExpiryReasonEQ(legacySlowPaymentReviewReason),
		order.ExpiredAtLT(now.Add(-paymentport.SlowPaymentGracePeriod)),
	))
}

func (uc *OrderUsecase) CancelOrder(ctx context.Context, no, reason, operator string, operatorID uint64) error {
	return uc.cancelOrder(ctx, no, reason, operator, operatorID, false)
}

// All cancellation entry points share the transaction and status/version guard.
func (uc *OrderUsecase) cancelOrder(ctx context.Context, no, reason, operator string, operatorID uint64, expiredOnly bool) error {
	return data.Tx(ctx, uc.Data, func(ctx context.Context) error {
		client := data.Client(ctx, uc.Data)
		o, err := client.Order.Query().Where(order.OrderNo(no)).Only(ctx)
		if err != nil {
			return err
		}
		if o.Status == order.StatusCanceled {
			return nil
		}
		if o.Status != order.StatusPendingPayment {
			return fmt.Errorf("order.CANNOT_CANCEL: %s", o.Status)
		}
		now := time.Now().UTC()
		q := client.Order.Update().Where(order.ID(o.ID), order.StatusEQ(order.StatusPendingPayment), order.VersionEQ(o.Version))
		if expiredOnly {
			if o.ExpiredAt.IsZero() || !o.ExpiredAt.Before(now) {
				return fmt.Errorf("order.NOT_EXPIRED")
			}
			q.Where(order.ExpiredAtLT(now), uc.expiryReviewFilter(now), order.Or(order.ExpiryRetryAtIsNil(), order.ExpiryRetryAtLTE(now)))
		}
		if o.CommerceVersion == 1 {
			q.SetShippingStatus("canceled")
		}
		n, err := q.SetStatus(order.StatusCanceled).SetClosedAt(now).SetExpiryReview(false).SetExpiryReason("").ClearExpiryRetryAt().SetVersion(o.Version + 1).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("order.CONCURRENT_UPDATE")
		}
		if _, err = client.OrderStatusEvent.Create().SetOrderID(o.ID).SetFromStatus(string(o.Status)).SetToStatus("canceled").SetEvent("canceled").SetOperator(orderstatusevent.Operator(operator)).SetOperatorID(operatorID).SetReason(reason).Save(ctx); err != nil {
			return err
		}
		if err = uc.settleFlashReservations(ctx, o, false); err != nil {
			return err
		}
		if err = data.ReleasePhysicalStock(ctx, uc.Data, o.ID, reason); err != nil {
			return err
		}
		if err = uc.Inv.Release(ctx, o.ID); err != nil {
			return err
		}
		if uc.Coupon != nil {
			return uc.Coupon.ReturnByOrder(ctx, o.ID)
		}
		return nil
	})
}

// Retry metadata never changes the original payment deadline. Query errors stop
// after three checks; pending slow payments use the bounded confirmation grace.
func (uc *OrderUsecase) deferExpiry(ctx context.Context, o *ent.Order, reason string, review bool) error {
	return data.Tx(ctx, uc.Data, func(ctx context.Context) error {
		client := data.Client(ctx, uc.Data)
		attempts := o.ExpiryAttempts + 1
		if o.ExpiryReason != reason {
			attempts = 1
		}
		review = review || (reason != slowPaymentWaitingReason && attempts >= 3)
		retryAt := time.Now().UTC().Add(5 * time.Minute)
		if deadline := o.ExpiredAt.Add(paymentport.SlowPaymentGracePeriod); reason == slowPaymentWaitingReason && deadline.Before(retryAt) {
			retryAt = deadline
		}
		n, err := client.Order.Update().Where(order.ID(o.ID), order.StatusEQ(order.StatusPendingPayment), order.VersionEQ(o.Version)).
			SetExpiryRetryAt(retryAt).SetExpiryAttempts(attempts).SetExpiryReview(review).SetExpiryReason(reason).SetVersion(o.Version + 1).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("order.CONCURRENT_UPDATE")
		}
		_, err = client.OrderStatusEvent.Create().SetOrderID(o.ID).SetFromStatus(string(o.Status)).SetToStatus(string(o.Status)).SetEvent("expiry_check").SetOperator(orderstatusevent.OperatorSystem).SetReason(reason).Save(ctx)
		return err
	})
}

func (uc *OrderUsecase) ExpireOrder(ctx context.Context) (int, error) {
	client := data.Client(ctx, uc.Data)
	now := time.Now().UTC()
	count, candidates, deferred, failed := 0, 0, 0, 0
	var errs []error
	var cursor uint64
	defer func() {
		slog.InfoContext(ctx, "order.expiry.scan", "candidates", candidates, "canceled", count, "deferred", deferred, "failed", failed)
	}()
	// Keyset pagination keeps a failing order from starving later batches.
	for batch := 0; batch < 10; batch++ {
		rows, err := client.Order.Query().Where(order.IDGT(cursor), order.StatusEQ(order.StatusPendingPayment), uc.expiryReviewFilter(now),
			order.Or(order.ExpiredAtIsNil(), order.ExpiredAtLT(now)), order.Or(order.ExpiryRetryAtIsNil(), order.ExpiryRetryAtLTE(now))).Order(ent.Asc(order.FieldID)).Limit(500).All(ctx)
		if err != nil {
			return count, errors.Join(append(errs, err)...)
		}
		for _, o := range rows {
			if err := ctx.Err(); err != nil {
				return count, errors.Join(append(errs, err)...)
			}
			cursor = o.ID
			candidates++
			reason, review := "", false
			if o.ExpiredAt.IsZero() {
				reason, review = "缺少支付截止時間，請核對歷史訂單", true
			} else if uc.SlowPay != nil {
				slow, checkErr := uc.SlowPay.HasPendingSlowPayment(ctx, o.ID)
				if checkErr != nil {
					reason = "支付狀態查詢失敗，請核對支付流水"
					failed++
					errs = append(errs, fmt.Errorf("order %s payment check: %w", o.OrderNo, checkErr))
				} else if slow {
					reason = slowPaymentWaitingReason
				}
			}
			if reason != "" {
				if err := uc.deferExpiry(ctx, o, reason, review); err != nil {
					failed++
					errs = append(errs, err)
				} else {
					deferred++
				}
				continue
			}
			if err := uc.cancelOrder(ctx, o.OrderNo, "超時未支付", "system", 0, true); err != nil {
				failed++
				errs = append(errs, fmt.Errorf("order %s cancel: %w", o.OrderNo, err))
				if e := uc.deferExpiry(ctx, o, "取消失敗，請檢查庫存釋放和數據庫日誌", false); e != nil {
					errs = append(errs, e)
				}
			} else {
				count++
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	return count, errors.Join(errs...)
}
