package supplier

// 前台對接申請（個人中心）：提交申請 → 後台審核 → 憑據管理。
// 憑據格式兼容三面板：api_key=16 字節 hex（32 字符，滿足 acg-faka app_id ≤32 校驗）、
// api_secret=32 字節 hex（64 字符，acg/dujiao/zcard 均放行）。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplieraccount"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	settingsport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	paymentport "github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// 防刷護欄：同一用戶申請中 ≤5、賬戶總數 ≤10。
const (
	maxApplyingPerUser = 5
	maxAccountsPerUser = 10
)

// StoreSupplierService 前台供貨對接服務。
type StoreSupplierService struct {
	storefrontv1.UnimplementedStoreSupplierServiceServer
	repo     *SupplierRepoImpl
	settings settingsport.SettingsReader // 充值檔位（recharge 組；nil = 目錄缺省）
	payer    paymentport.RechargePayer   // 充值支付單創建（nil = 支付管線未裝配）
}

// NewStoreSupplierService 構造。
func NewStoreSupplierService(repo *SupplierRepoImpl, settings settingsport.SettingsReader, payer paymentport.RechargePayer) *StoreSupplierService {
	return &StoreSupplierService{repo: repo, settings: settings, payer: payer}
}

// currentUser 當前登錄用戶（user realm JWT；未登錄 401）。
func currentUser(ctx context.Context) (uint64, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return 0, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	return claims.Subject, nil
}

// SubmitSupplierApplication 提交對接申請（服務端生成憑據；secret 不在此返回）。
func (s *StoreSupplierService) SubmitSupplierApplication(ctx context.Context, req *storefrontv1.SubmitSupplierApplicationRequest) (*storefrontv1.SupplierAccountReply, error) {
	uid, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	protocol := strings.TrimSpace(req.GetProtocol())
	if protocol != "zcard" && protocol != "dujiao_next" && protocol != "acg_faka" {
		return nil, errors.BadRequest("supplier.INVALID_PROTOCOL", "協議必須為 zcard|dujiao_next|acg_faka")
	}
	displayName := strings.TrimSpace(req.GetDisplayName())
	if displayName == "" {
		return nil, errors.BadRequest("supplier.NAME_REQUIRED", "站點/店鋪名不能為空")
	}
	if len([]rune(displayName)) > 100 {
		return nil, errors.BadRequest("supplier.NAME_TOO_LONG", "站點/店鋪名不能超過 100 字")
	}
	if len([]rune(req.GetApplyReason())) > 500 {
		return nil, errors.BadRequest("supplier.REASON_TOO_LONG", "申請理由不能超過 500 字")
	}
	// 回調地址：http/https 均支持；裸域名自動補 https://（用戶只需填域名）
	notifyURL := strings.TrimSpace(req.GetNotifyUrl())
	if notifyURL != "" {
		notifyURL = normalizeCallbackURL(notifyURL)
		u, err := url.Parse(notifyURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, errors.BadRequest("supplier.NOTIFY_URL_INVALID", "回調地址格式不正確（填域名即可，如 shop.example.com/callback）")
		}
	}
	// 防刷護欄（併發下輕微超限可接受——上限為軟約束）
	applying, err := s.repo.CountMyApplying(ctx, uid)
	if err != nil {
		return nil, err
	}
	if applying >= maxApplyingPerUser {
		return nil, errors.Forbidden("supplier.APPLYING_LIMIT", "申請中的賬戶已達上限，請等待審核")
	}
	total, err := s.repo.CountMyAccounts(ctx, uid)
	if err != nil {
		return nil, err
	}
	if total >= maxAccountsPerUser {
		return nil, errors.Forbidden("supplier.ACCOUNT_LIMIT", "對接賬戶數量已達上限")
	}
	apiKey, apiSecret, err := generateCredentials()
	if err != nil {
		return nil, err
	}
	acc, err := s.repo.CreateApplication(ctx, uid, protocol, displayName,
		strings.TrimSpace(req.GetContact()), strings.TrimSpace(req.GetApplyReason()), notifyURL, apiKey, apiSecret)
	if err != nil {
		return nil, err
	}
	return toStoreAccountPB(acc), nil
}

