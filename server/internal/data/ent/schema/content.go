package schema

// 所有權：mods/content（P2-04）/ mods/notify（P2-05）/ mods/dashboard（P3-07）
// 多語言字段用 JSON（*_json），按請求語言回落 zh_CN。

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Banner 橫幅/輪播（生效時間窗；link_type=ad 承載第三方廣告位）。
type Banner struct {
	ent.Schema
}

func (Banner) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (Banner) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(100),
		field.Enum("position").Values("top", "middle", "bottom").Default("top"),
		field.JSON("title_json", map[string]string{}).Optional().Comment("多語言標題"),
		field.String("image").MaxLen(255),
		field.String("mobile_image").MaxLen(255).Optional().Comment("缺省回落 PC 圖"),
		field.Enum("link_type").Values("product", "category", "url", "ad", "post", "notice").Default("url"),
		field.String("link_value").MaxLen(500).Optional(),
		field.Bool("is_active").Default(true),
		field.Time("start_at").SchemaType(mysqlTime).Optional(),
		field.Time("end_at").SchemaType(mysqlTime).Optional(),
		field.Int32("sort").Default(0),
	}
}

func (Banner) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "position", "is_active"),
	}
}

// PostCategory 文章分類。
type PostCategory struct {
	ent.Schema
}

func (PostCategory) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (PostCategory) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(60),
		field.String("slug").MaxLen(80).Unique(),
		field.Int32("sort").Default(0),
	}
}

// Post 文章/公告（type=notice 公告；content 經 sanitize）。
type Post struct {
	ent.Schema
}

func (Post) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (Post) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("slug").MaxLen(120),
		field.Enum("type").Values("blog", "notice").Default("blog"),
		field.JSON("title_json", map[string]string{}),
		field.JSON("summary_json", map[string]string{}).Optional(),
		field.Text("content_json").Comment("多語言內容（JSON，sanitize 後）"),
		field.String("thumbnail").MaxLen(255).Optional(),
		field.Uint64("category_id").Optional(),
		field.Bool("is_published").Default(false),
		field.Time("published_at").SchemaType(mysqlTime).Optional(),
		field.Int32("sort").Default(0).NonNegative(),
	}
}

func (Post) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "slug").Unique(),
		index.Fields("type", "is_published"),
	}
}

// Notification 站內信（用戶鈴鐺）。
type Notification struct {
	ent.Schema
}

func (Notification) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		field.String("title").MaxLen(200),
		field.Text("content"),
		field.Time("read_at").SchemaType(mysqlTime).Optional(),
		field.String("source_type").MaxLen(40).Optional().Comment("來源（order/ticket/broadcast）"),
		field.Uint64("source_id").Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (Notification) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "read_at", "created_at"),
	}
}

// NotifyTemplate 事件驅動模板（事件 × 通道 × 語言；佔位符白名單渲染）。
type NotifyTemplate struct {
	ent.Schema
}

func (NotifyTemplate) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (NotifyTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("event_type").MaxLen(64).Comment("order.paid / ticket.replied ..."),
		field.Enum("channel").Values("email", "sms", "inbox", "telegram"),
		field.String("locale").MaxLen(10).Default("zh_CN"),
		field.String("subject_tpl").MaxLen(255),
		field.Text("body_tpl"),
		field.Bool("enabled").Default(true),
	}
}

func (NotifyTemplate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_type", "channel", "locale").Unique(),
	}
}

// NotificationLog 發送日誌（status=skipped 為降級標記：通道未配置不報錯雪崩）。
type NotificationLog struct {
	ent.Schema
}

func (NotificationLog) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("delivery_key").MaxLen(128).Optional().Nillable().Unique(),
		field.Int("attempts").Default(0),
		field.Time("next_attempt_at").SchemaType(mysqlTime).Optional().Nillable(),
		field.Time("lease_until").SchemaType(mysqlTime).Optional().Nillable(),
		field.String("message_id").MaxLen(64).Optional(),
		field.String("event_type").MaxLen(64),
		field.String("biz_type").MaxLen(40).Optional(),
		field.Uint64("biz_id").Optional(),
		field.Enum("channel").Values("email", "sms", "inbox", "telegram"),
		field.String("recipient").MaxLen(255),
		field.String("locale").MaxLen(10).Default("zh_CN"),
		field.String("subject").MaxLen(255).Optional(),
		field.Text("body").Optional(),
		field.Enum("status").Values("pending", "sent", "failed", "skipped").Default("pending"),
		field.Text("error_message").Optional(),
		field.JSON("variables", map[string]any{}).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (NotificationLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "created_at"),
		index.Fields("event_type", "created_at"),
	}
}

