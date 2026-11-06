package wallet

// InTx 賬務內核（ 核心——全模塊最高紀律要求）：
//
// 一切餘額變動必須經 InTx：
// 1. 按 reference 查流水——存在直接返回成功（冪等重入，跨模塊冪等）
// 2. ensureAccountForUpdate（FOR UPDATE 鎖賬戶，不存在則建——併發競態處理）
// 3. 非負校驗（Debit 時 available-amount ≥ 0）
// 4. 更新餘額（樂觀鎖 version 兜底重試 ≤3 次）
// 5. 寫流水（balance_before/after 快照 + reference 唯一索引兜底）
//
// 不變量：total = available + locked 恆真；餘額永可由流水重算。

import (
	"context"
	"fmt"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/rechargeorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/walletaccount"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/wallettransaction"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	kerrors "github.com/go-kratos/kratos/v3/errors"
)

// Entry 賬務入賬請求。
type Entry struct {
	UserID     uint64
	Direction  string // "in" | "out"
	Type       string // order_pay / order_refund / recharge / commission / adjust ...
	Amount     int64  // 分（正數）
	Reference  string // 冪等鍵（重複提交直接返回成功）
	OrderID    uint64
	OperatorID uint64
	Remark     string
}

// WalletRepoImpl 錢包倉儲。
type WalletRepoImpl struct {
	data        *data.Data
	commissions CommissionConsumer // 佣金 FIFO 消耗（打款；nil=跳過）
}

// NewWalletRepoImpl 構造。
func NewWalletRepoImpl(d *data.Data) *WalletRepoImpl { return &WalletRepoImpl{data: d} }

// CreateRechargeOrder 落充值單（pending；贈送金額服務端已算定）。
// 入賬只發生在支付回調成功後——本方法絕不寫錢包流水（鐵律 16）。
func (r *WalletRepoImpl) createRechargeOrder(ctx context.Context, userID uint64, amount, giftAmount int64, giftPoints int32) (*ent.RechargeOrder, error) {
	return data.Client(ctx, r.data).RechargeOrder.Create().
		SetUserID(userID).
		SetAmount(amount).
		SetGiftAmount(giftAmount).
		SetGiftPoints(giftPoints).
		SetStatus(rechargeorder.StatusPending).
		Save(ctx)
}

// CreditInTx 入賬（冪等重入：reference 已存在直接返回成功）。
func (r *WalletRepoImpl) creditInTx(ctx context.Context, e Entry) error {
	client := data.Client(ctx, r.data)

	// 1) 冪等重入
	exists, err := client.WalletTransaction.Query().
		Where(wallettransaction.Reference(e.Reference)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("wallet: 冪等檢查失敗: %w", err)
	}
	if exists {
		return nil // 已入賬，冪等返回
	}

	// 2) 鎖賬戶（不存在則建）
	acc, err := r.ensureAccount(ctx, e.UserID)
	if err != nil {
		return err
	}
	if e.OrderID > 0 {
		o, err := client.Order.Get(ctx, e.OrderID)
		if err != nil {
			return err
		}
		currency := o.BaseCurrency
		if currency == "" {
			currency = data.DefaultBaseCurrency
		}
		if currency != acc.Currency {
			return fmt.Errorf("wallet.CURRENCY_MISMATCH: 訂單與錢包幣種不一致")
		}
	}

	// 3) 更新餘額（樂觀鎖）
	newBalance := acc.Available + e.Amount
	affected, err := client.WalletAccount.Update().
		Where(walletaccount.ID(acc.ID), walletaccount.VersionEQ(acc.Version)).
		SetAvailable(newBalance).
		SetVersion(acc.Version + 1).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("wallet: 更新餘額失敗: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("wallet: 樂觀鎖衝突（併發更新）")
	}

	// 4) 寫流水
	_, err = client.WalletTransaction.Create().
		SetUserID(e.UserID).
		SetDirection(e.Direction).
		SetType(e.Type).
		SetAmount(e.Amount).
		SetCurrency(acc.Currency).
		SetBalanceBefore(acc.Available).
		SetBalanceAfter(newBalance).
		SetReference(e.Reference).
		SetOrderID(e.OrderID).
		SetOperatorID(e.OperatorID).
		SetRemark(e.Remark).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("wallet: 寫流水失敗: %w", err)
	}
	return nil
}

