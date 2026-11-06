package server

// 中間件：租戶解析（Host → tenancy.Context）與 admin realm JWT 鑑權（雙 realm 防串用）。
//
// 權限判定經 authz.Directory（模塊聲明式目錄，）——本文件不再手工維護映射；
// 目錄 miss = fail-closed：403 + 錯誤日誌（新增路由漏聲明會在啓動對賬時直接失敗，
// 運行時 miss 屬異常路徑，絕不默認放行）。
//
// operation 形態（Kratos v3）：
// - HTTP：生成的 pb 路由 operation = proto 方法全名（帶前導斜槓）
// - gRPC：同構

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/mods/audit"
	auditport "github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	identityport "github.com/NovaWorks/zcard-next/server/internal/mods/identity/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/log"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// DomainResolver 域名→租戶解析端口（reseller 模塊實現；server 不依賴業務實現）。
type DomainResolver interface {
	ResolveDomain(ctx context.Context, host string) (subsiteID uint64, siteName string, err error)
}

// domainCache 域名解析緩存（30s——reseller_sites 變更窗口）。
type domainCache struct {
	mu  sync.RWMutex
	m   map[string]domainCacheEntry
	ttl time.Duration
}
type domainCacheEntry struct {
	subsiteID uint64
	siteName  string
	at        time.Time
}

var dcache = &domainCache{m: map[string]domainCacheEntry{}, ttl: 30 * time.Second}

func (c *domainCache) get(host string) (uint64, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[host]
	if !ok || time.Since(e.at) > c.ttl {
		return 0, "", false
	}
	return e.subsiteID, e.siteName, true
}

func (c *domainCache) put(host string, subsiteID uint64, siteName string) {
	c.mu.Lock()
	c.m[host] = domainCacheEntry{subsiteID: subsiteID, siteName: siteName, at: time.Now()}
	c.mu.Unlock()
}

// tenantFilter 域名解析過濾器（）。
// http.Filter 層實現（Kratos 中間件的 Transporter 是內部 *khttp.Transport——
// 拿不到 Host 字段，恆主站；與 audit/supplier 同款 Filter 模式）。
// Host → verified 分站域名 → 分站上下文；未匹配/未驗證 → 主站兜底（fail-open 絕不 5xx）。
// req.WithContext 後續中間件與業務 handler 自動繼承租戶上下文。
func tenantFilter(mainDomain string, resolver DomainResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc := tenancy.Context{SubsiteID: tenancy.MainSubsiteID, IsMain: true, Host: r.Host}
			if resolver != nil && r.Host != "" && r.Host != mainDomain {
				host := strings.Split(r.Host, ":")[0]
				if id, _, cached := dcache.get(host); cached {
					tc.SubsiteID, tc.IsMain = id, id == tenancy.MainSubsiteID
				} else if id, _, err := resolver.ResolveDomain(r.Context(), host); err == nil && id > 0 {
					dcache.put(host, id, "")
					tc.SubsiteID, tc.IsMain = id, false
				} else {
					dcache.put(host, 0, "") // 負緩存（主站）30s
				}
			}
			next.ServeHTTP(w, r.WithContext(tenancy.WithContext(r.Context(), tc)))
		})
	}
}

// userAuthMiddleware storefront 用戶 realm JWT（storefront 面需要登錄態的端點：
// wallet/ticket/affiliate/user.me）。解析失敗放行（claims 為 nil，業務端自行 401——
// 避免遊客可訪問的列表端點被一刀切）。
func userAuthMiddleware(signer *authn.Signer) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if tr, ok := transport.FromServerContext(ctx); ok {
				token := bearerToken(tr.RequestHeader().Get("Authorization"))
				if token != "" {
					if claims, err := signer.Verify(authn.RealmUser, token); err == nil {
						ctx = identity.WithClaims(ctx, claims)
					}
				}
			}
			return handler(ctx, req)
		}
	}
}

func hostOf(tr transport.Transporter) string {
	if hc, ok := tr.(khttp.Context); ok {
		h := hc.Request().Host
		if h == "" {
			h = hc.Header().Get("X-Forwarded-Host")
		}
		return h
	}
	return ""
}

const (
	adminPathPrefix = "/api/v1/admin/"
	adminOpPrefix   = "/zcard.api.admin.v1."
)