// ListMySupplierAccounts 我的對接賬戶。
func (s *StoreSupplierService) ListMySupplierAccounts(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.ListSupplierAccountsReply, error) {
	uid, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListMyAccounts(ctx, uid)
	if err != nil {
		return nil, err
	}
	reply := &storefrontv1.ListSupplierAccountsReply{}
	for _, r := range rows {
		reply.Accounts = append(reply.Accounts, toStoreAccountPB(r))
	}
	return reply, nil
}

// GetSupplierCredentials 查看憑據（僅 approved 且歸屬本人；返回明文 secret）。
func (s *StoreSupplierService) GetSupplierCredentials(ctx context.Context, req *storefrontv1.GetSupplierCredentialsRequest) (*storefrontv1.SupplierCredentialsReply, error) {
	uid, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.mine(ctx, uid, req.GetId())
	if err != nil {
		return nil, err
	}
	if acc.Status != supplieraccount.StatusApproved {
		return nil, errors.Forbidden("supplier.NOT_APPROVED", "審核通過後才能查看憑據")
	}
	_, secret, err := s.repo.CredentialsOf(ctx, acc.ID)
	if err != nil {
		return nil, errors.InternalServer("supplier.SECRET_DECRYPT_FAILED", "密鑰解密失敗，請聯繫管理員重置")
	}
	return &storefrontv1.SupplierCredentialsReply{
		Id: acc.ID, Protocol: string(acc.Protocol), Status: string(acc.Status),
		ApiKey: acc.APIKey, ApiSecret: secret,
	}, nil
}

// RegenerateSupplierSecret 重置密鑰（舊 secret 立即失效；新明文僅此一次）。
func (s *StoreSupplierService) RegenerateSupplierSecret(ctx context.Context, req *storefrontv1.RegenerateSupplierSecretRequest) (*storefrontv1.SupplierCredentialsReply, error) {
	uid, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.mine(ctx, uid, req.GetId())
	if err != nil {
		return nil, err
	}
	if acc.Status != supplieraccount.StatusApproved {
		return nil, errors.Forbidden("supplier.NOT_APPROVED", "審核通過後才能重置密鑰")
	}
	_, newSecret, err := generateCredentials()
	if err != nil {
		return nil, err
	}
	if err := s.repo.ResetSecret(ctx, acc.ID, newSecret); err != nil {
		return nil, err
	}
	return &storefrontv1.SupplierCredentialsReply{
		Id: acc.ID, Protocol: string(acc.Protocol), Status: string(acc.Status),
		ApiKey: acc.APIKey, ApiSecret: newSecret,
	}, nil
}

// CancelSupplierApplication 撤銷申請（僅 applying）。
func (s *StoreSupplierService) CancelSupplierApplication(ctx context.Context, req *storefrontv1.CancelSupplierApplicationRequest) (*emptypb.Empty, error) {
	uid, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.mine(ctx, uid, req.GetId()); err != nil {
		return nil, err
	}
	if err := s.repo.CancelApplication(ctx, req.GetId()); err != nil {
		return nil, errors.BadRequest("supplier.CANCEL_NOT_ALLOWED", err.Error())
	}
	return &emptypb.Empty{}, nil
}