// DebitInTx 扣款（餘額不足返回 ErrInsufficient）。
func (r *WalletRepoImpl) debitInTx(ctx context.Context, e Entry) error {
	client := data.Client(ctx, r.data)

	// 1) 冪等重入
	exists, err := client.WalletTransaction.Query().
		Where(wallettransaction.Reference(e.Reference)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// 2) 鎖賬戶
	acc, err := r.ensureAccount(ctx, e.UserID)
	if err != nil {
		return err
	}
	if e.OrderID > 0 {
		o, err := client.Order.Get(ctx, e.OrderID)
		if err != nil {
			return err
		}
		currency := o.BaseCurrency
		if currency == "" {
			currency = data.DefaultBaseCurrency
		}
		if currency != acc.Currency {
			return fmt.Errorf("wallet.CURRENCY_MISMATCH: 訂單與錢包幣種不一致")
		}
	}

	// 3) 非負校驗
	if acc.Available < e.Amount {
		return fmt.Errorf("%w: avail=%d need=%d", walletport.ErrInsufficientBalance, acc.Available, e.Amount)
	}

	// 4) 樂觀鎖更新
	newBalance := acc.Available - e.Amount
	affected, err := client.WalletAccount.Update().
		Where(walletaccount.ID(acc.ID), walletaccount.VersionEQ(acc.Version)).
		SetAvailable(newBalance).
		SetVersion(acc.Version + 1).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("wallet: 樂觀鎖衝突")
	}

	// 5) 寫流水
	_, err = client.WalletTransaction.Create().
		SetUserID(e.UserID).
		SetDirection(e.Direction).
		SetType(e.Type).
		SetAmount(e.Amount).
		SetCurrency(acc.Currency).
		SetBalanceBefore(acc.Available).
		SetBalanceAfter(newBalance).
		SetReference(e.Reference).
		SetOrderID(e.OrderID).
		SetOperatorID(e.OperatorID).
		SetRemark(e.Remark).
		Save(ctx)
	return err
}

// ensureAccount 鎖賬戶（不存在則建——併發建號競態處理）。
func (r *WalletRepoImpl) ensureAccount(ctx context.Context, userID uint64) (*ent.WalletAccount, error) {
	client := data.Client(ctx, r.data)
	var lockedCurrency string

	acc, err := client.WalletAccount.Query().
		Where(walletaccount.UserID(userID)).Only(ctx)
	if ent.IsNotFound(err) {
		// Serialize first account creation with a base-currency switch.
		code, e := data.LockCurrencyConfiguration(ctx, r.data)
		if e != nil {
			return nil, e
		}
		lockedCurrency = code
		acc, err = client.WalletAccount.Create().
			SetUserID(userID).
			SetCurrency(code).
			SetAvailable(0).
			SetLocked(0).
			SetVersion(0).
			Save(ctx)
		if ent.IsConstraintError(err) {
			q := client.WalletAccount.Query().Where(walletaccount.UserID(userID))
			if r.data.Dialect != db.SQLite {
				q = q.ForUpdate()
			}
			acc, err = q.Only(ctx)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("wallet: 獲取賬戶失敗: %w", err)
	}
	code := lockedCurrency
	if code == "" {
		code, err = data.BaseCurrency(ctx, r.data)
		if err != nil {
			return nil, err
		}
	}
	if acc.Currency != code {
		return nil, fmt.Errorf("wallet.CURRENCY_MISMATCH: 錢包幣種與基礎貨幣不一致，需要遷移")
	}
	return acc, nil
}

// GetBalance 查餘額。
func (r *WalletRepoImpl) GetBalance(ctx context.Context, userID uint64) (available, locked int64, err error) {
	acc, err := data.Client(ctx, r.data).WalletAccount.Query().
		Where(walletaccount.UserID(userID)).Only(ctx)
	if ent.IsNotFound(err) {
		return 0, 0, nil // 未開戶 = 零餘額
	}
	if err != nil {
		return 0, 0, err
	}
	base, err := data.BaseCurrency(ctx, r.data)
	if err != nil {
		return 0, 0, err
	}
	if acc.Currency != base {
		return 0, 0, kerrors.Conflict("wallet.CURRENCY_MISMATCH", "賬戶幣種與基礎貨幣不一致，請聯繫管理員遷移")
	}
	return acc.Available, acc.Locked, nil
}

// ListTransactions 流水列表（userID = 0 表示全站——賬單管理場景，按時間倒序）。
func (r *WalletRepoImpl) ListTransactions(ctx context.Context, userID uint64, page, size int) ([]*ent.WalletTransaction, int64, error) {
	client := data.Client(ctx, r.data)
	q := client.WalletTransaction.Query()
	if userID > 0 {
		q = q.Where(wallettransaction.UserID(userID))
	}
	q = q.Order(ent.Desc(wallettransaction.FieldCreatedAt))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Clone().Offset((page - 1) * size).Limit(size).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	base, err := data.BaseCurrency(ctx, r.data)
	if err != nil {
		return nil, 0, err
	}
	for _, row := range rows {
		if row.Currency != base {
			return nil, 0, kerrors.Conflict("wallet.CURRENCY_MISMATCH", "賬戶幣種與基礎貨幣不一致，請聯繫管理員遷移")
		}
	}
	return rows, int64(total), err
}

// UsernamesByIDs 批量取用戶名（全站流水回填歸屬人；缺失的 id 不出現在結果裏）。
func (r *WalletRepoImpl) UsernamesByIDs(ctx context.Context, ids []uint64) (map[uint64]string, error) {
	if len(ids) == 0 {
		return map[uint64]string{}, nil
	}
	rows, err := data.Client(ctx, r.data).User.Query().
		Where(user.IDIn(ids...)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]string, len(rows))
	for _, u := range rows {
		out[u.ID] = u.Username
	}
	return out, nil
}

