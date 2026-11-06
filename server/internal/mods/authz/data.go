package authz

// 角色/權限倉儲（）：只讀鑑權組 + 管理組 CRUD + 內置角色種子。

import (
	"context"
	"errors"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminrole"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminuser"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/rolepermission"
)

// 角色錯誤（service 層映射 reason）。
var (
	ErrRoleNotFound = errors.New("authz: 角色不存在")
	ErrRoleBuiltIn  = errors.New("authz: 內置角色受保護")
	ErrRoleInUse    = errors.New("authz: 角色仍有員工掛載")
)

// RoleRepoImpl 角色倉儲實現。
type RoleRepoImpl struct {
	data *data.Data
}

// NewRoleRepoImpl 構造。
func NewRoleRepoImpl(d *data.Data) *RoleRepoImpl { return &RoleRepoImpl{data: d} }

// RoleDetail 角色詳情（含權限點集）。
type RoleDetail struct {
	ID          uint64
	Name        string
	Code        string
	Description string
	IsBuiltin   bool
	Permissions []string
}

// PB 轉協議對象。
func (r *RoleDetail) PB() *adminv1.Role {
	return &adminv1.Role{
		Id: r.ID, Name: r.Name, Code: r.Code, Description: r.Description,
		IsBuiltin: r.IsBuiltin, Permissions: r.Permissions,
	}
}