// CreateSupplierRecharge 對接賬戶自助充值（金額服務端檔位裁決；支付確認前不入賬）。
//
// 安全口徑：amount_cents 只是意向，服務端按 settings.supplier_recharge 檔位
// （enabled/min_amount/max_amount——供貨充值獨立配置組，與錢包 recharge 組隔離）
// 校驗；落 recharge_orders (pending, target=supply)。餘額入賬只發生在支付回調
// 成功後（payment.succeeded → settleRecharge 按 target 分支入賬供貨賬戶，
// reference=recharge:<paymentID> 冪等）。供貨預存無贈送（gift=0）。
func (s *StoreSupplierService) CreateSupplierRecharge(ctx context.Context, req *storefrontv1.CreateSupplierRechargeRequest) (*storefrontv1.CreateSupplierRechargeReply, error) {
	uid, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.mine(ctx, uid, req.GetId())
	if err != nil {
		return nil, err
	}
	if acc.Status != supplieraccount.StatusApproved {
		return nil, errors.Forbidden("supplier.NOT_APPROVED", "審核通過後才能充值")
	}
	amount := req.GetAmountCents()
	minAmount, maxAmount, enabled := s.rechargePolicy(ctx)
	if !enabled {
		return nil, errors.Forbidden("supplier.RECHARGE_DISABLED", "充值功能未開放")
	}
	if !money.ValidCents(amount) || amount < minAmount || amount > maxAmount {
		return nil, errors.BadRequest("supplier.INVALID_AMOUNT", "充值金額超出允許範圍")
	}
	if s.payer == nil {
		return nil, errors.InternalServer("supplier.PAYMENT_UNBOUND", "支付管線未裝配")
	}
	// 贈送檔位（settings.supplier_recharge.gift_tiers：充滿 X 分贈 Y 分供貨餘額；
	// 服務端裁決，客戶端不可指定——與錢包充值 giftFor 同口徑）
	giftAmount := s.giftFor(ctx, amount)
	ro, err := s.repo.CreateSupplyRechargeOrder(ctx, uid, acc.ID, amount, giftAmount)
	if err != nil {
		return nil, errors.InternalServer("supplier.RECHARGE_FAILED", "創建充值單失敗")
	}
	info, err := s.payer.CreateRechargePayment(paymentport.WithQuoteKey(ctx, req.GetQuoteKey()), ro.ID, req.GetChannel(), req.GetMethod(), money.Cents(amount))
	if err != nil {
		return nil, mapSupplierRechargeErr(err)
	}
	return &storefrontv1.CreateSupplierRechargeReply{
		RechargeId: ro.ID, PaymentId: info.PaymentID,
		Type: info.Type, Payload: info.Payload, Quote: &storefrontv1.PaymentQuote{BaseCents: info.BaseCents, FeeCents: info.FeeCents, TotalCents: info.TotalCents},
	}, nil
}

// giftFor 供貨充值贈送（supplier_recharge.gift_tiers 精確匹配充值金額）。
func (s *StoreSupplierService) giftFor(ctx context.Context, amount int64) int64 {
	if s.settings == nil {
		return 0
	}
	raw, err := s.settings.GetJSON(ctx, "supplier_recharge", "gift_tiers")
	if err != nil || len(raw) == 0 {
		return 0
	}
	var tiers []struct {
		Amount      int64 `json:"amount"`
		GiftBalance int64 `json:"gift_balance"`
	}
	if json.Unmarshal(raw, &tiers) != nil {
		return 0
	}
	for _, t := range tiers {
		if t.Amount == amount {
			return t.GiftBalance
		}
	}
	return 0
}

// rechargePolicy 供貨充值檔位（settings.supplier_recharge 組——獨立於錢包
// recharge 組；缺省 min=1000 分 / max=500000 分 / enabled=true，與目錄一致）。
func (s *StoreSupplierService) rechargePolicy(ctx context.Context) (minAmount, maxAmount int64, enabled bool) {
	minAmount, maxAmount, enabled = 1000, 500000, true
	if s.settings == nil {
		return
	}
	get := func(key string, out any) bool {
		raw, err := s.settings.GetJSON(ctx, "supplier_recharge", key)
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

// mapSupplierRechargeErr 充值支付發起錯誤映射（與錢包 mapRechargeErr 同口徑）。
func mapSupplierRechargeErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "QUOTE_CHANGED") {
		return errors.BadRequest("payment.QUOTE_CHANGED", "支付金額已變化，請刷新費用明細後再次確認")
	}
	if strings.Contains(msg, "SCENE_DISABLED") {
		return errors.BadRequest("supplier.SCENE_DISABLED", "該支付方式未開放供貨賬號充值，請更換支付方式")
	}
	if strings.Contains(msg, "METHOD_INVALID") {
		return errors.BadRequest("supplier.METHOD_INVALID", "請選擇該渠道支持的支付方式")
	}
	for _, prefix := range []string{"payment.CHANNEL_NOT_FOUND", "payment.CHANNEL_DISABLED", "payment.CONFIG_INVALID"} {
		if strings.HasPrefix(msg, prefix) {
			return errors.BadRequest("supplier.PAYMENT_CHANNEL_UNAVAILABLE", "支付渠道不可用，請更換支付方式")
		}
	}
	return errors.InternalServer("supplier.PAYMENT_FAILED", "發起支付失敗，請稍後重試")
}

