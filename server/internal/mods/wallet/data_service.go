package wallet

// 錢包 API（；storefront 餘額/流水/充值 + admin 調賬/流水）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	settingsport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	paymentport "github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ── Storefront ──

// StoreWalletService 顧客錢包服務。
type StoreWalletService struct {
	storefrontv1.UnimplementedStoreWalletServiceServer
	repo *WalletRepoImpl
	data *data.Data
	// settings 充值檔位讀取（鐵律 16：客户端金額只作意向，檔位由服務端裁決）
	settings settingsport.SettingsReader
	// payer 充值支付單創建（payment 端口，通道 A；nil = 支付管線未裝配）
	payer paymentport.RechargePayer
	// giftcards 禮品卡兑換（；nil = 未裝配）
	giftcards *GiftcardRepo
	// commissions 佣金讀取（提現校驗；通道 A；nil = 跳過校驗）
	commissions CommissionSource
}

// NewStoreWalletService 構造。
func NewStoreWalletService(repo *WalletRepoImpl, d *data.Data, settings settingsport.SettingsReader, payer paymentport.RechargePayer, giftcards *GiftcardRepo, commissions CommissionSource) *StoreWalletService {
	return &StoreWalletService{repo: repo, data: d, settings: settings, payer: payer, giftcards: giftcards, commissions: commissions}
}

// GetBalance 餘額+積分。
func (s *StoreWalletService) GetBalance(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.BalanceReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	avail, locked, err := s.repo.GetBalance(ctx, claims.Subject)
	if err != nil {
		if errors.FromError(err).Reason == "wallet.CURRENCY_MISMATCH" {
			return nil, err
		}
		return nil, errors.InternalServer("wallet.BALANCE_FAILED", "查詢餘額失敗")
	}
	points, _ := s.repo.GetPoints(ctx, claims.Subject)
	return &storefrontv1.BalanceReply{
		AvailableCents: avail, LockedCents: locked, TotalCents: avail + locked,
		Points: points,
	}, nil
}

