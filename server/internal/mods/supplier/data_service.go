package supplier

// 管理面 API：下游賬户（申請/審核/啓停/重置密鑰）、定價、賬本、回調管理。
// secret 紀律：僅創建/重置時回顯一次，其餘響應零泄漏。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplierledgerentry"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/google/uuid"

	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminSupplierService 管理面供貨服務。
type AdminSupplierService struct {
	adminv1.UnimplementedAdminSupplierServiceServer
	repo *SupplierRepoImpl
	api  *SupplyAPIService
}

// NewAdminSupplierService 構造。
func NewAdminSupplierService(repo *SupplierRepoImpl, api *SupplyAPIService) *AdminSupplierService {
	return &AdminSupplierService{repo: repo, api: api}
}

// CreateAccount 申請（secret 明文返回一次）。
func (s *AdminSupplierService) CreateAccount(ctx context.Context, req *adminv1.CreateSupplierAccountRequest) (*adminv1.SupplierAccountReply, error) {
	if req.GetApiKey() == "" || req.GetApiSecret() == "" {
		return nil, errors.New("supplier.KEY_REQUIRED: api_key/api_secret 必填")
	}
	protocol := req.GetProtocol()
	if protocol == "" {
		protocol = "zcard"
	}
	if protocol != "zcard" && protocol != "dujiao_next" && protocol != "acg_faka" {
		return nil, errors.New("supplier.INVALID_PROTOCOL: protocol 必須為 zcard|dujiao_next|acg_faka")
	}
	acc, err := s.repo.CreateAccount(ctx, req.GetName(), req.GetApiKey(), req.GetApiSecret(), req.GetContact(), protocol, req.GetDisplayName())
	if err != nil {
		return nil, err
	}
	return toAccountPB(acc, req.GetApiSecret()), nil
}

// ListAccounts 列表（secret 零回顯）。
func (s *AdminSupplierService) ListAccounts(ctx context.Context, req *adminv1.ListSupplierAccountsRequest) (*adminv1.ListSupplierAccountsReply, error) {
	page, size := pageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListAccounts(ctx, page, size)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListSupplierAccountsReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, r := range rows {
		reply.Accounts = append(reply.Accounts, toAccountPB(r, ""))
	}
	return reply, nil
}

// ReviewAccount 審核（通過/駁回；駁回置 rejected 狀態並記錄意見）。
func (s *AdminSupplierService) ReviewAccount(ctx context.Context, req *adminv1.ReviewSupplierAccountRequest) (*adminv1.SupplierAccountReply, error) {
	if !req.GetApprove() && strings.TrimSpace(req.GetReviewNote()) == "" {
		return nil, errors.New("supplier.REVIEW_NOTE_REQUIRED: 駁回時請填寫審核意見")
	}
	acc, err := s.repo.ReviewAccount(ctx, req.GetId(), req.GetApprove(), strings.TrimSpace(req.GetReviewNote()))
	if err != nil {
		return nil, err
	}
	return toAccountPB(acc, ""), nil
}

// ToggleAccount 啓停。
func (s *AdminSupplierService) ToggleAccount(ctx context.Context, req *adminv1.ToggleSupplierAccountRequest) (*adminv1.SupplierAccountReply, error) {
	acc, err := s.repo.ToggleAccount(ctx, req.GetId(), req.GetEnabled())
	if err != nil {
		return nil, err
	}
	return toAccountPB(acc, ""), nil
}

// ResetSecret 重置密鑰（明文返回一次）。
func (s *AdminSupplierService) ResetSecret(ctx context.Context, req *adminv1.ResetSupplierSecretRequest) (*adminv1.ResetSupplierSecretReply, error) {
	if err := s.repo.ResetSecret(ctx, req.GetId(), req.GetNewSecret()); err != nil {
		return nil, err
	}
	return &adminv1.ResetSupplierSecretReply{ApiSecret: req.GetNewSecret()}, nil
}

