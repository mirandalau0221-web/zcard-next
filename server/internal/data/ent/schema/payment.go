package schema

// 所有權：mods/payment（M1；表結構 M0 落地）

import (
	"entgo.io/ent/dialect"

	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PaymentChannel 渠道配置（《數據庫架構設計.md》§4.4）。
// 憑據 AES-GCM 加密存儲；解密失敗必須降級為空並提示重配，列表接口絕不 500（鐵律 5）。
type PaymentChannel struct {
	ent.Schema
}

func (PaymentChannel) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (PaymentChannel) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(60),
		field.String("code").MaxLen(30).Comment("渠道碼（alipay/wechat/epay/wallet...）"),
		field.String("driver").MaxLen(100).Comment("驅動類型（適配器按 (provider, channel) 註冊表路由）"),
		field.Bytes("config").Comment("AES-256-GCM 加密憑據（結構隨 driver）"),
		field.Int64("fee").Default(0).Comment("手續費（分 或 萬分比，按 fee_type）"),
		field.Enum("fee_type").Values("percent", "fixed").Default("fixed"),
		field.Enum("fee_bearer").Values("merchant", "user").Default("merchant").Comment("手續費承擔方"),
		field.Bool("recommended").Default(false),
		field.String("recommend_label").MaxLen(24).Default(""),
		field.String("recommend_description").MaxLen(180).Default(""),
		field.Int32("sort").Default(0),
		field.Bool("enabled").Default(true),
		field.Time("deleted_at").SchemaType(mysqlTime).Optional().Comment("已刪除渠道從管理列表隱藏；保留歷史支付回調憑據"),
		field.Bool("allow_purchase").Default(true),
		field.Bool("allow_member_recharge").Default(true),
		field.Bool("allow_supply_recharge").Default(true),
		// 方式級收銀台（大廠模式：渠道=網關憑據單位，支付方式=顧客看到的選項）：
		// icon 渠道級自定義圖標（素材庫 URL，空=回落內置徽標）；methods 方式列表
		// [{code,name,icon,enabled,params}]——params 承載網關參數（易支付 type / USDT network+token）。
		// 空 methods = 單方式渠道（舊語義，收銀台顯示渠道本身）。
		field.String("icon").MaxLen(500).Default(""),
		field.JSON("methods", []map[string]any{}).Optional(),
	}
}

func (PaymentChannel) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subsite_id", "code").Unique(),
	}
}

// Payment 支付單（§4.4）。channel_order_no 隔離下游回傳格式；
// UNIQUE(channel, channel_order_no) 為回調冪等核對錨點之一（回調冪等三層：狀態機/行鎖二次校驗/業務冪等鍵）。
// order_id 與 recharge_order_id 二選一：訂單支付單關聯 orders，充值支付單關聯
// recharge_orders（M1b 充值走支付管線；回調按關聯類型分流）。
type Payment struct {
	ent.Schema
}

func (Payment) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}, TenantMixin{}} }

