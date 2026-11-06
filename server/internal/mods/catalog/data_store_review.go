package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/review"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/setting"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/sanitize"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
)

type StoreReviewService struct {
	storefrontv1.UnimplementedStoreReviewServiceServer
	repo *ProductRepoImpl
}

func NewStoreReviewService(repo *ProductRepoImpl) *StoreReviewService {
	return &StoreReviewService{repo: repo}
}

func (s *StoreReviewService) ownedOrder(ctx context.Context, no string) (*ent.Order, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil || claims.Subject == 0 {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "請先登錄")
	}
	o, err := data.Client(ctx, s.repo.data).Order.Query().Where(order.OrderNo(no), order.UserID(claims.Subject), order.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("review.ORDER_NOT_FOUND", "訂單不存在或無權評價")
	}
	if err != nil {
		return nil, errors.InternalServer("review.READ_FAILED", "讀取訂單失敗")
	}
	return o, nil
}
func (s *StoreReviewService) reviewState(ctx context.Context, o *ent.Order) (*storefrontv1.OrderReviewState, error) {
	c := data.Client(ctx, s.repo.data)
	enabled := true
	cfg, err := c.Setting.Query().Where(setting.Group("template"), setting.Key("show_reviews")).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, errors.InternalServer("review.CONFIG_FAILED", "讀取評價設置失敗")
	}
	if err == nil {
		if e := json.Unmarshal(cfg.Value, &enabled); e != nil {
			return nil, errors.InternalServer("review.CONFIG_FAILED", "評價設置異常")
		}
	}
	out := &storefrontv1.OrderReviewState{Enabled: enabled, Status: "not_ready", Message: "訂單發貨完成後即可評價"}
	if !enabled {
		out.Status = "disabled"
		out.Message = "評價功能暫未開放"
		return out, nil
	}
	existing, err := c.Review.Query().Where(review.OrderID(o.ID), review.UserID(o.UserID), review.SubsiteID(o.SubsiteID)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, errors.InternalServer("review.READ_FAILED", "讀取評價失敗")
	}
	if err == nil {
		out.Status = string(existing.Status)
		out.ProductId = existing.ProductID
		out.Rating = int32(existing.Rating)
		out.Content = existing.Content
		out.Message = map[string]string{"pending": "評價已提交，審核通過後展示", "approved": "評價已發佈", "rejected": "評價未通過審核，如有疑問請提交工單"}[out.Status]
		return out, nil
	}
	if o.Status != order.StatusDelivered && o.Status != order.StatusCompleted {
		return out, nil
	}
	summaries, err := data.OrderProductSummaries(ctx, s.repo.data, []*ent.Order{o})
	if err != nil {
		return nil, errors.InternalServer("review.PRODUCTS_FAILED", "讀取訂單商品失敗")
	}
	seen := map[uint64]bool{}
	for _, it := range summaries[o.ID] {
		if !seen[it.ProductID] {
			out.Products = append(out.Products, &storefrontv1.ReviewProduct{ProductId: it.ProductID, Name: it.Name})
			seen[it.ProductID] = true
		}
	}
	if len(out.Products) > 0 {
		out.Status = "available"
		out.Message = "每筆訂單可評價一件已購商品，審核通過後公開展示"
	}
	return out, nil
}
func (s *StoreReviewService) GetOrderReview(ctx context.Context, req *storefrontv1.GetOrderReviewRequest) (*storefrontv1.OrderReviewState, error) {
	o, e := s.ownedOrder(ctx, req.GetOrderNo())
	if e != nil {
		return nil, e
	}
	return s.reviewState(ctx, o)
}
func (s *StoreReviewService) SubmitOrderReview(ctx context.Context, req *storefrontv1.SubmitOrderReviewRequest) (*storefrontv1.OrderReviewState, error) {
	o, err := s.ownedOrder(ctx, req.GetOrderNo())
	if err != nil {
		return nil, err
	}
	state, err := s.reviewState(ctx, o)
	if err != nil {
		return nil, err
	}
	if !state.Enabled {
		return nil, errors.Forbidden("review.DISABLED", "評價功能暫未開放")
	}
	if state.Status != "available" {
		return nil, errors.Conflict("review.NOT_AVAILABLE", state.Message)
	}
	content := strings.TrimSpace(sanitize.Text(req.GetContent()))
	if req.GetRating() < 1 || req.GetRating() > 5 || utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > 1000 {
		return nil, errors.BadRequest("review.INVALID_INPUT", "請選擇 1–5 星，評價內容為 1–1000 字")
	}
	purchased, err := data.Client(ctx, s.repo.data).OrderItem.Query().Where(orderitem.OrderID(o.ID), orderitem.ProductID(req.GetProductId()), orderitem.SubsiteID(o.SubsiteID)).Exist(ctx)
	if err != nil {
		return nil, errors.InternalServer("review.READ_FAILED", "讀取訂單商品失敗")
	}
	if !purchased {
		return nil, errors.BadRequest("review.PRODUCT_NOT_PURCHASED", "只能評價本訂單購買的商品")
	}
	_, err = s.repo.CreateReview(ctx, req.GetProductId(), o.UserID, o.ID, int8(req.GetRating()), content)
	if err == ErrReviewDuplicate || ent.IsConstraintError(err) {
		return nil, errors.Conflict("review.DUPLICATE", "該訂單已經提交過評價")
	}
	if err != nil {
		return nil, errors.InternalServer("review.WRITE_FAILED", "提交失敗，請稍後重試")
	}
	return s.reviewState(ctx, o)
}
