// Package authn 雙 realm JWT（規劃 §7.3）。
//
// admin 與 user 獨立密鑰與聲明（防提權串用）：admin 令牌在 user 接口無效，反之亦然。
// access 2h；refresh 14d 輪換（sessions 表，M3）。本包為純 JWT 原語（golang-jwt/v5），
// 不 import Kratos transport（架構測試規則 3：platform 純淨）。
package authn

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Realm 令牌領域。
type Realm string

// 雙 realm。
const (
	RealmAdmin Realm = "admin"
	RealmUser  Realm = "user"
)

// Claims 自定義聲明。
type Claims struct {
	AuthVersion int    `json:"auth_version"`
	Subject     uint64 `json:"sub"`            // admin_users.id / users.id
	Username    string `json:"username"`       // 登錄名快照
	RoleID      uint64 `json:"role,omitempty"` // admin realm：角色 ID
	Realm       Realm  `json:"realm"`          // admin | user（校驗時必須匹配）
	TokenType   string `json:"typ"`            // access | refresh
	jwt.RegisteredClaims
}

// ErrInvalidToken 令牌無效/過期/領域不匹配（對外統一 401，不區分原因防探測）。
var ErrInvalidToken = errors.New("authn: 令牌無效")

// Signer 簽發器（每 realm 一把密鑰，從 conf.Security 裝配）。
type Signer struct {
	adminKey []byte
	userKey  []byte
	ttl      time.Duration
}

// NewSigner 構造。密鑰長度 >= 32 字節。
func NewSigner(adminKey, userKey []byte, accessTTL time.Duration) (*Signer, error) {
	if len(adminKey) < 32 || len(userKey) < 32 {
		return nil, fmt.Errorf("authn: JWT 密鑰長度不足（需 >= 32 字節，admin=%d user=%d）", len(adminKey), len(userKey))
	}
	if accessTTL <= 0 {
		accessTTL = 2 * time.Hour
	}
	return &Signer{adminKey: adminKey, userKey: userKey, ttl: accessTTL}, nil
}

// AccessTTL 訪問令牌有效期。
func (s *Signer) AccessTTL() time.Duration { return s.ttl }

// Issue 簽發 access 令牌。
func (s *Signer) Issue(realm Realm, subject uint64, username string, roleID uint64, version ...int) (token string, expiresAt time.Time, err error) {
	v := 0
	if len(version) > 0 {
		v = version[0]
	}
	return s.IssueUntil(realm, subject, username, roleID, v, time.Time{})
}

// IssueUntil caps the final access token at the original session deadline.
func (s *Signer) IssueUntil(realm Realm, subject uint64, username string, roleID uint64, version int, deadline time.Time) (token string, expiresAt time.Time, err error) {
	now := time.Now()
	expiresAt = now.Add(s.ttl)
	if !deadline.IsZero() && deadline.Before(expiresAt) {
		expiresAt = deadline
	}
	if !expiresAt.After(now) {
		return "", time.Time{}, ErrInvalidToken
	}
	claims := &Claims{
		Subject:   subject,
		Username:  username,
		RoleID:    roleID,
		Realm:     realm,
		TokenType: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Issuer:    "zcard",
		},
	}
	claims.AuthVersion = version
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	key := s.adminKey
	if realm == RealmUser {
		key = s.userKey
	}
	token, err = t.SignedString(key)
	return token, expiresAt, err
}

// Verify 校驗令牌並返回聲明；realm 必須與期望一致（雙 realm 防串用）。
func (s *Signer) Verify(realm Realm, tokenString string) (*Claims, error) {
	key := s.adminKey
	if realm == RealmUser {
		key = s.userKey
	}
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("authn: 非預期簽名算法 %v", t.Header["alg"])
		}
		return key, nil
	})
	if err != nil {
		return nil, ErrInvalidToken
	}
	if claims.Realm != realm || claims.TokenType != "access" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
