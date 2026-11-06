package schema

// 所有權：mods/inventory（M1；表結構 M0 落地——鐵律 11：卡密永不落明文）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Card 卡密/鏈接/兌換碼（最高頻、最可能破千萬的表，《數據庫架構設計.md》§4.2）。
//
// 安全要點（鐵律 11/§5.20.2）：
//   - content 強制 AES-256-GCM（隨機 nonce + AAD 綁定 product_id/tenant），無關閉開關；
//   - content_hash = HMAC-SHA256(cardKey, plain) keyed hash，防低熵卡彩虹表反推；
//   - 售出後按 delivery_mode 走「標記/即刪」，即刪模式物理刪除（不留密文殘骸）；
//   - 樂觀鎖 version 與「FOR UPDATE 鎖卡 + affected rows 校驗」雙保險（SQLite 走 BEGIN IMMEDIATE + CAS）。
type Card struct {
	ent.Schema
}

func (Card) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}, TenantMixin{}, VersionMixin{}}
}

func (Card) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("product_id").Comment("所屬商品（硬外鍵 → products）"),
		field.Bytes("content").Comment("AES-256-GCM 密文（AAD 綁定 product_id/subsite_id）"),
		field.String("content_hash").MaxLen(64).Comment("keyed hash = HMAC-SHA256(cardKey, plain)，商品內去重"),
		field.String("number_hash").MaxLen(64).Optional().Comment("靚號/卡號歸一化 hash（靚號自選檢索）"),
		field.Enum("status").Values("available", "reserved", "used", "disabled").Default("available"),
		// 軟外鍵：鎖定/售出訂單反查（有意不加 FK，防 cards↔orders 環）
		field.Uint64("order_id").Optional().Comment("鎖定/售出訂單（軟外鍵，僅索引）"),
		field.Uint64("sku_id").Optional().Comment("所屬 SKU（可空=商品級）"),
		field.Uint64("import_id").Optional().Comment("導入批次"),
		field.String("card_type").MaxLen(20).Optional().Comment("卡密類型（月卡/周卡，對應 SKU）"),
		field.String("note").MaxLen(255).Optional(),
		field.Uint64("owner_id").Default(0).Comment("所屬會員（0=系統；會員預選/鎖定靚號）"),
		field.Int64("draft_premium").Default(0).Comment("預選加價（分）"),
		field.Int64("draft_cost").Default(0).Comment("預選成本（分）"),
		field.Int64("price").Optional().Comment("靚號自選價（分，NULL=商品價）"),
		field.Time("locked_at").SchemaType(mysqlTime).Optional().Comment("鎖定時間（TTL 釋放）"),
		field.Time("used_at").SchemaType(mysqlTime).Optional().Comment("售出時間"),
	}
}

func (Card) Indexes() []ent.Index {
	return []ent.Index{
		// 唯一索引必須含 subsite_id（§4.11.5），避免分站間誤判重複
		index.Fields("subsite_id", "product_id", "content_hash").Unique(),
		// 發貨熱路徑：SELECT ... WHERE product_id=? AND status='available' FOR UPDATE
		index.Fields("product_id", "status"),
		index.Fields("subsite_id", "status"),
		index.Fields("order_id"),
		index.Fields("import_id"),
	}
}

func (Card) Edges() []ent.Edge {
	return []ent.Edge{
		// 硬外鍵 → products（核心聚合內，數據庫架構 §6 關係規則 1）
		edge.From("product", Product.Type).
			Ref("cards").
			Field("product_id").
			Required().
			Unique(),
	}
}