// SetNotifyURL 配置回調地址（http/https 均支持）。
func (s *AdminSupplierService) SetNotifyURL(ctx context.Context, req *adminv1.SetSupplierNotifyURLRequest) (*adminv1.SupplierAccountReply, error) {
	notifyURL := strings.TrimSpace(req.GetNotifyUrl())
	u, err := url.Parse(notifyURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("supplier.NOTIFY_URL_INVALID: 回調地址格式不正確（http/https）")
	}
	if err := s.repo.SetNotifyURL(ctx, req.GetId(), notifyURL); err != nil {
		return nil, err
	}
	acc, _ := s.repo.GetAccount(ctx, req.GetId())
	return toAccountPB(acc, ""), nil
}

// SetIPWhitelist 設置 IP 白名單（空 = 所有 IP 放行；接口鑑權層強制）。
func (s *AdminSupplierService) SetIPWhitelist(ctx context.Context, req *adminv1.SetSupplierIPWhitelistRequest) (*adminv1.SupplierAccountReply, error) {
	ips, err := normalizeWhitelist(req.GetIps())
	if err != nil {
		return nil, errors.New("supplier.IP_WHITELIST_INVALID: " + err.Error())
	}
	if err := s.repo.SetIPWhitelist(ctx, req.GetId(), ips); err != nil {
		return nil, err
	}
	acc, _ := s.repo.GetAccount(ctx, req.GetId())
	return toAccountPB(acc, ""), nil
}

