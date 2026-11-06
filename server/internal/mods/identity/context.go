package identity

// 請求上下文中的 admin 身份（server 鑑權中間件注入；biz/service 層讀取）。

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
)

type claimsKey struct{}

// WithClaims 注入 admin JWT claims（server 中間件調用）。
func WithClaims(ctx context.Context, claims *authn.Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, claims)
}

// ClaimsFromContext 取出 claims；未登錄返回 nil。
func ClaimsFromContext(ctx context.Context) *authn.Claims {
	c, _ := ctx.Value(claimsKey{}).(*authn.Claims)
	return c
}
