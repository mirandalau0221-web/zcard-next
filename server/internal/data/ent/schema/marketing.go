package schema

// 所有權：mods/memberlevel（P3-01）/ mods/coupon（P3-02）/ mods/affiliate（P3-03）
//          / mods/reseller（P3-04）/ mods/ticket（P3-05）/ mods/media（P3-06）

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// MemberLevel 會員等級（升級條件充值/消費雙條件 AND|OR；只升不降可配）。
type MemberLevel struct {
	ent.Schema
}

func (MemberLevel) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (MemberLevel) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(60),
		field.String("logo").MaxLen(255).Optional(),
		field.String("badge_color").MaxLen(20).Optional(),
		field.Enum("threshold_type").Values("recharge", "consume", "both_and", "both_or").Default("recharge"),
		field.Int64("threshold_recharge").Default(0).Comment("累計充值閾值（分；countAsRecharge 口徑防刷）"),
		field.Int64("threshold_consume").Default(0).Comment("累計消費閾值（分）"),
		field.String("acquire_mode").Default("auto"),
		field.String("display_mode").Default("public"),
		field.Int32("discount").Default(0).Comment("等級折扣（萬分比）"),
		field.JSON("points_rule", map[string]any{}).Optional().Comment("積分產生規則（消費 X 元產 Y 分）"),
		field.Int32("sort").Default(0),
		field.Bool("enabled").Default(true),
	}
}

// Coupon 優惠券（一行一碼：批量生成 N 行同前綴唯一碼；1.x 模式）。
type Coupon struct {
	ent.Schema
}

func (Coupon) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Coupon) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("batch_id").MaxLen(40).Default("").Comment("批次標識（按批作廢）"),
		field.String("name").MaxLen(100),
		field.Enum("type").Values("fixed", "percent"),
		field.Int64("value").Comment("fixed=分；percent=萬分比"),
		field.String("code").MaxLen(40).Unique(),
		field.JSON("scope", map[string]any{}).Optional().Comment("全場/商品IDs/分類IDs/等級IDs"),
		field.Time("expire_at").SchemaType(mysqlTime).Optional(),
		field.Int32("per_user_limit").Default(1).Comment("單碼次數（多次碼放開）"),
		field.Enum("status").Values("unused", "used", "disabled").Default("unused"),
		field.Uint64("user_id").Optional().Comment("領取人（兌換碼/後台贈送回填）"),
		field.Time("used_at").SchemaType(mysqlTime).Optional(),
		field.Uint64("used_order_id").Optional(),
	}
}

func (Coupon) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id"),
		index.Fields("user_id", "status"),
	}
}

// FlashSale 限時秒殺（限量與卡密同一把鎖防超賣；窗口判定無狀態）。
type FlashSale struct {
	ent.Schema
}

func (FlashSale) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (FlashSale) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("product_id"),
		field.Uint64("sku_id").Default(0).Comment("0=商品級（哨兵）"),
		field.Int64("flash_price").Comment("秒殺價（分）"),
		field.Time("start_at").SchemaType(mysqlTime),
		field.Time("end_at").SchemaType(mysqlTime),
		field.Int32("limit_qty").Comment("總量（與庫存同鎖釦減）"),
		field.Int32("sold_qty").Default(0).Comment("支付成功後正式扣減"),
		field.Int32("reserved_qty").Default(0).Comment("待付款預佔，取消或超時釋放"),
		field.Int32("per_user_limit").Default(1),
	}
}

func (FlashSale) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("product_id", "sku_id"),
		index.Fields("end_at"),
	}
}

// Promotion 通用促銷（門檻滿減/折扣/會員特價；範圍關聯商品/分類）。
type Promotion struct {
	ent.Schema
}

func (Promotion) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (Promotion) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(100),
		field.JSON("scope", map[string]any{}),
		field.Enum("type").Values("fixed", "percent", "special_price"),
		field.Int64("threshold").Default(0).Comment("滿 X（分）"),
		field.Int64("discount").Default(0).Comment("fixed=分；percent=萬分比"),
		field.Int64("special_price").Default(0).Comment("special_price=分"),
		field.Time("start_at").SchemaType(mysqlTime),
		field.Time("end_at").SchemaType(mysqlTime),
		field.Bool("enabled").Default(true),
	}
}

// AffiliateCommission 三級佣金（UNIQUE(order_id, tier) 冪等；凍結在自身狀態機，不佔 wallet.locked）。
type AffiliateCommission struct {
	ent.Schema
}

