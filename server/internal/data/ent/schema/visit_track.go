package schema

// 所有權：mods/audit（T5 訪問統計——PV/UV 明細 + 在線會話心跳；
// visit_logs 為按小時聚合表，page_views 為逐請求明細，二者並存互補）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PageView 訪問明細（每請求一行：PV 計數 + UV 按 ip 去重）。
// 保留 90 天（cron audit.visit_cleanup 清理）；埋點失敗不阻斷業務請求。
type PageView struct {
	ent.Schema
}

func (PageView) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}, TenantMixin{}}
}

func (PageView) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("day").MaxLen(8).Comment("統計日 yyyymmdd（UTC）"),
		field.String("path").MaxLen(255).Comment("請求路徑"),
		field.Uint64("user_id").Optional().Comment("登錄用戶（遊客為 0）"),
		field.String("ip").MaxLen(64).Comment("客戶端 IP"),
	}
}

func (PageView) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("day", "ip"),
		index.Fields("day", "subsite_id"),
	}
}

// UserSession 在線會話心跳（每登錄用戶一行，活躍即 upsert last_active_at）。
// 在線口徑 = last_active_at 距今 ≤ 5 分鐘；清理閾值 24 小時（cron）。
type UserSession struct {
	ent.Schema
}

func (UserSession) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}, TenantMixin{}}
}

func (UserSession) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		field.String("ip").MaxLen(64).Comment("最近活躍 IP"),
		field.Time("last_active_at").SchemaType(mysqlTime).Comment("最近活躍時間（在線判定基準）"),
	}
}

func (UserSession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "user_id").Unique(),
		index.Fields("last_active_at"),
	}
}
