// Package authz 自建 RBAC（）：admin_roles + role_permissions，權限目錄自動生成。
//
// 髮卡系統權限模型簡單，自建 RBAC 比 Casbin 更輕可控（規劃 ）；
// 「域內角色」（分站主只見自己分站）由 subsite_id 租户隔離實現，不做雙套權限源。
package authz

import (
	"context"
	"sync"
	"time"
)

// RoleRepo 角色倉儲（模塊內端口，實現於 data.go；管理方法僅供 service 層消費）。
type RoleRepo interface {
	// PermissionCodes 返回角色的全部權限點編碼。
	PermissionCodes(ctx context.Context, roleID uint64) ([]string, error)
	// Role 角色名（不存在返回空串）。
	Role(ctx context.Context, roleID uint64) (name string, err error)

	// ── 管理面（RoleService 消費）──
	Roles(ctx context.Context) ([]*RoleDetail, error)
	RoleByID(ctx context.Context, id uint64) (*RoleDetail, error)
	CreateRole(ctx context.Context, name, code, desc string, perms []string) (*RoleDetail, error)
	UpdateRole(ctx context.Context, id uint64, name, desc string) (*RoleDetail, error)
	DeleteRole(ctx context.Context, id uint64) error
	SetPermissions(ctx context.Context, id uint64, perms []string) (*RoleDetail, error)
}

// RbacUsecase 鑑權用例：權限點進程內緩存（30s TTL，角色變更後自然過期；
// 多員工實時吊銷走 sessions/版本號）。
type RbacUsecase struct {
	repo RoleRepo

	mu    sync.RWMutex
	cache map[uint64]cacheEntry
}

type cacheEntry struct {
	codes   []string
	name    string
	expires time.Time
}

const cacheTTL = 30 * time.Second

// NewRbacUsecase 構造。
func NewRbacUsecase(repo RoleRepo) *RbacUsecase {
	return &RbacUsecase{repo: repo, cache: map[uint64]cacheEntry{}}
}

// Allowed 判定權限；* 通配（super_admin 種子）。
func (uc *RbacUsecase) Allowed(ctx context.Context, roleID uint64, permission string) bool {
	codes := uc.codes(ctx, roleID)
	for _, c := range codes {
		if c == permission || c == "*" {
			return true
		}
	}
	return false
}

// PermissionsOf 權限點清單（含緩存）。
func (uc *RbacUsecase) PermissionsOf(ctx context.Context, roleID uint64) ([]string, error) {
	return uc.codes(ctx, roleID), nil
}

// RoleName 角色名（含緩存）。
func (uc *RbacUsecase) RoleName(ctx context.Context, roleID uint64) string {
	uc.mu.RLock()
	e, ok := uc.cache[roleID]
	uc.mu.RUnlock()
	if ok && time.Now().Before(e.expires) {
		return e.name
	}
	name, err := uc.repo.Role(ctx, roleID)
	if err != nil {
		return ""
	}
	uc.store(roleID, uc.codes(ctx, roleID), name)
	return name
}

func (uc *RbacUsecase) codes(ctx context.Context, roleID uint64) []string {
	uc.mu.RLock()
	e, ok := uc.cache[roleID]
	uc.mu.RUnlock()
	if ok && time.Now().Before(e.expires) {
		return e.codes
	}
	codes, err := uc.repo.PermissionCodes(ctx, roleID)
	if err != nil {
		return nil
	}
	var name string
	name, _ = uc.repo.Role(ctx, roleID)
	uc.store(roleID, codes, name)
	return codes
}

// Invalidate 失效角色緩存（權限變更後實時生效，）。
func (uc *RbacUsecase) Invalidate(roleID uint64) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	delete(uc.cache, roleID)
}

func (uc *RbacUsecase) store(roleID uint64, codes []string, name string) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.cache[roleID] = cacheEntry{codes: codes, name: name, expires: time.Now().Add(cacheTTL)}
}
