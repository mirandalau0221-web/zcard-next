package schema

// 所有權：mods/supply（P2-01）/ mods/procurement（P2-02）
// 字段對齊《數據庫架構設計.md》§4.7/§5。

import (
	"encoding/json"
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SupplyConnection 上游連接（憑據 AES-GCM；SSRF 校驗；定價三參數；健康緩存）。
type SupplyConnection struct {
	ent.Schema
}

func (SupplyConnection) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (SupplyConnection) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("name").MaxLen(100),
		field.String("driver").MaxLen(30).Comment("zcard | dujiao_next | acg_faka"),
		field.String("base_url").MaxLen(255).Comment("上游地址（httpx SSRF 校驗）"),
		field.Bytes("credentials").Comment("AES-256-GCM 加密憑據（結構隨 driver）"),
		field.Enum("status").Values("active", "disabled").Default("active"),
		field.Uint64("sync_task_id").Default(0),
		field.Int64("low_stock_scanned_at").Default(0),
		field.String("low_stock_message").Default(""),
		field.String("sms_lease_token").Default(""),
		field.Int64("sms_lease_until").Default(0),
		field.String("sync_lease_token").Default(""),
		field.Int64("sync_lease_until").Default(0),
		field.String("callback_url").MaxLen(500).Optional().Comment("本站作下游時的回調登記"),
		field.Int32("retry_max").Default(5),
		field.String("retry_intervals").MaxLen(200).Default("[30,60,300]").Comment("重試間隔數組（秒，JSON 字符串）"),
		field.Float("exchange_rate").
			SchemaType(map[string]string{dialect.MySQL: "decimal(20,8)", dialect.Postgres: "numeric(20,8)", dialect.SQLite: "real"}).
			Default(1).Comment("匯率（上游價 × 匯率 = 本地價）"),
		field.Float("price_markup_percent").
			SchemaType(map[string]string{dialect.MySQL: "decimal(20,8)", dialect.Postgres: "numeric(20,8)", dialect.SQLite: "real"}).
			Default(0).Comment("加價百分比（100=翻倍；與固定加價可組合）"),
		field.Int64("price_markup_amount").Default(0).Comment("固定加價（分；本地價 = 上游價×匯率×(1+%) + 固定額 → 取整）"),
		field.Enum("price_rounding_mode").Values("none", "ceil_int", "ceil_tenth").Default("none"),
		field.Bool("auto_sync_price").Default(true).Comment("同步時自動更新本地價（manual 改價保護）"),
		field.Enum("stock_mode").Values("real", "plenty").Default("real"),
		field.JSON("settings", map[string]any{}).Optional().Comment("其餘開關（髮卡模式/失敗策略等）"),
		field.Time("last_ping_at").SchemaType(mysqlTime).Optional(),
		field.Bool("last_ping_ok").Default(false),
		field.Time("last_synced_at").SchemaType(mysqlTime).Optional(),
		field.Text("last_error").Optional(),
		field.Int64("balance_cache").Default(0).Comment("上游餘額緩存（分）"),
		// P2-10 S2/S3：定時調度錨點（三類 scope 各自的下次執行判據）
		field.Time("last_collect_at").SchemaType(mysqlTime).Optional(),
		field.Time("last_price_sync_at").SchemaType(mysqlTime).Optional(),
		field.Time("last_status_sync_at").SchemaType(mysqlTime).Optional(),
		// P2-10 S2：自適應節奏器（AIMD）狀態與渠道熔斷冷卻
		field.JSON("rate_state", map[string]any{}).Optional().Comment("節奏器持久狀態（當前間隔/連續成功/封鎖計數/冷卻時長）"),
		field.Time("rate_limit_until").SchemaType(mysqlTime).Optional().Comment("熔斷冷卻截止（非空且未到=調度與出站跳過）"),
	}
}

func (SupplyConnection) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "driver"),
		index.Fields("last_synced_at"),
	}
}

// SupplyMapping 商品映射（上游分類/商品/SKU ↔ 本地）。
type SupplyMapping struct {
	ent.Schema
}

func (SupplyMapping) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (SupplyMapping) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("connection_id"),
		field.String("upstream_category").MaxLen(128).Optional(),
		field.Uint64("local_category_id").Optional(),
		field.String("upstream_product").MaxLen(128),
		field.Uint64("local_product_id").Optional(),
		field.String("upstream_sku").MaxLen(64).Default(""),
		field.Uint64("local_sku_id").Optional(),
		field.Int64("stock_probe_after").Default(0),
		field.Int64("stock_probe_lease").Default(0),
		field.Int("stock_probe_failures").Default(0),
		field.Int32("up_stock").Default(0).Comment("庫存緩存（-1=無限，-2=未知）"),
		field.Time("stock_checked_at").SchemaType(mysqlTime).Optional(),
		field.Int32("stock_reference").Default(-2).Comment("上次成功庫存，僅作展示參考"),
		field.Time("stock_reference_at").SchemaType(mysqlTime).Optional(),
		field.JSON("pricing_override", map[string]any{}).Optional(),
	}
}

func (SupplyMapping) Indexes() []ent.Index {
	return []ent.Index{
		// sku 用空串哨兵參與唯一索引（NULL 可重複問題同 cart_items）
		index.Fields("connection_id", "upstream_product", "upstream_sku").Unique(),
		index.Fields("local_product_id"),
		index.Fields("connection_id", "stock_probe_after", "id"),
	}
}

