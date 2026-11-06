package audit

// 風控內核：IP 規範化（IPv6 /64 聚合）+ 黑名單（精確 + CIDR）+ 進程內緩存。
// 友商 redislimiter 模式：黑名單進程內緩存 + 哈希查找；pending 閘門事務內複查。

import (
	"net"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
)

// ipBlacklist IP 黑名單（進程內緩存；admin 變更時失效重建）。
type ipBlacklist struct {
	exact map[string]bool
	cidrs []*net.IPNet
}

// parseBlacklist 解析黑名單條目（精確 IP 或 CIDR；非法條目跳過不阻斷）。
func parseBlacklist(entries []string) *ipBlacklist {
	bl := &ipBlacklist{exact: map[string]bool{}, cidrs: nil}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, ipnet, err := net.ParseCIDR(e); err == nil {
				bl.cidrs = append(bl.cidrs, ipnet)
			}
			continue
		}
		// 精確 IP：規範化後存（IPv6 已 /64 聚合——黑名單配置與閘門口徑一致）
		bl.exact[port.NormalizeIP(e)] = true
	}
	return bl
}

// contains 命中判定（ip 必須已規範化）。
func (bl *ipBlacklist) contains(ip string) bool {
	if bl == nil {
		return false
	}
	if bl.exact[ip] {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, ipnet := range bl.cidrs {
		if ipnet.Contains(parsed) {
			return true
		}
	}
	return false
}
