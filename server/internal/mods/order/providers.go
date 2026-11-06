package order

// wire providers。

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/order/port"

	"github.com/google/wire"
)

// ProviderSet order providers。
var ProviderSet = wire.NewSet(
	NewOrderUsecaseDep,
	NewOrderRepoImpl,
	NewStoreOrderService,
	NewStoreSocialProductService,
	NewStoreCartService,
	NewAdminOrderService,
	// 破環點：OrderLifecycle 端口（payment 回調消費；適配器返回接口類型）
	ProvideOrderLifecycle,
	// 管理列表已售聚合（catalog 消費，通道 A）
	wire.Bind(new(port.SoldCounter), new(*OrderUsecase)),
)