// mine 屬主校驗：賬戶必須歸屬當前用戶。
func (s *StoreSupplierService) mine(ctx context.Context, uid, id uint64) (*ent.SupplierAccount, error) {
	acc, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		if err == ErrNotFound {
			return nil, errors.NotFound("supplier.NOT_FOUND", "賬戶不存在")
		}
		return nil, err
	}
	if acc.OwnerUserID != uid {
		return nil, errors.NotFound("supplier.NOT_FOUND", "賬戶不存在")
	}
	return acc, nil
}

// normalizeCallbackURL 裸域名補 https://（用戶只填域名場景）；已帶 http/https
// 前綴原樣返回；帶其他 scheme（ftp:// 等）不處理——交由調用方格式校驗拒絕。
func normalizeCallbackURL(raw string) string {
	if strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + raw
}

// SetSupplierIPWhitelist 設置 IP 白名單（僅 approved 且歸屬本人；空 = 所有 IP 放行）。
func (s *StoreSupplierService) SetSupplierIPWhitelist(ctx context.Context, req *storefrontv1.SetSupplierIPWhitelistRequest) (*storefrontv1.SupplierAccountReply, error) {
	uid, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.mine(ctx, uid, req.GetId())
	if err != nil {
		return nil, err
	}
	if acc.Status != supplieraccount.StatusApproved {
		return nil, errors.Forbidden("supplier.NOT_APPROVED", "審核通過後才能配置 IP 白名單")
	}
	ips, err := normalizeWhitelist(req.GetIps())
	if err != nil {
		return nil, errors.BadRequest("supplier.IP_WHITELIST_INVALID", err.Error())
	}
	if err := s.repo.SetIPWhitelist(ctx, acc.ID, ips); err != nil {
		return nil, err
	}
	updated, err := s.repo.GetAccount(ctx, acc.ID)
	if err != nil {
		return nil, err
	}
	return toStoreAccountPB(updated), nil
}

// normalizeWhitelist 白名單條目清洗：去空/去重 + 格式校驗（精確 IP 或 CIDR）+ 數量上限。
func normalizeWhitelist(ips []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" || seen[ip] {
			continue
		}
		if err := ValidateIPWhitelistEntry(ip); err != nil {
			return nil, err
		}
		seen[ip] = true
		out = append(out, ip)
	}
	if len(out) > maxWhitelistEntries {
		return nil, fmt.Errorf("白名單最多 %d 條", maxWhitelistEntries)
	}
	return out, nil
}

// generateCredentials 生成一對憑據（api_key 32 字符 hex / api_secret 64 字符 hex）。
func generateCredentials() (apiKey, apiSecret string, err error) {
	keyBytes := make([]byte, 16)
	if _, err = rand.Read(keyBytes); err != nil {
		return "", "", err
	}
	secretBytes := make([]byte, 32)
	if _, err = rand.Read(secretBytes); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(keyBytes), hex.EncodeToString(secretBytes), nil
}

// toStoreAccountPB 前台賬戶視圖（secret 永不出現；審核通過後走憑據接口）。
func toStoreAccountPB(acc *ent.SupplierAccount) *storefrontv1.SupplierAccountReply {
	p := &storefrontv1.SupplierAccountReply{
		Id: acc.ID, Protocol: string(acc.Protocol), Status: string(acc.Status),
		DisplayName: acc.DisplayName, Contact: acc.Contact,
		ApplyReason: acc.ApplyReason, ReviewNote: acc.ReviewNote,
		ApiKey: acc.APIKey, CreatedAt: acc.CreatedAt.Unix(),
		BalanceCache: acc.BalanceCache, IpWhitelist: acc.IPWhitelist,
	}
	if !acc.ReviewedAt.IsZero() {
		p.ReviewedAt = acc.ReviewedAt.Unix()
	}
	return p
}
