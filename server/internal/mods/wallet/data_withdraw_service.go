package wallet

// 提現服務面（ ）：storefront 申請 + admin 審核/打款。
// 金額服務端裁決（鐵律 16）：白名單/最低額/手續費全部取 settings.withdraw。

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/withdrawal"
	affiliateport "github.com/NovaWorks/zcard-next/server/internal/mods/affiliate/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"

	"github.com/go-kratos/kratos/v3/errors"
)

// withdrawPolicy 提現配置（settings.withdraw 組；讀取失敗回退目錄默認值）。
type withdrawPolicy struct {
	Enabled   bool
	MinAmount int64
	FeeType   string // fixed | percent
	FeeValue  int64  // fixed=分；percent=萬分比
	Methods   []withdrawMethod
}

type withdrawMethod struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Remark string `json:"remark,omitempty"`
}

func (s *StoreWalletService) withdrawPolicy(ctx context.Context) withdrawPolicy {
	p := withdrawPolicy{Enabled: false, MinAmount: 1000, FeeType: "fixed"}
	if s.settings == nil {
		return p
	}
	get := func(key string, out any) bool {
		raw, err := s.settings.GetJSON(ctx, "withdraw", key)
		if err != nil || len(raw) == 0 {
			return false
		}
		return json.Unmarshal(raw, out) == nil
	}
	get("enabled", &p.Enabled)
	get("min_amount", &p.MinAmount)
	var feeType string
	if get("fee_type", &feeType) && (feeType == "fixed" || feeType == "percent") {
		p.FeeType = feeType
	}
	get("fee_value", &p.FeeValue)
	var rawMethods json.RawMessage
	if get("methods", &rawMethods) {
		_ = json.Unmarshal(rawMethods, &p.Methods)
	}
	return p
}

// feeOf 手續費（fixed=分；percent=萬分比）。
func (p withdrawPolicy) feeOf(amount int64) int64 {
	if p.FeeType == "percent" {
		return amount * p.FeeValue / 10000
	}
	return p.FeeValue
}

// CreateWithdrawal 提現申請（佣金提現）：檔位校驗 → 白名單收款方式 →
// USDT 地址校驗 → 佣金餘額校驗（available − 凍結 ≥ 金額）→ 落單（凍結口徑=單本身）。
func (s *StoreWalletService) CreateWithdrawal(ctx context.Context, req *storefrontv1.CreateWithdrawalRequest) (*storefrontv1.CreateWithdrawalReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	policy := s.withdrawPolicy(ctx)
	if !policy.Enabled {
		return nil, errors.Forbidden("wallet.WITHDRAW_DISABLED", "提現功能未開放")
	}
	amount := req.GetAmountCents()
	if !money.ValidCents(amount) || amount < policy.MinAmount {
		return nil, errors.BadRequest("wallet.INVALID_AMOUNT", "提現金額低於最低限額或超出上限")
	}
	// 收款方式白名單（方法類型必須命中；賬號服務端不設限但落快照）
	var matched *withdrawMethod
	for i := range policy.Methods {
		if policy.Methods[i].Type == req.GetMethodType() {
			matched = &policy.Methods[i]
			break
		}
	}
	if matched == nil || req.GetAccount() == "" {
		return nil, errors.BadRequest("wallet.METHOD_INVALID", "收款方式不在白名單或賬號為空")
	}
	// USDT TRC20 地址格式校驗
	if matched.Type == "usdt_trc20" && !usdtTrc20Re.MatchString(req.GetAccount()) {
		return nil, errors.BadRequest("wallet.USDT_ADDRESS_INVALID", "USDT TRC20 地址格式不正確（T 開頭 34 位）")
	}
	// 佣金餘額校驗（可提 = available − 凍結中提現單）
	if s.commissions != nil {
		stats, err := s.commissions.StatsByUser(ctx, claims.Subject)
		if err != nil {
			return nil, errors.InternalServer("wallet.WITHDRAW_FAILED", "佣金查詢失敗")
		}
		frozen, _ := s.commissions.FrozenWithdrawAmount(ctx, claims.Subject)
		if stats.AvailableCents-frozen < amount {
			return nil, errors.BadRequest("wallet.INSUFFICIENT_COMMISSION", "可提佣金不足")
		}
	}
	method := map[string]any{
		"type": matched.Type, "name": matched.Name, "account": req.GetAccount(),
	}
	if qr := req.GetQrCodeUrl(); qr != "" {
		method["qr_code_url"] = qr // 收款碼圖片 URL（微信/支付寶；admin 審核展示）
	}
	fee := policy.feeOf(amount)
	w, err := s.repo.CreateWithdrawal(ctx, claims.Subject, amount, fee, method)
	if err != nil {
		return nil, mapWithdrawErr(err)
	}
	return &storefrontv1.CreateWithdrawalReply{
		WithdrawalId: w.ID, AmountCents: w.Amount, FeeCents: w.Fee,
		CreditedCents: w.Amount - w.Fee,
	}, nil
}

