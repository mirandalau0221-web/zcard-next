package reseller

// 分站核心（/// 核心，主站 admin 面先行）：
// 申請/審核（等級+加價率區間）、定價引擎（4 模式 × 三級優先級 + 上下限）、
// 分賬賬本（冪等/雙階段/水位緩存/重算）、防自購三查快照。
// 域名驗證與租戶上下文貫穿、分站後台完整 API 面隨 storefront 用戶登錄體系接續。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellerbalanceaccount"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellerledgerentry"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellerpricing"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellerprofile"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellersite"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// 哨兵錯誤。
var (
	ErrNotFound       = errors.New("reseller: 記錄不存在")
	ErrDuplicateApply = errors.New("reseller.DUPLICATE_APPLY: 已有申請記錄")
	ErrMarkupExceed   = errors.New("reseller.MARKUP_EXCEED: 超過分站加價率上限")
	ErrBelowBase      = errors.New("reseller.BELOW_BASE: 分站價不得低於主站基礎價")
	ErrNotApproved    = errors.New("reseller.NOT_APPROVED: 分站未過審")
	ErrInsufficient   = errors.New("reseller.INSUFFICIENT_BALANCE")
)

// ResellerRepo 倉儲。
type ResellerRepo struct {
	data *data.Data
}

// NewResellerRepo 構造。
func NewResellerRepo(d *data.Data) *ResellerRepo { return &ResellerRepo{data: d} }

// ── 申請/審核 ─────────────────────────────────────────

// ApplyInput 申請入參。
type ApplyInput struct {
	UserID       uint64
	Reason       string
	DomainIntent string
}

// Apply 申請分站（一人一份：user_id 唯一；重複申請拒絕）。
func (r *ResellerRepo) Apply(ctx context.Context, in ApplyInput) (*ent.ResellerProfile, error) {
	p, err := data.Client(ctx, r.data).ResellerProfile.Create().
		SetUserID(in.UserID).
		SetStatus(resellerprofile.StatusApplying).
		SetApplyReason(in.Reason).
		SetDefaultMarkupPercent(10). // 審核通過時可調；默認 10%
		SetMaxMarkupPercent(50).
		SetConfirmDays(7).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, ErrDuplicateApply
	}
	return p, err
}

