package server

// HTTP Server：單進程承載 admin/storefront/supply 三面 API + 回調 + 健康檢查。
// 中間件棧：recovery → logging → validate → tenant（全局）+ adminAuth（selector：
// 僅 /api/v1/admin/* 前綴；回調與供貨路由絕不掛 JWT，架構測試規則 9）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	supplyv1 "github.com/NovaWorks/zcard-next/server/api/supply/v1"
	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/mods/affiliate"
	"github.com/NovaWorks/zcard-next/server/internal/mods/audit"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/captcha"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog"
	"github.com/NovaWorks/zcard-next/server/internal/mods/content"
	"github.com/NovaWorks/zcard-next/server/internal/mods/coupon"
	"github.com/NovaWorks/zcard-next/server/internal/mods/dashboard"
	"github.com/NovaWorks/zcard-next/server/internal/mods/fulfillment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	identityport "github.com/NovaWorks/zcard-next/server/internal/mods/identity/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/mods/license"
	"github.com/NovaWorks/zcard-next/server/internal/mods/lottery"
	"github.com/NovaWorks/zcard-next/server/internal/mods/media"
	"github.com/NovaWorks/zcard-next/server/internal/mods/memberlevel"
	"github.com/NovaWorks/zcard-next/server/internal/mods/notify"
	"github.com/NovaWorks/zcard-next/server/internal/mods/order"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/procurement"
	"github.com/NovaWorks/zcard-next/server/internal/mods/reseller"
	"github.com/NovaWorks/zcard-next/server/internal/mods/seo"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supplier"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply"
	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/ticket"
	"github.com/NovaWorks/zcard-next/server/internal/mods/update"
	"github.com/NovaWorks/zcard-next/server/internal/mods/wallet"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"
	"github.com/NovaWorks/zcard-next/server/internal/web"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/middleware/selector"
	"github.com/go-kratos/kratos/v3/middleware/validate"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"go.einride.tech/aip/fieldbehavior"
	"google.golang.org/protobuf/proto"
)

// Version 構建注入（-ldflags -X），health/ping 下發。
var Version = "dev"