// PermissionCodes 角色全部權限點（UNIQUE(role_id, permission_code)）。
func (r *RoleRepoImpl) PermissionCodes(ctx context.Context, roleID uint64) ([]string, error) {
	rows, err := data.Client(ctx, r.data).RolePermission.Query().
		Where(rolepermission.RoleID(roleID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	codes := make([]string, 0, len(rows))
	for _, row := range rows {
		codes = append(codes, row.PermissionCode)
	}
	return codes, nil
}

// Role 角色名。
func (r *RoleRepoImpl) Role(ctx context.Context, roleID uint64) (string, error) {
	row, err := data.Client(ctx, r.data).AdminRole.Query().
		Where(adminrole.ID(roleID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.Name, nil
}

// Roles 全部角色（含權限點集）。
func (r *RoleRepoImpl) Roles(ctx context.Context) ([]*RoleDetail, error) {
	rows, err := data.Client(ctx, r.data).AdminRole.Query().Order(ent.Asc(adminrole.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*RoleDetail, 0, len(rows))
	for _, row := range rows {
		d := &RoleDetail{ID: row.ID, Name: row.Name, Code: row.Code, Description: row.Description, IsBuiltin: row.IsBuiltin}
		if d.Permissions, err = r.PermissionCodes(ctx, row.ID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// RoleByID 角色詳情。
func (r *RoleRepoImpl) RoleByID(ctx context.Context, id uint64) (*RoleDetail, error) {
	row, err := data.Client(ctx, r.data).AdminRole.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrRoleNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &RoleDetail{ID: row.ID, Name: row.Name, Code: row.Code, Description: row.Description, IsBuiltin: row.IsBuiltin}
	if d.Permissions, err = r.PermissionCodes(ctx, row.ID); err != nil {
		return nil, err
	}
	return d, nil
}

// CreateRole 創建角色 + 權限點集。
func (r *RoleRepoImpl) CreateRole(ctx context.Context, name, code, desc string, perms []string) (*RoleDetail, error) {
	client := data.Client(ctx, r.data)
	role, err := client.AdminRole.Create().SetName(name).SetCode(code).SetDescription(desc).Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.replaceAll(ctx, role.ID, perms); err != nil {
		return nil, err
	}
	return r.RoleByID(ctx, role.ID)
}

// UpdateRole 修改名稱/描述（內置角色同樣可改名稱，code 不可改——接口不暴露 code 字段）。
func (r *RoleRepoImpl) UpdateRole(ctx context.Context, id uint64, name, desc string) (*RoleDetail, error) {
	q := data.Client(ctx, r.data).AdminRole.UpdateOneID(id)
	if name != "" {
		q.SetName(name)
	}
	if desc != "" {
		q.SetDescription(desc)
	}
	if _, err := q.Save(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrRoleNotFound
		}
		return nil, err
	}
	return r.RoleByID(ctx, id)
}

// DeleteRole 刪除（內置拒絕；有員工掛載拒絕；級聯刪權限點行）。
func (r *RoleRepoImpl) DeleteRole(ctx context.Context, id uint64) error {
	client := data.Client(ctx, r.data)
	role, err := client.AdminRole.Get(ctx, id)
	if ent.IsNotFound(err) {
		return ErrRoleNotFound
	}
	if err != nil {
		return err
	}
	if role.IsBuiltin {
		return ErrRoleBuiltIn
	}
	inUse, err := client.AdminUser.Query().Where(adminuser.RoleID(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if inUse {
		return ErrRoleInUse
	}
	if _, err := client.RolePermission.Delete().Where(rolepermission.RoleID(id)).Exec(ctx); err != nil {
		return err
	}
	return client.AdminRole.DeleteOneID(id).Exec(ctx)
}

// SetPermissions 全量替換權限點（事務內刪舊插新）。
func (r *RoleRepoImpl) SetPermissions(ctx context.Context, id uint64, perms []string) (*RoleDetail, error) {
	client := data.Client(ctx, r.data)
	if _, err := client.AdminRole.Get(ctx, id); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrRoleNotFound
		}
		return nil, err
	}
	if err := r.replaceAll(ctx, id, perms); err != nil {
		return nil, err
	}
	return r.RoleByID(ctx, id)
}

func (r *RoleRepoImpl) replaceAll(ctx context.Context, roleID uint64, perms []string) error {
	client := data.Client(ctx, r.data)
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		if _, err := client.RolePermission.Delete().Where(rolepermission.RoleID(roleID)).Exec(ctx); err != nil {
			return err
		}
		bulk := make([]*ent.RolePermissionCreate, 0, len(perms))
		for _, p := range perms {
			bulk = append(bulk, client.RolePermission.Create().SetRoleID(roleID).SetPermissionCode(p))
		}
		if len(bulk) > 0 {
			return client.RolePermission.CreateBulk(bulk...).Exec(ctx)
		}
		return nil
	})
}

// EnsureBuiltinRoles 落內置角色種子（冪等； 起 operator/support 覆蓋 authz 只讀
// 與員工只讀兩個基礎權限點，rbac_coverage_test 據此斷言目錄覆蓋）。
//
// 種子語義（2026-08-18 修訂）：
// - 僅在角色**新建**時寫入權限點——已有角色（含被後台改過權限的內置角色）不覆蓋，
// 後台的 UpdateRolePermissions 編輯因此跨重啓/重裝保留；
// - 例外：super_admin 的 * 通配是系統不變量，每次執行強制恢復（防誤操作鎖死超管）。
func EnsureBuiltinRoles(ctx context.Context, client *ent.Client) error {
	builtin := []struct{ code, name, desc string }{
		{code: "super_admin", name: "超級管理員", desc: "全部權限（* 通配）"},
		{code: "operator", name: "運營", desc: "商品/訂單/設置讀"},
		{code: "support", name: "客服", desc: "工單/訂單隻讀"},
	}
	operatorPerms := []string{
		"auth:profile", "auth:logout", "auth:totp",
		"settings:read", "settings:read_detail", "settings:update",
		"settings:currency_read",
		"authz:role_read", "authz:role_read_detail", "authz:tree",
		"identity:admin_read",
		"identity:user_read",
		"catalog:read", "catalog:read_detail", "catalog:category_read", "catalog:tag_read", "catalog:control_read",
		"catalog:review_read", "catalog:sku_read", "catalog:group_read",
		"inventory:read",
		"order:read", "order:read_detail",
		"payment:read", "payment:read_detail",
		"wallet:read", "wallet:withdraw",
		"giftcard:read", "license:read", "reconcile:read",
		"order:view_delivery",
		"memberlevel:read",
		"coupon:read",
		"lottery:read",
		"lottery:deliver",
		"dashboard:read",
		// ：貨源/採購/供貨讀權限（寫操作超管專屬）
		"supply:read",
		"procurement:read",
		"supplier:read",
		"content:read",
		"notify:read",
		"audit:read",
		"ticket:read",
		"ticket:write",
		"media:read",
		"media:upload",
		"reseller:read",
		"reseller:site",
		"reseller:product",
	}
	for _, b := range builtin {
		role, err := client.AdminRole.Query().Where(adminrole.Code(b.code)).Only(ctx)
		created := false
		if ent.IsNotFound(err) {
			role, err = client.AdminRole.Create().
				SetName(b.name).SetCode(b.code).SetDescription(b.desc).SetIsBuiltin(true).Save(ctx)
			created = true
		}
		if err != nil {
			return err
		}
		switch {
		case b.code == "super_admin":
			// 不變量：超管通配符每次強制（冪等替換為 *）
			if err := syncRolePerms(ctx, client, role.ID, []string{"*"}); err != nil {
				return err
			}
		case created:
			if err := syncRolePerms(ctx, client, role.ID, operatorPerms); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncRolePerms 種子權限同步（全量替換；運營角色權限目錄增長時隨發佈更新）。
func syncRolePerms(ctx context.Context, client *ent.Client, roleID uint64, perms []string) error {
	if _, err := client.RolePermission.Delete().Where(rolepermission.RoleID(roleID)).Exec(ctx); err != nil {
		return err
	}
	bulk := make([]*ent.RolePermissionCreate, 0, len(perms))
	for _, p := range perms {
		bulk = append(bulk, client.RolePermission.Create().SetRoleID(roleID).SetPermissionCode(p))
	}
	if len(bulk) > 0 {
		return client.RolePermission.CreateBulk(bulk...).Exec(ctx)
	}
	return nil
}
