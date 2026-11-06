package identity

// 用户自服務（）：找回密碼（郵箱驗證碼）/改密/改資料。
// 安全模型（ + ）：
// - 防枚舉：郵箱不存在同樣返回成功（僅真實郵箱收碼）
// - 防爆破：code_hash SHA-256 存儲（庫內無明文）；單條記錄 attempt≥5 作廢
// - 防濫發：同郵箱 60s 冷卻；驗證碼 15 分鐘；一次性（verified_at）
// - 改密踢線：sessions 批量吊銷（realm=user）；響應攜帶新 token 保當前會話

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/emailverification"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/session"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"

	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
)

// 找回密碼參數。
const (
	resetCodeTTL      = 15 * time.Minute // 驗證碼有效期
	resetCodeCooldown = 60 * time.Second // 同郵箱重發冷卻
	resetCodeMaxTry   = 5                // 單條驗證碼錯誤上限（作廢需重發）
)

// PasswordService 找回/改密/改資料（掛在 StoreUserService，構造注入 Sender）。
type PasswordService struct {
	data   *data.Data
	signer *authn.Signer
	sender notifyport.Sender
}

// NewPasswordService 構造（wire）。
func NewPasswordService(d *data.Data, signer *authn.Signer, sender notifyport.Sender) *PasswordService {
	return &PasswordService{data: d, signer: signer, sender: sender}
}

func codeHash(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ForgotPassword 發碼（防枚舉：任何輸入都成功；僅真實郵箱投遞）。
func (s *PasswordService) ForgotPassword(ctx context.Context, email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("identity.EMAIL_INVALID")
	}
	u, err := data.Client(ctx, s.data).User.Query().
		Where(user.Email(email)).Only(ctx)
	if err != nil {
		return nil // 防枚舉：不存在同樣成功
	}
	return s.sendResetCode(ctx, u)
}

