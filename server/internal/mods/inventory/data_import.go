package inventory

// 導入管線（ ）：預覽 → 確認 → 分片批量寫入 → 批次追蹤。
// 性能紀律（PG/MySQL 大數量優化）：去重一律 content_hash 分組 IN 批查
// （預覽全量一次、確認每分片一次，杜絕逐行 Exist 的 N+1）；寫入走
// CreateBulk + ON CONFLICT DO NOTHING（唯一索引 (subsite_id, product_id,
// content_hash) 兜底，併發導入窗口的重複行靜默跳過）。

import (
	"context"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/cardimport"

	entsql "entgo.io/ent/dialect/sql"
)

// ImportPreview 預覽結果（確認前端步展示）。
type ImportPreview struct {
	Total     int      `json:"total"`
	DupInFile int      `json:"dup_in_file"` // 文件內重複
	DupInDB   int      `json:"dup_in_db"`   // 與庫內重複（同商品）
	Invalid   int      `json:"invalid"`     // 格式非法行
	Sample    []string `json:"sample"`      // 前 50 行預覽
	IsPremium bool     `json:"is_premium"`  // 顯式靚號模式解析到了卡號
	SkuID     uint64
	SkuName   string
	Revision  int64
}

// ImportInput 導入參數。
type ImportInput struct {
	ProductID        uint64
	SkuID            uint64
	Lines            []string // 原始行（默認每行完整文本即一條交付卡密）
	ImportFormat     string   // line（默認）| premium（顯式使用舊靚號首段解析）
	Dedup            bool     // 去重開關（false=重複行報錯）
	Operator         uint64
	ExpectedRevision *int64
	SubsiteID        uint64
	SkuName          string
}

// dedupChunk IN 批查單批上限（500：IN 列表與唯一索引掃描的平衡點）。
const dedupChunk = 500

func validateImportFormat(format string) error {
	if format != "" && format != "line" && format != "premium" {
		return kerrors.BadRequest("inventory.IMPORT_FORMAT_INVALID", "請選擇整行卡密或靚號格式")
	}
	return nil
}

// importLine keeps delimiters inside ordinary credentials. Legacy number-only
// parsing is available only when the administrator explicitly selects premium.
// Later segments retain the old ignored behavior; they do not set price or note.
func importLine(raw, format string) (content string, premium bool) {
	content = strings.TrimSpace(raw)
	if format == "premium" {
		if parts := strings.SplitN(content, "---", 3); len(parts) >= 2 {
			return strings.TrimSpace(parts[0]), true
		}
	}
	return content, false
}

// existingHashes 批查庫內已存在的 content_hash 集合（分組 IN，替代逐行 Exist）。
func (r *CardRepoImpl) existingHashes(ctx context.Context, client *ent.Client, productID uint64, hashes []string) (map[string]bool, error) {
	out := make(map[string]bool, len(hashes))
	for i := 0; i < len(hashes); i += dedupChunk {
		end := i + dedupChunk
		if end > len(hashes) {
			end = len(hashes)
		}
		rows, err := client.Card.Query().
			Where(card.ProductID(productID), card.SubsiteID(tenancy.FromContext(ctx).SubsiteID), card.ContentHashIn(hashes[i:end]...)).
			Select(card.FieldContentHash).
			Strings(ctx)
		if err != nil {
			return nil, err
		}
		for _, h := range rows {
			out[h] = true
		}
	}
	return out, nil
}

// ParseLines 解析原始行（默認完整卡密，顯式靚號模式 + 去重 + 預覽統計）。
func (r *CardRepoImpl) ParseLines(ctx context.Context, in ImportInput) (*ImportPreview, error) {
	if err := validateImportFormat(in.ImportFormat); err != nil {
		return nil, err
	}
	product, err := r.inventoryProduct(ctx, in.ProductID)
	if err != nil {
		return nil, err
	}
	name, err := r.importTarget(ctx, product, in.SkuID, false)
	if err != nil {
		return nil, err
	}
	if r.Cipher == nil {
		return nil, kerrors.InternalServer("inventory.CIPHER_MISSING", "卡密密鑰未配置")
	}
	p := &ImportPreview{Sample: make([]string, 0, 50), SkuID: in.SkuID, SkuName: name, Revision: product.LockVersion}
	seen := map[string]bool{}
	client := data.Client(ctx, r.data)

	// 第一遍：文件內去重 + 收集待查 hash
	type pending struct {
		hash string
		line string
	}
	uniq := make([]pending, 0, len(in.Lines))
	hashes := make([]string, 0, len(in.Lines))
	for _, raw := range in.Lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		p.Total++

		line, premium := importLine(line, in.ImportFormat)
		p.IsPremium = p.IsPremium || premium
		if line == "" {
			p.Invalid++
			continue
		}

		// 文件內去重
		if seen[line] {
			p.DupInFile++
			continue
		}
		seen[line] = true

		hash := r.Cipher.ContentHash(line)
		hashes = append(hashes, hash)
		uniq = append(uniq, pending{hash: hash, line: line})
	}

	// 第二遍：庫內去重（一次批查全部 hash）
	exists, err := r.existingHashes(ctx, client, in.ProductID, hashes)
	if err != nil {
		return nil, err
	}
	for _, u := range uniq {
		if exists[u.hash] {
			p.DupInDB++
			continue
		}
		if len(p.Sample) < 50 {
			p.Sample = append(p.Sample, u.line)
		}
	}
	return p, nil
}

