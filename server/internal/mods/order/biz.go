// Package order 訂單模塊（M1a）：下單用例、父子訂單、狀態機、價格計算管線、
// 查詢密碼、取貨三重門。M0 落地狀態機純邏輯（白名單遷移 + 審計語義）。
package order

import "errors"

// 訂單狀態（§5.3 狀態機；與 ent schema orders.status 枚舉一一對應）。
const (
	StatusPendingPayment     = "pending_payment"
	StatusPaid               = "paid"
	StatusFulfilling         = "fulfilling"
	StatusPartiallyDelivered = "partially_delivered"
	StatusDelivered          = "delivered"
	StatusCompleted          = "completed"
	StatusCanceled           = "canceled"
	StatusExpired            = "expired"
	StatusRefundPending      = "refund_pending"
	StatusRefunded           = "refunded"
	StatusManualPending      = "manual_pending" // 全部人工發貨（父狀態聚合用）
)

// transitions 狀態機白名單（Allow(from, to)）。paid 之後禁止回 canceled（退款走獨立路徑）。
var transitions = map[string]map[string]bool{
	StatusPendingPayment: {
		StatusPaid: true, StatusCanceled: true, StatusExpired: true,
	},
	StatusPaid: {
		StatusFulfilling: true, StatusManualPending: true, StatusRefundPending: true,
	},
	StatusFulfilling: {
		StatusPartiallyDelivered: true, StatusDelivered: true, StatusRefundPending: true,
	},
	StatusPartiallyDelivered: {
		StatusDelivered: true, StatusRefundPending: true,
	},
	StatusDelivered: {
		StatusCompleted: true, StatusRefundPending: true,
	},
	StatusManualPending: {
		StatusFulfilling: true, StatusDelivered: true, StatusRefundPending: true,
	},
	StatusRefundPending: {
		StatusRefunded: true, StatusPaid: true, // 退款被拒/撤銷回 paid
	},
	StatusCompleted: {},
	StatusCanceled:  {},
	StatusExpired:   {},
	StatusRefunded:  {},
}

// ErrTransitionNotAllowed 非法狀態遷移（狀態機紅燈，必測項 §5.3）。
var ErrTransitionNotAllowed = errors.New("order.TRANSITION_NOT_ALLOWED")

// Allow 判定狀態遷移是否合法（每次遷移落 order_status_events，事件溯源）。
func Allow(from, to string) bool {
	allowed, ok := transitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// CalcParentStatus 父狀態由子項履約狀態聚合（優先級：refunded > canceled >
// manual_pending > partially_delivered > fulfilling > delivered > completed > paid）。
// M1 交付完整實現（此處先固定優先級口徑供 data 層引用）。
func CalcParentStatus(itemStatuses []string) string {
	// 優先級從高到低掃描，任一子項命中即取該狀態
	priority := []string{StatusRefunded, StatusCanceled, StatusManualPending, StatusPartiallyDelivered, StatusFulfilling, StatusDelivered, StatusPaid}
	counts := map[string]int{}
	for _, s := range itemStatuses {
		counts[s]++
	}
	for _, p := range priority {
		if counts[p] > 0 {
			// 部分 vs 全部：子項存在 delivered 且存在未 delivered → partially_delivered
			if p == StatusDelivered && counts[StatusDelivered] < len(itemStatuses) {
				return StatusPartiallyDelivered
			}
			return p
		}
	}
	return StatusPaid
}