// sendResetCode 生成驗證碼 + 冷卻檢查 + 郵件投遞。
func (s *PasswordService) sendResetCode(ctx context.Context, u *ent.User) error {
	client := data.Client(ctx, s.data)
	// 60s 冷卻：最新一條未過期未驗證記錄
	latest, err := client.EmailVerification.Query().
		Where(
			emailverification.Email(u.Email),
			emailverification.PurposeEQ(emailverification.PurposeReset),
			emailverification.VerifiedAtIsNil(),
		).
		Order(ent.Desc(emailverification.FieldCreatedAt)).
		First(ctx)
	if err == nil && time.Since(latest.CreatedAt) < resetCodeCooldown {
		return errors.New("identity.CODE_COOLDOWN") // 命中真實郵箱才暴露冷卻（防枚舉允許）
	}
	// 通道就緒前置校驗（fail-fast;防枚舉允許冷卻差異,通道缺失必須明示）
	if cr, ok := s.sender.(interface {
		ChannelReady(ctx context.Context, channel string) bool
	}); ok && !cr.ChannelReady(ctx, "email") {
		return errors.New("identity.EMAIL_NOT_READY: 郵件通道未配置——請聯繫管理員在後台「設置 → 郵件短信」配置 SMTP 發件賬號")
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	if _, err := client.EmailVerification.Create().
		SetEmail(u.Email).
		SetUserID(u.ID).
		SetPurpose(emailverification.PurposeReset).
		SetCodeHash(codeHash(code)).
		SetExpiresAt(time.Now().Add(resetCodeTTL)).
		Save(ctx); err != nil {
		return err
	}
	// 直髮（不走模板：無模板即 skipped 的降級語義不適用於驗證碼場景）
	return s.sender.Send(ctx, notifyport.Message{
		EventType: "user.password_reset",
		Channel:   "email",
		Recipient: u.Email,
		Locale:    "zh_CN",
		Subject:   "ZCard 找回密碼驗證碼",
		Body: fmt.Sprintf(
			"<p>您正在重置賬户 <b>%s</b> 的密碼。</p><p>驗證碼：<b style=\"font-size:20px\">%s</b>（%d 分鐘內有效）。</p><p>若非本人操作請忽略本郵件。</p>",
			u.Username, code, int(resetCodeTTL.Minutes())),
		Variables: map[string]string{"username": u.Username},
		BizType:   "password_reset", BizID: u.ID,
	})
}

// ResetPassword 驗碼重置（一次性；吊銷全部 session；重置即登錄）。
func (s *PasswordService) ResetPassword(ctx context.Context, email, code, newPassword string) (*ent.User, error) {
	if len(newPassword) < 6 {
		return nil, errors.New("identity.PASSWORD_TOO_SHORT: 至少 6 位")
	}
	email = strings.TrimSpace(strings.ToLower(email))
	client := data.Client(ctx, s.data)
	v, err := client.EmailVerification.Query().
		Where(
			emailverification.Email(email),
			emailverification.PurposeEQ(emailverification.PurposeReset),
		).
		Order(ent.Desc(emailverification.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		return nil, errors.New("identity.CODE_INVALID") // 無記錄（防枚舉：統一錯誤）
	}
	// 校驗鏈：過期/已用/超限/不匹配 全部統一 CODE_INVALID（不泄露差異）
	bad := !v.VerifiedAt.IsZero() ||
		time.Now().After(v.ExpiresAt) ||
		v.AttemptCount >= resetCodeMaxTry ||
		codeHash(code) != v.CodeHash
	if bad {
		_, _ = client.EmailVerification.UpdateOne(v).
			SetAttemptCount(v.AttemptCount + 1).Save(ctx)
		return nil, errors.New("identity.CODE_INVALID")
	}
	u, err := client.User.Get(ctx, v.UserID)
	if err != nil {
		return nil, errors.New("identity.CODE_INVALID")
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	err = data.Tx(ctx, s.data, func(txCtx context.Context) error {
		tc := data.Client(txCtx, s.data)
		if _, err := tc.User.UpdateOne(u).SetPasswordHash(hash).Save(txCtx); err != nil {
			return err
		}
		if _, err := tc.EmailVerification.UpdateOne(v).SetVerifiedAt(time.Now()).Save(txCtx); err != nil {
			return err
		}
		return revokeUserSessions(txCtx, tc, u.ID)
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ChangePassword 登錄態改密（舊密碼校驗；吊銷 session；新 token 保當前會話）。
func (s *PasswordService) ChangePassword(ctx context.Context, userID uint64, oldPassword, newPassword string) (*ent.User, error) {
	if len(newPassword) < 6 {
		return nil, errors.New("identity.PASSWORD_TOO_SHORT: 至少 6 位")
	}
	client := data.Client(ctx, s.data)
	u, err := client.User.Get(ctx, userID)
	if err != nil {
		return nil, errors.New("identity.UNAUTHORIZED")
	}
	if !crypto.VerifyPassword(u.PasswordHash, oldPassword) {
		return nil, errors.New("identity.BAD_CREDENTIALS")
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	err = data.Tx(ctx, s.data, func(txCtx context.Context) error {
		tc := data.Client(txCtx, s.data)
		if _, err := tc.User.UpdateOne(u).SetPasswordHash(hash).Save(txCtx); err != nil {
			return err
		}
		return revokeUserSessions(txCtx, tc, u.ID)
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UpdateProfile 改郵箱（唯一性校驗；空=不改）。
func (s *PasswordService) UpdateProfile(ctx context.Context, userID uint64, email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("identity.EMAIL_INVALID")
	}
	client := data.Client(ctx, s.data)
	_, err := client.User.UpdateOneID(userID).SetEmail(email).Save(ctx)
	if err != nil {
		return errors.New("identity.EMAIL_TAKEN") // 唯一索引衝突
	}
	return nil
}

// revokeUserSessions 吊銷用户全部 user session（改密/重置踢線）。
func revokeUserSessions(ctx context.Context, client *ent.Client, userID uint64) error {
	_, err := client.Session.Update().
		Where(
			session.RealmEQ(session.RealmUser),
			session.UserID(userID),
			session.RevokedAtIsNil(),
		).
		SetRevokedAt(time.Now()).
		Save(ctx)
	return err
}