func (AffiliateCommission) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (AffiliateCommission) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("order_id"),
		field.Uint64("buyer_id"),
		field.Uint64("referrer_id").Comment("收傭人"),
		field.Int8("tier").Comment("層級 1/2/3"),
		field.Float("rate").
			SchemaType(map[string]string{dialect.MySQL: "decimal(20,8)", dialect.Postgres: "numeric(20,8)", dialect.SQLite: "real"}).
			Comment("費率快照（萬分比）"),
		field.Int64("base_amount").Comment("計算基數（分：訂單金額或毛利，settings 口徑）"),
		field.Int64("amount").Comment("佣金（分）"),
		field.Enum("status").Values("pending_confirm", "available", "withdrawn", "reversed").Default("pending_confirm"),
		field.Time("available_at").SchemaType(mysqlTime).Optional().Comment("凍結到期"),
	}
}

func (AffiliateCommission) Indexes() []ent.Index {
	return []ent.Index{
		// 冪等防重發傭
		index.Fields("order_id", "tier").Unique(),
		index.Fields("referrer_id", "status"),
		index.Fields("available_at"),
	}
}

// ResellerProfile 分站站長資格（申請/審核；加價率上下限管控；等級權限位）。
type ResellerProfile struct {
	ent.Schema
}

func (ResellerProfile) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ResellerProfile) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id").Unique(),
		field.Enum("status").Values("applying", "approved", "rejected").Default("applying"),
		field.Text("apply_reason").Optional(),
		field.String("reject_reason").MaxLen(255).Optional(),
		field.Int8("level").Default(1).Comment("分站等級（域名/供貨/自助上架權限位）"),
		field.Float("default_markup_percent").
			SchemaType(map[string]string{dialect.MySQL: "decimal(20,8)", dialect.Postgres: "numeric(20,8)", dialect.SQLite: "real"}).
			Default(0).Comment("默認加價率"),
		field.Float("max_markup_percent").
			SchemaType(map[string]string{dialect.MySQL: "decimal(20,8)", dialect.Postgres: "numeric(20,8)", dialect.SQLite: "real"}).
			Default(100).Comment("加價率上限（管控）"),
		field.Int32("confirm_days").Default(7).Comment("利潤凍結天數（庫文檔 settlement_status 的落地語義）"),
		field.Uint64("reviewed_by").Optional(),
		field.Time("reviewed_at").SchemaType(mysqlTime).Optional(),
	}
}

// ResellerSite 分站域名 + 白標（DNS TXT / HTTP well-known 雙驗證，釘死公網 IP 防 rebinding）。
type ResellerSite struct {
	ent.Schema
}

func (ResellerSite) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ResellerSite) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("profile_id"),
		field.String("domain").MaxLen(255).Unique(),
		field.Enum("type").Values("main", "custom").Default("custom"),
		field.String("verification_token").MaxLen(64),
		field.Enum("verification_status").Values("pending", "verified", "failed").Default("pending"),
		field.Bool("is_primary").Default(false),
		field.String("site_name").MaxLen(100).Optional(),
		field.String("logo").MaxLen(255).Optional(),
		field.String("favicon").MaxLen(255).Optional(),
		field.JSON("announcement_json", map[string]any{}).Optional(),
		field.JSON("support_json", map[string]any{}).Optional().Comment("客服配置"),
		field.Enum("status").Values("active", "disabled").Default("active"),
	}
}

// ResellerPricing 分站定價（4 模式；優先級 SKU > 商品 > 分站默認；分站價不低於基礎價）。
type ResellerPricing struct {
	ent.Schema
}

func (ResellerPricing) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ResellerPricing) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("subsite_id"),
		field.Uint64("product_id"),
		field.Uint64("sku_id").Default(0).Comment("0=商品級（哨兵，參與唯一索引）"),
		field.Enum("mode").Values("inherit", "markup_percent", "fixed_markup", "fixed_price").Default("inherit"),
		field.Int64("value").Default(0).Comment("markup_percent=萬分比；fixed_*=分"),
	}
}

func (ResellerPricing) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "product_id", "sku_id").Unique(),
	}
}

// ResellerLedgerEntry 分站利潤賬本（冪等鍵 order_profit:<orderID>；凍結在自身狀態機）。
type ResellerLedgerEntry struct {
	ent.Schema
}

func (ResellerLedgerEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("subsite_id"),
		field.Uint64("order_id").Optional(),
		field.Enum("type").Values("order_profit", "refund_deduct", "withdraw_lock", "withdraw_paid", "manual_adjust"),
		field.Int64("amount").Comment("有符號（分）"),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.Enum("status").Values("pending", "available", "locked", "withdrawn").Default("pending"),
		field.Time("available_at").SchemaType(mysqlTime).Optional(),
		field.String("idempotency_key").MaxLen(120).Unique(),
		field.JSON("metadata_json", map[string]any{}).Optional(),
		field.String("remark").MaxLen(255).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (ResellerLedgerEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "status"),
		index.Fields("available_at"),
	}
}

