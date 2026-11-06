package inventory

// admin API（；薄 transport——導入預覽/確認/導出/列表/操作）。

import (
	"context"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/lotterydraw"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminInventoryService 庫存管理服務。
type AdminInventoryService struct {
	adminv1.UnimplementedAdminInventoryServiceServer
	repo *CardRepoImpl
	data *data.Data
}

// NewAdminInventoryService 構造。
func NewAdminInventoryService(repo *CardRepoImpl, d *data.Data) *AdminInventoryService {
	return &AdminInventoryService{repo: repo, data: d}
}

// ImportPreview 上傳預覽。
func (s *AdminInventoryService) ImportPreview(ctx context.Context, req *adminv1.ImportPreviewRequest) (*adminv1.ImportPreviewReply, error) {
	if req.GetProductId() == 0 || len(req.GetLines()) == 0 {
		return nil, errors.BadRequest("inventory.INVALID_INPUT", "商品 ID 與卡密行必填")
	}
	p, err := s.repo.ParseLines(ctx, ImportInput{
		ProductID:    req.GetProductId(),
		SkuID:        req.GetSkuId(),
		ImportFormat: req.GetImportFormat(),
		Lines:        req.GetLines(),
		Dedup:        req.GetDedup(),
	})
	if err != nil {
		return nil, inventoryTransportError(err, "inventory.PREVIEW_FAILED", "預覽失敗")
	}
	return &adminv1.ImportPreviewReply{
		Total: int32(p.Total), DupInFile: int32(p.DupInFile), DupInDb: int32(p.DupInDB),
		Invalid: int32(p.Invalid), Sample: p.Sample, IsPremium: p.IsPremium, SkuId: p.SkuID, SkuName: p.SkuName, Revision: p.Revision,
	}, nil
}

// ImportConfirm 確認導入。
func (s *AdminInventoryService) ImportConfirm(ctx context.Context, req *adminv1.ImportConfirmRequest) (*adminv1.ImportBatch, error) {
	if req.GetProductId() == 0 || len(req.GetLines()) == 0 {
		return nil, errors.BadRequest("inventory.INVALID_INPUT", "商品 ID 與卡密行必填")
	}
	imp, err := s.repo.ImportConfirm(ctx, ImportInput{
		ProductID:        req.GetProductId(),
		SkuID:            req.GetSkuId(),
		ImportFormat:     req.GetImportFormat(),
		ExpectedRevision: req.ExpectedRevision,
		Operator:         actorID(ctx),
		Lines:            req.GetLines(),
		Dedup:            req.GetDedup(),
	})
	if err != nil {
		if data.IsProductLocked(err) || errors.Reason(err) == "inventory.PHYSICAL_PRODUCT" {
			return nil, err
		}
		return nil, inventoryTransportError(err, "inventory.IMPORT_FAILED", "導入失敗")
	}
	// The write has committed; a metadata read failure must not turn it into an unknown import outcome.
	infos, e := s.batchInfos(ctx, []*ent.CardImport{imp})
	if e != nil {
		return toImportBatch(imp), nil
	}
	return infos[0], nil
}

// ListImports 批次列表。
func (s *AdminInventoryService) ListImports(ctx context.Context, req *adminv1.ListImportsRequest) (*adminv1.ListImportsReply, error) {
	if req.GetLegacyOnly() && req.SkuId != nil {
		return nil, errors.BadRequest("inventory.INVALID_INPUT", "歷史未記錄篩選不能同時指定導入規格")
	}
	rows, err := s.repo.ListImports(ctx, req.GetProductId(), req.SkuId, req.GetLegacyOnly())
	if err != nil {
		return nil, errors.InternalServer("inventory.LIST_FAILED", "讀取批次失敗")
	}
	batches, err := s.batchInfos(ctx, rows)
	if err != nil {
		return nil, errors.InternalServer("inventory.LIST_FAILED", "讀取批次失敗")
	}
	return &adminv1.ListImportsReply{Batches: batches}, nil
}

// CancelImport 撤銷批次。
func (s *AdminInventoryService) CancelImport(ctx context.Context, req *adminv1.CancelImportRequest) (*emptypb.Empty, error) {
	if err := s.repo.CancelImport(ctx, req.GetId(), req.GetExpectedRevision()); err != nil {
		if data.IsProductLocked(err) || errors.Reason(err) == "inventory.PHYSICAL_PRODUCT" {
			return nil, err
		}
		return nil, inventoryTransportError(err, "inventory.CANCEL_FAILED", "撤銷失敗")
	}
	return &emptypb.Empty{}, nil
}

// ListCards 卡密列表（掩碼默認）。
func (s *AdminInventoryService) ListCards(ctx context.Context, req *adminv1.ListCardsRequest) (*adminv1.ListCardsReply, error) {
	p, err := s.repo.inventoryProduct(ctx, req.GetProductId())
	if err != nil {
		return nil, inventoryTransportError(err, "inventory.LIST_FAILED", "讀取商品失敗")
	}
	names, err := s.skuNames(ctx, p.ID, p.SubsiteID)
	if err != nil {
		return nil, err
	}
	page := int(req.GetPage())
	size := int(req.GetPageSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	q := data.Client(ctx, s.data).Card.Query().
		Where(card.ProductID(req.GetProductId()), card.SubsiteID(p.SubsiteID), skuPredicate(req.SkuId))
	if req.ImportId != nil {
		q = q.Where(card.ImportID(req.GetImportId()))
	}
	if req.GetStatus() != "" {
		q = q.Where(card.StatusEQ(card.Status(req.GetStatus())))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, errors.InternalServer("inventory.LIST_FAILED", "讀取卡密失敗")
	}
	rows, err := q.Clone().Offset((page - 1) * size).Limit(size).Order(ent.Asc(card.FieldID)).All(ctx)
	if err != nil {
		return nil, errors.InternalServer("inventory.LIST_FAILED", "讀取卡密失敗")
	}
	awards := map[uint64]string{}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) > 0 {
		draws, e := data.Client(ctx, s.data).LotteryDraw.Query().Where(lotterydraw.CardIDIn(ids...)).All(ctx)
		if e != nil {
			return nil, errors.InternalServer("inventory.LIST_FAILED", "讀取發獎記錄失敗")
		}
		for _, draw := range draws {
			if draw.CardID != nil {
				awards[*draw.CardID] = draw.DrawNo
			}
		}
	}
	reply := &adminv1.ListCardsReply{Total: int64(total)}
	for _, r := range rows {
		reply.Cards = append(reply.Cards, &adminv1.CardInfo{
			Id: r.ID, ProductId: r.ProductID, Status: string(r.Status), SkuId: r.SkuID, SkuName: skuLabel(names, r.SkuID), ImportId: r.ImportID, CreatedAt: r.CreatedAt.Unix(),
			MaskedContent: s.maskPlain(r),
			Note:          r.Note,
			LotteryDrawNo: awards[r.ID],
		})
	}
	return reply, nil
}

// maskPlain 管理員默認掩碼（尾 4 位明文；解密失敗降級為 ****，絕不 500——鐵律 5/13）。
func (s *AdminInventoryService) maskPlain(r *ent.Card) string {
	if s.repo.Cipher == nil {
		return "****"
	}
	plain, err := s.repo.Cipher.Open(r.Content, r.ProductID, r.SubsiteID)
	if err != nil {
		return "****"
	}
	return maskContent(plain)
}

// ExportCards 導出（card:export 權限已在 middleware 校驗；此處內容解密）。
func (s *AdminInventoryService) ExportCards(ctx context.Context, req *adminv1.ExportCardsRequest) (*adminv1.ExportCardsReply, error) {
	lines, err := s.repo.ExportCards(ctx, req.GetProductId(), req.SkuId)
	if err != nil {
		return nil, inventoryTransportError(err, "inventory.EXPORT_FAILED", "導出失敗")
	}
	return &adminv1.ExportCardsReply{Lines: lines}, nil
}

// ToggleCard 禁用/啓用。
func (s *AdminInventoryService) toggleCard(ctx context.Context, req *adminv1.ToggleCardRequest) (*emptypb.Empty, error) {
	from, to := card.StatusAvailable, card.StatusDisabled
	if req.GetEnable() {
		from, to = card.StatusDisabled, card.StatusAvailable
	}
	c := data.Client(ctx, s.data)
	n, err := c.Card.Update().Where(card.ID(req.Id), card.SubsiteID(tenancy.FromContext(ctx).SubsiteID), card.StatusEQ(from)).SetStatus(to).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		ok, err := c.Card.Query().Where(card.ID(req.Id), card.SubsiteID(tenancy.FromContext(ctx).SubsiteID), card.StatusEQ(to)).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.BadRequest("inventory.STATUS_INVALID", "僅可啓用已禁用卡密或禁用可用卡密，已售出、已發獎和鎖定卡密不能修改")
		}
	}
	return &emptypb.Empty{}, nil
}

