package coupon

// 前台營銷 API（）：我的券/兑換/秒殺營銷位。

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"

	"google.golang.org/protobuf/types/known/emptypb"
)

// StoreCouponService 前台營銷服務。
type StoreCouponService struct {
	storefrontv1.UnimplementedStoreCouponServiceServer
	repo *CouponRepoImpl
}

// NewStoreCouponService 構造。
func NewStoreCouponService(repo *CouponRepoImpl) *StoreCouponService {
	return &StoreCouponService{repo: repo}
}

// ListMyCoupons 我的可用券（user realm JWT；未登錄空）。
func (s *StoreCouponService) ListMyCoupons(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.ListMyCouponsReply, error) {
	userID := currentUserID(ctx)
	if userID == 0 {
		return &storefrontv1.ListMyCouponsReply{}, nil
	}
	rows, err := s.repo.ListMyCoupons(ctx, userID)
	if err != nil {
		return nil, err
	}
	reply := &storefrontv1.ListMyCouponsReply{}
	for _, c := range rows {
		item := &storefrontv1.MyCoupon{
			Id: c.ID, Name: c.Name, Type: string(c.Type), Value: c.Value, Code: c.Code,
		}
		if c.Scope != nil {
			if b, err := json.Marshal(c.Scope); err == nil {
				item.ScopeJson = string(b)
			}
		}
		if !c.ExpireAt.IsZero() {
			item.ExpireAt = c.ExpireAt.Unix()
		}
		reply.Coupons = append(reply.Coupons, item)
	}
	return reply, nil
}

// RedeemCoupon 兑換碼領券。
func (s *StoreCouponService) RedeemCoupon(ctx context.Context, req *storefrontv1.RedeemCouponRequest) (*emptypb.Empty, error) {
	userID := currentUserID(ctx)
	if userID == 0 {
		return nil, errors.New("coupon.UNAUTHORIZED: 請先登錄")
	}
	if req.GetCode() == "" {
		return nil, errors.New("coupon.CODE_REQUIRED")
	}
	if err := s.repo.Redeem(ctx, req.GetCode(), userID); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListFlashSales 秒殺營銷位（進行中/即將開始 + 剩餘量）。
func (s *StoreCouponService) ListFlashSales(ctx context.Context, req *storefrontv1.ListFlashSalesRequest) (*storefrontv1.ListFlashSalesReply, error) {
	rows, err := s.repo.ListFlash(ctx, time.Now().UTC(), req.GetUpcoming())
	if err != nil {
		return nil, err
	}
	reply := &storefrontv1.ListFlashSalesReply{}
	for _, fs := range rows {
		reply.Items = append(reply.Items, &storefrontv1.StoreFlashSale{
			Id: fs.ID, ProductId: fs.ProductID, FlashPrice: fs.FlashPrice,
			StartAt: fs.StartAt.Unix(), EndAt: fs.EndAt.Unix(),
			Remaining: fs.LimitQty - fs.SoldQty,
		})
	}
	return reply, nil
}

// currentUserID user realm JWT 主體。
func currentUserID(ctx context.Context) uint64 {
	if claims := identity.ClaimsFromContext(ctx); claims != nil {
		return claims.Subject
	}
	return 0
}