// ResellerBalanceAccount 分站餘額緩存（可由流水重算；last_entry_id 水位）。
type ResellerBalanceAccount struct {
	ent.Schema
}

func (ResellerBalanceAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("subsite_id").Unique(),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.Int64("available").Default(0),
		field.Int64("locked").Default(0),
		field.Int64("negative").Default(0).Comment("負債（退款逆向不足扣）"),
		field.Uint64("last_entry_id").Default(0).Comment("賬本水位（增量重算）"),
		field.Time("updated_at").SchemaType(mysqlTime).Default(nowUTC).UpdateDefault(nowUTC),
	}
}

// ResellerRelatedAccount 分站關聯賬戶（自買風控數據源）。
type ResellerRelatedAccount struct {
	ent.Schema
}

func (ResellerRelatedAccount) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ResellerRelatedAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("reseller_id").Comment("分站 profile user_id"),
		field.Uint64("user_id").Comment("關聯用戶"),
		field.String("relation_type").MaxLen(40).Comment("family/same_device/sub_account..."),
		field.String("source").MaxLen(120).Optional().Comment("判定依據"),
		field.Enum("status").Values("active", "ignored").Default("active"),
	}
}

func (ResellerRelatedAccount) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("reseller_id", "user_id", "relation_type").Unique(),
	}
}

// Ticket 工單（付費加急 urgent_paid 置頂高亮；SLA 預留）。
type Ticket struct {
	ent.Schema
}

func (Ticket) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Ticket) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("ticket_no").MaxLen(40).Unique().Comment("雪花 T 前綴，不可枚舉"),
		field.Uint64("user_id").Optional().Comment("NULL=遊客"),
		field.String("guest_contact").MaxLen(150).Optional(),
		field.Enum("type").Values("presale", "aftersale", "withdraw"),
		field.Enum("priority").Values("low", "normal", "high", "urgent_paid").Default("normal"),
		field.Uint64("order_id").Optional(),
		field.Uint64("product_id").Optional(),
		field.Enum("status").Values("open", "processing", "resolved", "closed").Default("open"),
		field.Time("first_reply_at").SchemaType(mysqlTime).Optional(),
		field.Time("sla_due_at").SchemaType(mysqlTime).Optional().Comment("SLA 預留（M4 告警）"),
		field.Int8("satisfaction").Optional().Comment("1-5 評價"),
	}
}

func (Ticket) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "priority", "created_at"),
		index.Fields("user_id", "created_at"),
	}
}

// TicketMessage 工單消息（會話式；is_internal 內部備註用戶側雙過濾）。
type TicketMessage struct {
	ent.Schema
}

func (TicketMessage) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("ticket_id"),
		field.Enum("sender_type").Values("user", "admin", "system"),
		field.Uint64("sender_id").Optional(),
		field.Text("content").Comment("sanitize 後"),
		field.JSON("attachments", []uint64{}).Optional().Comment("media ids"),
		field.Bool("is_internal").Default(false).Comment("內部備註（用戶不可見）"),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (TicketMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("ticket_id", "created_at"),
	}
}

// MediaCategory 素材分類樹。
type MediaCategory struct {
	ent.Schema
}

func (MediaCategory) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (MediaCategory) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("parent_id").Optional(),
		field.String("name").MaxLen(60),
		field.Int32("sort").Default(0),
	}
}

// Media 素材（類型白名單 + 重編碼防圖片馬；ref_count 引用計數防誤刪）。
type Media struct {
	ent.Schema
}

func (Media) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Media) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("category_id").Optional(),
		field.String("path").MaxLen(500).Comment("相對存儲根（uploads/YYYY/MM/隨機名）"),
		field.String("name").MaxLen(255),
		field.String("mime").MaxLen(60),
		field.Int64("size"),
		field.Int32("width").Optional(),
		field.Int32("height").Optional(),
		field.String("sha256").MaxLen(64).Optional().Comment("秒傳去重"),
		field.Enum("storage").Values("local", "s3").Default("local"),
		field.Int32("ref_count").Default(0).Comment("引用計數（刪除需 confirm）"),
		field.Uint64("uploader_id").Optional(),
	}
}

func (Media) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("category_id"),
		index.Fields("sha256"),
	}
}
