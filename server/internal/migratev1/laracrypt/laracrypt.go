// Package laracrypt ZCard 1.x（Laravel）加密載荷的解密原語（migrate-from-v1 專用）。
//
// 1.x 兩類密文（與《數據遷移工具開發計劃》附錄 B 對齊）：
//   - Laravel Crypt（APP_KEY）：supply_sources.credentials / supplier_accounts.api_secret
//     / payment_channels.config（雙層）/ settings SECRET_KEYS（雙層）
//   - CardCipher（獨立 CARD_ENCRYPTION_KEY）：cards.content，開關可關 → 存在歷史明文，
//     必須經 LooksEncrypted 形態識別後直通
//
// 載荷格式（Illuminate\Encryption\Encrypter::encryptString）：
//
//	base64( json{"iv": b64(iv16), "value": b64(AES-256-CBC 密文), "mac": hex} )
//	mac = HMAC-SHA256(key, b64(iv)字符串 + b64(value)字符串)   ← 參與運算的是 base64 文本
//
// 正確性由 testdata/v1_crypto_fixtures.json（真實 Laravel 加密器生成的 golden vectors）
// 釘死；密鑰形態由 ParseKey 兼容（base64: 前綴 / 64 hex / 32 字節原文）。
package laracrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 哨兵錯誤：調用方按 errors.Is 分流（密鑰錯配告警 / 明文直通 / 通用失敗）。
var (
	// ErrNotEncrypted 載荷不是 Laravel 加密形態（歷史明文應直通，不應視為錯誤）。
	ErrNotEncrypted = errors.New("laracrypt: 非加密載荷形態")
	// ErrBadMac MAC 校驗失敗——最典型的成因是密鑰不對（錯配的 key 解不出可讀明文，
	// 必須拒收而非靜默產出亂碼）。
	ErrBadMac = errors.New("laracrypt: MAC 校驗失敗（疑似密鑰錯配或載荷損壞）")
	// ErrDecrypt 解密失敗（結構/填充/序列化異常）。
	ErrDecrypt = errors.New("laracrypt: 解密失敗")
)

const keyLen = 32 // AES-256

type cryptPayload struct {
	IV    string `json:"iv"`
	Value string `json:"value"`
	Mac   string `json:"mac"`
}

// ParseKey 解析 1.x 密鑰串：`base64:` 前綴（APP_KEY 約定）→ base64 解碼；
// 64 位 hex → hex 解碼；32 字節原文直用。三者都要求恰好 32 字節。
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("laracrypt: 密鑰為空")
	}
	if rest, ok := strings.CutPrefix(s, "base64:"); ok {
		key, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return nil, fmt.Errorf("laracrypt: base64 密鑰解碼失敗: %w", err)
		}
		if len(key) != keyLen {
			return nil, fmt.Errorf("laracrypt: 密鑰須 %d 字節，base64 解碼後為 %d 字節", keyLen, len(key))
		}
		return key, nil
	}
	if len(s) == 64 {
		if key, err := hex.DecodeString(s); err == nil {
			return key, nil
		}
	}
	if len(s) == keyLen {
		return []byte(s), nil
	}
	return nil, errors.New("laracrypt: 密鑰形態不識別（期望 base64: 前綴 / 64 位 hex / 32 字節原文）")
}

// LooksEncrypted 形態識別，與 1.x CardCipher::looksEncrypted 同口徑：
// base64 解出 JSON 對象且 iv/value/mac 三鍵齊備。
func LooksEncrypted(v string) bool {
	if v == "" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(raw) == 0 {
		return false
	}
	var p cryptPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	return p.IV != "" && p.Value != "" && p.Mac != ""
}

// Crypt Laravel Crypt 解密器（APP_KEY 或 CARD_ENCRYPTION_KEY 均可用本類型）。
type Crypt struct {
	key []byte
}

// New 構造（key 必須 32 字節）。
func New(key []byte) (*Crypt, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("laracrypt: AES-256 密鑰須 %d 字節，實際 %d", keyLen, len(key))
	}
	return &Crypt{key: key}, nil
}

// OpenString 解密 Crypt::encryptString 產物（含 MAC 校驗 + PKCS7 + maybe-unserialize）。
func (c *Crypt) OpenString(payload string) (string, error) {
	b, err := c.open(payload, true)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// OpenStringSkipMAC 免 MAC 校驗解密（僅限 --skip-mac 搶救場景；正常路徑必須校驗）。
func (c *Crypt) OpenStringSkipMAC(payload string) (string, error) {
	b, err := c.open(payload, false)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *Crypt) open(payload string, verifyMac bool) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: 載荷 base64 解碼失敗: %v", ErrDecrypt, err)
	}
	var p cryptPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.IV == "" || p.Value == "" {
		return nil, fmt.Errorf("%w: 載荷 JSON 結構異常", ErrNotEncrypted)
	}
	iv, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil || len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("%w: iv 非法", ErrDecrypt)
	}
	value, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil {
		return nil, fmt.Errorf("%w: 密文 base64 解碼失敗", ErrDecrypt)
	}
	if verifyMac {
		mac := hmac.New(sha256.New, c.key)
		mac.Write([]byte(p.IV))
		mac.Write([]byte(p.Value))
		want := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(strings.ToLower(p.Mac)), []byte(want)) {
			return nil, ErrBadMac
		}
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 || len(value)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("%w: 密文長度非塊對齊", ErrDecrypt)
	}
	dst := make([]byte, len(value))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(dst, value)
	out, err := pkcs7Unpad(dst)
	if err != nil {
		return nil, err
	}
	return maybeUnserialize(out)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: 空明文", ErrDecrypt)
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, fmt.Errorf("%w: PKCS7 填充非法", ErrDecrypt)
	}
	for _, v := range b[len(b)-n:] {
		if int(v) != n {
			return nil, fmt.Errorf("%w: PKCS7 填充非法", ErrDecrypt)
		}
	}
	return b[:len(b)-n], nil
}

