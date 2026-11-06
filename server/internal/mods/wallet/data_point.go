package wallet

// 積分賬本（ ，與餘額 InTx 同構）：
// point_accounts(balance, version) + point_transactions(reference 冪等鍵)；
// 非負校驗 + 樂觀鎖 CAS + 流水快照——餘額永可由流水重算。
// 產生口徑：充值贈送（earn_recharge）/消費（earn_consume）/兑換（redeem）/調賬（adjust）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/pointaccount"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/pointtransaction"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/wallettransaction"
)

// PointEntry 積分入賬請求。
type PointEntry struct {
	UserID    uint64
	Direction string // "in" | "out"
	Type      string // earn_recharge / earn_consume / redeem / adjust ...
	Amount    int64  // 正數
	Reference string // 冪等鍵（points:<orderID> 等；重複提交直接返回成功）
	OrderID   uint64
	Remark    string
}

// PointCreditInTx 積分入賬（冪等重入：reference 已存在直接返回成功）。
func (r *WalletRepoImpl) PointCreditInTx(ctx context.Context, e PointEntry) error {
	if e.Amount <= 0 {
		return fmt.Errorf("wallet: 積分必須為正")
	}
	return data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		// 冪等：reference 已存在 → ACK
		exists, err := client.PointTransaction.Query().
			Where(pointtransaction.ReferenceEQ(e.Reference)).Exist(txCtx)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		acc, err := client.PointAccount.Query().
			Where(pointaccount.UserID(e.UserID)).Only(txCtx)
		if ent.IsNotFound(err) {
			acc, err = client.PointAccount.Create().
				SetUserID(e.UserID).SetBalance(0).SetVersion(0).Save(txCtx)
		}
		if err != nil {
			return err
		}
		before := acc.Balance
		after := before + e.Amount
		affected, err := client.PointAccount.Update().
			Where(pointaccount.ID(acc.ID), pointaccount.Version(acc.Version)).
			SetBalance(after).SetVersion(acc.Version + 1).
			Save(txCtx)
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("wallet.CONCURRENT_UPDATE")
		}
		_, err = client.PointTransaction.Create().
			SetUserID(e.UserID).SetDirection(e.Direction).SetType(e.Type).
			SetAmount(e.Amount).SetBalanceBefore(before).SetBalanceAfter(after).
			SetReference(e.Reference).SetNillableOrderID(nilOrZero(e.OrderID)).
			SetRemark(e.Remark).
			Save(txCtx)
		return err
	})
}

// PointDebitInTx 積分扣減（非負校驗；不足拒絕不產生流水）。
func (r *WalletRepoImpl) PointDebitInTx(ctx context.Context, e PointEntry) error {
	return r.pointDebit(ctx, e, false)
}

// PointRevokeInTx records refund debt even if earned points have already been spent.
func (r *WalletRepoImpl) PointRevokeInTx(ctx context.Context, e PointEntry) error {
	return r.pointDebit(ctx, e, true)
}
func (r *WalletRepoImpl) pointDebit(ctx context.Context, e PointEntry, allowDebt bool) error {
	if e.Amount <= 0 {
		return fmt.Errorf("wallet: 積分必須為正")
	}
	return data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		exists, err := client.PointTransaction.Query().
			Where(pointtransaction.ReferenceEQ(e.Reference)).Exist(txCtx)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		acc, err := client.PointAccount.Query().
			Where(pointaccount.UserID(e.UserID)).Only(txCtx)
		if ent.IsNotFound(err) {
			return fmt.Errorf("wallet.POINTS_INSUFFICIENT")
		}
		if err != nil {
			return err
		}
		if !allowDebt && acc.Balance < e.Amount {
			return fmt.Errorf("wallet.POINTS_INSUFFICIENT")
		}
		before := acc.Balance
		after := before - e.Amount
		affected, err := client.PointAccount.Update().
			Where(pointaccount.ID(acc.ID), pointaccount.Version(acc.Version)).
			SetBalance(after).SetVersion(acc.Version + 1).
			Save(txCtx)
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("wallet.CONCURRENT_UPDATE")
		}
		_, err = client.PointTransaction.Create().
			SetUserID(e.UserID).SetDirection(e.Direction).SetType(e.Type).
			SetAmount(e.Amount).SetBalanceBefore(before).SetBalanceAfter(after).
			SetReference(e.Reference).SetNillableOrderID(nilOrZero(e.OrderID)).
			SetRemark(e.Remark).
			Save(txCtx)
		return err
	})
}

// GetPoints 積分餘額（賬户不存在返回 0）。
func (r *WalletRepoImpl) GetPoints(ctx context.Context, userID uint64) (int64, error) {
	acc, err := data.Client(ctx, r.data).PointAccount.Query().
		Where(pointaccount.UserID(userID)).Only(ctx)
	if ent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return acc.Balance, nil
}

// CumulativeRecharge 累計充值（countAsRecharge 口徑：僅 type=recharge 入賬流水；
// 等級閾值消費——互轉/調賬/佣金/退款均不計，防刷）。
func (r *WalletRepoImpl) CumulativeRecharge(ctx context.Context, userID uint64) (int64, error) {
	sum, err := data.Client(ctx, r.data).WalletTransaction.Query().
		Where(
			wallettransaction.UserID(userID),
			wallettransaction.Direction("in"),
			wallettransaction.TypeEQ("recharge"),
		).
		Aggregate(ent.Sum(wallettransaction.FieldAmount)).Int(ctx)
	// SUM 空集返回 NULL（scan 失敗）——口徑為 0
	if err != nil {
		if strings.Contains(err.Error(), "NULL") || strings.Contains(err.Error(), "Scan") {
			return 0, nil
		}
		return 0, err
	}
	return int64(sum), nil
}

// RebuildPoints 積分重算（對賬函數——測試斷言口徑）。
func (r *WalletRepoImpl) RebuildPoints(ctx context.Context, userID uint64) (int64, error) {
	rows, err := data.Client(ctx, r.data).PointTransaction.Query().
		Where(pointtransaction.UserID(userID)).All(ctx)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, t := range rows {
		if t.Direction == "in" {
			sum += t.Amount
		} else {
			sum -= t.Amount
		}
	}
	return sum, nil
}

func nilOrZero(v uint64) *uint64 {
	if v == 0 {
		return nil
	}
	return &v
}
