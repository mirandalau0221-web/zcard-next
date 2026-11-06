package crypto

// 密碼哈希：bcrypt（成本 12）。
// 登錄密碼與取貨查詢密碼統一走本包，比對使用庫內 constant-time 實現（鐵律 12）。

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// HashPassword 哈希密碼（bcrypt）。
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("crypto: hash password: %w", err)
	}
	return string(b), nil
}

// VerifyPassword constant-time 校驗。錯誤信息不區分「用户不存在」與「密碼錯誤」（防枚舉）。
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
