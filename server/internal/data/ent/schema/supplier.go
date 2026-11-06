package schema

// 所有權：mods/supplier（P2-03 對外供貨：本站作上游）
// 字段對齊《數據庫架構設計.md》§4.7/§5；HMAC 四頭協議見主文檔 §5.8。

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SupplierAccount 下游賬户（申請→審核→發 key；secret 只顯示一次）。
type SupplierAccount struct {
	ent.Schema
}

func (SupplierAccount) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (SupplierAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(100),
		field.String("api_key").MaxLen(64).Unique().Comment("下游識別 key（可索引明文）"),
		field.Bytes("api_secret").Comment("AES-256-GCM 加密 secret"),
		field.String("contact").MaxLen(255).Optional().Comment("聯繫方式（申請審核用）"),
		field.Enum("status").Values("applying", "approved", "rejected", "disabled").Default("applying"),
		field.Int64("balance_cache").Default(0).Comment("供貨餘額緩存（可由流水重算）"),
		field.String("notify_url").MaxLen(500).Optional().Comment("交付回調地址（HTTPS 強制）"),
		field.Time("reviewed_at").SchemaType(mysqlTime).Optional(),
		// P2-10 B/C：該賬號對外説話用的協議（決定兼容層憑據形態與回調簽名器）。
		// dujiao_next：api_key=Api-Key、secret=簽名密鑰；acg_faka：api_key=數字
		// app_id、secret=32 位 app_key（MD5 驗籤時解密參與）。
		field.Enum("protocol").Values("zcard", "dujiao_next", "acg_faka").Default("zcard"),
		field.String("display_name").MaxLen(100).Optional().Comment("connect/ping 回顯的店鋪名（shopName/site_name）"),
		// 前台對接申請（P3-xx）：owner=前台用户 id（admin 手動建號恆 0）；
		// apply_reason 申請理由、review_note 審核意見（駁回時必填）。
		field.Uint64("owner_user_id").Optional().Default(0).Comment("申請歸屬前台用户（0=admin 建號）"),
		field.String("apply_reason").MaxLen(500).Optional().Comment("申請理由（審核用）"),
		field.String("review_note").MaxLen(500).Optional().Comment("審核意見/駁回理由"),
		// IP 白名單（申請通過後可配置）：空數組 = 所有 IP 放行；非空 = 僅命中（精確 IP
		// 或 CIDR 網段）的請求可調供貨/兼容接口。鑑權層（三協議統一）強制。
		field.JSON("ip_whitelist", []string{}).Optional().Comment("IP 白名單（空=不限；支持精確 IP 與 CIDR）"),
	}
}

func (SupplierAccount) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("owner_user_id"),
	}
}

// SupplierLedgerEntry 供貨餘額賬本（冪等鍵 supply_order:<downID>；append-only）。
type SupplierLedgerEntry struct {
	ent.Schema
}

func (SupplierLedgerEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("account_id"),
		field.Uint64("supply_order_id").Optional(),
		field.String("type").MaxLen(40).Comment("recharge/supply_pay/supply_refund/adjust"),
		field.Int64("amount").Comment("有符號（分）"),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.String("reference").MaxLen(120).Unique().Comment("冪等鍵"),
		field.String("remark").MaxLen(255).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (SupplierLedgerEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "created_at"),
	}
}

// SupplyOrder 下游訂單（downstream_order_no 冪等；複用 inventory 鎖卡）。
type SupplyOrder struct {
	ent.Schema
}

func (SupplyOrder) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (SupplyOrder) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("account_id"),
		field.String("downstream_order_no").MaxLen(64).Comment("下游單號（賬户內冪等錨點）"),
		field.JSON("items", []map[string]any{}).Comment("商品行快照（上游商品/SKU/數量/單價）"),
		field.Int64("amount").Comment("應付（分，供貨價口徑）"),
		field.Enum("status").
			Values("pending", "paid", "fulfilling", "fulfilled", "rejected", "refunded").
			Default("pending"),
		field.Uint64("local_order_id").Optional().Comment("轉本地訂單（複用交付出口時回填）"),
		field.Time("paid_at").SchemaType(mysqlTime).Optional(),
		field.Time("fulfilled_at").SchemaType(mysqlTime).Optional(),
	}
}

func (SupplyOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "downstream_order_no").Unique(),
		index.Fields("account_id", "created_at"),
		index.Fields("status"),
	}
}

// SupplyNonce 供貨 API nonce 防重放（Redis 優先，DB 兜底；過期清理 cron）。
type SupplyNonce struct {
	ent.Schema
}

func (SupplyNonce) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("key").MaxLen(64).Comment("api_key（命名空間隔離）"),
		field.String("nonce").MaxLen(64),
		field.Time("expires_at").SchemaType(mysqlTime),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (SupplyNonce) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("key", "nonce").Unique(),
		index.Fields("expires_at"),
	}
}

// SupplierProductPrice 對外供貨定價（本站→下游差異化定價；區別於 supply_mappings 的上游→本地）。
type SupplierProductPrice struct {
	ent.Schema
}

func (SupplierProductPrice) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (SupplierProductPrice) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("supplier_account_id"),
		field.Uint64("product_id"),
		field.Uint64("sku_id").Default(0).Comment("0=商品級（哨兵，參與唯一索引）"),
		field.Int64("price").Comment("供貨價（分）"),
		field.String("scope").Default("product").Comment("product 固定價 / category 分類折扣 / global 整站折扣"),
		field.Uint64("category_id").Default(0),
		field.Int32("discount_bps").Default(0).Comment("折後比例，9000=9折"),
	}
}

func (SupplierProductPrice) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("supplier_account_id", "scope", "product_id", "sku_id", "category_id").Unique().StorageKey("supplier_price_scope_unique"),
	}
}

// DownstreamCallback 下游回調轉發重試（交付完成 → POST notify_url；死信可手動重發）。
type DownstreamCallback struct {
	ent.Schema
}

func (DownstreamCallback) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (DownstreamCallback) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("supply_order_id").Unique(),
		field.Uint64("account_id"),
		field.String("downstream_order_no").MaxLen(64),
		field.String("callback_url").MaxLen(500),
		field.String("trace_id").MaxLen(64).Optional(),
		field.Enum("callback_status").Values("pending", "success", "failed").Default("pending"),
		field.Int32("retry_count").Default(0),
		field.Time("last_callback_at").SchemaType(mysqlTime).Optional(),
		field.Text("last_error").Optional(),
	}
}

func (DownstreamCallback) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("callback_status", "retry_count"),
	}
}
