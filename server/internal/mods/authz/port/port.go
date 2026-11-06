// Package port 為 authz 模塊對外契約（零依賴包，規劃 §4.4）。
//
// 權限管理三要素（功能模塊設計 §1.3）：功能權限（本包接口級 RBAC）+
// 數據權限（tenancy subsite_id 行級隔離，M1 interceptor）+ 前端權限（動態路由，
// 權限目錄由啓動時從 Kratos 路由表自動生成）。
package port

import "context"

// 內置角色編碼（種子數據，後台不可刪除）。
const (
	RoleSuperAdmin = "super_admin" // 超管：權限通配符 *
	RoleOperator   = "operator"    // 運營
	RoleSupport    = "support"     // 客服
)

// PermissionAll 超管通配符。
const PermissionAll = "*"

// Authorizer 鑑權窄接口（server 中間件與 identity 消費，通道 A）。
type Authorizer interface {
	// Allowed 判定角色是否擁有權限點；super_admin 的 * 通配全部放行。
	Allowed(ctx context.Context, roleID uint64, permission string) bool
	// PermissionsOf 角色的權限點清單（登錄後下發給前端動態路由）。
	PermissionsOf(ctx context.Context, roleID uint64) ([]string, error)
	// RoleName 角色名（profile 展示）。
	RoleName(ctx context.Context, roleID uint64) string
}
