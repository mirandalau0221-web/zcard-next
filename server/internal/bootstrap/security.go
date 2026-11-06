package bootstrap

// 密鑰裝配：四把鑰匙解耦（§4.11.6），env/secret 注入優先，conf 兜底，
// 開發環境允許隨機臨時密鑰（帶顯著告警；重啓失效，僅限本機開發）。

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"

	"github.com/google/wire"
)

var securityProviderSet = wire.NewSet(
	NewSigner,
	NewCardCipher,
	NewDataBox,
)

// NewDataBox 業務數據加密盒（ZCARD_DATA_KEY：憑據列/TOTP 密鑰共用）。
func NewDataBox(sec *conf.Security) (*crypto.Box, error) {
	if sec == nil {
		sec = &conf.Security{}
	}
	raw := os.Getenv("ZCARD_DATA_KEY")
	if raw == "" {
		raw = sec.DataKey
	}
	if raw == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		raw = hex.EncodeToString(buf)
		slog.Warn("bootstrap: 未配置 ZCARD_DATA_KEY，使用隨機臨時密鑰（僅限開發）")
	}
	key, err := crypto.ParseHexKey(raw)
	if err != nil {
		return nil, err
	}
	return crypto.NewBox(key)
}

// resolveKey 密鑰解析：env > conf > dev 隨機（告警）。
// kind 用於日誌標識（jwt_admin/jwt_user/card/data）。
func resolveKey(envName, confVal, kind string) []byte {
	if v := os.Getenv(envName); v != "" {
		return []byte(v)
	}
	if confVal != "" {
		return []byte(confVal)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("bootstrap: 開發隨機密鑰生成失敗：" + err.Error())
	}
	slog.Warn("bootstrap: 未配置密鑰，使用隨機臨時密鑰（僅限開發；生產必須經 env 注入）",
		"key", kind, "env", envName)
	return buf
}

func key32(b []byte) []byte {
	if len(b) >= 32 {
		return b[:32]
	}
	out := make([]byte, 32)
	copy(out, b)
	return out
}

// NewSigner 構造 JWT 簽發器（admin/user 雙 realm 獨立密鑰）。
func NewSigner(sec *conf.Security) (*authn.Signer, error) {
	if sec == nil {
		sec = &conf.Security{}
	}
	return authn.NewSigner(
		key32(resolveKey("ZCARD_JWT_ADMIN_KEY", sec.JwtAdminKey, "jwt_admin")),
		key32(resolveKey("ZCARD_JWT_USER_KEY", sec.JwtUserKey, "jwt_user")),
		24*time.Hour, // access TTL 24h（頻繁重啓/更新的部署形態下 2h 掉線感過強；refresh 會話 14 天不變）
	)
}

// NewCardCipher 構造卡密加密器（ZCARD_CARD_KEY：hex 32 字節 → AES-256-GCM + keyed hash）。
func NewCardCipher(sec *conf.Security) (*inventory.CardCipher, error) {
	if sec == nil {
		sec = &conf.Security{}
	}
	raw := os.Getenv("ZCARD_CARD_KEY")
	if raw == "" {
		raw = sec.CardKey
	}
	if raw == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		raw = hex.EncodeToString(buf)
		slog.Warn("bootstrap: 未配置 ZCARD_CARD_KEY，使用隨機臨時卡密密鑰（僅限開發；重啓後已存卡密不可解）")
	}
	key, err := crypto.ParseHexKey(raw)
	if err != nil {
		return nil, err
	}
	return inventory.NewCardCipher(key)
}
