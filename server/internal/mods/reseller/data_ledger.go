package reseller

// 賬本補全：提現（FIFO 消費 + 部分拆行 + 冪等）+ 退款逆向（負行 + 負債優先抵扣）。
//
// 賬本模型：ledger_entries 行即餘額（有符號）；狀態流轉：
// available → locked（withdraw_lock，部分提現拆行）→ withdrawn（打款）/ available（駁回）
// order.refunded → refund_deduct 負行（available 直接扣減；不足 → pending 負債，
// 後續利潤優先抵扣——與 affiliate 負債態同構）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellerledgerentry"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// ErrWithdrawLocked 提現鎖定衝突（冪等鍵重放）。
var ErrWithdrawLocked = errors.New("reseller.WITHDRAW_LOCKED: 提現單已鎖定")

// WithdrawLock 提現鎖定（FIFO 消費 available 正行；行額度不足時拆行）。
// ref 為提現單號（冪等鍵 withdraw_lock:<ref>；重放直接返回已鎖定）。
func (r *ResellerRepo) WithdrawLock(ctx context.Context, subsiteID uint64, amount int64, ref string) error {
	if amount <= 0 || !money.ValidCents(amount) {
		return fmt.Errorf("reseller.INVALID_AMOUNT")
	}
	return data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		// 冪等：同 ref 已鎖定過 → ACK
		exists, err := client.ResellerLedgerEntry.Query().
			Where(resellerledgerentry.IdempotencyKeyEQ("withdraw_lock:" + ref)).Exist(txCtx)
		if err != nil {
			return err
		}
		if exists {
			return ErrWithdrawLocked
		}
		// FIFO 取 available 正行（先到先得）
		rows, err := client.ResellerLedgerEntry.Query().
			Where(
				resellerledgerentry.SubsiteIDEQ(subsiteID),
				resellerledgerentry.StatusEQ(resellerledgerentry.StatusAvailable),
				resellerledgerentry.AmountGT(0),
			).
			Order(ent.Asc(resellerledgerentry.FieldID)).
			All(txCtx)
		if err != nil {
			return err
		}
		remaining := amount
		now := time.Now().UTC()
		anchorCreated := false
		for _, row := range rows {
			if remaining <= 0 {
				break
			}
			if row.Amount <= remaining {
				// 整行鎖定（remark 標記提現單號，打款/駁回按 ref 關聯）
				if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
					Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
					SetStatus(resellerledgerentry.StatusLocked).
					SetRemark("withdraw_ref:" + ref).
					Save(txCtx); err != nil {
					return err
				}
				remaining -= row.Amount
				continue
			}
			// 部分拆行：原行剩 (行額-剩餘)，新建鎖定行（amount=剩餘）
			if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
				Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
				SetAmount(row.Amount - remaining).
				Save(txCtx); err != nil {
				return err
			}
			if _, err := client.ResellerLedgerEntry.Create().
				SetSubsiteID(subsiteID).
				SetType("withdraw_lock").
				SetAmount(remaining).
				SetStatus(resellerledgerentry.StatusLocked).
				SetIdempotencyKey("withdraw_lock:" + ref).
				SetCreatedAt(now).
				SetRemark("withdraw_ref:" + ref).
				Save(txCtx); err != nil {
				return err
			}
			anchorCreated = true
			remaining = 0
		}
		if remaining > 0 {
			return fmt.Errorf("reseller.INSUFFICIENT_BALANCE")
		}
		// 無拆行時也需冪等錨點行（標記提現單已鎖定；與拆行冪等鍵互斥）
		if !anchorCreated {
			if _, err := client.ResellerLedgerEntry.Create().
				SetSubsiteID(subsiteID).
				SetType("withdraw_lock").
				SetAmount(0).
				SetStatus(resellerledgerentry.StatusLocked).
				SetIdempotencyKey("withdraw_lock:" + ref).
				SetCreatedAt(now).
				SetRemark("withdraw_ref:" + ref).
				Save(txCtx); err != nil {
				return err
			}
		}
		// 緩存：available -amount / locked +amount
		return r.updateBalance(txCtx, subsiteID, -amount, amount, 0)
	})
}

