package schema

// 所有權：mods/lottery。免費抽獎與訂單/賬務分離；站點、賬號與請求唯一鍵防重複發獎。
import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type LotteryActivity struct{ ent.Schema }

func (LotteryActivity) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }
func (LotteryActivity) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"), field.String("name").MaxLen(100), field.Text("description").Default(""), field.String("image").MaxLen(2048).Default(""),
		field.String("status").MaxLen(20).Default("draft"), field.Time("start_at").SchemaType(mysqlTime), field.Time("end_at").SchemaType(mysqlTime),
		field.String("timezone").MaxLen(64).Default("Asia/Shanghai"), field.String("chance_mode").MaxLen(16).Default("once"), field.Int32("chance_count").Default(3),
		field.Int32("revision").Default(1), field.Bool("published").Default(false),
	}
}
func (LotteryActivity) Indexes() []ent.Index {
	return []ent.Index{index.Fields("subsite_id", "status")}
}

type LotteryPrize struct{ ent.Schema }

func (LotteryPrize) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }
func (LotteryPrize) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"), field.Uint64("activity_id"), field.String("name").MaxLen(100), field.String("image").MaxLen(2048).Default(""),
		field.String("mode").MaxLen(16), field.Uint64("product_id").Default(0), field.Uint64("sku_id").Default(0), field.Int32("probability").Default(0),
		field.Int32("quantity").Default(1), field.Int32("issued").Default(0), field.Int32("sort").Default(0), field.Bool("enabled").Default(true),
		field.Bytes("content").Optional().Comment("固定內容/領取説明密文，僅獲獎者或配置管理員讀取"),
	}
}
func (LotteryPrize) Indexes() []ent.Index {
	return []ent.Index{index.Fields("subsite_id", "activity_id", "enabled"), index.Fields("product_id", "sku_id")}
}

type LotteryAccount struct{ ent.Schema }

func (LotteryAccount) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }
func (LotteryAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"), field.Uint64("activity_id"), field.Uint64("user_id"), field.String("period").MaxLen(16), field.Int32("balance").Default(0), field.Bool("auto_granted").Default(false), field.Int32("version").Default(0),
	}
}
func (LotteryAccount) Indexes() []ent.Index {
	return []ent.Index{index.Fields("subsite_id", "activity_id", "user_id", "period").Unique()}
}

type LotteryChanceLog struct{ ent.Schema }

func (LotteryChanceLog) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }
func (LotteryChanceLog) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"), field.Uint64("activity_id"), field.Uint64("user_id"), field.String("period").MaxLen(16), field.Int32("amount"), field.String("kind").MaxLen(16), field.String("request_key").MaxLen(80), field.String("remark").MaxLen(500).Default(""), field.Uint64("admin_id").Default(0),
	}
}
func (LotteryChanceLog) Indexes() []ent.Index {
	return []ent.Index{index.Fields("subsite_id", "activity_id", "user_id", "request_key").Unique()}
}

type LotteryDraw struct{ ent.Schema }

func (LotteryDraw) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }
func (LotteryDraw) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"), field.Uint64("activity_id"), field.Uint64("user_id"), field.String("draw_no").MaxLen(40).Unique(), field.String("request_key").MaxLen(64),
		field.String("activity_name").MaxLen(100), field.String("prize_name").MaxLen(100).Default(""), field.String("mode").MaxLen(16).Default(""), field.String("status").MaxLen(20),
		field.Uint64("prize_id").Default(0), field.Uint64("product_id").Default(0), field.Uint64("sku_id").Default(0), field.Uint64("card_id").Optional().Nillable(),
		field.Int64("cost").Default(0), field.Int32("revision"), field.JSON("rule_snapshot", map[string]any{}).Optional(),
		field.Bytes("content").Optional().Comment("抽獎專用密文，AAD 綁定中獎編號和站點"), field.Time("delivered_at").SchemaType(mysqlTime).Optional(), field.Uint64("admin_id").Default(0), field.String("remark").MaxLen(500).Default(""),
	}
}
func (LotteryDraw) Indexes() []ent.Index {
	return []ent.Index{index.Fields("subsite_id", "activity_id", "user_id", "request_key").Unique(), index.Fields("card_id").Unique(), index.Fields("subsite_id", "user_id", "id"), index.Fields("subsite_id", "activity_id", "status"), index.Fields("product_id", "sku_id")}
}

type LotteryRevision struct{ ent.Schema }

func (LotteryRevision) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }
func (LotteryRevision) Fields() []ent.Field {
	return []ent.Field{field.Uint64("id"), field.Uint64("activity_id"), field.Int32("revision"), field.Uint64("admin_id"), field.JSON("snapshot", map[string]any{})}
}
func (LotteryRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("subsite_id", "activity_id", "revision").Unique()}
}
