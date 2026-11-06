package schema

// 所有權：mods/authz（M0：自建 RBAC，權限目錄自動生成 + 內置角色種子）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AdminRole 角色/權限組。權限目錄由啓動時從 Kratos 路由表自動生成（§5.14），
// 不建獨立權限表；role_permissions 存權限點編碼。
type AdminRole struct {
	ent.Schema
}

func (AdminRole) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (AdminRole) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(60).Unique(),
		field.String("code").MaxLen(60).Unique().Comment("super_admin/operator/support/..."),
		field.String("description").MaxLen(255).Optional(),
		// 內置角色種子由代碼維護，後台不可刪除
		field.Bool("is_builtin").Default(false),
	}
}

// RolePermission 角色-權限點（多對多）。權限點如 order:list / card:view_content / order:refund。
// 敏感權限點獨立拆分（防內部偷卡 §5.20.4），rbac_coverage_test 保證每條管理路由被角色覆蓋（§4.10-8）。
type RolePermission struct {
	ent.Schema
}

func (RolePermission) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (RolePermission) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		// 硬外鍵 → admin_roles（聚合內）
		field.Uint64("role_id"),
		field.String("permission_code").MaxLen(100),
	}
}

func (RolePermission) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("role_id", "permission_code").Unique(),
	}
}
