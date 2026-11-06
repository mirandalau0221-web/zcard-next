package affiliate

// 佣金倉儲（）：冪等入賬（UNIQUE(order_id,tier)）、凍結確認、逆向扣回（負債態）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/affiliatecommission"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/predicate"
	entUser "github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/withdrawal"
	"github.com/NovaWorks/zcard-next/server/internal/mods/affiliate/port"
)

// 哨兵錯誤。
var (
	ErrDuplicate = errors.New("affiliate: 佣金已存在（冪等 ACK）")
)

// CommissionRepo 佣金倉儲。
type CommissionRepo struct {
	data *data.Data
}

// NewCommissionRepo 構造。
func NewCommissionRepo(d *data.Data) *CommissionRepo { return &CommissionRepo{data: d} }

// CommissionRow 入賬行。
type CommissionRow struct {
	OrderID     uint64
	BuyerID     uint64
	ReferrerID  uint64
	Tier        int8 // 1|2|3
	Rate        int32
	BaseAmount  int64
	Amount      int64
	AvailableAt time.Time
}

// Insert 冪等入賬（UNIQUE(order_id, tier) 衝突 → ErrDuplicate）。
func (r *CommissionRepo) Insert(ctx context.Context, row CommissionRow) error {
	exists, e := data.Client(ctx, r.data).AffiliateCommission.Query().Where(affiliatecommission.OrderID(row.OrderID), affiliatecommission.Tier(row.Tier)).Exist(ctx)
	if e != nil {
		return e
	}
	if exists {
		return ErrDuplicate
	}
	_, err := data.Client(ctx, r.data).AffiliateCommission.Create().
		SetOrderID(row.OrderID).
		SetBuyerID(row.BuyerID).
		SetReferrerID(row.ReferrerID).
		SetTier(row.Tier).
		SetRate(float64(row.Rate)).
		SetBaseAmount(row.BaseAmount).
		SetAmount(row.Amount).
		SetStatus(affiliatecommission.StatusPendingConfirm).
		SetAvailableAt(row.AvailableAt).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return ErrDuplicate
	}
	return err
}

// ListDueConfirm 到期待確認（available_at <= now 且 pending_confirm）。
func (r *CommissionRepo) ListDueConfirm(ctx context.Context, now time.Time, limit int) ([]*ent.AffiliateCommission, error) {
	return data.Client(ctx, r.data).AffiliateCommission.Query().
		Where(
			affiliatecommission.StatusEQ(affiliatecommission.StatusPendingConfirm),
			affiliatecommission.AvailableAtLTE(now),
		).
		Order(ent.Asc(affiliatecommission.FieldID)).
		Limit(limit).
		All(ctx)
}

// MarkAvailable 確認（pending_confirm → available）。
func (r *CommissionRepo) MarkAvailable(ctx context.Context, id uint64) error {
	_, err := data.Client(ctx, r.data).AffiliateCommission.UpdateOneID(id).
		SetStatus(affiliatecommission.StatusAvailable).
		Save(ctx)
	return err
}

// ListByOrder 訂單全部佣金行（逆向扣回輸入）。
func (r *CommissionRepo) ListByOrder(ctx context.Context, orderID uint64) ([]*ent.AffiliateCommission, error) {
	return data.Client(ctx, r.data).AffiliateCommission.Query().
		Where(affiliatecommission.OrderID(orderID)).
		All(ctx)
}

// MarkReversed 逆向（→ reversed）。
func (r *CommissionRepo) MarkReversed(ctx context.Context, id uint64) error {
	_, err := data.Client(ctx, r.data).AffiliateCommission.UpdateOneID(id).
		SetStatus(affiliatecommission.StatusReversed).
		Save(ctx)
	return err
}

// InsertDebt 負債行（負數佣金；tier 取負標記與原行區分 UNIQUE(order_id,tier)；
// cron 重試扣款直至餘額充足——負債態抵扣後續佣金語義）。
func (r *CommissionRepo) InsertDebt(ctx context.Context, orderID, referrerID uint64, tier int8, amount int64) error {
	_, err := data.Client(ctx, r.data).AffiliateCommission.Create().
		SetOrderID(orderID).
		SetBuyerID(0). // 負債行無買家語義（逆向產生）
		SetReferrerID(referrerID).
		SetTier(-tier). // 負 tier = 負債行（與原佣金行同單不撞唯一鍵）
		SetRate(0).
		SetBaseAmount(0).
		SetAmount(-amount). // 負數 = 負債
		SetStatus(affiliatecommission.StatusPendingConfirm).
		SetAvailableAt(time.Now().UTC()).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return nil // 同 (order,tier) 負債已登記（冪等）
	}
	return err
}

