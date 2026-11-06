package identity

// identity 身份認證模塊（M0 收尾）：管理員 TOTP/登錄失敗鎖定/異地告警/refresh 輪換。
// user realm 註冊登錄 M1a 交付。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/session"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
)

// 錯誤 reason（附錄 B 區段：identity.*）
var (
	// ErrLoginFailed 登錄失敗（賬號或密碼錯誤——對外不區分，防枚舉）。
	ErrLoginFailed = errors.New("identity.LOGIN_FAILED")
	// ErrAdminDisabled 賬號已禁用。
	ErrAdminDisabled = errors.New("identity.ADMIN_DISABLED")
	// ErrLocked 登錄失敗次數過多，賬號被鎖定。
	ErrLocked = errors.New("identity.ACCOUNT_LOCKED")
	// ErrTOTPRequired 需要兩步驗證碼。
	ErrTOTPRequired = errors.New("identity.TOTP_REQUIRED")
	// ErrTOTPInvalid 兩步驗證碼錯誤。
	ErrTOTPInvalid = errors.New("identity.TOTP_INVALID")
	// ErrSessionInvalid refresh 令牌無效或已過期。
	ErrSessionInvalid = errors.New("identity.SESSION_INVALID")
)

// 登錄鎖定參數。
const (
	lockThreshold = 5                // 連續失敗次數
	lockDuration  = 15 * time.Minute // 鎖定時長
)

// AdminUserRepo 員工倉儲（模塊內端口）。
type AdminUserRepo interface {
	FindByUsername(ctx context.Context, username string) (*AdminUser, error)
	TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error
	// TOTP
	SetTOTPSecret(ctx context.Context, id uint64, secret []byte) error
	ClearTOTPSecret(ctx context.Context, id uint64) error
}

// AdminUser 員工聚合。
type AdminUser struct {
	TOTPBoundAt   int64
	ID            uint64
	Username      string
	PasswordHash  string
	Nickname      string
	Avatar        string
	RoleID        uint64
	TOTPSecretEnc []byte
	Enabled       bool
	LastLoginIP   string
	LastLoginAt   time.Time
}

// IdentityUsecase 身份用例。
type IdentityUsecase struct {
	repo   AdminUserRepo
	signer *authn.Signer
	data   *data.Data
	cipher *crypto.Box // ZCARD_DATA_KEY 解密 TOTP 密鑰
}

// NewIdentityUsecase 構造。
func NewIdentityUsecase(repo AdminUserRepo, signer *authn.Signer, d *data.Data, box *crypto.Box) *IdentityUsecase {
	return &IdentityUsecase{repo: repo, signer: signer, data: d, cipher: box}
}

// AdminLoginResult 登錄結果。
type AdminLoginResult struct {
	Challenge      string
	RecoveryTicket string
	AccessToken    string
	RefreshToken   string
	ExpiresAt      time.Time
	Admin          AdminUser
}

// AdminLogin 管理員登錄：鎖定檢查 → bcrypt 校驗 → TOTP 校驗 → 簽發雙令牌。
func (uc *IdentityUsecase) AdminLogin(ctx context.Context, username, password, totpCode, clientIP string) (*AdminLoginResult, error) {
	return uc.passwordLogin(ctx, username, password, totpCode, clientIP, false)
}

// RefreshAccess 用 refresh token 換新令牌對（一次性：舊 session 吊銷 + 新 session）。
func (uc *IdentityUsecase) RefreshAccess(ctx context.Context, refreshToken string) (*AdminLoginResult, error) {
	sess, err := uc.data.Client.Session.Query().Where(session.RefreshTokenHash(hashToken(refreshToken)), session.RealmEQ(session.RealmAdmin)).Only(ctx)
	if err != nil {
		return nil, ErrSessionInvalid
	}
	var out *AdminLoginResult
	err = mfaChange(ctx, uc.data, sess.UserID, func(ctx context.Context, u *ent.AdminUser, s *mfaState) error {
		if !u.Enabled || u.AuthVersion != sess.AuthVersion || time.Now().After(sess.ExpiresAt) {
			return ErrSessionInvalid
		}
		n, err := data.Client(ctx, uc.data).Session.Update().Where(session.ID(sess.ID), session.RevokedAtIsNil()).SetRevokedAt(time.Now().UTC()).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrSessionInvalid
		}
		out, err = uc.issueSession(ctx, u, sess.IP, sess.ExpiresAt)
		return err
	})
	return out, err
}

// Logout 吊銷指定 refresh token。
func (uc *IdentityUsecase) Logout(ctx context.Context, refreshToken string) error {
	hash := hashToken(refreshToken)
	_, err := uc.data.Client.Session.Update().
		Where(session.RefreshTokenHash(hash)).
		SetRevokedAt(time.Now().UTC()).
		Save(ctx)
	return err
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := randRead(b); err != nil {
		panic("identity: 隨機數生成失敗: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func randRead(b []byte) (int, error) { return rand.Read(b) }
