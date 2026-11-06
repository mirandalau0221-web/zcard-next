package schema

// 所有權：mods/catalog（M1；M0 先落表結構支撐 storefront 列表鏈路）

import (
	"context"
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"fmt"
)

// Product 商品主表（《數據庫架構設計.md》§4.1，字段精確對齊）。
type Product struct {
	ent.Schema
}

func (Product) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (Product) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("product_kind").Default("standard"),
		field.String("delivery_kind").Default("card"),
		field.JSON("sms_product", map[string]string{}).Optional(),
		field.JSON("social_schema", map[string]any{}).Optional(),
		field.Uint64("category_id").Optional().Comment("分類（軟外鍵，僅索引）"),
		field.Bool("category_protected").Default(false),
		field.String("name").MaxLen(1024),
		field.String("goods_type").Default("virtual"),
		field.String("product_property").Default("").MaxLen(16).Validate(func(v string) error {
			if v != "" && v != "virtual" && v != "physical" {
				return fmt.Errorf("商品屬性無效")
			}
			return nil
		}).Comment("商品屬性；goods_type 為舊客户端兼容鏡像"),
		field.Bool("track_inventory").Default(true).Comment("實體商品是否管理數量庫存；規格繼承"),
		field.Bool("sales_visible").Default(true).Comment("是否向買家顯示已售數量"),
		field.String("shipping_mode").Default("free"),
		field.Int64("shipping_fee").Default(0),
		field.JSON("shipping_countries", []string{}).Optional(),
		field.Int64("physical_stock").Default(0),
		field.String("slug").MaxLen(150).Comment("唯一標識"),
		field.Text("description").Optional().Comment("詳情（服務端 sanitize 後存儲）"),
		field.String("cover").MaxLen(255).Optional(),
		field.JSON("images", []string{}).Optional().Comment("詳情圖集"),
		field.Bool("cover_protected").Default(false),
		field.Bool("description_protected").Default(false),
		// 金額一律 int64 分（鐵律 1）
		field.Int64("price").Default(0).Comment("售價（分）"),
		field.Int64("factory_price").Default(0).Comment("成本價（分，上游/自營成本快照）"),
		field.Int64("draft_premium").Default(0).Comment("預選卡密默認加價（分）"),
		field.JSON("member_price", map[string]int64{}).Optional().Comment("按會員等級價 {level_id: 分}"),
		field.Int64("points_required").Default(0).Comment("積分兑換價（分單位積分；0=不參與積分商城，P3-01）"),
		field.Enum("stock_type").Values("card", "url", "code").Default("card").Comment("卡密/鏈接/兑換碼"),
		field.Bytes("direct_content").Optional().Comment("直髮內容密文（url/code 商品：網盤鏈接/兑換碼，AES-GCM AAD=product/subsite；同一內容發給每個買家"),
		field.Bool("stock_visible").Default(true).Comment("是否顯示庫存"),
		field.String("fulfillment_mode").Default("auto"),
		field.Int64("manual_stock").Default(-1).Comment("人工共享總額度（含已售及預佔），-1不限"),
		field.Enum("delivery_mode").Values("status", "delete").Default("status").Comment("發貨模式：標記/即刪"),
		field.JSON("control_config", map[string]any{}).Optional().Comment("自定義控件配置（結構化控件走 product_controls 表，M1）"),
		field.Bool("dedup").Default(true).Comment("導入去重開關"),
		field.Int32("sort").Default(0),
		field.Bool("is_recommend").Default(false).Comment("運營推薦（storefront 首頁推薦位 recommend_only 過濾；admin 商品表單可標）"),
		field.Int8("status").Default(1).Comment("1=上架 0=下架 2=隱藏（遊客不可見會員可見）"),
		field.Uint64("upstream_source_id").Optional().Comment("貨源連接（NULL=自營；M2）"),
		field.String("upstream_product_code").MaxLen(128).Optional().Comment("上游商品標識（M2）"),
		field.Time("upstream_synced_at").SchemaType(mysqlTime).Optional(),
		field.Bool("auto_listing").Default(false),
		field.String("listing_reason").Default("").MaxLen(40),
		field.Int8("listing_restore_status").Default(1),
		field.Int64("listing_changed_at").Default(0),
		field.Int64("listing_observed_at").Default(0),
		field.Int64("listing_zero_since").Default(0),
		field.Int32("listing_last_stock").Default(-2),
		field.Bool("listing_restocked").Default(false),
		field.String("listing_message").Default("").MaxRuneLen(200),
		field.Bool("is_locked").Default(false),
		field.Int64("lock_version").Default(0),
		field.Uint64("locked_by").Default(0),
		field.Time("locked_at").SchemaType(mysqlTime).Optional().Nillable(),
	}
}

// Keep legacy writers and the dedicated property column consistent at the storage boundary.
func (Product) Hooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			property, hasProperty := m.Field("product_property")
			legacy, hasLegacy := m.Field("goods_type")
			p, _ := property.(string)
			g, _ := legacy.(string)
			if p != "" && g != "" && p != g && !m.Op().Is(ent.OpCreate) {
				return nil, fmt.Errorf("商品屬性與舊類型不一致")
			}
			if p != "" {
				if err := m.SetField("goods_type", p); err != nil {
					return nil, err
				}
			} else if hasLegacy && g != "" {
				if err := m.SetField("product_property", g); err != nil {
					return nil, err
				}
			} else if m.Op().Is(ent.OpCreate) || hasProperty {
				if err := m.SetField("product_property", "virtual"); err != nil {
					return nil, err
				}
			}
			return next.Mutate(ctx, m)
		})
	}}
}

func (Product) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "slug").Unique(),
		index.Fields("subsite_id", "category_id"),
		index.Fields("subsite_id", "status"),
		index.Fields("upstream_source_id"),
		// 貨源同步冪等判據（UpsertUpstreamProduct 唯一索引兜底，防併發重複）
		index.Fields("subsite_id", "upstream_source_id", "upstream_product_code").Unique(),
	}
}

func (Product) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("skus", ProductSku.Type).Comment("多規格"),
		edge.To("cards", Card.Type).Comment("庫存卡密（硬 FK）"),
	}
}

// ProductSku 多規格（《數據庫架構設計.md》§4.1）。
type ProductSku struct {
	ent.Schema
}

func (ProductSku) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (ProductSku) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("product_id").Comment("所屬商品（硬外鍵 → products）"),
		field.Int64("physical_stock").Default(0),
		field.String("name").MaxLen(100).Comment("規格名（如「月卡」）"),
		field.JSON("spec_values", map[string]string{}).Comment("規格值組合 {規格: 值}"),
		field.Int64("price").Optional().Comment("獨立售價（分，NULL=繼承商品價）"),
		field.Int64("cost").Optional().Comment("獨立成本（分）"),
		field.String("fulfillment_mode").Default("follow"),
		field.Int32("stock_offset").Default(0).Comment("獨立庫存位"),
		field.String("upstream_sku_id").MaxLen(64).Optional().Comment("上游 SKU 標識（M2）"),
	}
}

func (ProductSku) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("product_id"),
		index.Fields("product_id", "name").Unique(),
	}
}

func (ProductSku) Edges() []ent.Edge {
	return []ent.Edge{
		// 硬外鍵 → products（核心聚合內）
		edge.From("product", Product.Type).
			Ref("skus").
			Field("product_id").
			Required().
			Unique(),
	}
}
