package order

// 訂單倉儲骨架（M1a 交付：創建訂單 + 鎖卡同事務、金額行快照、狀態事件、
// 超時取消掃描、遊標分頁）。當前註冊 wire provider。

import "github.com/NovaWorks/zcard-next/server/internal/data"

// OrderRepoImpl 訂單倉儲實現（M1a 交付方法體）。
type OrderRepoImpl struct {
	data *data.Data
}

// NewOrderRepoImpl 構造。
func NewOrderRepoImpl(d *data.Data) *OrderRepoImpl { return &OrderRepoImpl{data: d} }
