package authn

// TOTP 兩步驗證（）：密鑰 AES-GCM 加密存儲（ZCARD_DATA_KEY），
// 驗證窗口 ±1（30s 步長）。啓用流程：生成密鑰 → 管理員掃碼/手輸 → 驗證一次確認。

import (
	"fmt"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTPIssuer 服務名（ authenticator app 顯示）。
const TOTPIssuer = "ZCard"

// GenerateTOTP 生成新密鑰（返回 otpauth URL 供二維碼 + 密鑰明文供手輸；由調用方加密存儲）。
func GenerateTOTP(account string) (secret string, url string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TOTPIssuer,
		AccountName: account,
		Period:      30,
		Digits:      otp.DigitsSix,
	})
	if err != nil {
		return "", "", fmt.Errorf("authn: 生成 TOTP 密鑰失敗: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// VerifyTOTP 驗證六位動態碼（±1 窗口）。
func VerifyTOTP(secret, code string) bool {
	if secret == "" || len(code) != 6 {
		return false
	}
	return totp.Validate(code, secret)
}
