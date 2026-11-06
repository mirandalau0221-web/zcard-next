package migratev1

// P3 卡密域遷移：card_imports → cards 全鏈（1.x CBC 解密 → 2.0 GCM Seal +
// HMAC hash 全量重算 → 商品內重複跳過 → 狀態映射與鎖超時釋放）→
// order_deliveries 缺失卡密回填（delivery_mode=delete 已物理刪的唯一副本）。
// 映射規格《數據遷移工具開發計劃》§5.4；order_id 置 0 由 P4 遷完訂單後回填。
//
// 性能：cards 走專用批量循環（keyset 分頁 + 批級冪等 IN 查詢 + CreateBulk），
// 目標 ≥5k 行/s；同商品重複卡密撞唯一索引時該批迴退逐行並計 skipped_dup。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/cardimport"
	"github.com/NovaWorks/zcard-next/server/internal/migratev1/laracrypt"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
)

// lockReleaseTTL 1.x 鎖卡超時釋放口徑：locked_at 早於此閾值的 locked 卡按
// available 遷移（對齊 1.x 超時關單釋放語義；訂單 TTL 為分鐘量級，取保守上界）。
const lockReleaseTTL = 30 * time.Minute

// MigrateInventory P3 階段。
func (m *Migrator) MigrateInventory(ctx context.Context) error {
	if err := m.migrateCardImports(ctx); err != nil {
		return err
	}
	if err := m.migrateCards(ctx); err != nil {
		return err
	}
	return m.backfillOrderDeliveries(ctx)
}

// migrateCardImports 1.x card_imports → card_imports（導入批次歷史）。
func (m *Migrator) migrateCardImports(ctx context.Context) error {
	var (
		id, productID, operatorID int64
		total, success, failed    int64
		skipped                   sql.NullInt64
		source                    sql.NullString
		status                    string
		createdAt, updatedAt      sql.NullString
	)
	return m.scanTable(ctx, "card_imports",
		[]string{"id", "product_id", "operator_id", "source", "total",
			"success_count", "failed_count", "skipped_count", "status", "created_at", "updated_at"},
		func() []any {
			return []any{&id, &productID, &operatorID, &source, &total,
				&success, &failed, &skipped, &status, &createdAt, &updatedAt}
		},
		func(int64) error {
			if _, ok := m.IDs.Get(ctx, "card_imports", uint64(id)); ok {
				m.st.Record("card_imports", "skip")
				return nil
			}
			newPID, ok := m.IDs.Get(ctx, "products", uint64(productID))
			if !ok {
				m.st.Record("card_imports", "skip") // 分站商品：P6 補
				return nil
			}
			ca, caOK, err := mustTime(nullStr(createdAt), m.TZ)
			if err != nil {
				return err
			}
			ua, _, err := mustTime(nullStr(updatedAt), m.TZ)
			if err != nil {
				return err
			}
			b := m.Client.CardImport.Create().
				SetProductID(newPID).
				SetOperatorID(0). // 1.x operator 是普通用戶表；2.0 語義為管理員，不直遷
				SetFilename(orString(nullStr(source), "v1-import-"+fmt.Sprint(id))).
				SetTotal(int32(total)).
				SetImported(int32(success)).
				SetFailed(int32(failed)).
				SetSkipped(int32(nullInt(skipped))).
				SetStatus(cardimport.Status(mapCardImportStatus(status)))
			if caOK {
				b.SetCreatedAt(ca)
			}
			b.SetUpdatedAt(ua)
			ci, err := b.Save(ctx)
			if err != nil {
				return err
			}
			if _, err := m.IDs.Put(ctx, m.Client, "card_imports", uint64(id), ci.ID); err != nil {
				return err
			}
			m.st.Record("card_imports", "migrated")
			return nil
		},
	)
}

func mapCardImportStatus(s string) string {
	switch s {
	case "running":
		return "processing"
	case "failed":
		return "failed"
	default: // completed
		return "done"
	}
}

// cardRow 掃描中間結構（cards 專用批量循環）。
type cardRow struct {
	id                      int64
	productID, importID     sql.NullInt64
	orderID                 sql.NullInt64
	content, contentHash    string
	status                  string
	lockedAt, usedAt        sql.NullString
	createdAt, updatedAt    sql.NullString
	note, cardType          sql.NullString
	ownerID                 sql.NullInt64
	draftPremium, draftCost int64
	price                   sql.NullInt64
	numberHash              sql.NullString
}