// ListByUser 用户佣金流水（正 + 負債）。
func (r *CommissionRepo) ListByUser(ctx context.Context, userID uint64, page, size int) ([]*ent.AffiliateCommission, int, error) {
	q := data.Client(ctx, r.data).AffiliateCommission.Query().
		Where(affiliatecommission.ReferrerID(userID)).
		Order(ent.Desc(affiliatecommission.FieldID))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// StatsByUser 用户統計（port.CommissionReader 實現）。
func (r *CommissionRepo) StatsByUser(ctx context.Context, userID uint64) (*port.CommissionStats, error) {
	rows, err := data.Client(ctx, r.data).AffiliateCommission.Query().
		Where(affiliatecommission.ReferrerID(userID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := &port.CommissionStats{}
	for _, c := range rows {
		switch string(c.Status) {
		case "pending_confirm":
			if c.Amount >= 0 {
				out.PendingCents += c.Amount
			} else {
				out.DebtCents += -c.Amount // 負債行也以 pending 態重試扣款
			}
		case "available":
			out.AvailableCents += c.Amount
		case "withdrawn":
			out.WithdrawnCents += c.Amount
		case "reversed":
			_ = c
		}
		if c.Amount > 0 {
			out.TotalCents += c.Amount
		}
	}
	return out, nil
}

// ListAll 管理面列表（按狀態篩選）。
func (r *CommissionRepo) ListAll(ctx context.Context, status string, page, size int) ([]*ent.AffiliateCommission, int, error) {
	q := data.Client(ctx, r.data).AffiliateCommission.Query().Order(ent.Desc(affiliatecommission.FieldID))
	if status != "" {
		q = q.Where(affiliatecommission.StatusEQ(affiliatecommission.Status(status)))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ListCommissions port 方法（DTO 轉換）。
func (r *CommissionRepo) ListCommissions(ctx context.Context, status string, page, size int) ([]port.CommissionRow, int64, error) {
	rows, total, err := r.ListAll(ctx, status, page, size)
	if err != nil {
		return nil, 0, err
	}
	out := make([]port.CommissionRow, 0, len(rows))
	for _, c := range rows {
		row := port.CommissionRow{
			ID: c.ID, OrderID: c.OrderID, BuyerID: c.BuyerID, ReferrerID: c.ReferrerID,
			Tier: int32(c.Tier), Rate: c.Rate, BaseAmount: c.BaseAmount, Amount: c.Amount,
			Status: string(c.Status), CreatedAt: c.CreatedAt.Unix(),
		}
		if !c.AvailableAt.IsZero() {
			row.AvailableAt = c.AvailableAt.Unix()
		}
		out = append(out, row)
	}
	return out, int64(total), nil
}

// refKey 冪等鍵（wallet 入賬）。
func refKey(id uint64) string { return fmt.Sprintf("commission:%d", id) }

// debtRefKey 負債扣款冪等鍵。
func debtRefKey(id uint64) string { return fmt.Sprintf("commission:debt:%d", id) }

// TeamCounts 三級團隊人數。
func (r *CommissionRepo) TeamCounts(ctx context.Context, userID uint64) (l1, l2, l3 int64, err error) {
	client := data.Client(ctx, r.data)
	c1, err := client.User.Query().Where(entUser.InviteL1(userID)).Count(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	c2, err := client.User.Query().Where(entUser.InviteL2(userID)).Count(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	c3, err := client.User.Query().Where(entUser.InviteL3(userID)).Count(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	return int64(c1), int64(c2), int64(c3), nil
}

// ListTeam 下級列表（按層級過濾；tier=0 全部）。
func (r *CommissionRepo) ListTeam(ctx context.Context, userID uint64, tier, maxTier, page, size int) ([]*ent.User, int, error) {
	q := data.Client(ctx, r.data).User.Query().Order(ent.Desc(entUser.FieldID))
	if tier < 0 || tier > maxTier || maxTier < 1 || maxTier > 3 {
		return nil, 0, nil
	}
	predicates := []predicate.User{entUser.InviteL1(userID), entUser.InviteL2(userID), entUser.InviteL3(userID)}
	if tier > 0 {
		q = q.Where(predicates[tier-1])
	} else {
		q = q.Where(entUser.Or(predicates[:maxTier]...))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ── 佣金提現（凍結口徑 + FIFO 消耗；提現源=佣金，與推廣中心數字一致）──

// FrozenWithdrawAmount 凍結中提現額（pending/approved 提現單金額合計）——
// 可提 = stats.available − frozen（申請校驗與前端展示共用口徑）。
func (r *CommissionRepo) FrozenWithdrawAmount(ctx context.Context, userID uint64) (int64, error) {
	var rows []*ent.Withdrawal
	var err error
	if rows, err = data.Client(ctx, r.data).Withdrawal.Query().
		Where(
			withdrawal.UserID(userID),
			withdrawal.StatusIn(withdrawal.StatusPending, withdrawal.StatusApproved),
		).
		All(ctx); err != nil {
		return 0, err
	}
	var sum int64
	for _, w := range rows {
		sum += w.Amount
	}
	return sum, nil
}

// ConsumeAvailableFIFO 打款消耗：available 佣金行按 ID 升序置 withdrawn；
// 末行超出部分拆分（原行減額 + 複製一行 withdrawn=所需）——dujiao 同款紀律。
// 事務內調用；可用不足返回錯誤（整單回滾）。
func (r *CommissionRepo) ConsumeAvailableFIFO(ctx context.Context, userID uint64, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("affiliate.WITHDRAW_AMOUNT_INVALID")
	}
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		c := data.Client(ctx, r.data)
		rows, err := c.AffiliateCommission.Query().Where(affiliatecommission.ReferrerID(userID), affiliatecommission.StatusEQ(affiliatecommission.StatusAvailable), affiliatecommission.AmountGT(0)).Order(ent.Asc(affiliatecommission.FieldID)).All(ctx)
		if err != nil {
			return err
		}
		remaining := amount
		for _, row := range rows {
			if remaining == 0 {
				break
			}
			take := min(remaining, row.Amount)
			q := c.AffiliateCommission.Update().Where(affiliatecommission.ID(row.ID), affiliatecommission.StatusEQ(affiliatecommission.StatusAvailable), affiliatecommission.Amount(row.Amount))
			if take == row.Amount {
				q.SetStatus(affiliatecommission.StatusWithdrawn)
			} else {
				q.AddAmount(-take)
			}
			n, e := q.Save(ctx)
			if e != nil {
				return e
			}
			if n != 1 {
				return fmt.Errorf("佣金已變化，請刷新後重試")
			}
			if take < row.Amount {
				// Preserve the historical split-row identity, accumulating subsequent withdrawals.
				split, e := c.AffiliateCommission.Query().Where(affiliatecommission.OrderID(row.OrderID+1_000_000_000_000), affiliatecommission.Tier(row.Tier)).Only(ctx)
				if ent.IsNotFound(e) {
					create := c.AffiliateCommission.Create().SetReferrerID(row.ReferrerID).SetOrderID(row.OrderID + 1_000_000_000_000).SetBuyerID(row.BuyerID).SetTier(row.Tier).SetRate(row.Rate).SetBaseAmount(row.BaseAmount).SetAmount(take).SetStatus(affiliatecommission.StatusWithdrawn)
					if !row.AvailableAt.IsZero() {
						create.SetAvailableAt(row.AvailableAt)
					}
					if e = create.Exec(ctx); e != nil {
						return e
					}
				} else if e != nil {
					return e
				} else {
					if split.ReferrerID != row.ReferrerID || split.BuyerID != row.BuyerID || (split.Status != affiliatecommission.StatusWithdrawn && split.Status != affiliatecommission.StatusReversed) {
						return fmt.Errorf("歷史提現分攤衝突，請核對")
					}
					n, e := c.AffiliateCommission.Update().Where(affiliatecommission.ID(split.ID), affiliatecommission.Amount(split.Amount), affiliatecommission.StatusEQ(split.Status)).AddAmount(take).SetStatus(affiliatecommission.StatusWithdrawn).Save(ctx)
					if e != nil {
						return e
					}
					if n != 1 {
						return fmt.Errorf("提現分攤已變化，請重試")
					}
				}
			}
			remaining -= take
		}
		if remaining > 0 {
			return fmt.Errorf("affiliate.INSUFFICIENT_AVAILABLE: 可提佣金不足")
		}
		return nil
	})
}