// DailyStat 日結指標（報表只掃此表不掃大表；今日實時走 report 層）。
type DailyStat struct {
	ent.Schema
}

func (DailyStat) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("subsite_id").Default(0),
		field.String("stat_date").MaxLen(8).Comment("yyyymmdd"),
		field.String("metric").MaxLen(64).Comment("orders/amount/profit/pay_success_rate..."),
		field.String("dimension_key").MaxLen(128).Default("").Comment("維度（商品ID 等；空=總量）"),
		field.Int64("value").Default(0),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
		field.Time("updated_at").SchemaType(mysqlTime).Default(nowUTC).UpdateDefault(nowUTC),
	}
}

func (DailyStat) Indexes() []ent.Index {
	return []ent.Index{
		// 日結冪等（重跑覆蓋同維度行）
		index.Fields("subsite_id", "stat_date", "metric", "dimension_key").Unique(),
	}
}

// ReconciliationJob 對賬任務（時間窗內 procurement vs 上游訂單）。
type ReconciliationJob struct {
	ent.Schema
}

func (ReconciliationJob) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ReconciliationJob) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("connection_id"),
		field.String("type").MaxLen(40).Default("orders"),
		field.Enum("status").Values("pending", "processing", "done", "failed").Default("pending"),
		field.Time("time_range_start").SchemaType(mysqlTime),
		field.Time("time_range_end").SchemaType(mysqlTime),
		field.Int32("total_count").Default(0),
		field.Int32("matched_count").Default(0),
		field.Int32("mismatched_count").Default(0),
		field.JSON("result_json", map[string]any{}).Optional(),
	}
}

// ReconciliationItem 對賬明細（四態：matched/mismatched/local_only/upstream_only）。
type ReconciliationItem struct {
	ent.Schema
}

func (ReconciliationItem) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("job_id"),
		field.Uint64("procurement_order_id").Optional(),
		field.String("local_order_no").MaxLen(64).Optional(),
		field.String("upstream_order_no").MaxLen(80).Optional(),
		field.Enum("status").Values("matched", "mismatched", "local_only", "upstream_only"),
		field.JSON("diff_json", map[string]any{}).Optional().Comment("金額/狀態差異"),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (ReconciliationItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_id", "status"),
	}
}

// V1IDMap 1.x→2.0 遷移 ID 映射（migrate-from-v1 專用，業務不讀）。
type V1IDMap struct {
	ent.Schema
}

func (V1IDMap) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("table_name").MaxLen(64).Comment("1.x 表名（列名 table 為保留字，取 table_name）"),
		field.Uint64("old_id"),
		field.Uint64("new_id"),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (V1IDMap) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("table_name", "old_id").Unique(),
	}
}

// NotifyBroadcast 羣發任務（P2-05 T4：目標篩選/定時/取消/統計）。
type NotifyBroadcast struct {
	ent.Schema
}

func (NotifyBroadcast) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (NotifyBroadcast) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("title").MaxLen(200),
		field.Text("content").Comment("正文（羣發渠道適配：inbox 原文/email HTML）"),
		field.JSON("channels", []string{}).Comment("投放通道集合（inbox/email/sms/telegram）"),
		field.Enum("target_type").Values("all", "active", "specified").Default("all").Comment("目標篩選"),
		field.JSON("target_ids", []uint64{}).Optional().Comment("specified 時的用戶 ID 集合"),
		field.Time("scheduled_at").SchemaType(mysqlTime).Optional().Comment("定時發送（空=立即）"),
		field.Enum("status").Values("pending", "sending", "done", "canceled").Default("pending"),
		field.Uint64("created_by").Comment("創建管理員"),
		field.Int64("audience").Default(0).Comment("覆蓋人數（發送時預估回填）"),
		field.Int64("sent_count").Default(0),
		field.Int64("failed_count").Default(0),
		field.Time("started_at").SchemaType(mysqlTime).Optional(),
		field.Time("finished_at").SchemaType(mysqlTime).Optional(),
	}
}

func (NotifyBroadcast) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "scheduled_at"),
	}
}
