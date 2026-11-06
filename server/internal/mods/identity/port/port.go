// Package port 為 identity 模塊對外契約（零依賴包，跨模塊引用的唯一合法入口，規劃 ）。
//
// 只允許 import 標準庫 / platform/* / api 生成類型；違反即架構測試紅燈（-11）。
package port

import "context"

// AdminAccount 員工賬户 DTO（供 audit 等模塊取操作者信息；不含密碼哈希等敏感字段）。
type AdminAccount struct {
	AuthVersion int
	ID          uint64
	Username    string
	Nickname    string
	Avatar      string
	RoleID      uint64
	Enabled     bool
	TOTPEnabled bool
	LastLoginIP string
}

// AdminReader 其他模塊讀取員工信息的窄接口（通道 A：需要返回值的同步調用）。
type AdminReader interface {
	Admin(ctx context.Context, id uint64) (*AdminAccount, error)
}

// UserReader 其他模塊讀取前台用户賬號的窄接口（audit 安全日誌主體名稱富化）。
type UserReader interface {
	Username(ctx context.Context, id uint64) (string, error)
}

// AdminInput 創建/更新員工參數。
type AdminInput struct {
	Username string
	Password string // 僅創建
	Nickname string
	Avatar   string
	RoleID   uint64
	Remark   string
	Enabled  *bool // 僅更新（Toggle）
}

// AdminMutator 員工管理窄接口（authz API 面消費，數據層在 identity 模塊；）。
type AdminMutator interface {
	List(ctx context.Context) ([]AdminAccount, error)
	Create(ctx context.Context, in AdminInput) (*AdminAccount, error)
	Update(ctx context.Context, id uint64, in AdminInput) (*AdminAccount, error)
	// ExistsRoleInUse 角色是否仍有員工掛載（刪除角色前置校驗）。
	RoleInUse(ctx context.Context, roleID uint64) (bool, error)
	// ResetPassword 重置員工密碼（bcrypt；authz API 面消費——重置後須 RevokeAdminSessions）。
	ResetPassword(ctx context.Context, id uint64, password string) error
	// ClearTOTP 解綁員工 TOTP（authz API 面消費——解綁後須 RevokeAdminSessions）。
	ClearTOTP(ctx context.Context, id uint64) error
	// RevokeAdminSessions 吊銷員工全部管理面會話（密碼重置/解綁 TOTP 後強制重登）。
	RevokeAdminSessions(ctx context.Context, id uint64) error
	// Delete 刪除員工（連同其管理面會話；超管/自刪保護由 authz API 面負責）。
	Delete(ctx context.Context, id uint64) error
}
