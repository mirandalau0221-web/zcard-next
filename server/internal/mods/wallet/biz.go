package wallet

// 錢包與賬務模塊（）：Reference 冪等鍵構造器（InTx 內核見 data_intx.go）。

import "fmt"

// Reference 冪等鍵構造（全賬務統一規範，數據庫架構 ）。
func Reference(kind string, ids ...uint64) string {
	s := kind
	for _, id := range ids {
		s = fmt.Sprintf("%s:%d", s, id)
	}
	return s
}

// 常用冪等鍵前綴。
const (
	RefOrderPay    = "order_pay"    // order_pay:<orderID>
	RefOrderRefund = "order_refund" // order_refund:<id>:<seq>
	RefRecharge    = "recharge"     // recharge:<payID>
	RefCommission  = "commission"   // commission:<id>
	RefAdjust      = "adjust"       // adjust:<auditID>
)