// NewHTTPServer 構造 HTTP server（wire provider）。
func NewHTTPServer(
	c *conf.Server,
	d *data.Data,
	signer *authn.Signer,
	az port.Authorizer,
	adminReader identityport.AdminReader,
	authSvc *identity.AdminAuthService,
	settingsSvc *settings.AdminSettingsService,
	installSvc *settings.AdminInstallService,
	catalogSvc *catalog.StoreCatalogService,
	reviewSvc *catalog.StoreReviewService,
	supplyAdminSvc *supply.AdminSupplyService,
	procureAdminSvc *procurement.AdminProcurementService,
	smsSvc *procurement.StoreSMSService,
	smsChannelSvc *supply.StoreSMSChannelService,
	socialProductSvc *order.StoreSocialProductService,
	socialOrderSvc *procurement.StoreSocialOrderService,
	supplyAPISvc *supplier.SupplyAPIService,
	supplierAdminSvc *supplier.AdminSupplierService,
	supplierStoreSvc *supplier.StoreSupplierService,
	supplierRepo *supplier.SupplierRepoImpl,
	contentAdminSvc *content.AdminContentService,
	contentStoreSvc *content.StoreContentService,
	notifyAdminSvc *notify.AdminNotifyService,
	notifyStoreSvc *notify.StoreNotificationService,
	couponStoreSvc *coupon.StoreCouponService,
	ticketStoreSvc *ticket.StoreTicketService,
	ticketAdminSvc *ticket.AdminTicketService,
	affiliateStoreSvc *affiliate.StoreAffiliateService,
	mediaAdminSvc *media.AdminMediaService,
	licenseAdminSvc *license.AdminLicenseService,
	resellerAdminSvc *reseller.AdminResellerService,
	resellerRepo *reseller.ResellerRepo,
	userStoreSvc *identity.StoreUserService,
	userManageSvc *identity.AdminUserManageService,
	auditAdminSvc *audit.AdminAuditService,
	auditRepo *audit.AuditRepo,
	roleSvc *authz.RoleService,
	adminSvc *authz.AdminUserService,
	confSvc *settings.StorefrontConfigService,
	captchaSvc *captcha.StoreCaptchaService,
	storeMediaSvc *media.StoreMediaService,
	currencySvc *settings.AdminCurrencyService,
	catalogAdminSvc *catalog.AdminCatalogService,
	memberLevelSvc *memberlevel.AdminMemberLevelService,
	memberLevelStoreSvc *memberlevel.StoreMemberLevelService,
	licenseStoreSvc *license.StoreLicenseService,
	couponSvc *coupon.AdminCouponService,
	lotteryAdmin *lottery.AdminService,
	lotteryStore *lottery.StoreService,
	dashboardSvc *dashboard.AdminDashboardService,
	invSvc *inventory.AdminInventoryService,
	orderAdminSvc *order.AdminOrderService,
	procureSvc *procurement.ProcureService,
	upstreamGW supplyport.UpstreamGateway,
	orderStoreSvc *order.StoreOrderService,
	cartSvc *order.StoreCartService,
	payAdminSvc *payment.AdminPaymentService,
	payStoreSvc *payment.StorePaymentService,
	payRepo *payment.PaymentRepoImpl,
	walletStoreSvc *wallet.StoreWalletService,
	walletAdminSvc *wallet.AdminWalletService,
	fulfillStoreSvc *fulfillment.StoreDeliveryService,
	fulfillAdminSvc *fulfillment.AdminFulfillmentService,
	enq queue.Enqueuer,
	dir *authz.Directory,
	tracker *audit.TrackRepo,
	seoSvc *seo.SeoService,
	updateSvc *update.AdminUpdateService,
) *khttp.Server {
	// ⚠️ Kratos khttp.Filter 是整體替換（o.filters = filters）非追加——
	// 多次調用只有最後一次生效（曾致 supplier HMAC/CORS/租戶被 audit 靜默覆蓋）。
	// 全部 Filter 必須合併為一次註冊。
	requestTimeout := 30 * time.Second
	if c != nil && c.Http != nil && c.Http.Timeout != nil {
		requestTimeout = c.Http.Timeout.AsDuration()
	}
	var opts = []khttp.ServerOption{
		khttp.Filter(
			requestDeadlineFilter(requestTimeout),
			privateOrderResponseFilter,
			corsFilter,
			// ：租戶域名解析（Filter 層——中間件拿不到 Host；最外層確保全鏈路繼承）
			tenantFilter(tenancyMainDomain(c), resellerRepo),
			settings.PreviewWriteGuard,
			// 未安裝守門（在線安裝 Web 嚮導；Filter 層直寫 302）
			installGuard(func() bool { return settings.Installed(context.Background(), d) }),
			// ：對外供貨 HMAC 四頭鑑權（Filter 層能拿原始請求字節——簽名不變式；
			// 僅 /api/supply/*，Ping 免簽名；不掛 JWT，架構測試規則 9）
			supplier.SupplyAuthFilter(supplierRepo, 0),
			// ：變更類 admin 操作審計（Filter 層——中間件拿不到原始請求；
			// POST/PUT/DELETE 且 2xx；異步落庫失敗不阻斷）
			audit.OpAuditFilter(auditRepo, signer,
				func(op string) (string, bool) {
					code, _, ok := dir.PermissionForOp(op)
					return code, ok
				},
				func(r *http.Request) string { return adminOpOfPath(r) },
			),
		),
		khttp.Middleware(
			recovery.Recovery(),
			requestLogging(log.Default()),
			validate.Validator(func(req any) error {
				if msg, ok := req.(proto.Message); ok {
					return fieldbehavior.ValidateRequiredFields(msg)
				}
				return nil
			}),
			i18nMiddleware(settings.NewRepoImpl(d)),
			// ：storefront user realm JWT（解析失敗放行——遊客端點不受影響；
			// 需登錄端點由業務側 claims==nil 自行 401）
			userAuthMiddleware(signer),
			// ：storefront 訪問埋點（PV/UV 明細 + 在線心跳；遊客/登錄均記，
			// 失敗忽略）。僅掛 storefront operation，admin/supply/回調不埋。
			selector.Server(storefrontVisitMiddleware(tracker)).
				Match(func(_ context.Context, operation string) bool {
					return strings.HasPrefix(operation, "/zcard.api.storefront.v1.") ||
						strings.HasPrefix(operation, "/api/v1/storefront/")
				}).
				Build(),
			// admin realm 鑑權僅掛管理面 operation；Public 聲明（登錄）經目錄豁免；
			// storefront/supply/回調路由不掛 JWT（架構測試規則 9）。
			selector.Server(adminAuthMiddleware(signer, az, dir, adminReader)).
				Match(func(_ context.Context, operation string) bool {
					return isAdminOperation(operation, dir)
				}).
				Build(),
		),
		khttp.Timeout(0),
	}
	if c != nil && c.Http != nil {
		if c.Http.Network != "" {
			opts = append(opts, khttp.Network(c.Http.Network))
		}
		if c.Http.Addr != "" {
			opts = append(opts, khttp.Address(c.Http.Addr))
		}
	}
	srv := khttp.NewServer(opts...)

	// 業務路由（proto 註解生成；靜態路由先於參數路由由註冊順序保證，鐵律 4）
	adminv1.RegisterAdminAuthServiceHTTPServer(srv, authSvc)
	adminv1.RegisterAdminInstallServiceHTTPServer(srv, installSvc)
	adminv1.RegisterAdminSettingsServiceHTTPServer(srv, settingsSvc)
	adminv1.RegisterRoleServiceHTTPServer(srv, roleSvc)
	adminv1.RegisterAdminUserServiceHTTPServer(srv, adminSvc)
	adminv1.RegisterAdminCurrencyServiceHTTPServer(srv, currencySvc)
	adminv1.RegisterAdminMemberLevelServiceHTTPServer(srv, memberLevelSvc)
	storefrontv1.RegisterStoreMemberLevelServiceHTTPServer(srv, memberLevelStoreSvc)
	storefrontv1.RegisterStoreLicenseServiceHTTPServer(srv, licenseStoreSvc)
	adminv1.RegisterAdminCouponServiceHTTPServer(srv, couponSvc)
	adminv1.RegisterAdminLotteryServiceHTTPServer(srv, lotteryAdmin)
	storefrontv1.RegisterStoreLotteryServiceHTTPServer(srv, lotteryStore)
	adminv1.RegisterAdminDashboardServiceHTTPServer(srv, dashboardSvc)
	storefrontv1.RegisterStorefrontConfigServiceHTTPServer(srv, confSvc)
	storefrontv1.RegisterStoreCaptchaServiceHTTPServer(srv, captchaSvc)
	storefrontv1.RegisterStoreMediaServiceHTTPServer(srv, storeMediaSvc)
	adminv1.RegisterAdminCatalogServiceHTTPServer(srv, catalogAdminSvc)
	adminv1.RegisterAdminInventoryServiceHTTPServer(srv, invSvc)
	adminv1.RegisterAdminOrderServiceHTTPServer(srv, orderAdminSvc)
	adminv1.RegisterAdminPaymentServiceHTTPServer(srv, payAdminSvc)
	storefrontv1.RegisterStoreOrderServiceHTTPServer(srv, orderStoreSvc)
	storefrontv1.RegisterStoreCartServiceHTTPServer(srv, cartSvc)
	storefrontv1.RegisterStorePaymentServiceHTTPServer(srv, payStoreSvc)
	storefrontv1.RegisterStoreWalletServiceHTTPServer(srv, walletStoreSvc)
	adminv1.RegisterAdminWalletServiceHTTPServer(srv, walletAdminSvc)
	adminv1.RegisterAdminFulfillmentServiceHTTPServer(srv, fulfillAdminSvc)
	storefrontv1.RegisterStoreDeliveryServiceHTTPServer(srv, fulfillStoreSvc)
	storefrontv1.RegisterStoreCatalogServiceHTTPServer(srv, catalogSvc)
	storefrontv1.RegisterStoreReviewServiceHTTPServer(srv, reviewSvc)
	supplyv1.RegisterSupplyServiceHTTPServer(srv, supplyAPISvc)
	adminv1.RegisterAdminSupplyServiceHTTPServer(srv, supplyAdminSvc)
	adminv1.RegisterAdminProcurementServiceHTTPServer(srv, procureAdminSvc)
	storefrontv1.RegisterStoreSocialProductServiceHTTPServer(srv, socialProductSvc)
	storefrontv1.RegisterStoreSocialOrderServiceHTTPServer(srv, socialOrderSvc)
	storefrontv1.RegisterStoreSMSServiceHTTPServer(srv, smsSvc)
	storefrontv1.RegisterStoreSMSChannelServiceHTTPServer(srv, smsChannelSvc)
	adminv1.RegisterAdminSupplierServiceHTTPServer(srv, supplierAdminSvc)
	adminv1.RegisterAdminContentServiceHTTPServer(srv, contentAdminSvc)
	storefrontv1.RegisterStoreContentServiceHTTPServer(srv, contentStoreSvc)
	adminv1.RegisterAdminNotifyServiceHTTPServer(srv, notifyAdminSvc)
	storefrontv1.RegisterStoreNotificationServiceHTTPServer(srv, notifyStoreSvc)
	adminv1.RegisterAdminAuditServiceHTTPServer(srv, auditAdminSvc)
	storefrontv1.RegisterStoreCouponServiceHTTPServer(srv, couponStoreSvc)
	storefrontv1.RegisterStoreTicketServiceHTTPServer(srv, ticketStoreSvc)
	adminv1.RegisterAdminTicketServiceHTTPServer(srv, ticketAdminSvc)
	storefrontv1.RegisterStoreAffiliateServiceHTTPServer(srv, affiliateStoreSvc)
	mediaAdminSvc.RegisterVideoUpload(srv)
	adminv1.RegisterAdminMediaServiceHTTPServer(srv, mediaAdminSvc)
	adminv1.RegisterAdminLicenseServiceHTTPServer(srv, licenseAdminSvc)
	adminv1.RegisterAdminResellerServiceHTTPServer(srv, resellerAdminSvc)
	storefrontv1.RegisterStoreUserServiceHTTPServer(srv, userStoreSvc)
	storefrontv1.RegisterStoreSupplierServiceHTTPServer(srv, supplierStoreSvc)
	adminv1.RegisterAdminUserManageServiceHTTPServer(srv, userManageSvc)
	// ：素材靜態服務（ETag + 白名單擴展；目錄列表禁用）
	media.RegisterStatic(srv)
	// 主題模板靜態服務（預覽圖；白名單圖片擴展 + 防穿越）
	settings.RegisterTemplateStatic(srv)

	// B/C：協議兼容層（對外供貨——讓 dujiao-next / acg-faka 平台不改代碼
	// 即可對接本站）。原生 handler 掛載（外部協議契約不進我們的 proto/OpenAPI）；
	// 不掛 JWT（架構測試規則 9）；須在 SPA 兜底前註冊。
	supplier.RegisterDujiaoCompat(srv, supplyAPISvc)
	supplier.RegisterAcgFakaCompat(srv, supplyAPISvc)

	// E：上游回調接收（採購三通道之回調；不掛 JWT，驗簽在 handler 內按驅動分支）
	procurement.RegisterUpstreamCallback(srv, procureSvc, upstreamGW)

	// 在線更新管理面（；system:update 超管專屬）
	adminv1.RegisterAdminUpdateServiceHTTPServer(srv, updateSvc)

	// 保留路徑（規劃 ：/api /uploads /health /payments /install 為保留前綴）
	registerHealth(srv, d, enq)

	// 啓動對賬（ fail-fast）：管理路由未聲明權限點 → 拒絕啓動
	if err := reconcileRoutes(srv, dir); err != nil {
		panic(err)
	}
	// ：審計過濾器路徑 → operation 映射緩存（對賬通過後必命中）
	buildAuditOpMap(dir)
	registerPaymentCallback(srv, payRepo, d)

	// SEO 基礎（robots.txt / sitemap.xml）：動態生成，須在 SPA 兜底前註冊
	registerSEO(srv, seoSvc)

	// fullstack SPA（）：storefront 兜底根 + admin 獨立前綴；未匹配回落
	// index.html 由前端路由接管。保留前綴（/api /uploads /health /payments
	// /install）已註冊的業務/靜態路由優先命中，回落僅在未匹配時發生。
	adminBase := "/admin"
	if c != nil && c.AdminBasePath != "" {
		adminBase = c.AdminBasePath
	}
	if err := settingsSvc.SetAdminBasePath(adminBase); err != nil {
		panic(err)
	}
	if web.Available() {
		admin := web.NewAdminHandler()
		srv.HandlePrefix("/", web.NewEntryHandler(settingsSvc.ThemeMiddleware(web.NewStorefrontHandler(seoSvc, settingsSvc.ActiveTheme)), admin.ServeAt, settingsSvc.AdminPath, "/"+strings.Trim(adminBase, "/")))
	}
	return srv
}