// ListPremiumCards 靚號列表（number_hash 命中 + 可用卡；card:premium 權限已在 middleware 校驗）。
func (s *AdminInventoryService) ListPremiumCards(ctx context.Context, req *adminv1.ListPremiumCardsRequest) (*adminv1.ListPremiumCardsReply, error) {
	p, err := s.repo.inventoryProduct(ctx, req.GetProductId())
	if err != nil {
		return nil, err
	}
	names, err := s.skuNames(ctx, p.ID, p.SubsiteID)
	if err != nil {
		return nil, err
	}
	q := data.Client(ctx, s.data).Card.Query().
		Where(
			card.ProductID(req.GetProductId()), card.SubsiteID(p.SubsiteID), skuPredicate(req.SkuId),
			card.StatusEQ(card.StatusAvailable),
			card.NumberHashNotNil(),
		)
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, errors.InternalServer("inventory.PREMIUM_LIST_FAILED", "讀取靚號失敗")
	}
	rows, err := q.Clone().Order(ent.Asc(card.FieldID)).Limit(500).All(ctx)
	if err != nil {
		return nil, errors.InternalServer("inventory.PREMIUM_LIST_FAILED", "讀取靚號失敗")
	}
	reply := &adminv1.ListPremiumCardsReply{Total: int64(total)}
	for _, r := range rows {
		reply.Cards = append(reply.Cards, &adminv1.PremiumCardInfo{
			Id: r.ID, ProductId: r.ProductID, MaskedContent: s.maskPlain(r),
			DraftPremium: r.DraftPremium, DraftCost: r.DraftCost,
			PriceCents: r.Price, Status: string(r.Status), SkuId: r.SkuID, SkuName: skuLabel(names, r.SkuID), ImportId: r.ImportID,
		})
	}
	return reply, nil
}

