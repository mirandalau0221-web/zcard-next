// Package port 為 wallet 模塊對外契約（零依賴包）。
package port

import (
	"context"
	"errors"

	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

var ErrInsufficientBalance = errors.New("wallet.INSUFFICIENT_BALANCE")

// Direction 流水方向（string(8)，與 ent schema 一致）。
const (
	DirectionIn  = "in"
	DirectionOut = "out"
)

// Entry 賬務入賬請求（一切餘額變動的唯一入口 InTx 的參數）。
type Entry struct {
	UserID    uint64
	Direction string
	Type      string // order_pay/order_refund/recharge/commission/adjust...
	Amount    money.Cents
	Reference string // 冪等鍵（重複提交直接返回成功，§5.6）
	OrderID   uint64
	Operator  uint64
	Remark    string
}

// PointEntry 積分入賬請求（與 Entry 同構）。
type PointEntry struct {
	UserID    uint64
	Direction string // in | out
	Type      string // earn_recharge / earn_consume / redeem / adjust ...
	Amount    int64  // 正數
	Reference string // 冪等鍵
	OrderID   uint64
	Remark    string
}

// Points 積分賬本窄接口（payment 充值贈送消費，通道 B 同事務）。
type Points interface {
	PointCreditInTx(ctx context.Context, e PointEntry) error
}

// PointsDebiter 積分扣減（order 積分兌換下單消費，通道 B 同事務）。
type PointsDebiter interface {
	PointDebitInTx(ctx context.Context, e PointEntry) error
}

// PointsReader 積分餘額讀取（memberlevel 等級進度頁消費）。
type PointsReader interface {
	GetPoints(ctx context.Context, userID uint64) (int64, error)
}

// RechargeReader 累計充值口徑（memberlevel 升級閾值消費；countAsRecharge——
// 僅 type=recharge 的真實入賬，互轉/調賬/退款不計，防小號互轉刷級）。
type RechargeReader interface {
	CumulativeRecharge(ctx context.Context, userID uint64) (int64, error)
}

// Wallet 錢包窄接口（order 餘額支付 / payment 退款 / affiliate 佣金入賬消費，通道 B 同事務）。
type Wallet interface {
	// CreditInTx 入賬（充值/退款回/佣金）；冪等重入：reference 已存在直接返回成功。
	CreditInTx(ctx context.Context, e Entry) error
	// DebitInTx 扣款（餘額支付下單）；餘額不足返回 ErrInsufficientBalance，訂單不創建。
	DebitInTx(ctx context.Context, e Entry) error
	// Lock / Unlock 凍結流轉（佣金/分站利潤凍結期、提現申請）。
	Lock(ctx context.Context, userID uint64, amount money.Cents, availableAt int64) error
	Unlock(ctx context.Context, userID uint64, amount money.Cents) error
}