// SupplySyncTask 同步任務追蹤（進度/統計/心跳/取消；1.x SupplySyncTask 對齊）。
type SupplySyncTask struct {
	ent.Schema
}

func (SupplySyncTask) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (SupplySyncTask) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("connection_id"),
		field.String("mode").MaxLen(20).Comment("full | incremental"),
		field.String("scope").MaxLen(60).Optional(),
		field.Bool("force_reprice").Default(false),
		field.String("request_key").MaxLen(64).Optional().Nillable(),
		field.String("request_hash").MaxLen(64).Optional(),
		field.JSON("import_payload", json.RawMessage{}).Optional(),
		field.Enum("status").Values("pending", "processing", "done", "failed", "canceled").Default("pending"),
		field.Int32("total_count").Default(0),
		field.Int32("processed_count").Default(0),
		field.Int32("created_count").Default(0),
		field.Int32("updated_count").Default(0),
		field.Int32("price_updated_count").Default(0),
		field.Int32("manual_skipped_count").Default(0),
		field.Int32("hidden_count").Default(0),
		field.Int32("deleted_count").Default(0),
		field.String("error_code").MaxLen(64).Optional(),
		field.Text("error_context").Optional(),
		field.Time("started_at").SchemaType(mysqlTime).Optional(),
		field.Time("heartbeat_at").SchemaType(mysqlTime).Optional().Comment("心跳（卡死檢測）"),
		field.String("current_stage").MaxLen(60).Optional(),
		field.Int32("current_page").Default(0),
		field.Time("cancel_requested_at").SchemaType(mysqlTime).Optional(),
		field.String("worker_version").MaxLen(40).Optional(),
		field.Time("finished_at").SchemaType(mysqlTime).Optional(),
	}
}

func (SupplySyncTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("connection_id", "created_at"),
		index.Fields("connection_id", "request_key").Unique(),
		index.Fields("status"),
	}
}

// SupplyImportItem checkpoints a selected product. Product and checkpoint writes
// commit together; a stock retry never repeats a price write.
type SupplyImportItem struct{ ent.Schema }

func (SupplyImportItem) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (SupplyImportItem) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"), field.Uint64("task_id"),
		field.String("code").MaxLen(128), field.String("name").MaxLen(1024),
		field.JSON("snapshot", json.RawMessage{}),
		field.String("state").MaxLen(20).Default("pending"),
		field.String("stage").MaxLen(20).Default("import"),
		field.Int("attempts").Default(0), field.Int64("next_attempt_at").Default(0),
		field.Bool("saved").Default(false), field.Bool("created").Default(false),
		field.Bool("activate_after_stock").Default(false),
		field.Uint64("local_product_id").Default(0), field.String("local_revision").Default(""),
		field.String("error_code").MaxLen(64).Default(""), field.String("error_summary").MaxLen(500).Default(""),
	}
}
func (SupplyImportItem) Indexes() []ent.Index {
	return []ent.Index{index.Fields("task_id", "code").Unique(), index.Fields("task_id", "state", "next_attempt_at")}
}

// ProcurementOrder 採購單（狀態機 §5.7.2；三通道結果匯聚；dedupe 冪等）。
type ProcurementOrder struct {
	ent.Schema
}

func (ProcurementOrder) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ProcurementOrder) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("order_item_id").Comment("觸發採購的訂單子項"),
		field.Uint64("connection_id"),
		field.String("upstream_order_id").MaxLen(80).Optional(),
		field.Enum("status").
			Values("pending", "submitted", "polling", "fulfilled", "rejected", "refunding", "refunded", "manual", "failed").
			Default("pending"),
		field.Enum("fail_strategy").Values("auto_refund", "manual").Default("auto_refund"),
		field.Int32("retry_count").Default(0),
		field.Time("next_retry_at").SchemaType(mysqlTime).Optional().Comment("退避重試調度"),
		field.Time("last_poll_at").SchemaType(mysqlTime).Optional().Comment("巡檢掃描錨點"),
		field.String("dedupe_key").MaxLen(120).Unique().Comment("持久化上游請求號（新單隨機生成，舊單保持原值）"),
		field.String("trace_id").MaxLen(64).Optional(),
		field.Text("last_error").Optional().Comment("最近失敗原因（重試/審計）"),
		field.String("upstream_refund_id").MaxLen(80).Optional().Comment("上游退款單號（退款傳導回填）"),
	}
}

func (ProcurementOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("connection_id", "upstream_order_id"),
		index.Fields("order_item_id").Unique(),
		index.Fields("status", "last_poll_at"),
	}
}

// ProcurementItem 採購明細（顯式多卡密/多 SKU；到手即加密）。
type ProcurementItem struct {
	ent.Schema
}

func (ProcurementItem) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ProcurementItem) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("procurement_id"),
		field.String("upstream_sku").MaxLen(64).Default(""),
		field.Int32("quantity"),
		field.Int64("unit_cost").Default(0).Comment("單位成本（分）"),
		field.JSON("received_content", []string{}).Optional().Comment("到手卡密密文（base64 行，AES-GCM，零明文落盤）"),
	}
}

func (ProcurementItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("procurement_id"),
	}
}
