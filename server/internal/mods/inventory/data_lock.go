package inventory

// 鎖卡生命週期（P1 最高優先——防超賣雙路徑）：
//
// MySQL/PG：事務內 SELECT ... WHERE status='available' ORDER BY id LIMIT n FOR UPDATE SKIP LOCKED
// → 全部置 reserved + order_id + locked_at；數量不足整批迴滾
// SQLite： 單寫者事務（BEGIN IMMEDIATE 語義由 database/sql 驅動自動處理）
// → UPDATE cards SET status='reserved' WHERE id IN (...) AND status='available'
// 校驗 affected rows == n（CAS 語義，）
//
// Release：reserved → available，**必須同時清 order_id**（1.x 踩坑：unlock 不清 order_id 導致舊訂單發貨錯亂）
// MarkUsed：UPDATE WHERE status='reserved' 校驗 affected rows（防併發重發，友商紀律）
// TTL 釋放：週期任務掃 locked_at 超時（ order 超時取消時調用）

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
)

// 錯誤。
var (
	ErrInsufficient = fmt.Errorf("inventory.INSUFFICIENT_STOCK")
	ErrNotFound     = fmt.Errorf("inventory.CARD_NOT_FOUND")
	ErrNotReserved  = fmt.Errorf("inventory.CARD_NOT_RESERVED")
)

// ── Reserve ───────────────────────────────────────────────────

// Reserve 事務內鎖卡（防超賣）。必須在 data.Tx 內調用以共享事務。
func (r *CardRepoImpl) Reserve(ctx context.Context, subsiteID uint64, items []port.ReserveItem) (*port.Reservation, error) {
	client := data.Client(ctx, r.data)
	supportsLock := r.data.Dialect.Capabilities().SupportsSkipLocked
	var locked []port.ReservedCard // 鎖到的卡（ 供貨交付消費）

	for _, item := range items {
		if item.Quantity <= 0 {
			continue
		}
		// 1) 查可用卡（靚號精確匹配 or 按序取）
		query := client.Card.Query().
			Where(
				card.ProductID(item.ProductID),
				card.StatusEQ(card.StatusAvailable),
				card.SubsiteID(subsiteID),
			).
			Order(ent.Asc(card.FieldID)).
			Limit(int(item.Quantity))

		if item.NumberHash != "" {
			// 靚號自選：精確匹配
			query = client.Card.Query().
				Where(
					card.ProductID(item.ProductID),
					card.StatusEQ(card.StatusAvailable),
					card.SubsiteID(subsiteID),
					card.NumberHash(item.NumberHash),
				).
				Limit(1)
		}

		// A selected SKU may only reserve its own cards.
		if item.SkuID == 0 {
			query = query.Where(card.Or(card.SkuIDIsNil(), card.SkuID(0)))
		} else {
			query = query.Where(card.SkuID(item.SkuID))
		}

		// 行鎖（MySQL/PG；SQLite 單寫者天然串行）
		if supportsLock {
			query = query.ForUpdate(entsql.WithLockAction(sql.SkipLocked))
		}

		rows, err := query.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("inventory: 查詢可用卡失敗: %w", err)
		}
		want := int(item.Quantity)
		if item.NumberHash != "" {
			want = 1
		}
		if len(rows) < want {
			return nil, ErrInsufficient // 數量不足整批迴滾
		}
		for _, row := range rows {
			locked = append(locked, port.ReservedCard{CardID: row.ID, Locked: true})
		}

		// 2) 置 reserved + order_id + locked_at（CAS：affected rows 校驗）
		ids := make([]uint64, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		affected, err := client.Card.Update().
			Where(
				card.IDIn(ids...),
				card.StatusEQ(card.StatusAvailable),
			).
			SetStatus(card.StatusReserved).
			SetLockedAt(time.Now().UTC()).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("inventory: 鎖卡失敗: %w", err)
		}
		if int(affected) != len(ids) {
			return nil, ErrInsufficient // CAS 失敗：併發競爭，回滾
		}
	}

	// 返回預留（caller 傳入 order_id 後需調 BindOrder）
	return &port.Reservation{
		ReservationID: fmt.Sprintf("batch-%d", time.Now().UnixNano()),
		Cards:         locked,
		ExpiresAt:     time.Now().Add(30 * time.Minute).UTC(),
	}, nil
}

// BindOrder 鎖卡後綁定訂單（Reserve 成功 → caller 持有 reservation → 調用綁定）。
func (r *CardRepoImpl) BindOrder(ctx context.Context, subsiteID, productID, orderID uint64, quantity int32) error {
	client := data.Client(ctx, r.data)
	_, err := client.Card.Update().
		Where(
			card.ProductID(productID),
			card.SubsiteID(subsiteID),
			card.StatusEQ(card.StatusReserved),
			card.OrderIDIsNil(),
		).
		SetOrderID(orderID).
		Save(ctx)
	return err
}

// ── Release ───────────────────────────────────────────────────

// Release 釋放預留（訂單取消/超時；**必須清 order_id**——1.x 踩坑）。
func (r *CardRepoImpl) Release(ctx context.Context, orderID uint64) error {
	client := data.Client(ctx, r.data)
	_, err := client.Card.Update().
		Where(
			card.OrderID(orderID),
			card.StatusEQ(card.StatusReserved),
		).
		SetStatus(card.StatusAvailable).
		SetOrderID(0).
		ClearLockedAt().
		Save(ctx)
	return err
}

