package payment

// wire providers。

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"

	"github.com/google/wire"
)

// ProviderSet payment providers。
var ProviderSet = wire.NewSet(
	NewPaymentRepoImpl,
	NewRegistry,
	NewPaymentUsecase,
	NewAdminPaymentService,
	NewStorePaymentService,
	// ：訂單退款入口（procurement 消費，通道 A）
	wire.Bind(new(port.OrderRefunder), new(*PaymentRepoImpl)),
	// ：充值支付單創建端口（wallet 模塊消費，通道 A）
	wire.Bind(new(port.RechargePayer), new(*PaymentRepoImpl)),
	// ：慢通道 pending 探測（order 超時取消順延消費，通道 A）
	wire.Bind(new(port.SlowPaymentChecker), new(*PaymentRepoImpl)),
)
