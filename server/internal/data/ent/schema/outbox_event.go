package schema

// 所有權：platform/events（事務性 Outbox，規劃 §4.8；M1 交付 relay）

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OutboxEvent 事務性 Outbox 事件：業務事務內寫入，relay（500ms 掃描）投遞 asynq 或進程內分發。
// dedupe_key 唯一索引天然防重複發佈（如訂單重複回調只發一次 order.paid）。
type OutboxEvent struct {
	ent.Schema
}

func (OutboxEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("module").MaxLen(32).Comment("發佈模塊（order/payment/...）"),
		field.String("type").MaxLen(64).Comment("事件類型（order.paid 等，目錄見附錄 C）"),
		field.String("aggregate_id").MaxLen(64).Comment("聚合根 ID（訂單號/支付單號）"),
		field.JSON("payload", json.RawMessage{}).Optional().Comment("事件載荷（proto schema，只加字段不改語義）"),
		field.String("dedupe_key").MaxLen(120).Unique().Comment("防重複發佈冪等鍵（order:123:paid）"),
		field.Enum("status").Values("publishing", "published", "failed").Default("publishing"),
		field.Time("published_at").SchemaType(mysqlTime).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (OutboxEvent) Indexes() []ent.Index {
	return []ent.Index{
		// relay 掃描熱路徑
		index.Fields("status", "created_at"),
	}
}

// ProcessedEvent 消費冪等：消費成功即記錄，重複投遞直接 ACK（UNIQUE(event_id, consumer)）。
type ProcessedEvent struct {
	ent.Schema
}

func (ProcessedEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("event_id").Comment("outbox_events.id"),
		field.String("consumer").MaxLen(64).Comment("消費者標識（模塊.處理器名）"),
		field.Time("processed_at").SchemaType(mysqlTime).Default(nowUTC),
	}
}

func (ProcessedEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "consumer").Unique(),
	}
}
