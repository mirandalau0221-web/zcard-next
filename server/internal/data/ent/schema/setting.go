package schema

// 所有權：mods/settings（M0）

import (
	"encoding/json"
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Setting 運行時業務開關的真理源（鐵律 7：後台可改；env/config 只作首次部署兜底）。
type Setting struct {
	ent.Schema
}

func (Setting) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Setting) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		// group 為 SQL 保留字，Ent/Atlas 生成 DDL 時會引用標識符
		field.String("group").MaxLen(50).Comment("設置分組：site/template/trade/security/ops/notify..."),
		field.String("key").MaxLen(100),
		// value 為任意 JSON 文檔（對象/數組/標量），json.RawMessage 保持原樣存儲
		field.JSON("value", json.RawMessage{}).Comment("設置值（JSON 文檔）"),
	}
}

func (Setting) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group", "key").Unique(),
	}
}