// ImportConfirm 確認導入（創建批次 + 分片批量寫入）。
func (r *CardRepoImpl) importConfirm(ctx context.Context, in ImportInput) (*ent.CardImport, error) {
	client := data.Client(ctx, r.data)

	// 創建批次
	imp, err := client.CardImport.Create().
		SetProductID(in.ProductID).
		SetSubsiteID(in.SubsiteID).
		SetSkuID(in.SkuID).
		SetSkuName(in.SkuName).
		SetFilename("api-import").
		SetTotal(int32(len(in.Lines))).
		SetStatus(cardimport.StatusProcessing).
		SetOperatorID(in.Operator).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	// 分片寫入（1000/批；每片一次 IN 批查去重 + 一次批量 INSERT）
	var imported, skipped, failed int32
	seen := map[string]bool{}
	batchSize := 1000
	for i := 0; i < len(in.Lines); i += batchSize {
		end := i + batchSize
		if end > len(in.Lines) {
			end = len(in.Lines)
		}
		chunk := in.Lines[i:end]

		creates := make([]*ent.CardCreate, 0, len(chunk))
		pendingHashes := make([]string, 0, len(chunk))
		for _, line := range chunk {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			content, premium := importLine(line, in.ImportFormat)
			if content == "" {
				failed++
				continue
			}

			// 文件內去重
			if seen[content] {
				skipped++
				continue
			}
			seen[content] = true

			// 加密 + keyed hash
			enc, err := r.Cipher.Seal(content, in.ProductID, in.SubsiteID)
			if err != nil {
				failed++
				continue
			}
			hash := r.Cipher.ContentHash(content)
			pendingHashes = append(pendingHashes, hash)

			create := client.Card.Create().
				SetProductID(in.ProductID).
				SetSubsiteID(in.SubsiteID).
				SetContent(enc).
				SetContentHash(hash).
				SetStatus(card.StatusAvailable)
			create.SetSkuID(in.SkuID)
			if imp.ID > 0 {
				create.SetImportID(imp.ID)
			}
			// 靚號字段
			if premium {
				nh := r.Cipher.ContentHash("number:" + content)
				create.SetNumberHash(nh)
			}
			creates = append(creates, create)
		}

		// 分片內批查去重（批次間/與庫內重複；併發窗口漏網的由 ON CONFLICT 兜底）
		if len(pendingHashes) > 0 {
			exists, err := r.existingHashes(ctx, client, in.ProductID, pendingHashes)
			if err != nil {
				return nil, err
			}
			kept := creates[:0]
			for idx, c := range creates {
				if exists[pendingHashes[idx]] {
					skipped++
					continue
				}
				kept = append(kept, c)
			}
			creates = kept
		}

		// 批量寫入：衝突（併發導入競態）靜默跳過，不退化為逐行
		if len(creates) > 0 {
			if err := client.Card.CreateBulk(creates...).
				OnConflict(entsql.DoNothing()).
				Exec(ctx); err != nil {
				return nil, err
			}
			actual, e := client.Card.Query().Where(card.ImportID(imp.ID), card.SubsiteID(in.SubsiteID)).Count(ctx)
			if e != nil {
				return nil, e
			}
			skipped += int32(len(creates)) - (int32(actual) - imported)
			imported = int32(actual)
		}
	}

	// 更新批次狀態（UpdateOne 返回值不入內存對象——重新讀取保證
	// imported/skipped/status 反映真實結果，否則響應恆為創建態 0/processing）
	if _, err = client.CardImport.UpdateOne(imp).
		SetImported(imported).
		SetSkipped(skipped).
		SetFailed(failed).
		SetStatus(cardimport.StatusDone).
		Save(ctx); err != nil {
		return nil, err
	}
	return client.CardImport.Get(ctx, imp.ID)
}