// registerSEO robots.txt + sitemap.xml（SPA 兜底前註冊；均免鑑權）。
func registerSEO(srv *khttp.Server, seoSvc *seo.SeoService) {
	srv.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(seoSvc.RobotsTXT(r.Context(), r.Host)))
	})
	srv.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		body, err := seoSvc.SitemapXML(r.Context(), r.Host)
		if err != nil {
			http.Error(w, "sitemap generate failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(body))
	})
}

// registerHealth 健康檢查（DB 連通 + 隊列模式 + 版本；/metrics Prometheus 接入內網口）。
func registerHealth(srv *khttp.Server, d *data.Data, enq queue.Enqueuer) {
	srv.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := contextWithTimeout(r)
		defer cancel()
		dbOK := d.Ping(ctx) == nil
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  map[string]bool{"server": true, "database": dbOK},
			"queue":   map[string]any{"enabled": enq.Enabled()},
			"version": Version,
			"dialect": string(d.Dialect),
		})
	})
}

// registerPaymentCallback 支付回調（ 統一入口——四重校驗+冪等+markPaid）。
// 不掛 JWT（架構測試規則 9）；驗籤由渠道適配器完成（ 接真實渠道）。
func registerPaymentCallback(srv *khttp.Server, payRepo *payment.PaymentRepoImpl, d *data.Data) {
	payment.RegisterPaymentCallback(srv, payRepo, d)
}

