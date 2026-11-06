package identity

// 員工倉儲 Ent 實現（Ent 收口邊界內：data.go 允許 import ent，架構測試 -5）。

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminuser"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/session"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
)

// ErrAdminUserNotFound 員工不存在。
var ErrAdminUserNotFound = errors.New("identity: 員工不存在")

// AdminUserRepoImpl 員工倉儲實現。
type AdminUserRepoImpl struct {
	data *data.Data
}

// NewAdminUserRepoImpl 構造。
func NewAdminUserRepoImpl(d *data.Data) *AdminUserRepoImpl { return &AdminUserRepoImpl{data: d} }

// FindByUsername 按用戶名查找（登錄熱路徑，UNIQUE(username) 索引命中）。
func (r *AdminUserRepoImpl) FindByUsername(ctx context.Context, username string) (*AdminUser, error) {
	row, err := data.Client(ctx, r.data).AdminUser.Query().
		Where(adminuser.Username(username)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrAdminUserNotFound
	}
	if err != nil {
		return nil, err
	}
	var state mfaState
	_ = json.Unmarshal([]byte(row.MfaState), &state)
	return &AdminUser{
		TOTPBoundAt:   state.BoundAt,
		ID:            row.ID,
		Username:      row.Username,
		PasswordHash:  row.PasswordHash,
		Nickname:      row.Nickname,
		Avatar:        row.Avatar,
		RoleID:        row.RoleID,
		TOTPSecretEnc: row.TotpSecret,
		Enabled:       row.Enabled,
		LastLoginIP:   row.LastLoginIP,
		LastLoginAt:   row.LastLoginAt,
	}, nil
}

// TouchLogin 更新最近登錄 IP 與時間（審計輔助）。
func (r *AdminUserRepoImpl) TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error {
	_, err := data.Client(ctx, r.data).AdminUser.UpdateOneID(id).
		SetLastLoginIP(ip).
		SetLastLoginAt(at).
		Save(ctx)
	return err
}

// ── 員工管理（port.AdminMutator 實現，authz API 面消費；）────────────

// Admin 按 ID 取員工賬戶（port.AdminReader 實現——admin 鑑權中間件每請求
// 校驗 enabled 與當前 RoleID，禁用/換角色即時生效）。
func (r *AdminUserRepoImpl) Admin(ctx context.Context, id uint64) (*port.AdminAccount, error) {
	row, err := data.Client(ctx, r.data).AdminUser.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrAdminUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &port.AdminAccount{
		ID: row.ID, Username: row.Username, Nickname: row.Nickname, Avatar: row.Avatar,
		AuthVersion: row.AuthVersion, RoleID: row.RoleID, Enabled: row.Enabled, TOTPEnabled: len(row.TotpSecret) > 0,
	}, nil
}