// ListMyWithdrawals 本人提現記錄（狀態/駁回原因/時間）。
func (s *StoreWalletService) ListMyWithdrawals(ctx context.Context, req *storefrontv1.ListMyWithdrawalsRequest) (*storefrontv1.ListMyWithdrawalsReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	page, size := withdrawPage(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListWithdrawalsByUser(ctx, claims.Subject, page, size)
	if err != nil {
		return nil, errors.InternalServer("wallet.LIST_FAILED", "讀取提現記錄失敗")
	}
	reply := &storefrontv1.ListMyWithdrawalsReply{Total: total}
	for _, w := range rows {
		item := &storefrontv1.MyWithdrawalItem{
			WithdrawalId: w.ID, AmountCents: w.Amount, FeeCents: w.Fee,
			Status: string(w.Status), RejectReason: w.RejectReason,
		}
		var m struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Account string `json:"account"`
		}
		if raw, err := json.Marshal(w.Method); err == nil {
			_ = json.Unmarshal(raw, &m)
		}
		item.MethodType, item.MethodName, item.Account = m.Type, m.Name, m.Account
		if !w.ReviewedAt.IsZero() {
			item.ReviewedAt = w.ReviewedAt.Unix()
		}
		if !w.PaidAt.IsZero() {
			item.PaidAt = w.PaidAt.Unix()
		}
		item.Receipt = w.Receipt
		item.CreatedAt = w.CreatedAt.Unix()
		reply.Withdrawals = append(reply.Withdrawals, item)
	}
	return reply, nil
}

func mapWithdrawErr(err error) error {
	msg := err.Error()
	switch {
	case containsStr(msg, "INSUFFICIENT"):
		return errors.BadRequest("wallet.INSUFFICIENT_BALANCE", "可用餘額不足")
	case containsStr(msg, "CONCURRENT"):
		return errors.InternalServer("wallet.CONCURRENT_UPDATE", "併發衝突，請重試")
	case containsStr(msg, "NOT_FOUND"):
		return errors.NotFound("wallet.WITHDRAWAL_NOT_FOUND", "提現單不存在")
	case containsStr(msg, "NOT_PENDING"), containsStr(msg, "NOT_APPROVED"):
		return errors.BadRequest("wallet.WITHDRAWAL_STATE", "提現單狀態不允許該操作")
	default:
		return errors.InternalServer("wallet.WITHDRAW_FAILED", "提現操作失敗")
	}
}

// ── Admin 提現管理 ──────────────────────────────────────────

