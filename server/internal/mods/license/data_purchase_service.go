package license

// StoreLicenseService 專業套餐購買 storefront 面（ ）。

import (
	"context"
	"strings"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// StoreLicenseService 服務。
type StoreLicenseService struct {
	storefrontv1.UnimplementedStoreLicenseServiceServer
	repo *PurchaseRepo
}

// NewStoreLicenseService 構造。
func NewStoreLicenseService(repo *PurchaseRepo) *StoreLicenseService {
	return &StoreLicenseService{repo: repo}
}

// GetLicenseOffer 報價（登錄；價格服務端裁決；餘額走 wallet GetBalance）。
func (s *StoreLicenseService) GetLicenseOffer(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.LicenseOfferReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	offer, err := s.repo.Offer(ctx)
	if err != nil {
		return nil, errors.InternalServer("license.OFFER_FAILED", "讀取報價失敗")
	}
	return &storefrontv1.LicenseOfferReply{
		MonthlyCents: offer.MonthlyCents,
		YearlyCents:  offer.YearlyCents,
		Purchasable:  offer.Purchasable,
		InstanceId:   offer.InstanceID,
	}, nil
}

// PurchaseLicense 購買（扣款+簽發同事務；返回許可證文件）。
func (s *StoreLicenseService) PurchaseLicense(ctx context.Context, req *storefrontv1.PurchaseLicenseRequest) (*storefrontv1.PurchaseLicenseReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	plan := strings.ToLower(req.GetPlan())
	if plan != "monthly" && plan != "yearly" {
		return nil, errors.BadRequest("license.PLAN_INVALID", "套餐檔位非法（monthly|yearly）")
	}
	row, err := s.repo.Purchase(ctx, claims.Subject, plan, req.GetInstanceId(), req.GetDomain())
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "ISSUER_UNCONFIGURED"):
			return nil, errors.BadRequest("license.ISSUER_UNCONFIGURED", "本部署未配置簽發密鑰，暫不支持在線購買")
		case strings.Contains(msg, "BALANCE_INSUFFICIENT"):
			return nil, errors.BadRequest("license.BALANCE_INSUFFICIENT", "錢包餘額不足，請先充值")
		case strings.Contains(msg, "PLAN_INVALID"), strings.Contains(msg, "PRICE_INVALID"):
			return nil, errors.BadRequest("license.PLAN_INVALID", "套餐檔位非法或價格未配置")
		case strings.Contains(msg, "PUBKEY"):
			return nil, errors.InternalServer("license.PUBKEY_UNCONFIGURED", "公鑰未配置（發行側安裝校驗依賴）")
		}
		return nil, errors.InternalServer("license.PURCHASE_FAILED", "購買失敗")
	}
	return &storefrontv1.PurchaseLicenseReply{
		OrderId:     row.ID,
		LicenseFile: row.LicenseFile,
		ExpiresAt:   row.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		PaidCents:   row.Amount,
	}, nil
}

// ListLicenseOrders 購買記錄（含許可證文件重下載）。
func (s *StoreLicenseService) ListLicenseOrders(ctx context.Context, req *storefrontv1.ListLicenseOrdersRequest) (*storefrontv1.ListLicenseOrdersReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	page, size := int(req.GetPage()), int(req.GetPageSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 50 {
		size = 20
	}
	rows, total, err := s.repo.ListPurchases(ctx, claims.Subject, page, size)
	if err != nil {
		return nil, errors.InternalServer("license.ORDERS_FAILED", "讀取購買記錄失敗")
	}
	reply := &storefrontv1.ListLicenseOrdersReply{Total: total}
	for _, row := range rows {
		reply.Orders = append(reply.Orders, toLicenseOrderPB(row))
	}
	return reply, nil
}

func toLicenseOrderPB(row *ent.LicenseOrder) *storefrontv1.LicenseOrderItem {
	item := &storefrontv1.LicenseOrderItem{
		Id: row.ID, Plan: string(row.Plan), AmountCents: row.Amount,
		Status: string(row.Status), InstanceId: row.InstanceID, Domain: row.Domain,
		LicenseFile: row.LicenseFile,
		ExpiresAt:   row.ExpiresAt.Format("2006-01-02T15:04:05Z"),
	}
	if !row.CreatedAt.IsZero() {
		item.CreatedAt = row.CreatedAt.Unix()
	}
	return item
}