// Review 審核（approve=true 生成 approved；拒絕寫 reject_reason）。
func (r *ResellerRepo) Review(ctx context.Context, id uint64, approve bool, reason string, reviewedBy uint64, defaultMarkup, maxMarkup float64, confirmDays int32) (*ent.ResellerProfile, error) {
	client := data.Client(ctx, r.data)
	status := resellerprofile.StatusRejected
	if approve {
		status = resellerprofile.StatusApproved
	}
	upd := client.ResellerProfile.UpdateOneID(id).
		SetStatus(status).
		SetReviewedBy(reviewedBy).
		SetReviewedAt(time.Now().UTC())
	if !approve && reason != "" {
		upd.SetRejectReason(reason)
	}
	if approve {
		if defaultMarkup > 0 {
			upd.SetDefaultMarkupPercent(defaultMarkup)
		}
		if maxMarkup > 0 {
			upd.SetMaxMarkupPercent(maxMarkup)
		}
		if confirmDays > 0 {
			upd.SetConfirmDays(confirmDays)
		}
	}
	p, err := upd.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// ListProfiles 申請/分站列表。
func (r *ResellerRepo) ListProfiles(ctx context.Context, status string, page, size int) ([]*ent.ResellerProfile, int, error) {
	q := data.Client(ctx, r.data).ResellerProfile.Query().Order(ent.Desc(resellerprofile.FieldID))
	if status != "" {
		q = q.Where(resellerprofile.StatusEQ(resellerprofile.Status(status)))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ProfileByUser 按用戶查（分站主身份判定）。
func (r *ResellerRepo) ProfileByUser(ctx context.Context, userID uint64) (*ent.ResellerProfile, error) {
	p, err := data.Client(ctx, r.data).ResellerProfile.Query().
		Where(resellerprofile.UserID(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// ── 定價引擎 ───────────────────────────────────────────

// UpsertPricing 定價規則（SKU>商品>分站默認三級之一）。
// value 語義隨 mode：markup_percent=萬分比；fixed_markup/fixed_price=分。
// 上限校驗：markup_percent 不得超 profile.max_markup_percent（百分×100）。
func (r *ResellerRepo) upsertPricing(ctx context.Context, subsiteID, productID, skuID uint64, mode string, value int64, maxMarkupPercent float64) (*ent.ResellerPricing, error) {
	client := data.Client(ctx, r.data)
	if mode == "markup_percent" && maxMarkupPercent > 0 && float64(value) > maxMarkupPercent*100 {
		return nil, ErrMarkupExceed
	}
	existing, err := client.ResellerPricing.Query().
		Where(
			resellerpricing.SubsiteIDEQ(subsiteID),
			resellerpricing.ProductIDEQ(productID),
			resellerpricing.SkuIDEQ(skuID),
		).Only(ctx)
	if ent.IsNotFound(err) {
		return client.ResellerPricing.Create().
			SetSubsiteID(subsiteID).SetProductID(productID).SetSkuID(skuID).
			SetMode(resellerpricing.Mode(mode)).SetValue(value).
			Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	return client.ResellerPricing.UpdateOneID(existing.ID).
		SetMode(resellerpricing.Mode(mode)).SetValue(value).
		Save(ctx)
}

// ResolveUnitPrice 分站單價（listing 與 checkout 共用同一實現——1.x 鐵律）。
// 優先級：SKU 規則 > 商品規則 > 分站默認加價率 > 繼承主站價。
// 下限保護：結果不得低於 basePrice（主站基礎價）。
func (r *ResellerRepo) ResolveUnitPrice(ctx context.Context, subsiteID, productID, skuID uint64, basePrice money.Cents) (money.Cents, error) {
	client := data.Client(ctx, r.data)
	// SKU 級
	if skuID > 0 {
		if p, err := client.ResellerPricing.Query().Where(
			resellerpricing.SubsiteIDEQ(subsiteID), resellerpricing.ProductIDEQ(productID),
			resellerpricing.SkuIDEQ(skuID),
		).Only(ctx); err == nil {
			return applyPricingMode(p.Mode, p.Value, basePrice)
		}
	}
	// 商品級
	if p, err := client.ResellerPricing.Query().Where(
		resellerpricing.SubsiteIDEQ(subsiteID), resellerpricing.ProductIDEQ(productID),
		resellerpricing.SkuIDEQ(0),
	).Only(ctx); err == nil {
		return applyPricingMode(p.Mode, p.Value, basePrice)
	}
	// 分站默認加價率（profiles.default_markup_percent——subsite_id 即 profile 主鍵）
	profile, err := client.ResellerProfile.Get(ctx, subsiteID)
	if err == nil {
		return applyPricingMode("markup_percent", int64(profile.DefaultMarkupPercent*100), basePrice)
	}
	// 繼承主站價
	return basePrice, nil
}

// applyPricingMode 4 模式換算 + 下限保護。
// value 語義：markup_percent=萬分比；fixed_markup/fixed_price=分。
func applyPricingMode(mode resellerpricing.Mode, value int64, basePrice money.Cents) (money.Cents, error) {
	base := int64(basePrice)
	var out int64
	switch string(mode) {
	case "inherit":
		out = base
	case "markup_percent": // 萬分比（1000 = +10%）
		out = base + base*value/10000
	case "fixed_markup":
		out = base + value
	case "fixed_price":
		out = value
	default:
		return basePrice, nil
	}
	final := money.Cents(out)
	if final < basePrice {
		return basePrice, ErrBelowBase // 下限保護（fixed_price/fixed_markup 可能低於）
	}
	return final, nil
}

// ── 分賬賬本 ───────────────────────────────────────────

// SettleInput 分賬入賬輸入。
type SettleInput struct {
	SubsiteID uint64
	OrderID   uint64
	Amount    money.Cents // 分站利潤（subsite_markup 合計）
}

// SettleOrderProfit 訂單分賬入賬（冪等鍵 order_profit:<orderID>；凍結 available_at）。
// 入賬前優先抵扣 pending 負債（利潤先還債，與 affiliate 負債態同構）。
func (r *ResellerRepo) SettleOrderProfit(ctx context.Context, in SettleInput) error {
	if in.Amount <= 0 {
		return nil
	}
	client := data.Client(ctx, r.data)
	profile, err := client.ResellerProfile.Get(ctx, in.SubsiteID)
	if err != nil {
		return ErrNotApproved
	}
	confirmDays := int(profile.ConfirmDays)
	if confirmDays <= 0 {
		confirmDays = 7
	}
	// 負債優先抵扣（同事務；不足才產生淨入賬）
	net, err := r.settleDebt(ctx, in.SubsiteID, int64(in.Amount))
	if err != nil {
		return err
	}
	if net <= 0 {
		// 利潤全部用於抵債：negative 緩存減少（抵債額=入賬額）
		return r.updateBalance(ctx, in.SubsiteID, 0, 0, -int64(in.Amount))
	}
	if net < int64(in.Amount) {
		// 部分抵債：negative 減少差額
		if err := r.updateBalance(ctx, in.SubsiteID, 0, 0, -(int64(in.Amount) - net)); err != nil {
			return err
		}
	}
	_, err = client.ResellerLedgerEntry.Create().
		SetSubsiteID(in.SubsiteID).
		SetOrderID(in.OrderID).
		SetType("order_profit").
		SetAmount(net).
		SetStatus(resellerledgerentry.StatusPending).
		SetAvailableAt(time.Now().UTC().AddDate(0, 0, confirmDays)).
		SetIdempotencyKey(fmt.Sprintf("order_profit:%d", in.OrderID)).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return nil // 冪等 ACK
	}
	if err != nil {
		return err
	}
	return r.refreshBalance(ctx, in.SubsiteID, net)
}

// ConfirmDue 到期確認（pending → available；cron）。
func (r *ResellerRepo) ConfirmDue(ctx context.Context, now time.Time, limit int) (int, error) {
	client := data.Client(ctx, r.data)
	rows, err := client.ResellerLedgerEntry.Query().
		Where(
			resellerledgerentry.StatusEQ(resellerledgerentry.StatusPending),
			resellerledgerentry.AvailableAtLTE(now),
		).Order(ent.Asc(resellerledgerentry.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range rows {
		if count, err := client.ResellerLedgerEntry.Update().Where(resellerledgerentry.ID(e.ID), resellerledgerentry.StatusEQ(resellerledgerentry.StatusPending), resellerledgerentry.Amount(e.Amount)).SetStatus(resellerledgerentry.StatusAvailable).Save(ctx); err == nil {
			n += count
		}
	}
	return n, nil
}

// refreshBalance 餘額緩存增量（available 維度——pending/locked 獨立核算）。
func (r *ResellerRepo) refreshBalance(ctx context.Context, subsiteID uint64, delta int64) error {
	return r.updateBalance(ctx, subsiteID, delta, 0, 0)
}

// updateBalance 餘額緩存三維增量（available/locked/negative——提現鎖定/打款/
// 駁回/退款扣回/負債抵扣全部經此維護，緩存可由流水重算對賬）。
func (r *ResellerRepo) updateBalance(ctx context.Context, subsiteID uint64, availDelta, lockedDelta, negDelta int64) error {
	client := data.Client(ctx, r.data)
	acc, err := client.ResellerBalanceAccount.Query().
		Where(resellerbalanceaccount.SubsiteIDEQ(subsiteID)).Only(ctx)
	if ent.IsNotFound(err) {
		_, err = client.ResellerBalanceAccount.Create().
			SetSubsiteID(subsiteID).
			SetAvailable(availDelta).SetLocked(lockedDelta).SetNegative(negDelta).
			Save(ctx)
		return err
	}
	if err != nil {
		return err
	}
	_, err = client.ResellerBalanceAccount.UpdateOneID(acc.ID).
		AddAvailable(availDelta).
		AddLocked(lockedDelta).
		AddNegative(negDelta).
		Save(ctx)
	return err
}

// RecomputeBalance 流水重算（對賬函數——測試斷言口徑）。
func (r *ResellerRepo) RecomputeBalance(ctx context.Context, subsiteID uint64) (available, locked, negative int64, err error) {
	client := data.Client(ctx, r.data)
	rows, err := client.ResellerLedgerEntry.Query().
		Where(resellerledgerentry.SubsiteIDEQ(subsiteID)).All(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range rows {
		switch string(e.Status) {
		case "available":
			available += e.Amount
		case "locked":
			locked += e.Amount
		case "pending":
			if e.Amount >= 0 {
				available += e.Amount // 凍結態計入可提快照（可用口徑=available+pending 正數）
			} else {
				negative += -e.Amount
			}
		case "withdrawn":
			// 已提現不計
		}
	}
	return available, locked, negative, nil
}

// ── 防自購三查 ─────────────────────────────────────────

// ProfitEligible 防自購判定（下單快照落 orders.profit_eligible）。
// 三查：買家==分站主 / 買家∈分站主上級鏈 / 買家∈同鏈分站主（互推）。
func (r *ResellerRepo) ProfitEligible(ctx context.Context, subsiteID, buyerID uint64) bool {
	client := data.Client(ctx, r.data)
	profile, err := client.ResellerProfile.Get(ctx, subsiteID)
	if err != nil {
		return true // 非分站單（主站直營）：默認分賬資格=主站自身利潤邏輯
	}
	owner := profile.UserID
	if buyerID == owner {
		return false // 查一：分站主自購
	}
	// 查二/三：買家與分站主的邀請鏈交叉（任一方向命中即視為關聯自購）
	buyer, err := client.User.Get(ctx, buyerID)
	if err != nil {
		return true
	}
	if buyer.InviteL1 == owner || buyer.InviteL2 == owner || buyer.InviteL3 == owner {
		return false // 買家是分站主的下級
	}
	// 分站主是買家的下級（反向）
	if owner != 0 {
		if o, err := client.User.Get(ctx, owner); err == nil {
			if o.InviteL1 == buyerID || o.InviteL2 == buyerID || o.InviteL3 == buyerID {
				return false
			}
		}
	}
	return true
}

var (
	_ = order.FieldSubsiteID
	_ = user.FieldInviteL1
	_ = json.Marshal
	_ = events.OrderPaid
	_ = slog.Default
)

// GetProfile 按 ID 查分站。
func (r *ResellerRepo) GetProfile(ctx context.Context, id uint64) (*ent.ResellerProfile, error) {
	p, err := data.Client(ctx, r.data).ResellerProfile.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// Ledger 賬本流水（按狀態篩選）。
func (r *ResellerRepo) Ledger(ctx context.Context, subsiteID uint64, status string, page, size int) ([]*ent.ResellerLedgerEntry, int, error) {
	q := data.Client(ctx, r.data).ResellerLedgerEntry.Query().
		Where(resellerledgerentry.SubsiteIDEQ(subsiteID)).
		Order(ent.Desc(resellerledgerentry.FieldID))
	if status != "" {
		q = q.Where(resellerledgerentry.StatusEQ(resellerledgerentry.Status(status)))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// GetBalance 餘額緩存賬戶（缺失返回零值賬戶語義）。
func (r *ResellerRepo) GetBalance(ctx context.Context, subsiteID uint64) (*ent.ResellerBalanceAccount, error) {
	acc, err := data.Client(ctx, r.data).ResellerBalanceAccount.Query().
		Where(resellerbalanceaccount.SubsiteIDEQ(subsiteID)).Only(ctx)
	if ent.IsNotFound(err) {
		return &ent.ResellerBalanceAccount{SubsiteID: subsiteID}, nil
	}
	if err != nil {
		return nil, err
	}
	return acc, nil
}

// ListSites 分站域名列表。
func (r *ResellerRepo) ListSites(ctx context.Context, profileID uint64) ([]*ent.ResellerSite, error) {
	return data.Client(ctx, r.data).ResellerSite.Query().
		Where(resellersite.ProfileID(profileID)).
		Order(ent.Desc(resellersite.FieldID)).
		All(ctx)
}

// GetSite 單站點。
func (r *ResellerRepo) GetSite(ctx context.Context, id uint64) (*ent.ResellerSite, error) {
	s, err := data.Client(ctx, r.data).ResellerSite.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s, nil
}

// SetWhitelabel 白標設置。
func (r *ResellerRepo) SetWhitelabel(ctx context.Context, siteID uint64, siteName, logo, favicon string) error {
	upd := data.Client(ctx, r.data).ResellerSite.UpdateOneID(siteID)
	if siteName != "" {
		upd.SetSiteName(siteName)
	}
	if logo != "" {
		upd.SetLogo(logo)
	}
	if favicon != "" {
		upd.SetFavicon(favicon)
	}
	_, err := upd.Save(ctx)
	return err
}

func (r *ResellerRepo) UpsertPricing(ctx context.Context, subsiteID, productID, skuID uint64, mode string, value int64, maxMarkupPercent float64) (out *ent.ResellerPricing, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.upsertPricing(ctx, subsiteID, productID, skuID, mode, value, maxMarkupPercent)
		return e
	})
	return out, err
}