// ListTransactions 流水。
func (s *StoreWalletService) ListTransactions(ctx context.Context, req *storefrontv1.ListTxRequest) (*storefrontv1.ListTxReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	page := int(req.GetPage())
	size := int(req.GetPageSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, total, err := s.repo.ListTransactions(ctx, claims.Subject, page, size)
	if err != nil {
		if errors.FromError(err).Reason == "wallet.CURRENCY_MISMATCH" {
			return nil, err
		}
		return nil, errors.InternalServer("wallet.TX_FAILED", "查詢流水失敗")
	}
	references, err := s.repo.DisplayReferences(ctx, rows)
	if err != nil {
		return nil, errors.InternalServer("wallet.TX_FAILED", "查詢關聯單號失敗")
	}
	reply := &storefrontv1.ListTxReply{Total: total}
	for _, r := range rows {
		reply.Transactions = append(reply.Transactions, &storefrontv1.Tx{
			Id: r.ID, Direction: r.Direction, Type: r.Type,
			AmountCents: r.Amount, BalanceAfterCents: r.BalanceAfter,
			DisplayReference: references[r.ID], Reference: r.Reference, Remark: r.Remark, CreatedAt: r.CreatedAt.Unix(),
		})
	}
	return reply, nil
}

// CreateRecharge 充值（金額服務端檔位裁決；支付確認前絕不入賬——鐵律 16）。
//
// 安全口徑：客户端提交的 amount_cents 只是「意向」，服務端按 settings.recharge
// 檔位（enabled/min_amount/max_amount）校驗，贈送（gift_tiers）由服務端計算，
// 落 recharge_orders(pending)。餘額入賬只發生在支付回調成功後
// （payment.succeeded → wallet 訂閲，reference=recharge:<paymentID>—— 支付
// 管線接線後完成）。抓包改金額只能落在檔位區間內且不會直接入賬。
func (s *StoreWalletService) CreateRecharge(ctx context.Context, req *storefrontv1.CreateRechargeRequest) (*storefrontv1.CreateRechargeReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	amount := req.GetAmountCents()
	minAmount, maxAmount, enabled := s.rechargePolicy(ctx)
	if !enabled {
		return nil, errors.Forbidden("wallet.RECHARGE_DISABLED", "充值功能未開放")
	}
	if !money.ValidCents(amount) || amount < minAmount || amount > maxAmount {
		return nil, errors.BadRequest("wallet.INVALID_AMOUNT", "充值金額超出允許範圍")
	}
	giftAmount, giftPoints := s.giftFor(ctx, amount)
	ro, err := s.repo.CreateRechargeOrder(ctx, claims.Subject, amount, giftAmount, giftPoints)
	if err != nil {
		return nil, errors.InternalServer("wallet.RECHARGE_FAILED", "創建充值單失敗")
	}
	// 創建支付單併發起渠道（充值單保持 pending；餘額入賬只發生在回調成功後）
	if s.payer == nil {
		return nil, errors.InternalServer("wallet.PAYMENT_UNBOUND", "支付管線未裝配")
	}
	info, err := s.payer.CreateRechargePayment(paymentport.WithQuoteKey(ctx, req.GetQuoteKey()), ro.ID, req.GetChannel(), req.GetMethod(), money.Cents(amount))
	if err != nil {
		return nil, mapRechargeErr(err)
	}
	return &storefrontv1.CreateRechargeReply{
		RechargeId: ro.ID, PaymentId: info.PaymentID,
		Type: info.Type, Payload: info.Payload, Quote: &storefrontv1.PaymentQuote{BaseCents: info.BaseCents, FeeCents: info.FeeCents, TotalCents: info.TotalCents},
	}, nil
}

// mapRechargeErr 充值支付發起錯誤映射（渠道不存在/停用/配置無效等）。
func mapRechargeErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "QUOTE_CHANGED"):
		return errors.BadRequest("payment.QUOTE_CHANGED", "支付金額已變化，請刷新費用明細後再次確認")
	case containsStr(msg, "SCENE_DISABLED"):
		return errors.BadRequest("wallet.SCENE_DISABLED", "該支付方式未開放此充值用途，請更換支付方式")
	case containsStr(msg, "CHANNEL_DISABLED"):
		return errors.BadRequest("wallet.CHANNEL_DISABLED", "支付渠道已停用，請更換支付方式")
	case containsStr(msg, "CHANNEL_NOT_FOUND"):
		return errors.NotFound("wallet.CHANNEL_NOT_FOUND", "支付渠道不存在或未啓用")
	case containsStr(msg, "CHANNEL_INVALID"):
		return errors.BadRequest("wallet.CHANNEL_INVALID", "充值不支持餘額渠道")
	case containsStr(msg, "CHANNEL_UNSUPPORTED"):
		return errors.BadRequest("wallet.CHANNEL_UNSUPPORTED", "支付渠道驅動未實現")
	case containsStr(msg, "CHANNEL_CONFIG_INVALID"):
		return errors.InternalServer("wallet.CHANNEL_CONFIG_INVALID", "支付渠道配置無效")
	case containsStr(msg, "METHOD_INVALID"):
		return errors.BadRequest("wallet.METHOD_INVALID", "請選擇該渠道支持的支付方式")
	case containsStr(msg, "RECHARGE_NOT_FOUND"):
		return errors.NotFound("wallet.RECHARGE_NOT_FOUND", "充值單不存在")
	default:
		return errors.InternalServer("wallet.PAYMENT_CREATE_FAILED", "發起支付失敗")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// rechargePolicy 充值檔位（settings.recharge 組；讀取失敗回退目錄默認值——
// min=1000 分 / max=500000 分 / enabled=true，與 settings 目錄一致）。
func (s *StoreWalletService) rechargePolicy(ctx context.Context) (minAmount, maxAmount int64, enabled bool) {
	minAmount, maxAmount, enabled = 1000, 500000, true
	if s.settings == nil {
		return
	}
	get := func(key string, out any) bool {
		raw, err := s.settings.GetJSON(ctx, "recharge", key)
		if err != nil || len(raw) == 0 {
			return false
		}
		return json.Unmarshal(raw, out) == nil
	}
	get("enabled", &enabled)
	get("min_amount", &minAmount)
	get("max_amount", &maxAmount)
	return
}

