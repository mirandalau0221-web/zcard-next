package schema

// 所有權：mods/license（P3-08 在線購買：專業套餐訂閲購買單）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LicenseOrder 專業套餐購買單（發行側在線簽發記錄；錢包扣款同事務落賬）。
type LicenseOrder struct {
	ent.Schema
}

func (LicenseOrder) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (LicenseOrder) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Enum("plan").Values("monthly", "yearly").Comment("套餐檔位（3U/月、30U/年）"),
		field.Int64("amount").Comment("實付（分）"),
		field.Enum("status").Values("pending", "success", "canceled").Default("pending"),
		field.Uint64("payer_user_id").Comment("購買人（發行側站點用戶；錢包扣款對象）"),
		field.String("instance_id").MaxLen(64).Comment("目標實例 ID（許可證綁定；空=本實例）"),
		field.String("domain").MaxLen(255).Optional().Comment("目標域名（許可證綁定；空=不限）"),
		field.JSON("features", []string{}).Optional().Comment("授權特性快照"),
		field.Time("expires_at").SchemaType(mysqlTime).Comment("訂閲到期（許可證簽發口徑）"),
		field.Text("license_file").Optional().Comment("簽發的許可證文件（購買人下載後安裝）"),
		field.Time("paid_at").SchemaType(mysqlTime).Optional(),
	}
}

func (LicenseOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("payer_user_id"),
		index.Fields("instance_id"),
	}
}
