package schema

// 所有權：mods/identity + mods/authz（M0）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AdminUser 員工賬户（§5.14：頭像/暱稱/賬號/密碼/權限組/備註/啓用/TOTP/最近登錄）。
// 分站主登錄分站後台 = AdminUser + subsite 隔離（M3），與顧客 User 嚴格分離。
type AdminUser struct {
	ent.Schema
}

func (AdminUser) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (AdminUser) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("username").MaxLen(60).Unique(),
		field.String("password_hash").MaxLen(255).Comment("bcrypt"),
		field.String("nickname").MaxLen(60).Optional(),
		field.String("avatar").MaxLen(255).Optional(),
		// 軟外鍵 → admin_roles（僅索引；員工單角色，§5.14）
		field.Uint64("role_id").Comment("所屬角色"),
		// TOTP 密鑰（AES-GCM 加密存儲，鐵律：憑據加密；明文永不落庫）
		field.Bytes("totp_secret").Optional().Comment("AES-256-GCM 加密的 TOTP 密鑰"),
		field.Int("auth_version").Default(0),
		field.Int("mfa_revision").Default(0),
		field.String("mfa_state").MaxLen(4096).Default("{}"),
		field.Bool("enabled").Default(true),
		field.String("remark").MaxLen(255).Optional(),
		field.String("last_login_ip").MaxLen(64).Optional(),
		field.Time("last_login_at").SchemaType(mysqlTime).Optional(),
	}
}

func (AdminUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("role_id"),
	}
}