func nilOr(v uint64) *uint64 {
	if v == 0 {
		return nil
	}
	return &v
}

// ── 工具 ──

func toImportBatch(imp *ent.CardImport) *adminv1.ImportBatch {
	return &adminv1.ImportBatch{
		Id: imp.ID, ProductId: imp.ProductID, SkuId: imp.SkuID, SkuName: imp.SkuName,
		Total: imp.Total, Imported: imp.Imported,
		Skipped: imp.Skipped, Failed: imp.Failed,
		Status: string(imp.Status),
	}
}

// maskContent 掩碼明文（尾 4 位；不足 4 位返回 ****）。
func maskContent(plain string) string {
	if len(plain) <= 4 {
		return "****"
	}
	return "****" + plain[len(plain)-4:]
}

var _ = strings.TrimSpace

func (s *AdminInventoryService) ToggleCard(ctx context.Context, req *adminv1.ToggleCardRequest) (out *emptypb.Empty, err error) {
	err = data.Tx(ctx, s.data, func(ctx context.Context) error {
		row, e := data.Client(ctx, s.data).Card.Get(ctx, req.Id)
		if e != nil {
			return e
		}
		if _, e = data.GuardProductWrite(ctx, s.data, row.ProductID); e != nil {
			return e
		}
		out, e = s.toggleCard(ctx, req)
		return e
	})
	return
}