// WithdrawPaid 打款（鎖定行 → withdrawn；金額從 locked 出賬）。
func (r *ResellerRepo) WithdrawPaid(ctx context.Context, subsiteID uint64, ref string) error {
	client := data.Client(ctx, r.data)
	rows, err := client.ResellerLedgerEntry.Query().
		Where(
			resellerledgerentry.SubsiteIDEQ(subsiteID),
			resellerledgerentry.StatusEQ(resellerledgerentry.StatusLocked),
			resellerledgerentry.RemarkHasPrefix("withdraw_ref:"+ref),
		).All(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("reseller.WITHDRAW_NOT_LOCKED")
	}
	var paidTotal int64
	for _, row := range rows {
		if row.Amount == 0 {
			continue // 錨點行
		}
		if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
			Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
			SetStatus(resellerledgerentry.StatusWithdrawn).
			Save(ctx); err != nil {
			return err
		}
		paidTotal += row.Amount
	}
	return r.updateBalance(ctx, subsiteID, 0, -paidTotal, 0)
}

// WithdrawReject 駁回（鎖定行 → available 回餘額）。
func (r *ResellerRepo) WithdrawReject(ctx context.Context, subsiteID uint64, ref string) error {
	client := data.Client(ctx, r.data)
	rows, err := client.ResellerLedgerEntry.Query().
		Where(
			resellerledgerentry.SubsiteIDEQ(subsiteID),
			resellerledgerentry.StatusEQ(resellerledgerentry.StatusLocked),
			resellerledgerentry.RemarkHasPrefix("withdraw_ref:"+ref),
		).All(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("reseller.WITHDRAW_NOT_LOCKED")
	}
	var rejectTotal int64
	for _, row := range rows {
		status := resellerledgerentry.StatusAvailable
		if row.Amount == 0 {
			status = resellerledgerentry.StatusWithdrawn // 錨點行直接作廢
		} else {
			rejectTotal += row.Amount
		}
		if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
			Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
			SetStatus(status).
			Save(ctx); err != nil {
			return err
		}
	}
	return r.updateBalance(ctx, subsiteID, rejectTotal, -rejectTotal, 0)
}

