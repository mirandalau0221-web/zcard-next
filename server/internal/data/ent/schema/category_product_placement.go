package schema

// 所有權：mods/catalog。分類內運營配置與商品所屬分類、首頁推薦獨立。
import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type CategoryProductPlacement struct{ ent.Schema }

func (CategoryProductPlacement) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }
func (CategoryProductPlacement) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"), field.Uint64("category_id"), field.Uint64("product_id"),
		field.Bool("is_pinned").Default(false), field.Bool("is_recommended").Default(false), field.Int32("position").Default(0),
	}
}
func (CategoryProductPlacement) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "category_id", "product_id").Unique(), index.Fields("subsite_id", "product_id"),
	}
}
