package migratev1

// 遷移階段定義與拓撲（與《數據遷移工具開發計劃》§4 一致）。
// P0 僅交付 preflight；P1 起逐階段填充執行器（phases.go 是 --phase 的唯一口徑）。

import (
	"fmt"
	"strconv"
	"strings"
)

// Phase 遷移階段（依賴拓撲順序即切片順序）。
type Phase struct {
	ID     int
	Key    string
	Name   string
	Tables []string // 涉及的 1.x 源表（報告與過濾用）
}

// Phases 全部階段（順序 = 執行順序）。
var Phases = []Phase{
	{0, "system", "系統與配置", []string{"settings", "currencies", "supply_sources", "user_groups"}},
	{1, "identity", "身份", []string{"users", "model_has_roles"}},
	{2, "catalog", "目錄", []string{"categories", "products", "product_skus", "reviews"}},
	{3, "inventory", "卡密", []string{"card_imports", "cards", "order_deliveries"}},
	{4, "trade", "交易", []string{"orders", "order_items", "payments", "recharges", "coupons"}},
	{5, "money", "資金與分銷", []string{"bills", "withdrawals", "commissions"}},
	{6, "reseller", "分站與供貨", []string{"merchants", "subsite_domains", "subsite_product_settings",
		"subsite_ledger_entries", "subsite_order_snapshots", "supplier_accounts",
		"supplier_product_prices", "supplier_ledger_entries", "supply_orders", "media", "visit_logs", "security_audit_logs"}},
}

// PhaseByID 按編號取階段。
func PhaseByID(id int) (Phase, bool) {
	for _, p := range Phases {
		if p.ID == id {
			return p, true
		}
	}
	return Phase{}, false
}

// ParsePhaseSpec 解析 --phase 取值："0-5" / "0,1,2" / "all" / 空=全部。
// 返回有序去重的階段編號列表。
func ParsePhaseSpec(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "all" {
		out := make([]int, len(Phases))
		for i, p := range Phases {
			out[i] = p.ID
		}
		return out, nil
	}
	var ids []int
	seen := map[int]bool{}
	add := func(n int) error {
		if _, ok := PhaseByID(n); !ok {
			return fmt.Errorf("階段編號 %d 不存在（0-%d）", n, len(Phases)-1)
		}
		if !seen[n] {
			seen[n] = true
			ids = append(ids, n)
		}
		return nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			l, err1 := strconv.Atoi(strings.TrimSpace(lo))
			h, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || l > h {
				return nil, fmt.Errorf("區間 %q 非法", part)
			}
			for n := l; n <= h; n++ {
				if err := add(n); err != nil {
					return nil, err
				}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("階段 %q 非法（數字 / 區間 / 逗號列表）", part)
		}
		if err := add(n); err != nil {
			return nil, err
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("--phase 為空")
	}
	return ids, nil
}

// PhaseSummary 人讀的階段清單（確認提示與報告用）。
func PhaseSummary(ids []int) string {
	if len(ids) == len(Phases) {
		return "全部（P0-P6）"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		if p, ok := PhaseByID(id); ok {
			parts[i] = fmt.Sprintf("P%d %s", p.ID, p.Name)
		}
	}
	return strings.Join(parts, " → ")
}