// isAdminOperation 是否管理面操作（admin realm 鑑權目標；Public 聲明的登錄路由豁免）。
// storefront/supply/回調路由一律不掛 JWT（架構測試規則 9）。
func isAdminOperation(operation string, dir *authz.Directory) bool {
	if strings.HasPrefix(operation, adminOpPrefix) {
		if _, public, ok := dir.PermissionForOp(operation); ok && public {
			return false
		}
		return true
	}
	return strings.HasPrefix(operation, adminPathPrefix)
}

// adminAuthMiddleware admin realm 鑑權：JWT 校驗 → 賬戶實時狀態（enabled + 當前
// RoleID，禁用/換角色即時生效）→ 權限點校驗（RBAC）→ 注入 claims。
// 超管角色（*）通配放行（authz 模塊）。
//
// 每請求按主鍵回查 admin_users 一次（管理面低頻、PK get 代價可忽略）——JWT 內的
// RoleID 是簽發時快照，換角色後舊 token 若沿用快照將保留舊權限直至過期，此處
// 以庫中當前值為準（fail-closed：賬戶不存在/查詢失敗一律 401）。
func adminAuthMiddleware(signer *authn.Signer, az port.Authorizer, dir *authz.Directory, admins identityport.AdminReader) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return nil, errors.Unauthorized("identity.UNAUTHORIZED", "缺少 transport 上下文")
			}
			token := bearerToken(tr.RequestHeader().Get("Authorization"))
			if token == "" {
				return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
			}
			claims, err := signer.Verify(authn.RealmAdmin, token)
			if err != nil {
				return nil, errors.Unauthorized("identity.UNAUTHORIZED", "令牌無效或已過期")
			}
			acc, err := admins.Admin(ctx, claims.Subject)
			if err != nil || acc == nil {
				return nil, errors.Unauthorized("identity.UNAUTHORIZED", "賬戶不存在")
			}
			if claims.AuthVersion != acc.AuthVersion {
				return nil, errors.Unauthorized("identity.SESSION_INVALID", "安全設置已變更，請重新登錄")
			}
			if !acc.Enabled {
				return nil, errors.Unauthorized("identity.ADMIN_DISABLED", "賬號已禁用")
			}
			claims.RoleID = acc.RoleID
			if perm, _, ok := dir.PermissionForOp(tr.Operation()); ok && !az.Allowed(ctx, claims.RoleID, perm) {
				return nil, errors.Forbidden("authz.PERMISSION_DENIED", "權限不足")
			}
			return handler(identity.WithClaims(ctx, claims), req)
		}
	}
}

// storefrontVisitMiddleware storefront 訪問埋點（）：PV/UV 明細 + 登錄用戶在線心跳。
// 註冊在 userAuthMiddleware 之後（需已解析 claims）；僅 selector 匹配 storefront
// operation；埋點寫失敗忽略——統計性質絕不阻斷業務請求。
func storefrontVisitMiddleware(tracker *audit.TrackRepo) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if tracker != nil {
				if r, ok := khttp.RequestFromServerContext(ctx); ok {
					var userID uint64
					if claims := identity.ClaimsFromContext(ctx); claims != nil {
						userID = claims.Subject
					}
					ip := r.RemoteAddr
					if host, _, err := net.SplitHostPort(ip); err == nil {
						ip = host
					}
					tracker.RecordVisit(ctx, tenancy.FromContext(ctx).SubsiteID, r.URL.Path, userID, auditport.NormalizeIP(ip))
				}
			}
			return handler(ctx, req)
		}
	}
}

func bearerToken(header string) string {
	if v, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// reconcileRoutes 啓動對賬（ fail-fast）：真實路由表逐條核對 admin 前綴必須已聲明權限點。
func reconcileRoutes(srv *khttp.Server, dir *authz.Directory) error {
	var routes []authz.RouteInfo
	err := srv.WalkRoute(func(ri khttp.RouteInfo) error {
		routes = append(routes, authz.RouteInfo{Method: ri.Method, Path: ri.Path})
		return nil
	})
	if err != nil {
		return err
	}
	if err := dir.Reconcile(routes, adminPathPrefix); err != nil {
		return err
	}
	log.Default().Info("authz.reconciled", "routes", len(routes), "perms", len(dir.Codes()))
	return nil
}