// Recharge 充值（賬本入賬；reference 冪等由調用方保證）。
func (s *AdminSupplierService) Recharge(ctx context.Context, req *adminv1.RechargeSupplierRequest) (*emptypb.Empty, error) {
	if req.GetAmountCents() <= 0 || !money.ValidCents(req.GetAmountCents()) {
		return nil, kerrors.BadRequest("supplier.INVALID_AMOUNT", "充值金額須大於零且不能超過單筆金額上限")
	}
	key := strings.TrimSpace(req.GetReference())
	if len(key) > 80 {
		return nil, kerrors.BadRequest("supplier.INVALID_REFERENCE", "充值冪等鍵不能超過 80 字節")
	}
	if key == "" { // 兼容尚未發送冪等鍵的客户端；金額不能充當充值標識。
		key = uuid.NewString()
	}
	ref := fmt.Sprintf("recharge:manual:%d:%s", req.GetId(), key)
	if err := s.repo.Recharge(ctx, req.GetId(), req.GetAmountCents(), ref, orEmpty(req.GetRemark(), "管理員充值")); err != nil {
		if errors.Is(err, ErrDuplicateLedger) {
			entry, e := data.Client(ctx, s.repo.data).SupplierLedgerEntry.Query().Where(supplierledgerentry.Reference(ref)).Only(ctx)
			if e != nil {
				return nil, e
			}
			if entry.AccountID == req.GetId() && entry.Type == "recharge" && entry.Amount == req.GetAmountCents() {
				return &emptypb.Empty{}, nil
			}
			return nil, kerrors.Conflict("supplier.RECHARGE_REFERENCE_CONFLICT", "此充值已提交，請使用原金額重試；新充值請重新打開充值窗口")
		}
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListLedger 賬本流水。
func (s *AdminSupplierService) ListLedger(ctx context.Context, req *adminv1.ListSupplierLedgerRequest) (*adminv1.ListSupplierLedgerReply, error) {
	page, size := pageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListLedger(ctx, req.GetAccountId(), page, size)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListSupplierLedgerReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, r := range rows {
		reply.Entries = append(reply.Entries, &adminv1.SupplierLedgerEntry{
			Id: r.ID, AccountId: r.AccountID, SupplyOrderId: r.SupplyOrderID,
			Type: r.Type, Amount: r.Amount, Reference: r.Reference, Remark: r.Remark,
			CreatedAt: r.CreatedAt.Unix(),
		})
	}
	return reply, nil
}

// UpsertPrice 供貨定價。
func (s *AdminSupplierService) UpsertPrice(ctx context.Context, req *adminv1.UpsertSupplierPriceRequest) (*emptypb.Empty, error) {
	if err := s.repo.UpsertPriceRule(ctx, req.GetAccountId(), req.GetProductId(), req.GetSkuId(), req.GetCategoryId(), req.GetScope(), req.GetPrice(), req.GetDiscountBps()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListPrices 賬號的專屬價列表（：瀏覽覆蓋價）。
func (s *AdminSupplierService) ListPrices(ctx context.Context, req *adminv1.ListSupplierPricesRequest) (*adminv1.ListSupplierPricesReply, error) {
	rows, err := s.repo.ListPrices(ctx, req.GetAccountId())
	if err != nil {
		return nil, err
	}
	names, err := s.repo.PriceNames(ctx, rows)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListSupplierPricesReply{}
	for _, r := range rows {
		reply.Prices = append(reply.Prices, &adminv1.SupplierPriceItem{
			Id: r.ID, ProductId: r.ProductID, SkuId: r.SkuID,
			ProductName: names[r.ID][0], CategoryName: names[r.ID][1], SkuName: names[r.ID][2],
			Price: r.Price, UpdatedAt: r.UpdatedAt.Unix(), Scope: r.Scope, CategoryId: r.CategoryID, DiscountBps: r.DiscountBps,
		})
	}
	return reply, nil
}

// DeletePrice 刪除專屬價（恢復基礎供貨價）。
func (s *AdminSupplierService) DeletePrice(ctx context.Context, req *adminv1.DeleteSupplierPriceRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeletePrice(ctx, req.GetId()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListCallbacks 回調記錄。
func (s *AdminSupplierService) ListCallbacks(ctx context.Context, req *adminv1.ListSupplierCallbacksRequest) (*adminv1.ListSupplierCallbacksReply, error) {
	page, size := pageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListCallbacks(ctx, req.GetStatus(), page, size)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListSupplierCallbacksReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, r := range rows {
		reply.Callbacks = append(reply.Callbacks, &adminv1.SupplierCallback{
			Id: r.ID, SupplyOrderId: r.SupplyOrderID, AccountId: r.AccountID,
			DownstreamOrderNo: r.DownstreamOrderNo, CallbackUrl: r.CallbackURL,
			TraceId: r.TraceID, CallbackStatus: string(r.CallbackStatus),
			RetryCount: r.RetryCount, LastError: r.LastError,
		})
		if !r.LastCallbackAt.IsZero() {
			reply.Callbacks[len(reply.Callbacks)-1].LastCallbackAt = r.LastCallbackAt.Unix()
		}
	}
	return reply, nil
}

// ResendCallback 手動重發（死信恢復）。
func (s *AdminSupplierService) ResendCallback(ctx context.Context, req *adminv1.ResendSupplierCallbackRequest) (*emptypb.Empty, error) {
	cb, err := s.repo.ResetCallback(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	s.api.EnqueueCallback(ctx, cb.SupplyOrderID)
	return &emptypb.Empty{}, nil
}

func toAccountPB(acc *ent.SupplierAccount, secretOnce string) *adminv1.SupplierAccountReply {
	p := &adminv1.SupplierAccountReply{
		Id: acc.ID, Name: acc.Name, ApiKey: acc.APIKey,
		Contact: acc.Contact, Status: string(acc.Status),
		BalanceCache: acc.BalanceCache, NotifyUrl: acc.NotifyURL,
		CreatedAt: acc.CreatedAt.Unix(),
		Protocol:  string(acc.Protocol), DisplayName: acc.DisplayName,
		OwnerUserId: acc.OwnerUserID, ApplyReason: acc.ApplyReason,
		ReviewNote: acc.ReviewNote, IpWhitelist: acc.IPWhitelist,
	}
	if !acc.ReviewedAt.IsZero() {
		p.ReviewedAt = acc.ReviewedAt.Unix()
	}
	if secretOnce != "" {
		p.ApiSecret = secretOnce
	}
	return p
}

func pageParams(page, pageSize int32) (int, int) {
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

func orEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