// giftFor 贈送檔位（gift_tiers 精確匹配充值金額；客户端無法指定贈送——
// 防止「改贈送字段刷餘額」）。
func (s *StoreWalletService) giftFor(ctx context.Context, amount int64) (giftAmount int64, giftPoints int32) {
	if s.settings == nil {
		return 0, 0
	}
	raw, err := s.settings.GetJSON(ctx, "recharge", "gift_tiers")
	if err != nil || len(raw) == 0 {
		return 0, 0
	}
	var tiers []struct {
		Amount      int64 `json:"amount"`
		GiftBalance int64 `json:"gift_balance"`
		GiftPoints  int32 `json:"gift_points"`
	}
	if json.Unmarshal(raw, &tiers) != nil {
		return 0, 0
	}
	for _, t := range tiers {
		if t.Amount == amount {
			return t.GiftBalance, t.GiftPoints
		}
	}
	return 0, 0
}

// ── Admin ──

// AdminWalletService 錢包管理服務。
type AdminWalletService struct {
	adminv1.UnimplementedAdminWalletServiceServer
	repo      *WalletRepoImpl
	data      *data.Data
	giftcards *GiftcardRepo // 禮品卡批次管理（；nil = 未裝配）
}

// NewAdminWalletService 構造。
func NewAdminWalletService(repo *WalletRepoImpl, d *data.Data, giftcards *GiftcardRepo) *AdminWalletService {
	return &AdminWalletService{repo: repo, data: d, giftcards: giftcards}
}

// GetBalance 指定用户餘額。
func (s *AdminWalletService) GetBalance(ctx context.Context, req *adminv1.GetBalanceRequest) (*adminv1.Balance, error) {
	avail, locked, err := s.repo.GetBalance(ctx, req.GetUserId())
	if err != nil {
		if errors.FromError(err).Reason == "wallet.CURRENCY_MISMATCH" {
			return nil, err
		}
		return nil, errors.InternalServer("wallet.BALANCE_FAILED", "查詢失敗")
	}
	return &adminv1.Balance{
		UserId: req.GetUserId(), AvailableCents: avail, LockedCents: locked,
		TotalCents: avail + locked,
	}, nil
}

// AdjustPoints 積分調整（正=增加 負=扣減；走積分賬本，冪等鍵帶時間戳防同額去重誤傷）。
func (s *AdminWalletService) AdjustPoints(ctx context.Context, req *adminv1.AdjustPointsRequest) (*adminv1.PointsBalance, error) {
	if req.GetReason() == "" {
		return nil, errors.BadRequest("wallet.REASON_REQUIRED", "調賬原因必填")
	}
	if req.GetPoints() == 0 {
		return nil, errors.BadRequest("wallet.INVALID_POINTS", "積分調整數量必須非零")
	}
	pts := req.GetPoints()
	direction := "in"
	if pts < 0 {
		direction = "out"
		pts = -pts
	}
	ref := fmt.Sprintf("adjust-points:%d:%d:%d", req.GetUserId(), req.GetPoints(), time.Now().UnixNano())
	entry := PointEntry{
		UserID: req.GetUserId(), Direction: direction, Type: "adjust",
		Amount: pts, Reference: ref, Remark: req.GetReason(),
	}
	var err error
	if direction == "in" {
		err = s.repo.PointCreditInTx(ctx, entry)
	} else {
		err = s.repo.PointDebitInTx(ctx, entry)
	}
	if err != nil {
		return nil, errors.InternalServer("wallet.POINTS_ADJUST_FAILED", "積分調整失敗: "+err.Error())
	}
	balance, err := s.repo.GetPoints(ctx, req.GetUserId())
	if err != nil {
		balance = 0
	}
	return &adminv1.PointsBalance{UserId: req.GetUserId(), Points: balance}, nil
}