func (Payment) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("order_id").Optional().Comment("關聯訂單（訂單支付單；充值支付單為空）"),
		field.Uint64("recharge_order_id").Optional().Comment("關聯充值單（充值支付單；訂單支付單為空）"),
		field.String("channel").MaxLen(50).Comment("渠道碼"),
		field.Uint64("channel_id").Default(0).Comment("發起時渠道身份；0 為歷史流水"),
		field.String("driver_snapshot").MaxLen(100).Default(""),
		field.Time("expires_at").SchemaType(mysqlTime).Optional(),
		field.String("review_reason").MaxLen(255).Default(""),
		field.String("gateway_order_ref").MaxLen(64).Optional().Comment("支付嘗試的穩定網關訂單號；舊流水保持 NULL"),
		field.JSON("gateway_context", json.RawMessage{}).Optional().Comment("非敏感網關請求快照、租約及收銀台地址"),
		field.String("channel_order_no").MaxLen(80).Optional().Comment("網關單號（回調時回填）"),
		field.Int64("amount").Comment("應收（分，基礎貨幣）"),
		field.Int64("charged_amount").Default(0).Comment("實收（分，回調核對；金額核對永遠對基礎貨幣）"),
		// ── 幣種快照（P2-09 T2：跨境渠道下單瞬間固化，回調按快照核對零二次換算）──
		// 同幣直收（CNY）路徑三列零寫：charged_units=0 即走舊核對（fact.Amount==amount）
		field.String("charged_currency").MaxLen(8).Optional().Comment("渠道幣種 ISO 碼（空=CNY 同幣直收）"),
		field.Float("exchange_rate").Default(0).
			SchemaType(map[string]string{
				dialect.MySQL:    "decimal(20,8)",
				dialect.Postgres: "numeric(20,8)",
				dialect.SQLite:   "real",
			}).
			Comment("快照匯率（1 基礎貨幣=rate 渠道幣；0=未換算）"),
		field.Int32("charged_precision").Default(-1).Comment("渠道金額單位精度快照；-1 為歷史未記錄，不能使用顯示精度推斷"),
		field.Int64("charged_units").Default(0).Comment("渠道幣種應收最小單位（發適配器金額；回調精確核對）"),
		field.Int64("fee").Default(0).Comment("用户支付手續費（分）"),
		field.JSON("pricing_snapshot", json.RawMessage{}).Optional().Comment("支付本金、費率、承擔方及方式快照；空為歷史流水"),
		field.Enum("status").Values("pending", "success", "failed").Default("pending"),
		field.Time("paid_at").SchemaType(mysqlTime).Optional(),
		field.JSON("raw", json.RawMessage{}).Optional().Comment("回調原文（審計；不含敏感憑據）"),
		field.String("idempotency_key").MaxLen(64).Optional().Comment("業務冪等鍵（寫接口 Idempotency-Key 頭）"),
	}
}

func (Payment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
		index.Fields("gateway_order_ref").Unique(),
		index.Fields("channel", "channel_order_no").Unique(),
		index.Fields("status", "created_at"),
	}
}

func (Payment) Edges() []ent.Edge {
	return []ent.Edge{
		// 軟外鍵 → orders（訂單支付單；充值支付單不關聯）
		edge.From("order", Order.Type).
			Ref("payments").
			Field("order_id").
			Unique(),
	}
}

// RefundOrder 退款單——2.0 新增退款編排（§5.5.4）：渠道原路 / 錢包 / 上游傳導三通道。
type RefundOrder struct {
	ent.Schema
}

func (RefundOrder) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (RefundOrder) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("order_id"),
		field.Int64("amount").Comment("退款本金（分，可部分退款）"),
		field.JSON("item_allocations", []map[string]int64{}).Optional(),
		field.String("request_key").MaxLen(64).Optional().Unique().Comment("訂單範圍內的退款請求冪等摘要；歷史記錄為空"),
		field.String("request_hash").MaxLen(64).Optional().Comment("退款請求內容摘要"),
		field.Int64("shipping_amount").Default(0),
		field.Int64("fee_amount").Default(0).Comment("本次退還的支付手續費（分）"),
		field.Enum("channel").Values("gateway", "wallet", "upstream").Comment("退款通道"),
		field.Enum("status").Values("created", "processing", "succeeded", "failed").Default("created"),
		field.Text("reason").Optional(),
		field.Uint64("operator_id").Optional().Comment("操作者（需 order:refund 權限 + 二次確認 + 審計）"),
		field.String("upstream_refund_id").MaxLen(64).Optional().Comment("上游退款單號（傳導，M2）"),
	}
}

func (RefundOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
		index.Fields("status"),
	}
}

func (RefundOrder) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("order", Order.Type).
			Ref("refunds").
			Field("order_id").
			Required().
			Unique(),
	}
}
