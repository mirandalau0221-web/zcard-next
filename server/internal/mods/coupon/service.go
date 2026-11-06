package coupon

// AdminCouponService 管理面優惠券服務（M1b 基礎版：批量生成 + 列表 + 作廢）。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminCouponService 服務。
type AdminCouponService struct {
	adminv1.UnimplementedAdminCouponServiceServer
	repo *CouponRepoImpl
}

// NewAdminCouponService 構造。
func NewAdminCouponService(repo *CouponRepoImpl) *AdminCouponService {
	return &AdminCouponService{repo: repo}
}

// ListCoupons 券列表（狀態/批次 + 分頁）。
func (s *AdminCouponService) ListCoupons(ctx context.Context, req *adminv1.ListCouponsRequest) (*adminv1.CouponList, error) {
	rows, total, batches, err := s.repo.ListCoupons(ctx, req.GetStatus(), req.GetBatchId(), int(req.GetPage()), int(req.GetPageSize()))
	if err != nil {
		return nil, errors.InternalServer("coupon.LIST_FAILED", "讀取券失敗")
	}
	reply := &adminv1.CouponList{
		Total:    int64(total),
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
		Batches:  batches,
	}
	for _, c := range rows {
		reply.Coupons = append(reply.Coupons, &adminv1.Coupon{
			Id: c.ID, BatchId: c.BatchID, Name: c.Name, Type: string(c.Type),
			Value: c.Value, Code: c.Code, Status: string(c.Status),
		})
	}
	return reply, nil
}

// CreateCouponBatch 批量生成。
func (s *AdminCouponService) CreateCouponBatch(ctx context.Context, req *adminv1.CreateCouponBatchRequest) (*adminv1.CreateCouponBatchReply, error) {
	if req.GetName() == "" || req.GetCount() <= 0 {
		return nil, errors.BadRequest("coupon.INVALID_INPUT", "名稱/數量必填且數量>0")
	}
	var expireAt *time.Time
	if req.GetExpireAt() > 0 {
		t := time.Unix(req.GetExpireAt(), 0).UTC()
		expireAt = &t
	}
	n, err := s.repo.CreateBatch(ctx, req.GetName(), req.GetType(), req.GetValue(), req.GetCount(), expireAt)
	if err != nil {
		return nil, errors.InternalServer("coupon.CREATE_FAILED", "生成失敗: "+err.Error())
	}
	return &adminv1.CreateCouponBatchReply{Count: n}, nil
}

// DisableCoupon 作廢（按 batch_id）。
func (s *AdminCouponService) DisableCoupon(ctx context.Context, req *adminv1.DisableCouponRequest) (*emptypb.Empty, error) {
	if _, err := s.repo.Disable(ctx, req.GetBatchId()); err != nil {
		return nil, errors.InternalServer("coupon.DISABLE_FAILED", "作廢失敗")
	}
	return &emptypb.Empty{}, nil
}

// DeleteCoupons 批量刪除（ids 優先；否則刪整批次未使用）。已使用/已作廢的跳過保審計。
func (s *AdminCouponService) DeleteCoupons(ctx context.Context, req *adminv1.DeleteCouponsRequest) (*adminv1.DeleteCouponsReply, error) {
	var (
		n   int
		err error
	)
	if len(req.GetIds()) > 0 {
		n, err = s.repo.DeleteCoupons(ctx, req.GetIds())
	} else {
		n, err = s.repo.DeleteBatchUnused(ctx, req.GetBatchId())
	}
	if err != nil {
		return nil, errors.InternalServer("coupon.DELETE_FAILED", "刪除失敗")
	}
	if n == 0 && len(req.GetIds()) == 0 && req.GetBatchId() == "" {
		return nil, errors.BadRequest("coupon.NO_TARGET", "未選擇要刪除的券")
	}
	return &adminv1.DeleteCouponsReply{Deleted: int32(n)}, nil
}

// ExportCoupons 導出券碼 CSV。
func (s *AdminCouponService) ExportCoupons(ctx context.Context, req *adminv1.ExportCouponsRequest) (*adminv1.ExportCouponsReply, error) {
	csv, err := s.repo.ExportCSV(ctx, req.GetStatus(), req.GetBatchId())
	if err != nil {
		return nil, errors.InternalServer("coupon.EXPORT_FAILED", "導出失敗")
	}
	name := time.Now().Format("20060102_150405")
	if b := req.GetBatchId(); b != "" {
		name = b + "_" + name
	} else if st := req.GetStatus(); st != "" {
		name = st + "_" + name
	}
	return &adminv1.ExportCouponsReply{Filename: "coupons_" + name + ".csv", Csv: csv}, nil
}