// ListImports 導入批次列表。
func (r *CardRepoImpl) ListImports(ctx context.Context, productID uint64, skuID *uint64, legacyOnly bool) ([]*ent.CardImport, error) {
	q := data.Client(ctx, r.data).CardImport.Query().Where(cardimport.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).
		Order(ent.Desc(cardimport.FieldCreatedAt)).
		Limit(50)
	if productID > 0 {
		q = q.Where(cardimport.ProductID(productID))
	}
	if legacyOnly {
		q = q.Where(cardimport.SkuIDIsNil())
	} else if skuID != nil {
		q = q.Where(cardimport.SkuID(*skuID))
	}
	return q.All(ctx)
}

// CancelImport 撤銷批次（刪除本批 available 卡）。
func (r *CardRepoImpl) cancelImport(ctx context.Context, importID uint64) error {
	client := data.Client(ctx, r.data)
	n, err := client.Card.Delete().
		Where(card.ImportID(importID), card.SubsiteID(tenancy.FromContext(ctx).SubsiteID), card.StatusEQ(card.StatusAvailable)).
		Exec(ctx)
	if err != nil {
		return err
	}
	_, err = client.CardImport.UpdateOneID(importID).
		SetStatus(cardimport.StatusCanceled).
		Save(ctx)
	_ = n
	return err
}

// ── 導出（）────────────────────────────────────────────────

// CsvSafe 防 Excel 注入（= + - @ 開頭前綴 '）。
func CsvSafe(s string) string {
	if len(s) > 0 && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@') {
		return "'" + s
	}
	return s
}

// ExportCards 導出 available 卡密（返回解密明文行；需 card:export 權限 + 審計）。
func (r *CardRepoImpl) ExportCards(ctx context.Context, productID uint64, skuID *uint64) ([]string, error) {
	if _, err := r.inventoryProduct(ctx, productID); err != nil {
		return nil, err
	}
	rows, err := data.Client(ctx, r.data).Card.Query().
		Where(card.ProductID(productID), card.SubsiteID(tenancy.FromContext(ctx).SubsiteID), card.StatusEQ(card.StatusAvailable), skuPredicate(skuID)).
		Order(ent.Asc(card.FieldID)).
		Limit(10000). // 導出上限
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		plain, err := r.Cipher.Open(row.Content, row.ProductID, row.SubsiteID)
		if err != nil {
			continue // 解密失敗降級跳過（絕不 500）
		}
		out = append(out, CsvSafe(plain))
	}
	return out, nil
}

func (r *CardRepoImpl) ImportConfirm(ctx context.Context, in ImportInput) (out *ent.CardImport, err error) {
	if err := validateImportFormat(in.ImportFormat); err != nil {
		return nil, err
	}
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {
		p, e := data.GuardProductWrite(ctx, r.data, in.ProductID)
		if e != nil {
			return e
		}
		if in.ExpectedRevision != nil && p.LockVersion != *in.ExpectedRevision {
			return kerrors.Conflict("inventory.CONFIG_CHANGED", "商品設置已變化，請重新預覽")
		}
		name, e := r.importTarget(ctx, p, in.SkuID, true)
		if e != nil {
			return e
		}
		if r.Cipher == nil {
			return kerrors.InternalServer("inventory.CIPHER_MISSING", "卡密密鑰未配置")
		}
		in.SubsiteID = p.SubsiteID
		in.SkuName = name
		out, e = r.importConfirm(ctx, in)
		return e
	})
	return
}
func (r *CardRepoImpl) CancelImport(ctx context.Context, id uint64, expected ...string) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		row, e := data.Client(ctx, r.data).CardImport.Query().Where(cardimport.ID(id), cardimport.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Only(ctx)
		if e != nil {
			return e
		}
		if _, e = data.GuardProductWrite(ctx, r.data, row.ProductID); e != nil {
			return e
		}
		current, e := r.batchCards(ctx, row, true)
		if e != nil {
			return e
		}
		if len(expected) > 0 && expected[0] != "" && expected[0] != cancellationRevision(current) {
			return kerrors.Conflict("inventory.BATCH_CHANGED", "批次庫存已變化，請重新預覽撤銷範圍")
		}
		return r.cancelImport(ctx, id)
	})
}
