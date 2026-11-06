package affiliate

// 佣金引擎（//）：
// 訂閲 order.paid → 歸因快照判定 → 三級計算入賬（pending_confirm + available_at）
// cron 到期確認 → available + wallet 入賬（冪等鍵 commission:<id>）
// order.refunded → 逆向：pending 作廢 / available 扣回 / 已提現不足 → 負債行
//
// 不發傭清單（1.x 繼承）：自購（buyer∈三級鏈）、無歸因（鏈空）、supply_orders
// （獨立表非 orders——天然排除）、分站自購（profit_eligible reseller 聯動）。
// 凍結期在佣金表自身狀態機，不佔用 wallet.locked（口徑 ）。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
)

// AffiliateConfig settings.affiliate 配置（運行時讀取，默認值兜底）。
type AffiliateConfig struct {
	Levels     int    `json:"levels"`      // 啓用層數（1—3）
	RateL1     int32  `json:"rate_l1"`     // 萬分比（默認 500 = 5%）
	RateL2     int32  `json:"rate_l2"`     // 默認 200 = 2%
	RateL3     int32  `json:"rate_l3"`     // 默認 100 = 1%
	BaseScope  string `json:"base_scope"`  // amount | profit（默認 amount）
	FreezeDays int    `json:"freeze_days"` // 凍結天數（默認 7）
	SelfBuy    bool   `json:"self_buy"`    // 自購發傭開關（默認 false 不發）
	Enabled    bool   `json:"enabled"`
}

func defaultConfig() AffiliateConfig {
	return AffiliateConfig{
		Levels: 3, RateL1: 500, RateL2: 200, RateL3: 100,
		BaseScope: "amount", FreezeDays: 7, SelfBuy: false, Enabled: true,
	}
}

// AffiliateService 佣金引擎。
type AffiliateService struct {
	repo     *CommissionRepo
	wallet   walletport.Wallet
	settings notifyport.SettingsReader
	outbox   events.Writer
	log      *slog.Logger
}

// NewAffiliateService 構造。
func NewAffiliateService(repo *CommissionRepo, wallet walletport.Wallet, settings notifyport.SettingsReader, outbox events.Writer, logger *slog.Logger) *AffiliateService {
	return &AffiliateService{repo: repo, wallet: wallet, settings: settings, outbox: outbox, log: logger}
}

// paidPayload order.paid 載荷（與 order 模塊發佈結構對齊 + 歸因鏈）。
type paidPayload struct {
	OrderNo     string `json:"order_no"`
	OrderID     uint64 `json:"order_id"`
	SubsiteID   uint64 `json:"subsite_id"`
	BuyerID     uint64 `json:"user_id"` // 事件載荷統一字段名
	InviteL1    uint64 `json:"invite_l1"`
	InviteL2    uint64 `json:"invite_l2"`
	InviteL3    uint64 `json:"invite_l3"`
	TotalCents  int64  `json:"total_cents"`
	ProfitCents int64  `json:"profit_cents"` // 毛利口徑（amount−cost；order 側隨事件附贈）
}

// OnOrderPaid 訂閲 order.paid（冪等：UNIQUE(order_id,tier) 兜底 + processed_events）。
func (s *AffiliateService) onOrderPaid(ctx context.Context, env events.Envelope) error {
	var p paidPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil // 載荷不合法：ACK 不重試（order 側契約破壞屬異常路徑）
	}
	cfg := s.config(ctx)
	if !cfg.Enabled {
		return nil
	}
	// 歸因鏈（快照已隨事件攜帶；空鏈 = 無歸因不發）
	chain := [3]uint64{p.InviteL1, p.InviteL2, p.InviteL3}
	if chain[0] == 0 {
		return nil
	}
	// 不發傭：自購（買家在三級鏈內且開關關閉）
	if !cfg.SelfBuy && p.BuyerID != 0 {
		for _, uid := range chain {
			if uid == p.BuyerID {
				return nil
			}
		}
	}
	// 基數口徑
	base := p.TotalCents
	if cfg.BaseScope == "profit" {
		base = p.ProfitCents
		if base <= 0 {
			return nil // 無毛利不發傭
		}
	}
	rates := [3]int32{cfg.RateL1, cfg.RateL2, cfg.RateL3}
	availableAt := time.Now().UTC().AddDate(0, 0, cfg.FreezeDays)
	for tier := 0; tier < cfg.Levels; tier++ {
		referrer := chain[tier]
		if referrer == 0 {
			continue
		}
		amount := base * int64(rates[tier]) / 10000
		if amount <= 0 {
			continue
		}
		if err := s.repo.Insert(ctx, CommissionRow{
			OrderID: p.OrderID, BuyerID: p.BuyerID, ReferrerID: referrer,
			Tier: int8(tier + 1), Rate: rates[tier],
			BaseAmount: base, Amount: amount, AvailableAt: availableAt,
		}); err != nil {
			if errors.Is(err, ErrDuplicate) {
				continue // 冪等 ACK
			}
			return err
		}
	}
	return nil
}

