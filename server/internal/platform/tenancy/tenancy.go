// Package tenancy 租戶上下文（規劃 §4.9 / §4.11）。
//
// 開源版只啓用 Row 行級隔離（subsite_id，0=主站）；Schema/Database 模式與
// 遠程庫屬商業版（字段已定、代碼不開源，ADR-D15）。subsite_id 列在三種模式下
// 統一保留，Ent schema 定義不因模式而變。
//
// 本包只承載「業務形態無關」的租戶上下文與純領域邏輯；涉及 *ent.Client 的
// TenantStore 抽象（Resolve/Migrate/Export/Import/Ping）定義在 internal/data
// （ent 收口邊界：platform 不 import ent，架構測試 §4.10-5）。
package tenancy

import (
	"context"
	"errors"
)

// 主站租戶 ID。
const MainSubsiteID uint64 = 0

// Context 租戶上下文（請求級，經中間件注入）。
// M1 交付 Ent interceptor：讀攔截器自動追加 subsite_id 過濾、寫攔截器從父實體
// 繼承填充——業務代碼不手寫租戶條件（鐵律 14）。
type Context struct {
	// SubsiteID 租戶 ID（0=主站）
	SubsiteID uint64
	// IsMain 是否主站請求
	IsMain bool
	// Host 請求 Host（域名解析中間件留痕，分站域名驗證 M3）
	Host string
}

type ctxKey struct{}

// ErrNoTenant 上下文缺失租戶信息（中間件未裝配或非 HTTP 入口）。
var ErrNoTenant = errors.New("tenancy: 上下文缺失租戶信息")

// WithContext 注入租戶上下文。
func WithContext(ctx context.Context, tc Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

// FromContext 取出租戶上下文；缺失時返回主站兜底（fail-open 到主站僅限
// 非 HTTP 入口如 worker：worker 消費事件時由事件載荷攜帶租戶）。
func FromContext(ctx context.Context) Context {
	if tc, ok := ctx.Value(ctxKey{}).(Context); ok {
		return tc
	}
	return Context{SubsiteID: MainSubsiteID, IsMain: true}
}

// Main 主站上下文（worker/定時任務的兜底）。
func Main() Context { return Context{SubsiteID: MainSubsiteID, IsMain: true} }
