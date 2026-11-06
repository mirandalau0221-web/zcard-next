package schema

// 所有權：mods/order（M1；表結構 M0 落地）

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Order 父訂單（一個交易單元：金額/支付/聯繫方式/查詢密碼，《數據庫架構設計.md》§4.3）。
// 父子單模型：父單承載交易，子項（order_items）每商品一行；父狀態由子狀態聚合（CalcParentStatus）。
type Order struct {
	ent.Schema
}

func (Order) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}, TenantMixin{}, VersionMixin{}}
}

func (Order) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		// 對外單號 = 雪花 ID + 前綴，不可枚舉（取貨三重門之一，鐵律 12）；與內部 id 嚴格分離
		field.String("order_no").MaxLen(40).Unique().Comment("對外單號（雪花 ID，platform/id 生成）"),
		field.String("subsite_domain").MaxLen(255).Optional().Comment("分站域名快照"),
		field.Int64("subsite_profit").Default(0).Comment("分站利潤快照（分）"),
		field.Bool("profit_eligible").Default(true).Comment("是否產生分站利潤（自購/上級鏈命中則 false）"),
		field.Uint64("user_id").Optional().Comment("下單用户（NULL=遊客）"),
		field.String("guest_contact").MaxLen(150).Optional().Comment("遊客聯繫方式"),
		// 查詢密碼（argon2 哈希；默認強制開啓，constant-time 比對，鐵律 12）
		field.String("query_password_hash").MaxLen(128).Optional(),
		field.String("order_access_token_hash").MaxLen(64).Default(""),
		field.Bytes("order_access_token_secret").Optional().Comment("訂單訪問憑證密文，僅創建重放使用"),
		field.Enum("status").
			Values(
				"pending_payment", "paid", "fulfilling", "partially_delivered",
				"delivered", "completed", "canceled", "expired",
				"refund_pending", "refunded",
			).
			Default("pending_payment").
			Comment("狀態機見規劃 §5.3；每次遷移落 order_status_events"),
		// total_amount = SUM(order_amount_lines.amount) 冗餘快照（對賬約束）
		field.Int32("commerce_version").Default(0),
		field.Int64("shipping_amount").Default(0),
		field.String("shipping_status").Default("none"),
		field.JSON("shipping_address", map[string]string{}).Optional(),
		field.String("request_hash").Default(""),
		field.Int64("total_amount").Default(0).Comment("應付總額（分）"),
		field.Int64("cost").Default(0).Comment("成本快照（分）"),
		field.String("base_currency").MaxLen(3).Optional().Comment("基礎貨幣（默認 CNY）"),
		field.String("display_currency").MaxLen(3).Optional().Comment("展示貨幣（下單快照）"),
		field.Float("exchange_rate").
			Optional().
			SchemaType(map[string]string{
				dialect.MySQL:    "decimal(20,8)",
				dialect.Postgres: "numeric(20,8)",
				dialect.SQLite:   "real",
			}).
			Comment("匯率快照（僅快照，換算中間過程用 decimal 庫）"),
		field.Int64("amount_display").Optional().Comment("展示貨幣金額（最小單位）"),
		field.String("payment_channel").MaxLen(30).Optional().Comment("支付渠道碼快照"),
		field.String("contact").MaxLen(150).Optional().Comment("聯繫方式"),
		field.String("client_ip").MaxLen(64).Optional().Comment("下單 IP"),
		field.String("risk_ip").MaxLen(80).Optional().Comment("規範化風控 IP（IPv6 按 /64 聚合）"),
		field.JSON("risk_flags", map[string]any{}).Optional().Comment("風控標記（M3 填充）"),
		field.Uint64("parent_id").Optional().Comment("子單歸屬父單（父子單模型）"),
		// 商業版掛載點（M4 擔保交易）：開源版恆 NULL，僅預留列
		field.Uint64("escrow_id").Optional().Comment("擔保交易關聯（商業版，M4）"),
		field.Uint64("invite_l1").Optional().Comment("三級分銷歸因鏈快照（下單瞬間鎖定）"),
		field.Uint64("invite_l2").Optional(),
		field.Uint64("invite_l3").Optional(),
		field.JSON("extra", map[string]any{}).Optional().Comment("擴展預留（控件答案等，加字段先進 extra）"),
		field.String("idempotency_key").MaxLen(80).Unique().Optional().Comment("下單冪等鍵哈希（P1-03：同 key 返回首單）"),
		field.Time("paid_at").SchemaType(mysqlTime).Optional(),
		field.Time("closed_at").SchemaType(mysqlTime).Optional(),
		field.Time("admin_deleted_at").SchemaType(mysqlTime).Optional().Nillable().Comment("管理列表軟刪除時間；保留訂單、支付及審計記錄"),
		field.Time("expired_at").SchemaType(mysqlTime).Optional().Comment("原始支付截止時間"),
		field.Time("expiry_retry_at").SchemaType(mysqlTime).Optional(),
		field.Int32("expiry_attempts").Default(0),
		field.Bool("expiry_review").Default(false),
		field.String("expiry_reason").MaxLen(255).Default(""),
	}
}

func (Order) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "created_at"),
		index.Fields("user_id", "status"),
		index.Fields("status", "expired_at"),
		index.Fields("parent_id"),
		index.Fields("escrow_id"),
		index.Fields("invite_l1"),
		// 風控複合索引（友商 idx_orders_risk_pending 思路）
		index.Fields("risk_ip", "user_id", "status"),
	}
}