// RefundDeduct 退款扣回（冪等鍵 refund_deduct:<orderID>）。
// ratio 萬分比（0=全額）。餘額不足 → 負行轉 pending 負債。
func (r *ResellerRepo) RefundDeduct(ctx context.Context, subsiteID, orderID uint64, profit int64, ratio int64, references ...string) error {
	if ratio <= 0 {
		ratio = 10000
	}
	key := fmt.Sprintf("refund_deduct:%d", orderID)
	if len(references) > 0 {
		key = references[0]
	}
	clawback := data.Prorate(profit, ratio, 10000)
	if clawback <= 0 {
		return nil
	}
	return data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		exists, err := client.ResellerLedgerEntry.Query().
			Where(resellerledgerentry.IdempotencyKeyEQ(key)).Exist(txCtx)
		if err != nil {
			return err
		}
		if exists {
			return nil // 冪等 ACK
		}
		// 可用口徑（available + pending 正數）累計
		availRows, err := client.ResellerLedgerEntry.Query().
			Where(
				resellerledgerentry.SubsiteIDEQ(subsiteID),
				resellerledgerentry.StatusEQ(resellerledgerentry.StatusAvailable),
				resellerledgerentry.AmountGT(0),
			).
			Order(ent.Asc(resellerledgerentry.FieldID)).
			All(txCtx)
		if err != nil {
			return err
		}
		var availTotal int64
		for _, row := range availRows {
			availTotal += row.Amount
		}
		now := time.Now().UTC()
		if availTotal >= clawback {
			// 足額：FIFO 逐行扣減
			remaining := clawback
			for _, row := range availRows {
				if remaining <= 0 {
					break
				}
				if row.Amount <= remaining {
					if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
						Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
						SetStatus(resellerledgerentry.StatusWithdrawn).
						Save(txCtx); err != nil {
						return err
					}
					remaining -= row.Amount
					continue
				}
				if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
					Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
					SetAmount(row.Amount - remaining).
					Save(txCtx); err != nil {
					return err
				}
				remaining = 0
			}
			// 扣回記錄行（負）
			if _, err := client.ResellerLedgerEntry.Create().
				SetSubsiteID(subsiteID).
				SetOrderID(orderID).
				SetType("refund_deduct").
				SetAmount(-clawback).
				SetStatus(resellerledgerentry.StatusWithdrawn).
				SetIdempotencyKey(key).
				SetCreatedAt(now).
				SetRemark("退款利潤扣回").
				Save(txCtx); err != nil {
				return err
			}
			return r.updateBalance(txCtx, subsiteID, -clawback, 0, 0)
		}
		// 不足：負債態（負行 pending，後續利潤優先抵扣）
		if _, err := client.ResellerLedgerEntry.Create().
			SetSubsiteID(subsiteID).
			SetOrderID(orderID).
			SetType("refund_deduct").
			SetAmount(-(clawback - availTotal)).
			SetStatus(resellerledgerentry.StatusPending).
			SetIdempotencyKey(key).
			SetCreatedAt(now).
			SetRemark("退款利潤扣回（負債，待利潤抵扣）").
			Save(txCtx); err != nil {
			return err
		}
		// 足額部分照扣
		if availTotal > 0 {
			remaining := availTotal
			for _, row := range availRows {
				if remaining <= 0 {
					break
				}
				if row.Amount <= remaining {
					if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
						Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
						SetStatus(resellerledgerentry.StatusWithdrawn).
						Save(txCtx); err != nil {
						return err
					}
					remaining -= row.Amount
					continue
				}
				if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
					Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
					SetAmount(row.Amount - remaining).
					Save(txCtx); err != nil {
					return err
				}
				remaining = 0
			}
		}
		// 緩存：available 扣減已扣部分；負債增差額
		return r.updateBalance(txCtx, subsiteID, -availTotal, 0, clawback-availTotal)
	})
}

// settleDebt 利潤入賬前優先抵扣 pending 負行（負債態）。
// 返回抵扣後剩餘可入賬金額。
func (r *ResellerRepo) settleDebt(ctx context.Context, subsiteID uint64, amount int64) (int64, error) {
	client := data.Client(ctx, r.data)
	rows, err := client.ResellerLedgerEntry.Query().
		Where(
			resellerledgerentry.SubsiteIDEQ(subsiteID),
			resellerledgerentry.StatusEQ(resellerledgerentry.StatusPending),
			resellerledgerentry.AmountLT(0),
		).
		Order(ent.Asc(resellerledgerentry.FieldID)).
		All(ctx)
	if err != nil {
		return amount, err
	}
	remaining := amount
	for _, row := range rows {
		if remaining <= 0 {
			break
		}
		debt := -row.Amount
		if debt <= remaining {
			// 負債清空
			if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
				Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
				SetStatus(resellerledgerentry.StatusWithdrawn).
				Save(ctx); err != nil {
				return amount, err
			}
			remaining -= debt
			continue
		}
		// 部分抵扣：負債減少
		if _, err := client.ResellerLedgerEntry.UpdateOneID(row.ID).
			Where(resellerledgerentry.StatusEQ(row.Status), resellerledgerentry.Amount(row.Amount)).
			SetAmount(-(debt - remaining)).
			Save(ctx); err != nil {
			return amount, err
		}
		remaining = 0
	}
	// 負債緩存隨抵扣減少（由調用方在入賬時一併 updateBalance）
	return remaining, nil
}

var _ = json.Marshal
