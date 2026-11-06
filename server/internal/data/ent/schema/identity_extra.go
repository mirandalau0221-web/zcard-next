package schema

// 所有權：mods/identity（P0-02）/ mods/audit（P2-06）
// 字段對齊《數據庫架構設計.md》§4/§5；業務功能按各任務書里程碑開發。

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Session 會話（access/refresh 雙令牌中的 refresh 載體：14d 輪換、一次性、可吊銷）。
type Session struct {
	ent.Schema
}

func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Enum("realm").Values("admin", "user").Comment("雙 realm：admin_users.id 或 users.id"),
		field.Uint64("user_id").Comment("按 realm 指向 admin_users/users"),
		field.String("refresh_token_hash").MaxLen(128).Comment("refresh 令牌哈希（SHA-256，明文絕不落庫）"),
		field.Int("auth_version").Default(0),
		field.String("device").MaxLen(120).Optional(),
		field.String("ip").MaxLen(64).Optional(),
		field.String("user_agent").MaxLen(255).Optional(),
		field.Time("expires_at").SchemaType(mysqlTime),
		field.Time("revoked_at").SchemaType(mysqlTime).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
		field.Time("updated_at").SchemaType(mysqlTime).Default(nowUTC).UpdateDefault(nowUTC),
	}
}

func (Session) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("refresh_token_hash").Unique(),
		index.Fields("realm", "user_id"),
		index.Fields("expires_at"),
	}
}

// UserGroup 1.x 用戶組兼容映射表：僅 migrate-from-v1 寫入、memberlevel 讀取，業務不寫。
type UserGroup struct {
	ent.Schema
}

func (UserGroup) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (UserGroup) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("code").MaxLen(60).Unique().Comment("1.x 組標識"),
		field.String("name").MaxLen(60),
		field.Uint64("level_id").Optional().Comment("映射 member_levels.id"),
	}
}

// ExternalIdentity 第三方登錄綁定（UNIQUE(provider, provider_user_id)；M3 接 OAuth）。
type ExternalIdentity struct {
	ent.Schema
}

func (ExternalIdentity) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ExternalIdentity) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		field.String("provider").MaxLen(32).Comment("telegram/google/..."),
		field.String("provider_user_id").MaxLen(128),
		field.String("username").MaxLen(120).Optional(),
		field.String("avatar_url").MaxLen(255).Optional(),
		field.Time("auth_at").SchemaType(mysqlTime).Optional(),
	}
}

func (ExternalIdentity) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "provider_user_id").Unique(),
		index.Fields("user_id"),
	}
}

// EmailVerification 郵箱/短信驗證碼（purpose 區分註冊/手機註冊/找回；attempt 上限防爆破。
// email 列複用為目標地址：郵箱地址或手機號——purpose 隔離通道）。
type EmailVerification struct {
	ent.Schema
}

func (EmailVerification) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("email").MaxLen(255).Comment("目標地址（郵箱或手機號；purpose 區分通道）"),
		field.Uint64("user_id").Optional(),
		field.Enum("purpose").Values("register", "phone_register", "reset", "order_access"),
		field.String("code_hash").MaxLen(128).Comment("驗證碼哈希"),
		field.Time("expires_at").SchemaType(mysqlTime),
		field.Time("verified_at").SchemaType(mysqlTime).Optional(),
		field.Int32("attempt_count").Default(0),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (EmailVerification) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("email", "purpose", "created_at"),
	}
}

// AuditLog 管理員操作審計（變更類 admin 操作：操作者/權限點/前後快照/IP）。
type AuditLog struct {
	ent.Schema
}

func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Enum("operator_type").Values("admin", "user", "system"),
		field.Uint64("operator_id").Optional(),
		field.String("permission_point").MaxLen(100).Optional(),
		field.String("action").MaxLen(10).Comment("POST/PUT/DELETE"),
		field.String("route").MaxLen(255),
		field.JSON("before", map[string]any{}).Optional().Comment("變更前快照"),
		field.JSON("after", map[string]any{}).Optional().Comment("變更後快照"),
		field.String("ip").MaxLen(64).Optional(),
		field.String("user_agent").MaxLen(255).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (AuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("operator_type", "operator_id", "created_at"),
		index.Fields("created_at"),
	}
}

// SecurityAuditLog 安全審計（登錄失敗/異地/敏感操作——解密/取貨/導出/權限變更/密鑰輪換）。
type SecurityAuditLog struct {
	ent.Schema
}

func (SecurityAuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Enum("actor_type").Values("admin", "user", "guest", "system"),
		field.Uint64("actor_id").Optional(),
		field.String("action").MaxLen(255).Comment("事件名（identity.login_failed / card.decrypt ...）"),
		field.String("ip").MaxLen(64).Optional(),
		field.JSON("metadata", map[string]any{}).Optional().Comment("關鍵 ID 等（明文卡密/憑據絕不入內）"),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (SecurityAuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("actor_type", "actor_id", "created_at"),
		index.Fields("action", "created_at"),
	}
}

// VisitLog 訪問統計（輕量聚合：按小時×路徑聚合行，不記明細）。
type VisitLog struct {
	ent.Schema
}

func (VisitLog) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (VisitLog) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("subsite_id").Default(0),
		field.String("stat_date").MaxLen(8).Comment("yyyymmdd"),
		field.Int8("stat_hour").Comment("0-23"),
		field.String("path").MaxLen(255).Comment("路徑或來源標識"),
		field.Int64("pv").Default(0),
		field.Int64("uv").Default(0),
	}
}

func (VisitLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "stat_date", "stat_hour", "path").Unique(),
	}
}

// RiskLockKey 風控鎖定（IP 維度哈希鍵 + TTL；下單 pending 閘門/取貨失敗鎖定）。
// 注：庫文檔寫 key_hash 為主鍵，實現取 uint64 自增主鍵 + key_hash 唯一索引
// （與全庫主鍵策略一致，唯一性語義等價）。
type RiskLockKey struct {
	ent.Schema
}

func (RiskLockKey) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("key_hash").MaxLen(128).Comment("鎖定鍵哈希（IP/訂單維度，命名空間前綴）"),
		field.Int("failure_count").Default(5).Comment("連續失敗次數；默認5保留升級前已有鎖定"),
		field.Time("expires_at").SchemaType(mysqlTime).Comment("TTL 過期自動失效"),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (RiskLockKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("key_hash").Unique(),
		index.Fields("expires_at"),
	}
}