// maybeUnserialize 兼容 Encrypter::encrypt($v, serialize=true) 的載荷：
// `s:LEN:"...";` → 原文；其他 serialize 類型（i/b/d/a/O…）在 1.x 數據中不存在，
// 出現即按損壞數據處理；非 serialize 形態原樣返回。
func maybeUnserialize(b []byte) ([]byte, error) {
	if len(b) < 2 || b[1] != ':' {
		return b, nil
	}
	switch b[0] {
	case 's':
		rest := b[2:]
		colon := bytes.IndexByte(rest, ':')
		if colon <= 0 {
			return nil, fmt.Errorf("%w: serialize s: 結構異常", ErrDecrypt)
		}
		n, err := strconv.Atoi(string(rest[:colon]))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%w: serialize 長度非法", ErrDecrypt)
		}
		body := rest[colon+1:] // 形如 `"數據";`，開引號屬於定界符而非數據
		if len(body) < n+3 || body[0] != '"' || body[n+1] != '"' || body[n+2] != ';' {
			return nil, fmt.Errorf("%w: serialize 字符串體截斷", ErrDecrypt)
		}
		return body[1 : n+1], nil
	case 'i', 'b', 'd', 'a', 'O':
		return nil, fmt.Errorf("%w: 不支持的 serialize 類型 %q（1.x 數據不應出現）", ErrDecrypt, b[0])
	default:
		return b, nil
	}
}

// OpenCard 解密 cards.content（1.x CardCipher::decrypt 等價物，錯誤策略由調用方決定）。
// 明文直通：wasEncrypted=false 且原樣返回（加密開關關閉期的存量明文卡）。
func OpenCard(content string, cardKey []byte, verifyMac bool) (plain string, wasEncrypted bool, err error) {
	if !LooksEncrypted(content) {
		return content, false, nil
	}
	c, err := New(cardKey)
	if err != nil {
		return "", true, err
	}
	if verifyMac {
		plain, err = c.OpenString(content)
	} else {
		plain, err = c.OpenStringSkipMAC(content)
	}
	if err != nil {
		return "", true, err
	}
	return plain, true, nil
}

// OpenPaymentConfig 解密 payment_channels.config 列（雙層）：
// 列值是 JSON 字符串（帶引號），內層才是 Crypt 密文；歷史明文配置（整列即明文 JSON）
// 原樣返回 wasEncrypted=false。
func OpenPaymentConfig(col string, appKey []byte, verifyMac bool) (plain string, wasEncrypted bool, err error) {
	var inner string
	if err := json.Unmarshal([]byte(col), &inner); err != nil {
		return col, false, nil // 非 JSON 字符串 → 歷史明文配置
	}
	if !LooksEncrypted(inner) {
		return col, false, nil
	}
	c, err := New(appKey)
	if err != nil {
		return "", true, err
	}
	if verifyMac {
		plain, err = c.OpenString(inner)
	} else {
		plain, err = c.OpenStringSkipMAC(inner)
	}
	if err != nil {
		return "", true, err
	}
	return plain, true, nil
}

// OpenSettingSecret 解密 settings 表 SECRET_KEYS 的 value（JSON 編碼字符串）：
// 內層 Crypt 密文 → 解密返回；歷史明文 → 解引號後原樣返回；非字符串 JSON → 原樣返回。
func OpenSettingSecret(raw string, appKey []byte, verifyMac bool) (plain string, wasEncrypted bool, err error) {
	var inner string
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		return raw, false, nil
	}
	if !LooksEncrypted(inner) {
		return inner, false, nil
	}
	c, err := New(appKey)
	if err != nil {
		return "", true, err
	}
	if verifyMac {
		plain, err = c.OpenString(inner)
	} else {
		plain, err = c.OpenStringSkipMAC(inner)
	}
	if err != nil {
		return "", true, err
	}
	return plain, true, nil
}

// CardKeyFromSetting 從 settings.card_encryption_key 的 value 解析卡密鑰匙
// （對應 1.x CardCipher::resolveKey：值本身是 Crypt 密文 → 用 APP_KEY 解出鑰匙串；
// 解密失敗降級視為歷史明文鑰匙串）。fromCipher 表示值確實是密文形態。
func CardKeyFromSetting(raw string, appKey []byte, verifyMac bool) (key []byte, fromCipher bool, err error) {
	val, wasEnc, err := OpenSettingSecret(raw, appKey, verifyMac)
	if err != nil {
		return nil, false, err
	}
	key, kerr := ParseKey(val)
	if kerr != nil {
		return nil, wasEnc, fmt.Errorf("settings 中的卡密鑰匙無法解析: %w", kerr)
	}
	return key, wasEnc, nil
}
