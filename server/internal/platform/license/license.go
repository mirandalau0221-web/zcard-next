// Package license 訂閲許可證校驗引擎：
//
//	ed25519 簽名驗證（公鑰編譯進核心、私鑰離線持有）→ 綁定實例 ID/域名
//	→ 訂閲到期 → 特性清單（features 控制高級功能開關）。
//
// 離線可驗、無網絡依賴；篡改/過期/綁定不符一律 fail-closed。
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// ErrInvalidLicense 許可證非法（簽名/格式/綁定任一失敗）。
var ErrInvalidLicense = errors.New("license: 許可證無效")

// License 許可證內容（JSON 明文 + 獨立簽名段）。
type License struct {
	InstanceID string   `json:"instance_id"` // 綁定實例 ID（安裝時生成）
	Domain     string   `json:"domain"`      // 綁定主站域名
	Features   []string `json:"features"`    // 授權特性清單
	IssuedAt   string   `json:"issued_at"`   // RFC3339
	ExpiresAt  string   `json:"expires_at"`  // RFC3339（空 = 永久）
}

// SignedLicense 許可證文件結構（<內容 JSON> + <base64 ed25519 簽名>）。
type SignedLicense struct {
	License   License `json:"license"`
	Signature string  `json:"signature"` // base64(ed25519.Sign(priv, content))
}

// Verify 校驗並解析許可證（fail-closed：任何一項不符即拒絕）。
// pub 為編譯進核心的公鑰；instanceID/domain 為當前實例綁定值。
func Verify(raw []byte, pub ed25519.PublicKey, instanceID, domain string, now time.Time) (*License, error) {
	var sl SignedLicense
	if err := json.Unmarshal(raw, &sl); err != nil {
		return nil, ErrInvalidLicense
	}
	// 重編碼內容段作為簽名原文（防字段注入：簽名覆蓋整個 License JSON）
	content, err := json.Marshal(sl.License)
	if err != nil {
		return nil, ErrInvalidLicense
	}
	sig, err := base64.StdEncoding.DecodeString(sl.Signature)
	if err != nil {
		return nil, ErrInvalidLicense
	}
	if !ed25519.Verify(pub, content, sig) {
		return nil, ErrInvalidLicense
	}
	lic := &sl.License
	// 綁定校驗
	if lic.InstanceID != instanceID {
		return nil, ErrInvalidLicense
	}
	if lic.Domain != "" && domain != "" && lic.Domain != domain {
		return nil, ErrInvalidLicense
	}
	// 到期校驗（空 = 永久）
	if lic.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, lic.ExpiresAt)
		if err != nil {
			return nil, ErrInvalidLicense
		}
		if now.After(exp) {
			return nil, ErrInvalidLicense
		}
	}
	return lic, nil
}

// HasFeature 特性是否授權（永久版含全部特性可用通配 *）。
func (l *License) HasFeature(feature string) bool {
	if l == nil {
		return false
	}
	for _, f := range l.Features {
		if f == "*" || f == feature {
			return true
		}
	}
	return false
}

// GenerateKeyPair 生成 ed25519 密鑰對（發行側/測試用；私鑰離線保管）。
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, nil, err
	}
	return pub, priv, nil
}

// Sign 簽發（私鑰 + License → 文件內容；發行側使用）。
func Sign(priv ed25519.PrivateKey, lic License) ([]byte, error) {
	content, err := json.Marshal(lic)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, content)
	return json.Marshal(SignedLicense{
		License:   lic,
		Signature: base64.StdEncoding.EncodeToString(sig),
	})
}
