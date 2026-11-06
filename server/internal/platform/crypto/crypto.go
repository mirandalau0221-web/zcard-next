// Package crypto 數據層加密基建（規劃 §4.11.6 / §5.20.2）。
//
// 四把鑰匙解耦（卡密密鑰/業務數據密鑰/JWT 密鑰/APP 密鑰），env 或 secret 注入，
// 永不進 DB/日誌/git。本包只提供算法原語，密鑰的裝配在啓動階段完成。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// 密鑰長度：AES-256。
const keyLen = 32

// ErrDecrypt 解密失敗。調用方必須降級處理（憑據列降級為空並提示重配，鐵律 5），
// 絕不向上拋 500。
var ErrDecrypt = errors.New("crypto: decrypt failed")

// Box AES-256-GCM 加密盒（卡密內容 / 憑據列 / TOTP 密鑰共用）。
// AAD 綁定業務上下文（如 product_id/subsite_id），防密文挪用。
type Box struct {
	aead cipher.AEAD
}

// NewBox 由 32 字節原始密鑰構造。密鑰來源：ZCARD_CARD_KEY / ZCARD_DATA_KEY（hex）。
func NewBox(key []byte) (*Box, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("crypto: key must be %d bytes, got %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal 加密：輸出 nonce || ciphertext+tag。aad 可為 nil。
// 每次加密使用隨機 nonce，同一明文兩次加密密文不同（不可作去重依據）。
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: read nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open 解密；失敗返回 ErrDecrypt（調用方降級，不透出密文細節）。
func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, ErrDecrypt
	}
	plain, err := b.aead.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// KeyedHash keyed hash 去重（鐵律 11）：HMAC-SHA256(key, plaintext)。
// 相比裸 sha256 防低熵卡密（短兌換碼）的彩虹表反推。
// 注意：密鑰輪換（zcard reencrypt-cards）時須與密文重加密同事務重算。
func KeyedHash(key []byte, plaintext []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(plaintext)
	return hex.EncodeToString(mac.Sum(nil))
}

// ParseHexKey 解析 hex 密鑰（env 注入形態：64 個 hex 字符 = 32 字節）。
func ParseHexKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("crypto: key is not valid hex: %w", err)
	}
	if len(key) != keyLen {
		return nil, fmt.Errorf("crypto: key must be %d bytes (64 hex chars), got %d bytes", keyLen, len(key))
	}
	return key, nil
}
