package admincmd

// reencrypt-cards 密鑰輪換子命令（ 骨架）：
// 舊 key 解密 → 新 key 重加密 → 同事務重算 content_hash（+ 靚號 number_hash），分批 1000。
// 冪等：舊 key 解不開但新 key 能解的行視為已輪換跳過（中斷可續跑）。

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
)

// RunReencrypt reencrypt-cards 子命令入口（main 分發調用）。
//
//	zcard reencrypt-cards --new-key <64 hex> [--conf configs] [--batch 1000]
func RunReencrypt(args []string) error {
	fs := flag.NewFlagSet("reencrypt-cards", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	newKeyHex := fs.String("new-key", "", "新密鑰（64 hex 字符 = 32 字節）")
	batchSize := fs.Int("batch", 1000, "每批行數")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *newKeyHex == "" {
		return fmt.Errorf("--new-key 必填（64 hex 字符）")
	}
	newKey, err := crypto.ParseHexKey(*newKeyHex)
	if err != nil {
		return err
	}

	// 舊 key：env ZCARD_CARD_KEY > conf.security.card_key
	bc := &conf.Bootstrap{}
	if err := scanConf(*confDir, bc); err != nil {
		return err
	}
	if bc.Security == nil {
		bc.Security = &conf.Security{}
	}
	oldRaw := os.Getenv("ZCARD_CARD_KEY")
	if oldRaw == "" {
		oldRaw = bc.Security.CardKey
	}
	if oldRaw == "" {
		return fmt.Errorf("未找到舊卡密密鑰（設置 ZCARD_CARD_KEY 或 conf.security.card_key）")
	}
	oldKey, err := crypto.ParseHexKey(oldRaw)
	if err != nil {
		return fmt.Errorf("舊密鑰非法: %w", err)
	}

	client, cleanup, err := open(*confDir)
	if err != nil {
		return err
	}
	defer cleanup()

	rotated, skipped, failed, err := ReencryptCards(context.Background(), client, oldKey, newKey, *batchSize)
	if err != nil {
		return err
	}
	fmt.Printf("密鑰輪換完成：重加密 %d，跳過（已輪換）%d，失敗 %d\n", rotated, skipped, failed)
	return nil
}

// ReencryptCards 分批輪換（可測試內核）：返回（重加密數, 已跳過數, 失敗數）。
// 每批一個事務：重加密 + 重算 content_hash（及靚號 number_hash）原子提交。
func ReencryptCards(ctx context.Context, client *ent.Client, oldKey, newKey []byte, batchSize int) (rotated, skipped, failed int, err error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	oldCipher, err := inventory.NewCardCipher(oldKey)
	if err != nil {
		return 0, 0, 0, err
	}
	newCipher, err := inventory.NewCardCipher(newKey)
	if err != nil {
		return 0, 0, 0, err
	}

	var cursor uint64
	for {
		rows, err := client.Card.Query().
			Where(card.IDGT(cursor)).
			Order(ent.Asc(card.FieldID)).
			Limit(batchSize).
			All(ctx)
		if err != nil {
			return rotated, skipped, failed, err
		}
		if len(rows) == 0 {
			break
		}

		tx, err := client.Tx(ctx)
		if err != nil {
			return rotated, skipped, failed, err
		}
		c := tx.Client()
		for _, row := range rows {
			plain, err := oldCipher.Open(row.Content, row.ProductID, row.SubsiteID)
			if err != nil {
				// 舊 key 解不開 → 嘗試新 key；能解説明已輪換過，跳過（冪等續跑）
				if p2, err2 := newCipher.Open(row.Content, row.ProductID, row.SubsiteID); err2 == nil {
					_ = p2
					skipped++
					continue
				}
				failed++
				continue
			}
			enc, err := newCipher.Seal(plain, row.ProductID, row.SubsiteID)
			if err != nil {
				failed++
				continue
			}
			u := c.Card.UpdateOneID(row.ID).
				SetContent(enc).
				SetContentHash(newCipher.ContentHash(plain))
			if row.NumberHash != "" {
				// 靚號 number_hash = HMAC(key, "number:"+plain)，隨 key 變一併重算
				u.SetNumberHash(newCipher.ContentHash("number:" + plain))
			}
			if _, err := u.Save(ctx); err != nil {
				failed++
				continue
			}
			rotated++
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return rotated, skipped, failed, err
		}
		cursor = rows[len(rows)-1].ID
	}
	r, s, f, e := reencryptOrderAccess(ctx, client, oldCipher, newCipher, batchSize)
	rotated, skipped, failed = rotated+r, skipped+s, failed+f
	if e != nil {
		return rotated, skipped, failed, e
	}
	return rotated, skipped, failed, nil
}
