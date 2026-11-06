package schema

// 所有權：mods/wallet（P1-05 充值/積分/提現/禮品卡；賬務紀律與 wallet 主表同構）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PointAccount 積分賬户（與 WalletAccount 同構紀律：行鎖+非負+流水重算）。
type PointAccount struct {
	ent.Schema
}

func (PointAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id").Unique(),
		field.Int64("balance").Default(0).Comment("積分餘額（非負）"),
		field.Int32("version").Default(0),
		field.Time("updated_at").SchemaType(mysqlTime).Default(nowUTC).UpdateDefault(nowUTC),
	}
}

// PointTransaction 積分流水（reference 冪等鍵同錢包規範）。
type PointTransaction struct {
	ent.Schema
}

func (PointTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		// string(8)：in/out（ent.Enum 的 in 值會生成 DirectionIn 常量與 IN 謂詞撞名，同 wallet 方案）
		field.String("direction").MaxLen(8).Comment("方向：in | out"),
		field.String("type").MaxLen(40).Comment("earn_consume/earn_recharge/redeem/adjust..."),
		field.Int64("amount").Comment("積分（正數）"),
		field.Int64("balance_before"),
		field.Int64("balance_after"),
		field.String("reference").MaxLen(120).Unique().Comment("冪等鍵（points:<orderID> 等）"),
		field.Uint64("order_id").Optional(),
		field.String("remark").MaxLen(255).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (PointTransaction) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
	}
}

// RechargeOrder 充值單（複用支付管線；預設贈送充 X 送 Y 餘額 Z 積分）。
type RechargeOrder struct {
	ent.Schema
}

func (RechargeOrder) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (RechargeOrder) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		field.Int64("amount").Comment("充值金額（分）"),
		field.Int64("gift_amount").Default(0).Comment("贈送餘額（分）"),
		field.Int32("gift_points").Default(0).Comment("贈送積分"),
		field.Enum("target").Values("balance", "supply").Default("balance").Comment("入賬方向（supply=供貨商預存，M2）"),
		field.Uint64("supplier_account_id").Optional().Comment("target=supply 時的入賬目標對接賬户"),
		field.Enum("status").Values("pending", "success", "failed", "expired").Default("pending"),
		field.Uint64("payment_id").Optional(),
		field.Time("paid_at").SchemaType(mysqlTime).Optional(),
	}
}

func (RechargeOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("status"),
	}
}

// Withdrawal 提現（申請即鎖定 available→locked；審核通過打款/駁回解鎖）。
type Withdrawal struct {
	ent.Schema
}

func (Withdrawal) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Withdrawal) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		field.Int64("amount").Comment("提現金額（分）"),
		field.Int64("fee").Default(0).Comment("手續費（分）"),
		field.JSON("method", map[string]any{}).Comment("收款方式快照（渠道/賬號/姓名）"),
		field.Enum("status").Values("pending", "approved", "paid", "rejected").Default("pending"),
		field.Uint64("reviewed_by").Optional(),
		field.String("reject_reason").MaxLen(255).Optional(),
		field.Time("paid_at").SchemaType(mysqlTime).Optional(),
		field.Time("reviewed_at").SchemaType(mysqlTime).Optional(),
		// 打款回執（交易流水號/憑證備註；打款時填寫，客户記錄展示）
		field.String("receipt").MaxLen(255).Optional(),
	}
}

func (Withdrawal) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "status"),
		index.Fields("status", "created_at"),
	}
}

// GiftcardBatch 禮品卡批次。
type GiftcardBatch struct {
	ent.Schema
}

func (GiftcardBatch) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (GiftcardBatch) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.String("batch_no").MaxLen(40).Unique(),
		field.Time("deleted_at").SchemaType(mysqlTime).Optional().Comment("批次刪除時間，保留兑換審計"),
		field.String("name").MaxLen(100),
		field.Int64("amount").Comment("面額（分）"),
		field.Int32("quantity").Comment("發行數量"),
		field.Uint64("operator_id").Optional(),
	}
}

// Giftcard 禮品卡（code 同卡密紀律：AES-256-GCM 密文 + keyed hash 去重，永不落明文）。
type Giftcard struct {
	ent.Schema
}

func (Giftcard) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Giftcard) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("batch_id"),
		field.Bytes("code").Comment("AES-256-GCM 密文（ZCARD_CARD_KEY）"),
		field.String("code_hash").MaxLen(64).Unique().Comment("keyed hash（兑換檢索，防爆破限流配套）"),
		field.Int64("amount").Comment("面額（分）"),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.Enum("status").Values("unused", "used", "disabled").Default("unused"),
		field.Uint64("used_by").Optional().Comment("兑換用户"),
		field.Time("used_at").SchemaType(mysqlTime).Optional(),
		field.Time("expires_at").SchemaType(mysqlTime).Optional(),
	}
}

func (Giftcard) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id"),
		index.Fields("status"),
	}
}
