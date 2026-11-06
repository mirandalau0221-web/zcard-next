package schema

// 所有權：mods/wallet（M1 充值 / M3 提現；表結構 M0 落地——賬務紀律核心）

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WalletAccount 賬户（凍結分離，超越友商單一 Balance，《數據庫架構設計.md》§4.5）。
//
// 不變量：total = available + locked 恆真；一切餘額變動必須經 wallet.InTx：
// 行鎖賬户 → 非負校驗 → 更新（樂觀鎖 version 兜底）→ 寫流水（reference 冪等重入）。
type WalletAccount struct {
	ent.Schema
}

func (WalletAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id").Unique(),
		field.String("currency").MaxLen(3).Default("CNY").Comment("基礎貨幣"),
		field.Int64("available").Default(0).Comment("可用餘額（分）"),
		field.Int64("locked").Default(0).Comment("凍結餘額（分）：提現凍結/佣金凍結期"),
		field.Int32("version").Default(0).Comment("樂觀鎖（併發扣款/凍結，與行鎖雙保險）"),
		field.Time("updated_at").SchemaType(mysqlTime).Default(nowUTC).UpdateDefault(nowUTC),
	}
}

// WalletTransaction 流水（只追加不更新；餘額永可由流水重算）。
// reference 即冪等鍵（鐵律 §5.6）：order_pay:<orderID> / recharge:<payID> / commission:<id> / adjust:<auditID>。
type WalletTransaction struct {
	ent.Schema
}

func (WalletTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("id"),
		field.Uint64("user_id"),
		// string(8)：in/out（取值約束由 wallet 模塊賬務紀律保證；不用 ent.Enum
		// 是因枚舉值 in 會生成 DirectionIn 常量、與 IN 查詢謂詞撞名）
		field.String("direction").MaxLen(8).Comment("方向：in | out"),
		field.String("type").MaxLen(40).Comment("order_pay/order_refund/recharge/commission/adjust..."),
		field.Int64("amount").Comment("金額（分，正數）"),
		field.Int64("balance_before").Comment("變動前快照"),
		field.Int64("balance_after").Comment("變動後快照"),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.String("reference").MaxLen(120).Unique().Comment("冪等鍵（重複入賬直接返回成功）"),
		field.Uint64("order_id").Optional(),
		field.Uint64("operator_id").Optional().Comment("手動調賬操作管理員"),
		field.String("remark").MaxLen(255).Optional(),
		field.Time("created_at").SchemaType(mysqlTime).Immutable().Default(nowUTC),
	}
}

func (WalletTransaction) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("type", "created_at"),
	}
}
