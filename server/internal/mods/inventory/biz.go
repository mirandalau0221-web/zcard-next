// Package inventory 卡密庫存模塊（M1）：卡密/鏈接/兑換碼、導入導出、鎖定預留、加密、預售/靚號。
//
// 鐵律 11：卡密內容強制 AES-256-GCM（無關閉開關），keyed hash 去重，永不落明文；
// 導入預覽確認與批量分片、CsvSafe 防 Excel 注入均 M1 交付。
package inventory

import (
	"context"
	"errors"

	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
)

// 卡密狀態機（§5.4）：available → reserved(訂單鎖定/TTL 自動釋放) → used(出售)；
// 旁路 disabled（禁用開關）。
const (
	StatusAvailable = "available"
	StatusReserved  = "reserved"
	StatusUsed      = "used"
	StatusDisabled  = "disabled"
)

// ErrInsufficientStock 庫存不足（事務內回滾，不發事件，§5.3）。
var ErrInsufficientStock = errors.New("inventory.INSUFFICIENT_STOCK")

// CardRepo 卡密倉儲（模塊內端口，實現於 data.go，M1 隨下單鏈路交付）。
type CardRepo interface {
	port.Inventory
}

// CardCipher 卡密加密器：加密入庫 + keyed hash 去重（鐵律 11 的模塊內落點）。
type CardCipher struct {
	box *crypto.Box
	key []byte
}

// NewCardCipher 構造（key = ZCARD_CARD_KEY 原始 32 字節）。
func NewCardCipher(key []byte) (*CardCipher, error) {
	box, err := crypto.NewBox(key)
	if err != nil {
		return nil, err
	}
	return &CardCipher{box: box, key: key}, nil
}

// Seal 加密卡密：AAD 綁定 product/subsite（防密文跨商品挪用）。
func (c *CardCipher) Seal(plain string, productID, subsiteID uint64) ([]byte, error) {
	return c.box.Seal([]byte(plain), aad(productID, subsiteID))
}

// Open 解密（交付時現場調用；失敗降級為空並提示重配——絕不 500）。
func (c *CardCipher) Open(ciphertext []byte, productID, subsiteID uint64) (string, error) {
	plain, err := c.box.Open(ciphertext, aad(productID, subsiteID))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// ContentHash keyed hash 去重（HMAC-SHA256(cardKey, plain)，防低熵卡彩虹表）。
func (c *CardCipher) ContentHash(plain string) string {
	return crypto.KeyedHash(c.key, []byte(plain))
}

func aad(productID, subsiteID uint64) []byte {
	return []byte("product:" + u64(productID) + ":subsite:" + u64(subsiteID))
}

func u64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// InventoryUsecase 庫存用例骨架（M1 交付 Reserve/Release/MarkUsed 編排）。
type InventoryUsecase struct {
	repo CardRepo
}

// NewInventoryUsecase 構造。
func NewInventoryUsecase(repo CardRepo) *InventoryUsecase { return &InventoryUsecase{repo: repo} }

// Reserve 事務內鎖卡（M1：併發防超賣 100/10 雙數據庫路徑集成測試）。
func (uc *InventoryUsecase) Reserve(ctx context.Context, subsiteID uint64, items []port.ReserveItem) (*port.Reservation, error) {
	return uc.repo.Reserve(ctx, subsiteID, items)
}

// Release 釋放預留（按訂單）。
func (uc *InventoryUsecase) Release(ctx context.Context, orderID uint64) error {
	return uc.repo.Release(ctx, orderID)
}
