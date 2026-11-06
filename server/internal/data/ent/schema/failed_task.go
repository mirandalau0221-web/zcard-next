package schema

// 所有權：platform/queue（無 Redis 降級的死信記錄，規劃 §4.8 降級矩陣）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// FailedTask 無 Redis 降級模式的死信：SyncQueue 同步執行失敗落庫，
// 供手動重放（`zcard admin retry-task` M2 交付）與排障查詢。
type FailedTask struct {
	ent.Schema
}

func (FailedTask) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("task_type").MaxLen(128).Comment("任務類型（event:order.paid / card:import...）"),
		field.Bytes("payload").Optional().Comment("任務載荷"),
		field.Text("error").Optional().Comment("失敗原因"),
		field.Int32("retry_count").Default(0).Comment("已重試次數"),
		field.Enum("status").Values("pending", "done").Default("pending").Comment("pending=待重放；done=已重放"),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (FailedTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "created_at"),
		index.Fields("task_type"),
	}
}