// List 全部員工。
func (r *AdminUserRepoImpl) List(ctx context.Context) ([]port.AdminAccount, error) {
	rows, err := data.Client(ctx, r.data).AdminUser.Query().Order(ent.Asc(adminuser.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.AdminAccount, 0, len(rows))
	for _, row := range rows {
		out = append(out, port.AdminAccount{
			ID: row.ID, Username: row.Username, Nickname: row.Nickname, Avatar: row.Avatar,
			AuthVersion: row.AuthVersion, RoleID: row.RoleID, Enabled: row.Enabled, TOTPEnabled: len(row.TotpSecret) > 0,
		})
	}
	return out, nil
}

// Create 創建員工（用戶名唯一；密碼 bcrypt）。
func (r *AdminUserRepoImpl) Create(ctx context.Context, in port.AdminInput) (*port.AdminAccount, error) {
	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	row, err := data.Client(ctx, r.data).AdminUser.Create().
		SetUsername(in.Username).
		SetPasswordHash(hash).
		SetNickname(in.Nickname).
		SetAvatar(in.Avatar).
		SetRoleID(in.RoleID).
		SetRemark(in.Remark).
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	return &port.AdminAccount{ID: row.ID, Username: row.Username, Nickname: row.Nickname, RoleID: row.RoleID, Enabled: true}, nil
}

// Update 更新員工（nil 字段不動；Enabled 非 nil 時切換）。
func (r *AdminUserRepoImpl) Update(ctx context.Context, id uint64, in port.AdminInput) (*port.AdminAccount, error) {
	q := data.Client(ctx, r.data).AdminUser.UpdateOneID(id)
	if in.Nickname != "" {
		q.SetNickname(in.Nickname)
	}
	if in.Avatar != "" {
		q.SetAvatar(in.Avatar)
	}
	if in.RoleID != 0 {
		q.SetRoleID(in.RoleID)
	}
	if in.Remark != "" {
		q.SetRemark(in.Remark)
	}
	if in.Enabled != nil {
		q.SetEnabled(*in.Enabled)
	}
	row, err := q.Save(ctx)
	if err != nil {
		return nil, err
	}
	return &port.AdminAccount{ID: row.ID, Username: row.Username, Nickname: row.Nickname, AuthVersion: row.AuthVersion, RoleID: row.RoleID, Enabled: row.Enabled, TOTPEnabled: len(row.TotpSecret) > 0}, nil
}

// RoleInUse 角色是否仍有員工掛載。
func (r *AdminUserRepoImpl) RoleInUse(ctx context.Context, roleID uint64) (bool, error) {
	return data.Client(ctx, r.data).AdminUser.Query().Where(adminuser.RoleID(roleID)).Exist(ctx)
}

// ResetPassword 重置員工密碼（bcrypt）——憑據接管類操作，調用方須同時吊銷會話。
func (r *AdminUserRepoImpl) ResetPassword(ctx context.Context, id uint64, password string) error {
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	return mfaChange(ctx, r.data, id, func(ctx context.Context, u *ent.AdminUser, state *mfaState) error {
		if err := data.Client(ctx, r.data).AdminUser.UpdateOneID(id).SetPasswordHash(hash).Exec(ctx); err != nil {
			return err
		}
		state.Pending = nil
		state.PendingUntil = 0
		return invalidateMFA(ctx, r.data, u, state)
	})
}

// ClearTOTP 解綁員工 TOTP（port.AdminMutator；冪等——未綁定時同樣成功）。
func (r *AdminUserRepoImpl) ClearTOTP(ctx context.Context, id uint64) error {
	return ResetAdminTwoFactor(ctx, r.data, id, 0, "server recovery")
}

// RevokeAdminSessions 吊銷員工全部未吊銷的管理面會話（密碼重置/解綁 TOTP 後強制重登；
// user realm 會話不受影響）。
func (r *AdminUserRepoImpl) RevokeAdminSessions(ctx context.Context, id uint64) error {
	_, err := data.Client(ctx, r.data).Session.Update().
		Where(
			session.UserID(id),
			session.RealmEQ(session.RealmAdmin),
			session.RevokedAtIsNil(),
		).
		SetRevokedAt(time.Now().UTC()).
		Save(ctx)
	return err
}

// Delete 刪除員工（同事務清其管理面會話，防殘留可刷新的 refresh token）。
func (r *AdminUserRepoImpl) Delete(ctx context.Context, id uint64) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		client := data.Client(ctx, r.data)
		if _, err := client.Session.Delete().
			Where(session.UserID(id), session.RealmEQ(session.RealmAdmin)).
			Exec(ctx); err != nil {
			return err
		}
		return client.AdminUser.DeleteOneID(id).Exec(ctx)
	})
}

// ── TOTP 倉儲方法（）────────────────────────────────────

// SetTOTPSecret 存儲 TOTP 密鑰密文。
func (r *AdminUserRepoImpl) SetTOTPSecret(ctx context.Context, id uint64, secret []byte) error {
	_, err := data.Client(ctx, r.data).AdminUser.UpdateOneID(id).SetTotpSecret(secret).Save(ctx)
	return err
}

// ClearTOTPSecret 清除 TOTP 綁定。
func (r *AdminUserRepoImpl) ClearTOTPSecret(ctx context.Context, id uint64) error {
	return ResetAdminTwoFactor(ctx, r.data, id, 0, "server recovery")
}