// adminOpOfPath 請求路徑 → Kratos operation（審計權限點查詢用）。
// 生成路由的 operation = /<package>.<Service>/<Method>；對賬目錄已建立
// Method+Path → operation 映射，此處經 WalkRoute 反查（啓動時緩存）。
func adminOpOfPath(r *http.Request) string {
	auditOpMu.Lock()
	defer auditOpMu.Unlock()
	if auditOpMap == nil {
		return ""
	}
	return auditOpMap[r.Method+" "+r.URL.Path]
}

// auditOpMap (Method Path) → operation 緩存（NewHTTPServer 裝配時填充）。
var (
	auditOpMap map[string]string
	auditOpMu  sync.Mutex
)

// buildAuditOpMap 從對賬目錄構建路徑映射。
func buildAuditOpMap(dir *authz.Directory) {
	m := map[string]string{}
	for _, p := range dir.All() {
		m[p.Method+" "+p.Path] = p.Op
	}
	auditOpMu.Lock()
	auditOpMap = m
	auditOpMu.Unlock()
}

func tenancyMainDomain(c *conf.Server) string { return "" } // 由 bootstrap 注入 Tenancy 配置後補齊

func contextWithTimeout(r *http.Request) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(r.Context(), 3*time.Second)
}

// Order data and bearer credentials must never be stored by shared caches.
func privateOrderResponseFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/storefront/orders") ||
			strings.HasPrefix(r.URL.Path, "/api/v1/storefront/payments") ||
			r.URL.Path == "/api/v1/storefront/payment/quote" ||
			(strings.HasPrefix(r.URL.Path, "/api/v1/admin/inventory/cards/") && strings.HasSuffix(r.URL.Path, "/content")) {
			w.Header().Set("Cache-Control", "no-store, private")
		}
		next.ServeHTTP(w, r)
	})
}

// corsFilter CORS 過濾器（開發階段前後端聯調）。
func corsFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,PATCH,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Requested-With,Accept-Language,Idempotency-Key,X-Order-Access-Token")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestDeadlineFilter(normal time.Duration) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			timeout := normal
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/media/video" {
				timeout = 10 * time.Minute
			}
			if timeout <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