// ReleaseExpired TTL 釋放（週期任務：locked_at 超時的 reserved → available）。
func (r *CardRepoImpl) ReleaseExpired(ctx context.Context, ttl time.Duration) (int, error) {
	client := data.Client(ctx, r.data)
	deadline := time.Now().Add(-ttl).UTC()
	n, err := client.Card.Update().
		Where(
			card.StatusEQ(card.StatusReserved),
			card.LockedAtLT(deadline),
		).
		SetStatus(card.StatusAvailable).
		SetOrderID(0).
		ClearLockedAt().
		Save(ctx)
	return int(n), err
}

// ── MarkUsed ──────────────────────────────────────────────────

// MarkUsed 售出標記（校驗 affected rows 防併發重發）。
func (r *CardRepoImpl) MarkUsed(ctx context.Context, cardIDs []uint64, orderID uint64) error {
	client := data.Client(ctx, r.data)
	// orderID>0：本地訂單嚴格校驗綁定（防併發重發）；
	// orderID=0：供貨交付（Reserve 未綁本地訂單，order_id 為 NULL）
	upd := client.Card.Update().
		Where(
			card.IDIn(cardIDs...),
			card.StatusEQ(card.StatusReserved),
		)
	if orderID > 0 {
		upd = upd.Where(card.OrderID(orderID))
	}
	affected, err := upd.
		SetStatus(card.StatusUsed).
		SetUsedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("inventory: 標記售出失敗: %w", err)
	}
	if int(affected) != len(cardIDs) {
		return ErrNotReserved // 部分卡不在 reserved 狀態（併發衝突）
	}
	return nil
}

// ReleaseCards 釋放指定鎖卡（供貨交付失敗回滾；reserved → available）。
func (r *CardRepoImpl) ReleaseCards(ctx context.Context, cardIDs []uint64) error {
	_, err := data.Client(ctx, r.data).Card.Update().
		Where(
			card.IDIn(cardIDs...),
			card.StatusEQ(card.StatusReserved),
		).
		SetStatus(card.StatusAvailable).
		ClearOrderID().
		ClearLockedAt().
		Save(ctx)
	return err
}

// MarkUsedAndDelete 即刪模式（delivery_mode=delete：發後物理刪除卡密行）。
func (r *CardRepoImpl) MarkUsedAndDelete(ctx context.Context, cardIDs []uint64, orderID uint64) error {
	if err := r.MarkUsed(ctx, cardIDs, orderID); err != nil {
		return err
	}
	client := data.Client(ctx, r.data)
	_, err := client.Card.Delete().Where(card.IDIn(cardIDs...)).Exec(ctx)
	return err
}

// ── Stock ─────────────────────────────────────────────────────

// Stock 可用庫存數（-1=無限，鏈接類）。
func (r *CardRepoImpl) Stock(ctx context.Context, productID, skuID uint64) (int64, error) {
	client := data.Client(ctx, r.data)
	p, e := client.Product.Get(ctx, productID)
	if e != nil {
		return 0, e
	}
	var sku *ent.ProductSku
	if skuID > 0 {
		sku, e = client.ProductSku.Get(ctx, skuID)
		if e != nil || sku.ProductID != productID {
			return 0, fmt.Errorf("inventory.SKU_INVALID")
		}
	}
	if data.FulfillmentMode(p, sku) == "manual" {
		return data.ManualAvailable(ctx, client, p)
	}

	if p.StockType != "card" {
		return -1, nil
	}
	q := client.Card.Query().Where(
		card.ProductID(productID),
		card.StatusEQ(card.StatusAvailable),
	)
	if skuID > 0 {
		q = q.Where(card.SkuID(skuID))
	}
	n, err := q.Count(ctx)
	return int64(n), err
}

// ListByOrder 按訂單查卡（交付/取貨用）。
func (r *CardRepoImpl) ListByOrder(ctx context.Context, orderID uint64) ([]*ent.Card, error) {
	return data.Client(ctx, r.data).Card.Query().
		Where(card.OrderID(orderID)).
		Order(ent.Asc(card.FieldID)).
		All(ctx)
}

// ── 編譯期接口斷言 ────────────────────────────────────────────
var _ port.Inventory = (*CardRepoImpl)(nil)
var _ = db.SQLite // 保持 db 引用

// Contents 交付卡密批量讀取解密（ 供貨交付出口）。
// 明文僅內存態返回，調用方負責 TLS 傳輸與零日誌。
func (r *CardRepoImpl) Contents(ctx context.Context, cardIDs []uint64, productID, subsiteID uint64) ([]string, error) {
	rows, err := data.Client(ctx, r.data).Card.Query().
		Where(card.IDIn(cardIDs...)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, c := range rows {
		plain, err := r.Cipher.Open(c.Content, c.ProductID, c.SubsiteID)
		if err != nil {
			return nil, fmt.Errorf("inventory: 卡密 %d 解密失敗: %w", c.ID, err)
		}
		out = append(out, plain)
	}
	return out, nil
}

// BindReserved binds only cards from this transaction's reservation, never another buyer's cards.
func (r *CardRepoImpl) BindReserved(ctx context.Context, orderID uint64, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	n, err := data.Client(ctx, r.data).Card.Update().Where(card.IDIn(ids...), card.StatusEQ(card.StatusReserved), card.Or(card.OrderIDIsNil(), card.OrderID(0))).SetOrderID(orderID).Save(ctx)
	if err != nil {
		return err
	}
	if n != len(ids) {
		return fmt.Errorf("inventory.RESERVATION_CHANGED")
	}
	return nil
}
