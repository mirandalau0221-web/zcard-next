package catalog

// 評價流數據層（ ）：真實評價（一單一評 + 審核流）+ 虛擬評價 + 前台合併展示。
// ent import 收口：data 前綴文件（架構測試規則 3b）。

import (
	"context"
	"fmt"
	"sort"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/review"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/virtualreview"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

// ErrReviewDuplicate 一單一評約束（UNIQUE(order_id) 兜底；此處先查給出業務語義）。
var ErrReviewDuplicate = fmt.Errorf("catalog.REVIEW_DUPLICATE")

// ── 真實評價（admin 審核）────────────────────────────────────

// ListReviews 評價列表（按 status 過濾；空=全部）。
func (r *ProductRepoImpl) ListReviews(ctx context.Context, status string, page, pageSize int32) ([]*ent.Review, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := data.Client(ctx, r.data).Review.Query().Order(ent.Desc(review.FieldCreatedAt))
	if status != "" {
		q = q.Where(review.StatusEQ(review.Status(status)))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Clone().
		Offset((int(page) - 1) * int(pageSize)).
		Limit(int(pageSize)).
		All(ctx)
	return rows, int64(total), err
}

// ApproveReview 審核通過（pending/approved/rejected → approved）。
func (r *ProductRepoImpl) ApproveReview(ctx context.Context, id uint64) (*ent.Review, error) {
	return data.Client(ctx, r.data).Review.UpdateOneID(id).
		SetStatus(review.StatusApproved).
		Save(ctx)
}

// RejectReview 審核拒絕（pending/approved/rejected → rejected）。
func (r *ProductRepoImpl) RejectReview(ctx context.Context, id uint64) (*ent.Review, error) {
	return data.Client(ctx, r.data).Review.UpdateOneID(id).
		SetStatus(review.StatusRejected).
		Save(ctx)
}

// CreateReview 創建真實評價（一單一評校驗；調用方：storefront 評價提交， 預留）。
func (r *ProductRepoImpl) CreateReview(ctx context.Context, productID, userID, orderID uint64, rating int8, content string) (*ent.Review, error) {
	tc := tenancy.FromContext(ctx)
	exists, err := data.Client(ctx, r.data).Review.Query().
		Where(review.OrderID(orderID)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrReviewDuplicate
	}
	return data.Client(ctx, r.data).Review.Create().
		SetSubsiteID(tc.SubsiteID).
		SetProductID(productID).
		SetUserID(userID).
		SetOrderID(orderID).
		SetRating(rating).
		SetContent(content).
		SetStatus(review.StatusPending).
		Save(ctx)
}

// ── 虛擬評價 ────────────────────────────────────────────────

// CreateVirtualReview 創建虛擬評價。
func (r *ProductRepoImpl) createVirtualReview(ctx context.Context, productID uint64, nickname, content string, rating int8, sort int32) (*ent.VirtualReview, error) {
	return data.Client(ctx, r.data).VirtualReview.Create().
		SetProductID(productID).
		SetNickname(nickname).
		SetContent(content).
		SetRating(rating).
		SetSort(sort).
		Save(ctx)
}

// ListVirtualReviews 商品虛擬評價（按 sort 升序，sort 相同按時間倒序）。
func (r *ProductRepoImpl) ListVirtualReviews(ctx context.Context, productID uint64) ([]*ent.VirtualReview, error) {
	return data.Client(ctx, r.data).VirtualReview.Query().
		Where(virtualreview.ProductID(productID)).
		Order(ent.Asc(virtualreview.FieldSort), ent.Desc(virtualreview.FieldCreatedAt)).
		All(ctx)
}

// ListProductReviews 前台評價合併：真實 approved（按時間倒序）+ 虛擬（按 sort/時間）。
// 合併策略：真實評價優先按時間倒序，虛擬評價按 sort 穿插在後（規劃 ）。
func (r *ProductRepoImpl) ListProductReviews(ctx context.Context, productID uint64) ([]port.ReviewItem, error) {
	client := data.Client(ctx, r.data)
	realRows, err := client.Review.Query().
		Where(review.ProductID(productID), review.StatusEQ(review.StatusApproved)).
		Order(ent.Desc(review.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	virtualRows, err := client.VirtualReview.Query().
		Where(virtualreview.ProductID(productID)).
		Order(ent.Asc(virtualreview.FieldSort), ent.Desc(virtualreview.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]port.ReviewItem, 0, len(realRows)+len(virtualRows))
	for _, v := range realRows {
		out = append(out, port.ReviewItem{
			ID:        v.ID,
			Nickname:  "", // 真實評價無暱稱列；前台用「匿名用户」兜底
			Content:   v.Content,
			Rating:    int32(v.Rating),
			IsVirtual: false,
			CreatedAt: v.CreatedAt,
		})
	}
	for _, v := range virtualRows {
		out = append(out, port.ReviewItem{
			ID:        v.ID,
			Nickname:  v.Nickname,
			Content:   v.Content,
			Rating:    int32(v.Rating),
			IsVirtual: true,
			Sort:      v.Sort,
			CreatedAt: v.CreatedAt,
		})
	}
	// 穩定排序：真實（時間倒序）在前，虛擬（sort 升序 → 時間倒序）在後。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsVirtual != out[j].IsVirtual {
			return !out[i].IsVirtual // 真實在前
		}
		if out[i].IsVirtual {
			if out[i].Sort != out[j].Sort {
				return out[i].Sort < out[j].Sort
			}
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (r *ProductRepoImpl) CreateVirtualReview(ctx context.Context, productID uint64, nickname, content string, rating int8, sort int32) (out *ent.VirtualReview, err error) {
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {
		if _, e := data.GuardProductWrite(ctx, r.data, productID); e != nil {
			return e
		}
		var e error
		out, e = r.createVirtualReview(ctx, productID, nickname, content, rating, sort)
		return e
	})
	return
}