// ListWithdrawals 提現單列表。
func (s *AdminWalletService) ListWithdrawals(ctx context.Context, req *adminv1.ListWithdrawalsRequest) (*adminv1.ListWithdrawalsReply, error) {
	page, size := withdrawPage(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListWithdrawals(ctx, req.GetStatus(), page, size)
	if err != nil {
		return nil, errors.InternalServer("wallet.LIST_FAILED", "讀取提現單失敗")
	}
	reply := &adminv1.ListWithdrawalsReply{Total: total}
	for _, w := range rows {
		reply.Withdrawals = append(reply.Withdrawals, toWithdrawalPB(w))
	}
	s.enrichUsernames(ctx, reply.Withdrawals)
	return reply, nil
}

// ReviewWithdrawal 審核（通過/駁回；駁回解鎖回餘額）。
func (s *AdminWalletService) ReviewWithdrawal(ctx context.Context, req *adminv1.ReviewWithdrawalRequest) (*adminv1.WithdrawalItem, error) {
	if !req.GetApprove() && req.GetReason() == "" {
		return nil, errors.BadRequest("wallet.REASON_REQUIRED", "駁回原因必填")
	}
	w, err := s.repo.ReviewWithdrawal(ctx, req.GetId(), req.GetApprove(), req.GetReason(), adminWalletUID(ctx))
	if err != nil {
		return nil, mapWithdrawErr(err)
	}
	return toWithdrawalPB(w), nil
}

// PayWithdrawal 打款（人工打款模式：approved→paid + locked 扣減 + 流水）。
func (s *AdminWalletService) PayWithdrawal(ctx context.Context, req *adminv1.PayWithdrawalRequest) (*adminv1.WithdrawalItem, error) {
	w, err := s.repo.PayWithdrawal(ctx, req.GetId(), req.GetReceipt())
	if err != nil {
		return nil, mapWithdrawErr(err)
	}
	return toWithdrawalPB(w), nil
}

func adminWalletUID(ctx context.Context) uint64 {
	if claims := identity.ClaimsFromContext(ctx); claims != nil {
		return claims.Subject
	}
	return 0
}

func withdrawPage(page, pageSize int32) (int, int) {
	p := int(page)
	if p < 1 {
		p = 1
	}
	ps := int(pageSize)
	if ps < 1 {
		ps = 20
	}
	if ps > 100 {
		ps = 100
	}
	return p, ps
}

// toWithdrawalPB 轉 admin 協議（method 快照拆出 type/name/account/qr）。
func toWithdrawalPB(w *ent.Withdrawal) *adminv1.WithdrawalItem {
	out := &adminv1.WithdrawalItem{
		Id: w.ID, UserId: w.UserID, AmountCents: w.Amount, FeeCents: w.Fee,
		Status: string(w.Status), RejectReason: w.RejectReason,
	}
	var m struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Account   string `json:"account"`
		QrCodeURL string `json:"qr_code_url"`
	}
	if raw, err := json.Marshal(w.Method); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	out.MethodType, out.MethodName, out.Account, out.QrCodeUrl = m.Type, m.Name, m.Account, m.QrCodeURL
	if !w.ReviewedAt.IsZero() {
		out.ReviewedAt = w.ReviewedAt.Unix()
	}
	if !w.PaidAt.IsZero() {
		out.PaidAt = w.PaidAt.Unix()
	}
	out.Receipt = w.Receipt
	out.CreatedAt = w.CreatedAt.Unix()
	return out
}

// enrichUsernames 批量富化用戶名（列表一次查詢防 N+1）。
func (s *AdminWalletService) enrichUsernames(ctx context.Context, items []*adminv1.WithdrawalItem) {
	if len(items) == 0 {
		return
	}
	ids := make([]uint64, 0, len(items))
	for _, it := range items {
		if it.UserId > 0 {
			ids = append(ids, it.UserId)
		}
	}
	if len(ids) == 0 {
		return
	}
	rows, err := data.Client(ctx, s.data).User.Query().Where(user.IDIn(ids...)).All(ctx)
	if err != nil {
		return
	}
	nameOf := map[uint64]string{}
	for _, u := range rows {
		nameOf[u.ID] = u.Username
	}
	for _, it := range items {
		it.Username = nameOf[it.UserId]
	}
}

var _ = withdrawal.StatusPending
var _ = fmt.Sprintf

// CommissionSource 佣金讀取端口（提現校驗；affiliate.CommissionRepo 實現，通道 A）。
type CommissionSource interface {
	StatsByUser(ctx context.Context, userID uint64) (*affiliateport.CommissionStats, error)
	FrozenWithdrawAmount(ctx context.Context, userID uint64) (int64, error)
}

// usdtTrc20Re TRC20 地址（T 開頭 34 位 base58，剔除 0OIl）。
var usdtTrc20Re = regexp.MustCompile(`^T[1-9A-HJ-NP-Za-km-z]{33}$`)
