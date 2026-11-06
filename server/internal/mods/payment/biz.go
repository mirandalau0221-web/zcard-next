// Package payment 支付模塊（M1a）：渠道/支付單/回調管線/補單/退款編排。
//
// 落地註冊表骨架；M1a 交付 alipay/wechat/epay/錢包四渠道 + 回調管線
// （事務內：行鎖 payment+order → 四重校驗 → 冪等 → markPaid）+ golden vector 契約測試
// （固定 key + 固定 body → 期望簽名/驗簽結果，1.x 最大測試缺口的門禁化）。
package payment

import (
	"fmt"
	"sort"
	"sync"

	"github.com/NovaWorks/zcard-next/server/internal/mods/payment/adapter"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
)

// ErrProviderUnknown 渠道未註冊（新增渠道 = 新增 adapter 文件 + 註冊）。
type ErrProviderUnknown struct{ Provider string }

func (e *ErrProviderUnknown) Error() string {
	return fmt.Sprintf("payment: 渠道 %q 未註冊", e.Provider)
}

// Registry 渠道註冊表（啓動裝配：各 adapter 自注冊）。
type Registry struct {
	mu        sync.RWMutex
	providers map[string]port.Provider
}

// Register 註冊渠道 adapter（init/裝配期調用）。
func (r *Registry) Register(p port.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Type()] = p
}

// Provider 按渠道取 adapter。
func (r *Registry) Provider(provider string) (port.Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[provider]
	if !ok {
		return nil, &ErrProviderUnknown{Provider: provider}
	}
	return p, nil
}

// All 全部已註冊 adapter（：admin 配置面驅動元數據遍歷）。
func (r *Registry) All() []port.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]port.Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })
	return out
}

// NewRegistry 構造註冊表並註冊內置渠道 adapter（alipay/wechat/epay）。
func NewRegistry() *Registry {
	r := &Registry{providers: map[string]port.Provider{}}
	// 內置渠道註冊失敗僅可能因實現缺陷（如重複 Type），這裏直接 panic 暴露問題。
	if err := adapter.RegisterAll(r); err != nil {
		panic(err)
	}
	return r
}

var _ port.Registry = (*Registry)(nil)

// PaymentUsecase 支付用例骨架（M1a 交付 CreatePayment/HandleCallback/退款編排）。
type PaymentUsecase struct {
	registry *Registry
}

// NewPaymentUsecase 構造。
func NewPaymentUsecase(reg *Registry) *PaymentUsecase {
	return &PaymentUsecase{registry: reg}
}