// RebuildBalance 餘額重算（對賬用——從流水重放驗證餘額一致）。
func (r *WalletRepoImpl) RebuildBalance(ctx context.Context, userID uint64) (int64, error) {
	rows, err := data.Client(ctx, r.data).WalletTransaction.Query().
		Where(wallettransaction.UserID(userID)).
		Order(ent.Asc(wallettransaction.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return 0, err
	}
	var balance int64
	for _, row := range rows {
		if row.Direction == "in" {
			balance += row.Amount
		} else {
			balance -= row.Amount
		}
	}
	return balance, nil
}

// Lock 凍結（available → locked；availableAt 到期時間戳——佣金/分站利潤凍結期）。
// 冪等口徑：金額校驗 available >= amount，樂觀鎖 CAS。
func (r *WalletRepoImpl) Lock(ctx context.Context, userID uint64, amount int64, availableAt int64) error {
	if amount <= 0 {
		return fmt.Errorf("wallet: 凍結金額必須為正")
	}
	client := data.Client(ctx, r.data)
	acc, err := client.WalletAccount.Query().
		Where(walletaccount.UserID(userID)).Only(ctx)
	if err != nil {
		return err
	}
	if acc.Available < amount {
		return fmt.Errorf("wallet.BALANCE_INSUFFICIENT")
	}
	affected, err := client.WalletAccount.Update().
		Where(walletaccount.ID(acc.ID), walletaccount.Available(acc.Available)).
		SetAvailable(acc.Available - amount).
		SetLocked(acc.Locked + amount).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("wallet.CONCURRENT_UPDATE")
	}
	// 凍結流水（reference 冪等由調用方保證）
	_, _ = client.WalletTransaction.Create().
		SetUserID(userID).
		SetDirection("out").
		SetType("lock").
		SetAmount(amount).
		SetCurrency(acc.Currency).
		SetReference(fmt.Sprintf("lock:%d:%d", userID, availableAt)).
		SetRemark("凍結").
		Save(ctx)
	return nil
}

// Unlock 解凍（locked → available；提現拒絕/佣金退回）。
func (r *WalletRepoImpl) Unlock(ctx context.Context, userID uint64, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("wallet: 解凍金額必須為正")
	}
	client := data.Client(ctx, r.data)
	acc, err := client.WalletAccount.Query().
		Where(walletaccount.UserID(userID)).Only(ctx)
	if err != nil {
		return err
	}
	if acc.Locked < amount {
		return fmt.Errorf("wallet.INSUFFICIENT_LOCKED")
	}
	affected, err := client.WalletAccount.Update().
		Where(walletaccount.ID(acc.ID), walletaccount.Locked(acc.Locked)).
		SetLocked(acc.Locked - amount).
		SetAvailable(acc.Available + amount).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("wallet.CONCURRENT_UPDATE")
	}
	return nil
}

// Public ledger entry points keep account creation and its currency lock in the
// same transaction, including callers that do not already own a transaction.
func (r *WalletRepoImpl) CreditInTx(ctx context.Context, e Entry) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error { return r.creditInTx(ctx, e) })
}
func (r *WalletRepoImpl) DebitInTx(ctx context.Context, e Entry) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error { return r.debitInTx(ctx, e) })
}
func (r *WalletRepoImpl) CreateRechargeOrder(ctx context.Context, userID uint64, amount, giftAmount int64, giftPoints int32) (out *ent.RechargeOrder, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.createRechargeOrder(ctx, userID, amount, giftAmount, giftPoints)
		return e
	})
	return out, err
}
