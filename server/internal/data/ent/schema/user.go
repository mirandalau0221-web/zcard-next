package schema

// 所有權：mods/identity（M0）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// User 顧客賬户（全局表，無 subsite_id：分站共享用户體系，規劃 §4.9）。
type User struct {
	ent.Schema
}

func (User) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("manual_level_id").Default(0),
		field.Uint64("referral_level_id").Default(0).Comment("註冊時獲贈等級，獨立於邀請人的當前配置"),
		field.Uint64("invite_level_id").Default(0).Comment("邀請新客户註冊贈送的等級，0為關閉"),
		field.String("username").MaxLen(60).Unique(),
		field.String("email").MaxLen(255).Unique().Optional().Comment("第三方登錄用户可無郵箱"),
		// 手機號（登錄標識之一；手機註冊通道必填——security.register_method=phone）
		field.String("phone").MaxLen(20).Unique().Optional(),
		// bcrypt；與查詢密碼（orders.query_password_hash）相互獨立
		field.String("password_hash").MaxLen(255).Optional().Comment("第三方登錄用户可無密碼"),
		field.Enum("status").Values("active", "banned", "deleted").Default("active"),
		field.Time("last_login_at").SchemaType(mysqlTime).Optional(),
		// 三級分銷歸因鏈快照（註冊/下單時綁定上級，供 affiliate 結算）
		field.Uint64("invite_l1").Optional(),
		field.Uint64("invite_l2").Optional(),
		field.Uint64("invite_l3").Optional(),
		// 推廣碼（8 位隨機，字母表剔除 I/O/0/1 防混淆；註冊自動生成——
		// 替代裸 user_id 做邀請標識，防枚舉；存量用户懶生成）
		field.String("promo_code").MaxLen(16).Unique().Optional(),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("invite_l1"),
		index.Fields("referral_level_id"),
		index.Fields("invite_level_id"),
	}
}
