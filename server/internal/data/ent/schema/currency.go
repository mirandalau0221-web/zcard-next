package schema

// 所有權：platform（多幣種展示，規劃 §5.1；記賬永遠走 platform/money.Cents）

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Currency 展示貨幣配置（1.x CurrencyService 表結構平移）。
// 記賬只認基礎貨幣（默認 CNY）；匯率僅用於展示換算與下單快照。
type Currency struct {
	ent.Schema
}

func (Currency) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Currency) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("code").MaxLen(3).Unique().Comment("ISO 4217 三字碼"),
		field.String("symbol").MaxLen(10).Comment("貨幣符號（¥/$）"),
		field.Enum("position").Values("prefix", "suffix").Default("prefix").Comment("符號位置"),
		field.Int32("precision").Default(2).Comment("小數位"),
		// 匯率不是金額（鐵律 1 只約束金額列），存儲用 decimal/numeric 精確類型
		field.Float("rate").
			Default(1).
			SchemaType(map[string]string{
				dialect.MySQL:    "decimal(20,8)",
				dialect.Postgres: "numeric(20,8)",
				dialect.SQLite:   "real",
			}).
			Comment("對基礎貨幣匯率（手動 + 可選定時拉取，M3 插件）"),
		field.Bool("enabled").Default(true),
		field.Int32("sort").Default(0),
	}
}

func (Currency) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
	}
}
