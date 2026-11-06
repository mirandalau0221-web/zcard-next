package schema

// 所有權：mods/catalog（P1-01）/ mods/inventory（P1-02）/ mods/order（P1-03 購物車）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Category 分類樹（parent_id 樹形；分站可見性白名單）。
type Category struct {
	ent.Schema
}

func (Category) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (Category) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("parent_id").Optional().Comment("父分類（NULL=根）"),
		field.String("name").MaxRuneLen(100),
		field.String("icon").MaxLen(255).Optional(),
		field.Bool("hide").Default(false),
		field.Int32("sort").Default(0),
		field.JSON("visible_subsites", []uint64{}).Optional().Comment("分站可見性白名單（空=全部可見）"),
		field.Int64("placement_version").Default(0),
	}
}

func (Category) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "parent_id"),
	}
}

// Tag 商品標籤（position：右上方/標題前/右下方）。
type Tag struct {
	ent.Schema
}

func (Tag) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Tag) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(60),
		field.String("slug").MaxLen(80).Unique(),
		field.String("icon").MaxLen(255).Optional(),
		field.String("color").MaxLen(20).Optional(),
		field.Enum("position").Values("top_right", "before_title", "bottom_right").Default("top_right"),
		field.Bool("hide").Default(false),
	}
}

// ProductControl 自定義控件（下單收集：文本/密碼/下拉/數字/多選/單選）。
type ProductControl struct {
	ent.Schema
}

func (ProductControl) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (ProductControl) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("product_id"),
		field.String("name").MaxLen(60),
		field.Enum("type").Values("text", "password", "select", "number", "checkbox", "radio"),
		field.String("placeholder").Default(""),
		field.String("validation").Default("text"),
		field.Int32("max_length").Default(500),
		field.Bool("required").Default(false),
		field.JSON("options", []string{}).Optional().Comment("選項（select/checkbox/radio）"),
		field.Int32("sort").Default(0),
	}
}

func (ProductControl) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("product_id"),
	}
}

// MemberProductGroup 會員商品組（組名/商品集/折扣/疊加規則）。
type MemberProductGroup struct {
	ent.Schema
}

func (MemberProductGroup) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (MemberProductGroup) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(60),
		field.JSON("product_ids", []uint64{}),
		field.Int32("discount").Default(0).Comment("折扣（萬分比）"),
		field.Bool("stack_member").Default(false).Comment("與會員等級折扣疊加"),
		field.Bool("stack_coupon").Default(false).Comment("與優惠券疊加"),
		field.String("badge_style").MaxLen(60).Optional().Comment("標籤樣式"),
	}
}

// Review 真實評價（已付訂單一單一評；審核流）。
type Review struct {
	ent.Schema
}

func (Review) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (Review) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("product_id"),
		field.Uint64("user_id"),
		field.Uint64("order_id"),
		field.Int8("rating").Comment("1-5"),
		field.Text("content").Comment("入庫前 sanitize"),
		field.Enum("status").Values("pending", "approved", "rejected").Default("pending"),
	}
}

func (Review) Indexes() []ent.Index {
	return []ent.Index{
		// 一單一評
		index.Fields("order_id").Unique(),
		index.Fields("product_id", "status"),
	}
}

// VirtualReview 虛擬評論（與真實評價合併展示）。
type VirtualReview struct {
	ent.Schema
}

func (VirtualReview) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("product_id"),
		field.String("nickname").MaxLen(60),
		field.Text("content"),
		field.Int8("rating").Default(5),
		field.Int32("sort").Default(0),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (VirtualReview) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("product_id"),
	}
}

// CardImport 導入批次（模板/預覽/分片/撤銷；P1-02 任務書 T2）。
type CardImport struct {
	ent.Schema
}

func (CardImport) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (CardImport) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("product_id"),
		field.String("filename").MaxLen(255),
		field.Uint64("sku_id").Optional().Nillable().Immutable().Comment("導入目標規格快照：NULL=歷史未記錄，0=商品默認池"),
		field.String("sku_name").MaxLen(100).Default("").Immutable().Comment("導入時規格名稱"),
		field.Int32("total").Default(0).Comment("文件總行數"),
		field.Int32("imported").Default(0).Comment("已導入（進度回填）"),
		field.Int32("skipped").Default(0).Comment("重複跳過"),
		field.Int32("failed").Default(0),
		field.Enum("status").Values("pending", "processing", "done", "failed", "canceled").Default("pending"),
		field.Uint64("operator_id").Optional(),
	}
}

func (CardImport) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("product_id", "created_at"),
	}
}

// CartItem 購物車（UNIQUE(user_id, product_id, sku_id)——sku_id 用 0 哨兵參與唯一索引，
// 規避三方言「唯一索引含 NULL 列可重複」語義差異）。
type CartItem struct {
	ent.Schema
}

func (CartItem) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (CartItem) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		field.Uint64("product_id"),
		field.Uint64("sku_id").Default(0).Comment("0=商品級（哨兵，參與唯一索引）"),
		field.Int32("quantity").Default(1),
		field.Enum("fulfillment_type").Values("auto", "manual", "upstream").Default("auto"),
	}
}

func (CartItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "product_id", "sku_id").Unique(),
	}
}