// ── M3 擴展 ────────────────────────────────────────────────

// GrantCoupon 批次贈送。
func (s *AdminCouponService) GrantCoupon(ctx context.Context, req *adminv1.GrantCouponRequest) (*adminv1.GrantCouponReply, error) {
	if req.GetCount() <= 0 || req.GetCount() > 1000 {
		return nil, fmt.Errorf("coupon.COUNT_INVALID: 1-1000")
	}
	n, err := s.repo.GrantToUser(ctx, req.GetBatchId(), req.GetUserId(), req.GetCount())
	if err != nil {
		return nil, err
	}
	return &adminv1.GrantCouponReply{Granted: n}, nil
}

// CreateFlashSale 創建秒殺。
func (s *AdminCouponService) CreateFlashSale(ctx context.Context, req *adminv1.CreateFlashSaleRequest) (*adminv1.FlashSaleItem, error) {
	if req.GetFlashPrice() <= 0 || req.GetLimitQty() <= 0 || req.GetEndAt() <= req.GetStartAt() {
		return nil, fmt.Errorf("coupon.FLASH_PARAMS_INVALID")
	}
	fs, err := s.repo.CreateFlash(ctx,
		req.GetProductId(), req.GetSkuId(), req.GetFlashPrice(),
		time.Unix(req.GetStartAt(), 0).UTC(), time.Unix(req.GetEndAt(), 0).UTC(),
		req.GetLimitQty(), orDefaultI32(req.GetPerUserLimit(), 1))
	if err != nil {
		return nil, err
	}
	return toFlashPB(fs), nil
}

// ListFlashSales 秒殺列表。
func (s *AdminCouponService) ListFlashSales(ctx context.Context, req *adminv1.ListFlashSalesRequest) (*adminv1.ListFlashSalesReply, error) {
	page, size := couponPageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListFlashAll(ctx, page, size)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListFlashSalesReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, fs := range rows {
		reply.Items = append(reply.Items, toFlashPB(fs))
	}
	return reply, nil
}

// DeleteFlashSale 刪除秒殺。
func (s *AdminCouponService) DeleteFlashSale(ctx context.Context, req *adminv1.DeleteFlashSaleRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteFlash(ctx, req.GetId()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// UpsertPromotion 創建/更新促銷。
func (s *AdminCouponService) UpsertPromotion(ctx context.Context, req *adminv1.UpsertPromotionRequest) (*adminv1.PromotionItem, error) {
	if req.GetEndAt() <= req.GetStartAt() {
		return nil, fmt.Errorf("coupon.PROMO_WINDOW_INVALID")
	}
	scope := map[string]any{}
	if req.GetScopeJson() != "" {
		if err := json.Unmarshal([]byte(req.GetScopeJson()), &scope); err != nil {
			return nil, errors.BadRequest("coupon.SCOPE_JSON_INVALID", "範圍 JSON 格式不正確，請參考商品或分類編號示例")
		}
	}
	if err := validatePromotionScope(scope); err != nil {
		return nil, err
	}
	p, err := s.repo.UpsertPromotion(ctx, req.GetId(), req.GetName(), scope, req.GetType(),
		req.GetThreshold(), req.GetDiscount(), req.GetSpecialPrice(),
		time.Unix(req.GetStartAt(), 0).UTC(), time.Unix(req.GetEndAt(), 0).UTC(), req.GetEnabled())
	if err != nil {
		return nil, err
	}
	return toPromoPB(p), nil
}

// ListPromotions 促銷列表。
func (s *AdminCouponService) ListPromotions(ctx context.Context, req *adminv1.ListPromotionsRequest) (*adminv1.ListPromotionsReply, error) {
	page, size := couponPageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListPromotions(ctx, page, size)
	if err != nil {
		return nil, err
	}
	reply := &adminv1.ListPromotionsReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, p := range rows {
		reply.Items = append(reply.Items, toPromoPB(p))
	}
	return reply, nil
}

func couponPageParams(page, pageSize int32) (int, int) {
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

func orDefaultI32(v, def int32) int32 {
	if v <= 0 {
		return def
	}
	return v
}