// Adjust 手動調賬（管理面唯一合法「客户端提交金額」路徑——RBAC 權限 + 服務端
// 邊界校驗 + 審計覆蓋，鐵律 16）。
func (s *AdminWalletService) Adjust(ctx context.Context, req *adminv1.AdjustRequest) (*adminv1.Balance, error) {
	if req.GetReason() == "" {
		return nil, errors.BadRequest("wallet.REASON_REQUIRED", "調賬原因必填")
	}
	if req.GetAmountCents() == 0 || !money.ValidSignedCents(req.GetAmountCents()) {
		return nil, errors.BadRequest("wallet.INVALID_AMOUNT", "調賬金額非法（非零且不超上限）")
	}
	claims := identity.ClaimsFromContext(ctx)
	var operatorID uint64
	if claims != nil {
		operatorID = claims.Subject
	}
	ref := fmt.Sprintf("adjust:%d:%d", req.GetUserId(), req.GetAmountCents())
	var err error
	if req.GetAmountCents() > 0 {
		err = s.repo.CreditInTx(ctx, Entry{
			UserID: req.GetUserId(), Direction: "in", Type: "adjust",
			Amount: req.GetAmountCents(), Reference: ref,
			OperatorID: operatorID, Remark: req.GetReason(),
		})
	} else if req.GetAmountCents() < 0 {
		err = s.repo.DebitInTx(ctx, Entry{
			UserID: req.GetUserId(), Direction: "out", Type: "adjust",
			Amount: -req.GetAmountCents(), Reference: ref,
			OperatorID: operatorID, Remark: req.GetReason(),
		})
	}
	if err != nil {
		return nil, errors.InternalServer("wallet.ADJUST_FAILED", "調賬失敗: "+err.Error())
	}
	// 返回新餘額
	avail, locked, _ := s.repo.GetBalance(ctx, req.GetUserId())
	return &adminv1.Balance{
		UserId: req.GetUserId(), AvailableCents: avail, LockedCents: locked,
		TotalCents: avail + locked,
	}, nil
}

// ListTransactions 指定用户流水。
func (s *AdminWalletService) ListTransactions(ctx context.Context, req *adminv1.ListWalletTxRequest) (*adminv1.ListWalletTxReply, error) {
	page := int(req.GetPage())
	size := int(req.GetPageSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, total, err := s.repo.ListTransactions(ctx, req.GetUserId(), page, size)
	if err != nil {
		if errors.FromError(err).Reason == "wallet.CURRENCY_MISMATCH" {
			return nil, err
		}
		return nil, errors.InternalServer("wallet.TX_FAILED", "查詢流水失敗")
	}
	// 全站流水（user_id=0）：批量回填歸屬用户名
	names := map[uint64]string{}
	if req.GetUserId() == 0 && len(rows) > 0 {
		ids := make([]uint64, 0, len(rows))
		seen := map[uint64]bool{}
		for _, r := range rows {
			if !seen[r.UserID] {
				seen[r.UserID] = true
				ids = append(ids, r.UserID)
			}
		}
		if names, err = s.repo.UsernamesByIDs(ctx, ids); err != nil {
			names = map[uint64]string{} // 用户名回填失敗不阻斷流水列表
		}
	}
	references, err := s.repo.DisplayReferences(ctx, rows)
	if err != nil {
		return nil, errors.InternalServer("wallet.TX_FAILED", "查詢關聯單號失敗")
	}
	reply := &adminv1.ListWalletTxReply{Total: total}
	for _, r := range rows {
		reply.Transactions = append(reply.Transactions, &adminv1.WalletTx{
			Id: r.ID, Direction: r.Direction, Type: r.Type,
			AmountCents: r.Amount, BalanceBeforeCents: r.BalanceBefore,
			DisplayReference: references[r.ID], BalanceAfterCents: r.BalanceAfter, Reference: r.Reference, Remark: r.Remark,
			UserId: r.UserID, Username: names[r.UserID], CreatedAt: r.CreatedAt.Unix(),
		})
	}
	return reply, nil
}

var _ = order.StatusPaid // 保持引用