// config 運行時讀配置（settings.affiliate 組扁平鍵——與設置目錄鍵名一致；
// 逐項讀取，單項缺失/非法回退默認值）。
func (s *AffiliateService) config(ctx context.Context) AffiliateConfig {
	return readAffiliateConfig(ctx, s.settings)
}

func readAffiliateConfig(ctx context.Context, settings notifyport.SettingsReader) AffiliateConfig {
	cfg := defaultConfig()
	if settings == nil {
		return cfg
	}
	get := func(key string, out any) bool {
		raw, err := settings.GetJSON(ctx, "affiliate", key)
		if err != nil || len(raw) == 0 {
			return false
		}
		return json.Unmarshal(raw, out) == nil
	}
	get("enabled", &cfg.Enabled)
	get("self_buy", &cfg.SelfBuy)
	var levels int
	if get("levels", &levels) && levels >= 1 && levels <= 3 {
		cfg.Levels = levels
	}
	// 0 表示該層不發傭；只有缺失或非法值才回落默認比例。
	var v int
	if get("rate_l1", &v) && v >= 0 {
		cfg.RateL1 = int32(v)
	}
	if get("rate_l2", &v) && v >= 0 {
		cfg.RateL2 = int32(v)
	}
	if get("rate_l3", &v) && v >= 0 {
		cfg.RateL3 = int32(v)
	}
	// 基數口徑（amount | profit；映射目錄 base 鍵）
	var base string
	if get("base", &base) && (base == "amount" || base == "profit") {
		cfg.BaseScope = base
	}
	// 凍結天數（confirm_days；>=0 均合法，0 = 立即到期）
	var days int
	if get("confirm_days", &days) && days >= 0 {
		cfg.FreezeDays = days
	}
	return cfg
}

// ConfirmDue 到期確認（cron 每小時；負債行重試扣款）。
// 正佣金 → wallet 入賬（冪等鍵 commission:<id>）；負負債行 → 嘗試扣款（成功標記 available=已抵扣）。
func (s *AffiliateService) ConfirmDue(ctx context.Context) {
	rows, err := s.repo.ListDueConfirm(ctx, time.Now().UTC(), 200)
	if err != nil {
		s.log.Warn("affiliate.confirm_list_failed", "err", err)
		return
	}
	for _, c := range rows {
		if e := s.confirmOne(ctx, c.ID); e != nil && s.log != nil {
			s.log.Warn("affiliate.confirm_failed", "id", c.ID, "err", e)
		}
	}
}

// OnOrderRefunded 訂閲 order.refunded（逆向扣回）。
func (s *AffiliateService) OnOrderRefunded(ctx context.Context, env events.Envelope) error {
	var p struct {
		OrderID         uint64 `json:"order_id"`
		RefundRatio     int64  `json:"refund_ratio"` // legacy
		CommerceVersion int32  `json:"commerce_version"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil
	}
	if p.CommerceVersion == 1 {
		return s.physicalRefund(ctx, p.OrderID)
	}
	ratio := p.RefundRatio
	if ratio <= 0 {
		ratio = 10000 // 全額
	}
	rows, err := s.repo.ListByOrder(ctx, p.OrderID)
	if err != nil {
		return err
	}
	for _, c := range rows {
		if string(c.Status) == "reversed" {
			continue // 冪等
		}
		clawback := c.Amount * ratio / 10000
		if clawback <= 0 {
			continue
		}
		switch string(c.Status) {
		case "pending_confirm":
			// 未入賬：作廢（按比例作廢整行——簡化口徑：任何比例退款全額作廢未確認行）
			_ = s.repo.MarkReversed(ctx, c.ID)
		case "available", "withdrawn":
			// 已入 wallet：扣回；不足 → 負債行（後續佣金抵扣）
			if err := s.wallet.DebitInTx(ctx, walletport.Entry{
				UserID: c.ReferrerID, Direction: "out", Type: "commission_reversal",
				Amount: centsOf(clawback), Reference: refKey(c.ID) + ":reversal",
				Remark: "退款佣金扣回",
			}); err != nil {
				_ = s.repo.InsertDebt(ctx, p.OrderID, c.ReferrerID, c.Tier, clawback)
			}
			_ = s.repo.MarkReversed(ctx, c.ID)
		}
	}
	return nil
}

func centsOf(v int64) money.Cents { return money.Cents(v) }

// PublishConfirmed 到賬通知事件（notify 消費；輕量直髮）。
func (s *AffiliateService) PublishConfirmed(ctx context.Context, c *ent.AffiliateCommission) {
	if s.outbox == nil {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"user_id": c.ReferrerID, "amount": c.Amount, "commission_id": c.ID,
	})
	_ = s.outbox.Write(ctx, "affiliate", "affiliate.commission.confirmed",
		refKey(c.ID), refKey(c.ID)+":confirmed", raw)
}

// Repo 佣金倉儲導出（wallet 打款 FIFO 消耗注入用，裝配期一次）。
func (s *AffiliateService) Repo() *CommissionRepo { return s.repo }