func (Order) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("items", OrderItem.Type).Comment("訂單子項（每商品一行）"),
		edge.To("amount_lines", OrderAmountLine.Type).Comment("行式金額明細"),
		edge.To("status_events", OrderStatusEvent.Type).Comment("狀態事件溯源"),
		edge.To("payments", Payment.Type).Comment("支付單"),
		edge.To("deliveries", OrderDelivery.Type).Comment("交付記錄"),
		edge.To("refunds", RefundOrder.Type).Comment("退款單"),
	}
}

// OrderItem 訂單子項（每商品一行，快照價 + 履約狀態）。
type OrderItem struct {
	ent.Schema
}

func (OrderItem) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (OrderItem) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("order_id").Comment("父訂單（硬外鍵 → orders）"),
		field.Uint64("product_id"),
		field.Uint64("sku_id").Optional(),
		field.String("product_name").Default(""),
		field.String("delivery_kind").Default("card"),
		field.Text("social_purchase_snapshot").Default(""),
		field.JSON("social_schema", map[string]any{}).Optional(),
		field.JSON("sms_product", map[string]string{}).Optional(),
		field.Text("sms_purchase_snapshot").Optional(),
		field.JSON("form_answers", []map[string]string{}).Optional(),
		field.Uint64("assigned_admin_id").Default(0),
		field.String("sku_name").MaxLen(100).Optional().Comment("SKU 名稱快照"),
		field.Int64("unit_price").Comment("單價快照（分）"),
		field.Int32("quantity"),
		field.Int64("amount").Comment("小計（分）"),
		field.Int64("cost").Default(0).Comment("成本快照（分）"),
		field.String("goods_type").Default("virtual"),
		field.Bool("inventory_tracked").Default(true).Comment("下單時數量庫存管理快照；舊實體項繼續管理"),
		field.Int64("paid_amount").Default(0),
		field.Int64("shipping_amount").Default(0),
		field.Int64("refunded_amount").Default(0),
		field.Int64("refunded_shipping").Default(0),
		field.Int32("canceled_quantity").Default(0),
		field.Int32("shipped_quantity").Default(0),
		field.Int32("received_quantity").Default(0),
		field.Int32("returned_quantity").Default(0),
		field.Enum("fulfillment_type").Values("auto", "manual", "upstream", "reuse", "shipping").Comment("履約類型"),
		field.Uint64("delivery_source_id").Optional(),
		field.String("fulfillment_status").MaxLen(20).Default("pending").Comment("履約狀態"),
		field.JSON("commission_snapshot", map[string]any{}).Optional().Comment("佣金快照（三級 + 費率）"),
		field.JSON("profit_snapshot", map[string]any{}).Optional().Comment("分站利潤快照"),
	}
}

func (OrderItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("delivery_source_id"),
		index.Fields("order_id"),
		index.Fields("product_id"),
	}
}

func (OrderItem) Edges() []ent.Edge {
	return []ent.Edge{
		// 硬外鍵 → orders（核心聚合內）
		edge.From("order", Order.Type).
			Ref("items").
			Field("order_id").
			Required().
			Unique(),
	}
}

// OrderAmountLine 訂單金額明細行——行式金額模型，超越友商 20 個平鋪 Amount 字段
// （數據庫架構 §4.3）：價格計算管線每一步落一行，total_amount = SUM(amount)，
// 加折扣類型只加 type 枚舉值、不加列；seq 對應管線順序可重放。
type OrderAmountLine struct {
	ent.Schema
}

func (OrderAmountLine) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		// 硬外鍵 → orders
		field.Uint64("order_id"),
		field.Uint64("item_id").Optional().Comment("訂單子項（NULL=父單級，如整單優惠券）"),
		field.Enum("type").
			Values(
				"base_price", "sku_adjust", "member_discount", "group_discount",
				"promo_discount", "coupon_discount", "points_discount",
				"subsite_markup", "fee", "tax", "rounding_adjust", "shipping",
			).
			Comment("金額行類型（新增折扣類型只加枚舉值）"),
		field.Int64("amount").Comment("有符號分（折扣為負、加價為正）"),
		field.String("source_type").MaxLen(32).Optional().Comment("來源類型 member_level/coupon/flash_sale/points/subsite/manual"),
		field.Uint64("source_id").Optional().Comment("來源 ID（優惠券/會員等級/秒殺）"),
		field.Int32("seq").Default(0).Comment("管線順序（PriceCalculator 步驟序，可重放）"),
		field.JSON("meta", map[string]any{}).Optional().Comment("快照（費率/匯率/取整規則）"),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (OrderAmountLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
		index.Fields("item_id"),
	}
}

func (OrderAmountLine) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("order", Order.Type).
			Ref("amount_lines").
			Field("order_id").
			Required().
			Unique(),
	}
}

// OrderStatusEvent 訂單狀態事件溯源——完整審計鏈「誰在何時把訂單從 X 改成 Y、為什麼」，
// 超越友商單一 status 字段（數據庫架構 §4.3）；支撐對賬/客服排查/風控回放。
type OrderStatusEvent struct {
	ent.Schema
}

func (OrderStatusEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("order_id"),
		field.String("from_status").MaxLen(20),
		field.String("to_status").MaxLen(20),
		field.String("event").MaxLen(32).Comment("created/paid/fulfilled/delivered/completed/canceled/expired/refund_requested/refunded"),
		field.Enum("operator").Values("system", "user", "admin", "worker"),
		field.Uint64("operator_id").Optional(),
		field.String("reason").MaxLen(255).Optional(),
		field.String("client_ip").MaxLen(64).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (OrderStatusEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id", "created_at"),
		index.Fields("event"),
	}
}

func (OrderStatusEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("order", Order.Type).
			Ref("status_events").
			Field("order_id").
			Required().
			Unique(),
	}
}
