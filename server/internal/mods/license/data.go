package license

// 訂閲許可證模塊（ 第 1 層落地）：
//   settings license 組存儲（file=許可證內容 / pubkey=ed25519 公鑰 / instance_id=實例 ID）；
//   安裝 = 校驗（簽名/實例綁定/到期）通過後落庫；查詢 = 實時 Verify fail-closed。
// 社區版無公鑰/無許可證 → 全部高級特性關閉（開源核心功能不受影響）。

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/platform/license"
)

// settingsStore 設置讀寫窄接口（consumer-side，settings.RepoImpl 滿足，通道 A）。
type settingsStore interface {
	Get(ctx context.Context, group, key string) (json.RawMessage, error)
	Put(ctx context.Context, group, key string, value json.RawMessage) error
}

// LicenseRepo 許可證倉儲。
type LicenseRepo struct {
	settings settingsStore
}

// NewLicenseRepo 構造。
func NewLicenseRepo(settings settingsStore) *LicenseRepo {
	return &LicenseRepo{settings: settings}
}

// InstanceID 讀取或生成實例 ID（生成後持久化，許可證綁定用）。
func (r *LicenseRepo) InstanceID(ctx context.Context) (string, error) {
	raw, err := r.settings.Get(ctx, "license", "instance_id")
	if err == nil && len(raw) > 0 && string(raw) != "null" && string(raw) != `""` {
		var v string
		if json.Unmarshal(raw, &v) == nil && v != "" {
			return v, nil
		}
	}
	id := randomInstanceID()
	if err := r.settings.Put(ctx, "license", "instance_id", json.RawMessage(`"`+id+`"`)); err != nil {
		return "", err
	}
	return id, nil
}

// Status 許可證狀態。
type Status struct {
	Installed  bool
	Valid      bool
	InstanceID string
	Domain     string
	Features   []string
	ExpiresAt  string
	Error      string
}

// Status 查詢：讀許可證 + 公鑰 → Verify（fail-closed）。
func (r *LicenseRepo) Status(ctx context.Context) Status {
	st := Status{InstanceID: "", Installed: false, Valid: false}
	id, err := r.InstanceID(ctx)
	if err != nil {
		st.Error = "實例 ID 生成失敗"
		return st
	}
	st.InstanceID = id
	raw, _ := r.settings.Get(ctx, "license", "file")
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		st.Error = "未安裝許可證（社區版）"
		return st
	}
	var content string
	if json.Unmarshal(raw, &content) != nil || content == "" {
		st.Error = "許可證存儲損壞"
		return st
	}
	st.Installed = true
	pub, err := r.PubKey(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	lic, err := license.Verify([]byte(content), pub, id, r.domain(ctx), time.Now().UTC())
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Valid = true
	st.Domain = lic.Domain
	st.Features = lic.Features
	st.ExpiresAt = lic.ExpiresAt
	return st
}

// Install 校驗並安裝許可證（綁定當前實例；失敗不落庫）。
func (r *LicenseRepo) Install(ctx context.Context, content string) error {
	pub, err := r.PubKey(ctx)
	if err != nil {
		return err
	}
	id, err := r.InstanceID(ctx)
	if err != nil {
		return err
	}
	if _, err := license.Verify([]byte(content), pub, id, r.domain(ctx), time.Now().UTC()); err != nil {
		return err
	}
	return r.settings.Put(ctx, "license", "file", json.RawMessage(`"`+jsonEscape(content)+`"`))
}

// Clear 清除許可證。
func (r *LicenseRepo) Clear(ctx context.Context) error {
	return r.settings.Put(ctx, "license", "file", json.RawMessage(`""`))
}

// PubKey 讀取 ed25519 公鑰（base64；未配置返回 ErrInvalidLicense）。
func (r *LicenseRepo) PubKey(ctx context.Context) (ed25519PublicKey, error) {
	raw, _ := r.settings.Get(ctx, "license", "pubkey")
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return nil, license.ErrInvalidLicense
	}
	var b64 string
	if json.Unmarshal(raw, &b64) != nil {
		return nil, license.ErrInvalidLicense
	}
	pub, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(pub) != ed25519PublicKeySize {
		return nil, license.ErrInvalidLicense
	}
	return pub, nil
}

// domain 主站域名（license 組 domain 配置；空 = 跳過域名綁定）。
func (r *LicenseRepo) domain(ctx context.Context) string {
	raw, _ := r.settings.Get(ctx, "license", "domain")
	if len(raw) == 0 {
		return ""
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return v
}

// ── 工具 ──

type ed25519PublicKey = []byte

const ed25519PublicKeySize = 32

func randomInstanceID() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "zcard-inst"
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0xf]
	}
	return "zcard-" + string(out)
}

func jsonEscape(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw[1 : len(raw)-1])
}
