package schema

// 所有權：mods/fulfillment（M1；表結構 M0 落地——2.0 關鍵改造：無明文快照）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OrderDelivery 交付記錄（《數據庫架構設計.md》§4.3）。
//
// 1.x 痛點反轉：不再存明文卡密快照，改為「卡密引用 + 一次性取貨令牌哈希」；
// 取貨時現場解密返回、不落庫明文（§5.20.2）；取貨響應 Cache-Control: no-store。
type OrderDelivery struct {
	ent.Schema
}

func (OrderDelivery) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (OrderDelivery) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		// 硬外鍵 → orders
		field.Uint64("order_id"),
		field.Uint64("item_id").Comment("訂單子項"),
		field.Uint64("card_id").Comment("卡密引用（現場解密，絕不存明文）"),
		field.String("delivery_token_hash").MaxLen(64).Comment("一次性取貨令牌哈希"),
		field.Bytes("service_content").Optional(),
		field.Int32("delivered_quantity").Default(0),
		field.Enum("delivered_mode").Values("status", "delete", "direct").Comment("標記/即刪/直發（direct = 商品級直發內容，無卡密）"),
		field.Uint64("delivered_by").Default(0).Comment("人工發貨管理員（auto 為 0）"),
		field.JSON("logistics", map[string]any{}).Optional().Comment("結構化交付信息（物流單號/追蹤鏈接）"),
		field.Int32("fetch_count").Default(0).Comment("已取貨次數（默認 1 次後掩碼）"),
		field.Time("delivered_at").SchemaType(mysqlTime).Optional(),
		field.String("fetched_ip").MaxLen(64).Optional().Comment("取貨 IP（審計，不含明文）"),
	}
}

func (OrderDelivery) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
		index.Fields("card_id"),
	}
}

func (OrderDelivery) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("order", Order.Type).
			Ref("deliveries").
			Field("order_id").
			Required().
			Unique(),
	}
}
