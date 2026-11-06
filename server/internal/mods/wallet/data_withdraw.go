package wallet

// 提現執行（ ）：
// 申請：available→locked（Lock）+ 手續費 + 收款方式白名單快照 → withdrawals(pending)
// 審核：通過→approved（鎖定保持）；駁回→rejected（Unlock 回餘額）
// 打款：人工打款模式 approved→paid（locked 扣減 + 流水 type=withdraw）
// 紀律：金額服務端裁決（鐵律 16）；鎖定/解鎖/扣減全部走賬户 CAS + 流水，可重算。

import (
	"context"
	"fmt"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/withdrawal"
)

// CommissionConsumer 佣金消耗端口（打款 FIFO；affiliate.CommissionRepo 實現，
// 通道 A——wallet 不直接依賴 affiliate 包）。
type CommissionConsumer interface {
	ConsumeAvailableFIFO(ctx context.Context, userID uint64, amount int64) error
}

// SetCommissionConsumer 裝配期注入（wire 後手工/構造器）。
func (r *WalletRepoImpl) SetCommissionConsumer(c CommissionConsumer) { r.commissions = c }

// CreateWithdrawal 提現申請（佣金提現：凍結口徑 = pending/approved 提現單金額
// 合計——不鎖錢包餘額、不逐行鎖佣金行；打款時 FIFO 消耗佣金置 withdrawn）。
// 可提校驗由 service 層完成（stats.available − frozen ≥ amount）。
func (r *WalletRepoImpl) CreateWithdrawal(ctx context.Context, userID uint64, amount, fee int64, method map[string]any) (*ent.Withdrawal, error) {
	return data.Client(ctx, r.data).Withdrawal.Create().
		SetUserID(userID).
		SetAmount(amount).
		SetFee(fee).
		SetMethod(method).
		SetStatus(withdrawal.StatusPending).
		Save(ctx)
}

// ListWithdrawalsByUser 本人提現記錄（按 ID 倒序分頁）。
func (r *WalletRepoImpl) ListWithdrawalsByUser(ctx context.Context, userID uint64, page, size int) ([]*ent.Withdrawal, int64, error) {
	q := data.Client(ctx, r.data).Withdrawal.Query().
		Where(withdrawal.UserID(userID)).
		Order(ent.Desc(withdrawal.FieldID))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, int64(total), err
}

// ListWithdrawals 提現單列表（狀態篩選；按 ID 倒序）。
func (r *WalletRepoImpl) ListWithdrawals(ctx context.Context, status string, page, size int) ([]*ent.Withdrawal, int64, error) {
	q := data.Client(ctx, r.data).Withdrawal.Query().
		Order(ent.Desc(withdrawal.FieldID))
	if status != "" {
		q = q.Where(withdrawal.StatusEQ(withdrawal.Status(status)))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, int64(total), err
}

// GetWithdrawal 單查。
func (r *WalletRepoImpl) GetWithdrawal(ctx context.Context, id uint64) (*ent.Withdrawal, error) {
	return data.Client(ctx, r.data).Withdrawal.Get(ctx, id)
}

// ReviewWithdrawal 審核：通過→approved；駁回→rejected（佣金凍結口徑由提現單
// 狀態自動釋放——rejected 不再計入 frozen，無需回滾動作）。
func (r *WalletRepoImpl) ReviewWithdrawal(ctx context.Context, id uint64, approve bool, reason string, reviewerID uint64) (*ent.Withdrawal, error) {
	var w *ent.Withdrawal
	err := data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		w, _ = client.Withdrawal.Get(txCtx, id)
		if w == nil {
			return fmt.Errorf("wallet.WITHDRAWAL_NOT_FOUND")
		}
		if w.Status != withdrawal.StatusPending {
			return fmt.Errorf("wallet.WITHDRAWAL_NOT_PENDING")
		}
		now := time.Now().UTC()
		if approve {
			w, _ = client.Withdrawal.UpdateOneID(id).
				SetStatus(withdrawal.StatusApproved).
				SetReviewedBy(reviewerID).
				SetReviewedAt(now).
				Save(txCtx)
			return nil
		}
		// 駁回：凍結口徑自動釋放（frozen 只統計 pending/approved）
		upd := client.Withdrawal.UpdateOneID(id).
			SetStatus(withdrawal.StatusRejected).
			SetReviewedBy(reviewerID).
			SetReviewedAt(now)
		if reason != "" {
			upd.SetRejectReason(reason)
		}
		w, _ = upd.Save(txCtx)
		return nil
	})
	return w, err
}

// PayWithdrawal 打款（人工打款模式）：approved→paid + 佣金 FIFO 消耗
// （available→withdrawn，末行拆分；線下人工轉賬不動錢包賬户）。
func (r *WalletRepoImpl) PayWithdrawal(ctx context.Context, id uint64, receipt string) (*ent.Withdrawal, error) {
	var w *ent.Withdrawal
	err := data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		w, _ = client.Withdrawal.Get(txCtx, id)
		if w == nil {
			return fmt.Errorf("wallet.WITHDRAWAL_NOT_FOUND")
		}
		if w.Status != withdrawal.StatusApproved {
			return fmt.Errorf("wallet.WITHDRAWAL_NOT_APPROVED")
		}
		n, e := client.Withdrawal.Update().Where(withdrawal.ID(id), withdrawal.StatusEQ(withdrawal.StatusApproved)).SetStatus(withdrawal.StatusPaid).Save(txCtx)
		if e != nil {
			return e
		}
		if n != 1 {
			return fmt.Errorf("wallet.WITHDRAWAL_NOT_APPROVED")
		}
		// 佣金 FIFO 消耗（nil = 未裝配佣金端口的舊部署，跳過——僅狀態流轉）
		if r.commissions != nil {
			if err := r.commissions.ConsumeAvailableFIFO(txCtx, w.UserID, w.Amount); err != nil {
				return err
			}
		}
		upd := client.Withdrawal.UpdateOneID(id).
			SetStatus(withdrawal.StatusPaid).
			SetPaidAt(time.Now().UTC())
		if receipt != "" {
			upd.SetReceipt(receipt)
		}
		w, _ = upd.Save(txCtx)
		return nil
	})
	return w, err
}
