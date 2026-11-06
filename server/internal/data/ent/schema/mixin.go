// Package schema 為 Ent schema 唯一真理源（《數據庫架構設計.md》§0）。
// 每個文件頭部註釋標註模塊所有權（架構測試 §4.10-7 依據）。
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
)

// TimeMixin 通用時間字段（UTC 存儲，展示時區由 settings 決定）。
type TimeMixin struct {
	mixin.Schema
}

func (TimeMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").SchemaType(mysqlTime).
			Immutable().
			Default(nowUTC),
		field.Time("updated_at").SchemaType(mysqlTime).
			Default(nowUTC).
			UpdateDefault(nowUTC),
	}
}

// TenantMixin 租戶列（Row 行級隔離，開源版唯一啓用的隔離模式，ADR-D15）。
//
// 三種隔離模式下統一保留該列（Schema/Database 模式恆為 0），Ent schema 定義不因模式而變。
// M1 交付 Ent interceptor 讀寫自動注入（讀攔截器追加 subsite_id IN (ctx)、寫攔截器從父實體
// 繼承填充），業務代碼不手寫租戶條件（鐵律 14）。唯一索引必須含 subsite_id（§4.11.5）。
type TenantMixin struct {
	mixin.Schema
}

func (TenantMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("subsite_id").
			Default(0).
			Comment("租戶（0=主站；由 interceptor 自動注入，業務不手寫）"),
	}
}

// VersionMixin 樂觀鎖（併發行與行鎖雙保險，數據庫架構 §3 通用字段約定）。
type VersionMixin struct {
	mixin.Schema
}

func (VersionMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Int32("version").Default(0).Comment("樂觀鎖版本號"),
	}
}
