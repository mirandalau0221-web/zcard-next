package server

// gRPC Server（規劃 D2：HTTP/gRPC 雙協議免費獲得；M4 聚合平台階段某些模塊
// 直接以 gRPC 互聯，無需重寫接口）。當前註冊與 HTTP 相同的服務面。

import (
	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog"
	"github.com/NovaWorks/zcard-next/server/internal/mods/fulfillment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/mods/license"
	"github.com/NovaWorks/zcard-next/server/internal/mods/memberlevel"
	"github.com/NovaWorks/zcard-next/server/internal/mods/order"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
	"github.com/NovaWorks/zcard-next/server/internal/mods/wallet"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/go-kratos/kratos/v3/middleware/logging"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/middleware/validate"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"go.einride.tech/aip/fieldbehavior"
	"google.golang.org/protobuf/proto"
)

// NewGRPCServer 構造 gRPC server（wire provider）。
func NewGRPCServer(
	c *conf.Server,
	authSvc *identity.AdminAuthService,
	settingsSvc *settings.AdminSettingsService,
	catalogSvc *catalog.StoreCatalogService,
	roleSvc *authz.RoleService,
	adminSvc *authz.AdminUserService,
	confSvc *settings.StorefrontConfigService,
	currencySvc *settings.AdminCurrencyService,
	catalogAdminSvc *catalog.AdminCatalogService,
	invSvc *inventory.AdminInventoryService,
	orderAdminSvc *order.AdminOrderService,
	orderStoreSvc *order.StoreOrderService,
	payAdminSvc *payment.AdminPaymentService,
	payStoreSvc *payment.StorePaymentService,
	walletStoreSvc *wallet.StoreWalletService,
	walletAdminSvc *wallet.AdminWalletService,
	fulfillStoreSvc *fulfillment.StoreDeliveryService,
	fulfillAdminSvc *fulfillment.AdminFulfillmentService,
	memberLevelStoreSvc *memberlevel.StoreMemberLevelService,
	licenseStoreSvc *license.StoreLicenseService,
) *kgrpc.Server {
	// addr 為空 = 不啓用 gRPC（單機 HTTP 部署形態；部分容器環境迴環綁定受限
	// bind EADDRNOTAVAIL——留空即完全跳過，裝配側過濾 nil）
	if c == nil || c.Grpc == nil || c.Grpc.Addr == "" {
		return nil
	}
	var opts = []kgrpc.ServerOption{
		kgrpc.Middleware(
			recovery.Recovery(),
			logging.Server(log.Default()),
			validate.Validator(func(req any) error {
				if msg, ok := req.(proto.Message); ok {
					return fieldbehavior.ValidateRequiredFields(msg)
				}
				return nil
			}),
			// gRPC 側 admin 鑑權 M1 接 metadata（當前管理面以 HTTP 為主）
		),
	}
	if c != nil && c.Grpc != nil {
		if c.Grpc.Network != "" {
			opts = append(opts, kgrpc.Network(c.Grpc.Network))
		}
		if c.Grpc.Addr != "" {
			opts = append(opts, kgrpc.Address(c.Grpc.Addr))
		}
		if c.Grpc.Timeout != nil {
			opts = append(opts, kgrpc.Timeout(c.Grpc.Timeout.AsDuration()))
		}
	}
	srv := kgrpc.NewServer(opts...)

	adminv1.RegisterAdminAuthServiceServer(srv, authSvc)
	adminv1.RegisterAdminSettingsServiceServer(srv, settingsSvc)
	adminv1.RegisterRoleServiceServer(srv, roleSvc)
	adminv1.RegisterAdminUserServiceServer(srv, adminSvc)
	adminv1.RegisterAdminCurrencyServiceServer(srv, currencySvc)
	storefrontv1.RegisterStorefrontConfigServiceServer(srv, confSvc)
	adminv1.RegisterAdminCatalogServiceServer(srv, catalogAdminSvc)
	adminv1.RegisterAdminInventoryServiceServer(srv, invSvc)
	adminv1.RegisterAdminOrderServiceServer(srv, orderAdminSvc)
	adminv1.RegisterAdminPaymentServiceServer(srv, payAdminSvc)
	storefrontv1.RegisterStoreOrderServiceServer(srv, orderStoreSvc)
	storefrontv1.RegisterStorePaymentServiceServer(srv, payStoreSvc)
	storefrontv1.RegisterStoreWalletServiceServer(srv, walletStoreSvc)
	adminv1.RegisterAdminWalletServiceServer(srv, walletAdminSvc)
	adminv1.RegisterAdminFulfillmentServiceServer(srv, fulfillAdminSvc)
	storefrontv1.RegisterStoreDeliveryServiceServer(srv, fulfillStoreSvc)
	storefrontv1.RegisterStoreCatalogServiceServer(srv, catalogSvc)
	storefrontv1.RegisterStoreMemberLevelServiceServer(srv, memberLevelStoreSvc)
	storefrontv1.RegisterStoreLicenseServiceServer(srv, licenseStoreSvc)
	// reflection 已由 kratos v3 grpc server 內置（手動註冊會 duplicate panic）
	return srv
}