// migrateCards 卡密全鏈批量遷移。
func (m *Migrator) migrateCards(ctx context.Context) error {
	t := m.st.table("cards")
	if m.dry {
		var n int64
		if err := m.Src.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM `cards`").Scan(&n); err != nil {
			return err
		}
		t.Planned = n
		return nil
	}
	cipher, err := inventory.NewCardCipher(m.NewCardKey)
	if err != nil {
		return fmt.Errorf("ZCARD_CARD_KEY 不可用（卡密重加密必需）: %w", err)
	}
	cols := "`id`, `product_id`, `import_id`, `order_id`, `content`, `content_hash`, `status`, " +
		"`locked_at`, `used_at`, `created_at`, `updated_at`, `note`, `card_type`, `owner_id`, " +
		"`draft_premium`, `draft_cost`, `price`, `number_hash`"
	query := fmt.Sprintf("SELECT %s FROM `cards` WHERE `id` > ? ORDER BY `id` LIMIT ?", cols)

	var cursor int64
	for {
		rows, err := m.Src.DB.QueryContext(ctx, query, cursor, m.Opts.Batch)
		if err != nil {
			return fmt.Errorf("掃描 cards 失敗: %w", err)
		}
		var batch []cardRow
		for rows.Next() {
			var r cardRow
			if err := rows.Scan(&r.id, &r.productID, &r.importID, &r.orderID, &r.content,
				&r.contentHash, &r.status, &r.lockedAt, &r.usedAt, &r.createdAt, &r.updatedAt,
				&r.note, &r.cardType, &r.ownerID, &r.draftPremium, &r.draftCost, &r.price,
				&r.numberHash); err != nil {
				rows.Close()
				return fmt.Errorf("掃描 cards 行失敗: %w", err)
			}
			batch = append(batch, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(batch) == 0 {
			return nil
		}
		cursor = batch[len(batch)-1].id

		if err := m.migrateCardBatch(ctx, cipher, batch); err != nil {
			return err
		}
		if len(batch) < m.Opts.Batch {
			return nil
		}
	}
}

// migrateCardBatch 單批：批級冪等 + 轉換 + CreateBulk（唯一衝突回退逐行）。
func (m *Migrator) migrateCardBatch(ctx context.Context, cipher *inventory.CardCipher, batch []cardRow) error {
	t := m.st.table("cards")

	oldIDs := make([]uint64, len(batch))
	for i, r := range batch {
		oldIDs[i] = uint64(r.id)
	}
	existing := m.IDs.GetBatch(ctx, "cards", oldIDs)

	var (
		preparedRows []preparedCard
		seen         = map[string]bool{} // 批內 (product,hash) 去重
	)
	for _, r := range batch {
		if _, ok := existing[uint64(r.id)]; ok {
			t.SkippedExists++
			continue
		}
		newPID, ok := m.IDs.Get(ctx, "products", uint64(nullInt(r.productID)))
		if !ok {
			t.SkippedExists++ // 分站商品：P6 補
			continue
		}
		plain, wasEnc, err := openV1Card(r.content, m.CardKey)
		if err != nil {
			if m.Opts.OnError == "abort" {
				return fmt.Errorf("卡密 %d 解密失敗（abort）: %w", r.id, err)
			}
			t.Failed++
			m.RW.AddError("cards", uint64(r.id), "解密失敗: "+err.Error())
			continue
		}
		// 1.x content_hash 校驗（裸 sha256）：漂移僅告警不阻斷（老庫存在不可信 hash）
		if sum := sha256.Sum256([]byte(plain)); hex.EncodeToString(sum[:]) != r.contentHash {
			m.RW.AddError("cards", uint64(r.id), "content_hash 與明文不一致（按明文重算遷移）")
		}
		hash := cipher.ContentHash(plain)
		sealed, err := cipher.Seal(plain, newPID, m.productSubsite(ctx, newPID))
		if err != nil {
			t.Failed++
			m.RW.AddError("cards", uint64(r.id), "重加密失敗: "+err.Error())
			continue
		}
		dedupKey := fmt.Sprintf("%d:%s", newPID, hash)
		if seen[dedupKey] {
			t.SkippedExists++ // 同商品重複卡密（1.x dedup_hash 可空導致）：跳過
			continue
		}
		seen[dedupKey] = true
		_ = wasEnc
		preparedRows = append(preparedRows, preparedCard{row: r, newPID: newPID, content: sealed, hash: hash})
	}
	if len(preparedRows) == 0 {
		return nil
	}

	builders := make([]*ent.CardCreate, 0, len(preparedRows))
	for _, p := range preparedRows {
		builders = append(builders, m.cardBuilder(ctx, cipher, p.row, p.newPID, p.content, p.hash))
	}
	// 性能關鍵路徑：bulk 插入前取 MAX(id) 基線，插入後一次回查區間連續 ID，
	// 再批量寫 v1id_maps（3 次往返替代原先的 2N 次）；回查異常回退逐行保正確性。
	base, err := m.Client.Card.Query().
		Aggregate(ent.Max(card.FieldID)).
		Int(ctx)
	if err != nil {
		base = 0
	}
	if _, err := m.Client.Card.CreateBulk(builders...).Save(ctx); err != nil {
		// 唯一衝突（目標庫已有同商品同 hash）→ 回退逐行，衝突行計跳過
		for _, p := range preparedRows {
			c, err := m.cardBuilder(ctx, cipher, p.row, p.newPID, p.content, p.hash).Save(ctx)
			if err != nil {
				if isUniqueViolation(err) {
					t.SkippedExists++
					continue
				}
				if m.Opts.OnError == "abort" {
					return err
				}
				t.Failed++
				m.RW.AddError("cards", uint64(p.row.id), err.Error())
				continue
			}
			t.Migrated++
			if _, err := m.IDs.Put(ctx, m.Client, "cards", uint64(p.row.id), c.ID); err != nil {
				return err
			}
		}
		return nil
	}
	newIDs, err := m.Client.Card.Query().
		Where(card.IDGT(uint64(base))).
		Order(ent.Asc(card.FieldID)).
		IDs(ctx)
	if err != nil || len(newIDs) != len(preparedRows) {
		// 回查異常（行數不符=自增不連續假設被打破）：回退逐行回查保正確性
		t.Migrated += int64(len(preparedRows))
		return m.recordBulkIDs(ctx, preparedRows)
	}
	pairs := make([][2]uint64, len(preparedRows))
	for i, p := range preparedRows {
		pairs[i] = [2]uint64{uint64(p.row.id), newIDs[i]}
	}
	if err := m.IDs.PutBatch(ctx, m.Client, "cards", pairs); err != nil {
		return err
	}
	t.Migrated += int64(len(preparedRows))
	return nil
}

// preparedCard 已完成解密+重加密的待寫行。
type preparedCard struct {
	row     cardRow
	newPID  uint64
	content []byte
	hash    string
}

// recordBulkIDs bulk 插入後按內容回查真實新 ID 寫 idmap（bulk 不返回逐行 ID；
// 以 (subsite=0, product, content_hash) 定位，唯一索引保證至多一行）。
func (m *Migrator) recordBulkIDs(ctx context.Context, prepared []preparedCard) error {
	for _, p := range prepared {
		row, err := m.Client.Card.Query().
			Where(
				card.ProductID(p.newPID),
				card.ContentHash(p.hash),
			).Only(ctx)
		if err != nil {
			return fmt.Errorf("bulk 卡密回查失敗（product=%d hash=%s）: %w", p.newPID, p.hash, err)
		}
		if _, err := m.IDs.Put(ctx, m.Client, "cards", uint64(p.row.id), row.ID); err != nil {
			return err
		}
	}
	return nil
}

// openV1Card 1.x content 解密（形態識別直通明文；等價 CardCipher::decrypt 非 strict）。
func openV1Card(content string, cardKey []byte) (string, bool, error) {
	if !laracrypt.LooksEncrypted(content) {
		return content, false, nil
	}
	if len(cardKey) == 0 {
		return "", true, fmt.Errorf("密文卡但舊卡密鑰匙不可用")
	}
	c, err := laracrypt.New(cardKey)
	if err != nil {
		return "", true, err
	}
	plain, err := c.OpenString(content)
	if err != nil {
		return "", true, err
	}
	return plain, true, nil
}

// cardBuilder 單行構建。
func (m *Migrator) cardBuilder(ctx context.Context, cipher *inventory.CardCipher, r cardRow,
	newPID uint64, sealed []byte, hash string) *ent.CardCreate {

	b := m.Client.Card.Create().
		SetProductID(newPID).
		SetContent(sealed).
		SetContentHash(hash).
		SetStatus(card.Status(mapCardStatus(r.status, nullStr(r.lockedAt), m.TZ))).
		SetDraftPremium(r.draftPremium).
		SetDraftCost(r.draftCost)
	if imp := nullInt(r.importID); imp > 0 {
		if newImp, ok := m.IDs.Get(ctx, "card_imports", uint64(imp)); ok {
			b.SetImportID(newImp)
		}
	}
	if o := nullInt(r.ownerID); o > 0 {
		if newU, ok := m.IDs.Get(ctx, "users", uint64(o)); ok {
			b.SetOwnerID(newU)
		}
	}
	if n := nullStr(r.note); n != "" {
		b.SetNote(n)
	}
	if ct := nullStr(r.cardType); ct != "" {
		b.SetCardType(ct)
	}
	if nh := nullStr(r.numberHash); nh != "" {
		b.SetNumberHash(nh)
	}
	if pr := nullInt(r.price); pr > 0 {
		b.SetPrice(pr)
	}
	if t, ok, err := mustTime(nullStr(r.lockedAt), m.TZ); err == nil && ok {
		b.SetLockedAt(t)
	}
	if t, ok, err := mustTime(nullStr(r.usedAt), m.TZ); err == nil && ok {
		b.SetUsedAt(t)
	}
	if t, ok, err := mustTime(nullStr(r.createdAt), m.TZ); err == nil && ok {
		b.SetCreatedAt(t)
	}
	if t, ok, err := mustTime(nullStr(r.updatedAt), m.TZ); err == nil && ok {
		b.SetUpdatedAt(t)
	}
	// order_id：1.x 訂單 P4 才遷——置 0，P4 遷完訂單後按 v1id_maps(cards) 回填
	return b
}

func mapCardStatus(status, lockedAt string, tz *time.Location) string {
	switch status {
	case "used":
		return "used"
	case "disabled":
		return "disabled"
	case "locked":
		if t, ok := parseNaiveTime(lockedAt, tz); ok && time.Since(t) > lockReleaseTTL {
			return "available" // 鎖超時釋放（1.x 超時關單語義）
		}
		return "reserved"
	default: // unused
		return "available"
	}
}

// backfillOrderDeliveries delivery_mode=delete 的訂單已物理刪卡，
// order_deliveries.card_content 是唯一明文副本 → 重建 used 卡（order_id 置 0，
// v1id_maps("order_deliveries") 記 old delivery id → new card id 供 P4 關聯）。
func (m *Migrator) backfillOrderDeliveries(ctx context.Context) error {
	t := m.st.table("order_deliveries_backfill")
	if m.dry {
		return nil
	}
	cipher, err := inventory.NewCardCipher(m.NewCardKey)
	if err != nil {
		return err
	}
	rows, err := m.Src.DB.QueryContext(ctx,
		"SELECT `id`, `order_id`, `product_id`, `card_content`, `delivered_at` FROM `order_deliveries` ORDER BY `id`")
	if err != nil {
		return fmt.Errorf("掃描 order_deliveries 失敗: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, orderID, productID sql.NullInt64
			content                string
			deliveredAt            sql.NullString
		)
		if err := rows.Scan(&id, &orderID, &productID, &content, &deliveredAt); err != nil {
			return err
		}
		if _, ok := m.IDs.Get(ctx, "order_deliveries", uint64(nullInt(id))); ok {
			t.SkippedExists++
			continue
		}
		newPID, ok := m.IDs.Get(ctx, "products", uint64(nullInt(productID)))
		if !ok {
			t.SkippedExists++ // 分站商品：P6 補
			continue
		}
		// 源庫 cards 仍有該卡（status 模式未物理刪）→ 無需重建，僅記 delivery 映射佔位
		sum := sha256.Sum256([]byte(content))
		var exists int
		if err := m.Src.DB.QueryRowContext(ctx,
			"SELECT 1 FROM `cards` WHERE `product_id` = ? AND `content_hash` = ? LIMIT 1",
			nullInt(productID), hex.EncodeToString(sum[:])).Scan(&exists); err == nil {
			t.SkippedExists++
			continue
		} else if err != sql.ErrNoRows {
			return err
		}
		hash := cipher.ContentHash(content)
		sealed, err := cipher.Seal(content, newPID, m.productSubsite(ctx, newPID))
		if err != nil {
			t.Failed++
			continue
		}
		b := m.Client.Card.Create().
			SetProductID(newPID).
			SetContent(sealed).
			SetContentHash(hash).
			SetStatus(card.StatusUsed)
		if dt, ok, err := mustTime(nullStr(deliveredAt), m.TZ); err == nil && ok {
			b.SetUsedAt(dt)
		}
		c, err := b.Save(ctx)
		if err != nil {
			if isUniqueViolation(err) { // 與已遷卡密同明文（回填與主遷移重疊）
				t.SkippedExists++
				continue
			}
			t.Failed++
			m.RW.AddError("order_deliveries", uint64(nullInt(id)), err.Error())
			continue
		}
		if _, err := m.IDs.Put(ctx, m.Client, "order_deliveries", uint64(nullInt(id)), c.ID); err != nil {
			return err
		}
		t.Migrated++
	}
	return rows.Err()
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "unique constraint")
}

func orString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
