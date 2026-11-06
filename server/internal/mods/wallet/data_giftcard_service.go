package wallet

// 禮品卡服務面（）：admin 批次創建/列表 + storefront 兑換。

import (
	"context"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// 禮品卡方法掛在 AdminWalletService（批次管理）與 StoreWalletService（兑換）上，
// 構造注入 *GiftcardRepo（wire）。// CreateGiftcardBatch 批次創建（面額/數量服務端邊界校驗——鐵律 16）。
// 明文碼僅本響應一次性返回（此後不可取——庫內無明文，鐵律 11）。
func (s *AdminWalletService) CreateGiftcardBatch(ctx context.Context, req *adminv1.CreateGiftcardBatchRequest) (*adminv1.CreateGiftcardBatchReply, error) {
	if !money.ValidCents(req.GetAmountCents()) || req.GetAmountCents() <= 0 {
		return nil, errors.BadRequest("wallet.GIFTCARD_INVALID", "面額須為正且不超上限")
	}
	batch, codes, err := s.giftcards.CreateBatch(ctx, BatchInput{
		BatchNo: req.GetBatchNo(), Name: req.GetName(),
		Amount: req.GetAmountCents(), Quantity: req.GetQuantity(),
		Operator: adminWalletUID(ctx),
	})
	if err != nil {
		msg := err.Error()
		if containsStr(msg, "BATCH_EXISTS") {
			return nil, errors.BadRequest("wallet.BATCH_EXISTS", "批次號已存在")
		}
		if containsStr(msg, "QUANTITY") {
			return nil, errors.BadRequest("wallet.QUANTITY_INVALID", "數量須在 1-5000 之間")
		}
		return nil, errors.InternalServer("wallet.BATCH_FAILED", "批次創建失敗")
	}
	return &adminv1.CreateGiftcardBatchReply{Batch: toGiftcardBatchPB(batch), Codes: codes}, nil
}

// ListGiftcardBatches 批次列表。
func (s *AdminWalletService) ListGiftcardBatches(ctx context.Context, req *adminv1.ListGiftcardBatchesRequest) (*adminv1.ListGiftcardBatchesReply, error) {
	page, size := withdrawPage(req.GetPage(), req.GetPageSize())
	rows, total, err := s.giftcards.ListBatches(ctx, page, size)
	if err != nil {
		return nil, errors.InternalServer("wallet.BATCH_LIST_FAILED", "讀取批次失敗")
	}
	reply := &adminv1.ListGiftcardBatchesReply{Total: total}
	for _, b := range rows {
		reply.Batches = append(reply.Batches, toGiftcardBatchPB(b))
	}
	return reply, nil
}

func (s *AdminWalletService) DeleteGiftcardBatch(ctx context.Context, req *adminv1.DeleteGiftcardBatchRequest) (*emptypb.Empty, error) {
	if req.GetId() == 0 {
		return nil, errors.BadRequest("wallet.BATCH_INVALID", "請選擇要刪除的批次")
	}
	if err := s.giftcards.DeleteBatch(ctx, req.GetId()); err != nil {
		if ent.IsNotFound(err) {
			return nil, errors.NotFound("wallet.BATCH_NOT_FOUND", "禮品卡批次不存在")
		}
		return nil, errors.InternalServer("wallet.BATCH_DELETE_FAILED", "刪除失敗，請重試")
	}
	return &emptypb.Empty{}, nil
}

// RedeemGiftcard 兑換（登錄用户；失敗統一「卡密無效」防枚舉）。
func (s *StoreWalletService) RedeemGiftcard(ctx context.Context, req *storefrontv1.RedeemGiftcardRequest) (*storefrontv1.RedeemGiftcardReply, error) {
	claims := identity.ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	if req.GetCode() == "" {
		return nil, errors.BadRequest("wallet.CODE_REQUIRED", "卡密必填")
	}
	amount, err := s.giftcards.Redeem(ctx, req.GetCode(), claims.Subject)
	if err != nil {
		msg := err.Error()
		if containsStr(msg, "LOCKED") {
			return nil, errors.Forbidden("wallet.GIFTCARD_LOCKED", "嘗試次數過多，請稍後再試")
		}
		return nil, errors.BadRequest("wallet.GIFTCARD_INVALID", "卡密無效或已使用")
	}
	avail, _, _ := s.giftcards.wallet.GetBalance(ctx, claims.Subject)
	return &storefrontv1.RedeemGiftcardReply{
		AmountCents: amount, BalanceAfterCents: avail,
	}, nil
}

func toGiftcardBatchPB(b *ent.GiftcardBatch) *adminv1.GiftcardBatchItem {
	out := &adminv1.GiftcardBatchItem{
		Id: b.ID, BatchNo: b.BatchNo, Name: b.Name,
		AmountCents: b.Amount, Quantity: b.Quantity,
	}
	if !b.CreatedAt.IsZero() {
		out.CreatedAt = b.CreatedAt.Unix()
	}
	return out
}

var _ = emptypb.Empty{}
