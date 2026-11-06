package supplier

// IP 白名單判定（三協議鑑權統一入口）：
//   - 空名單 = 所有 IP 放行（默認，兼容存量賬户）；
//   - 非空 = 請求來源 IP 必須命中任一條目（精確 IP 或 CIDR 網段）；
//   - 客户端 IP 取 X-Forwarded-For 首段（反向代理場景），否則 RemoteAddr 主機部分。
//
// 條目格式：IPv4/IPv6 精確地址（1.2.3.4、::1）或 CIDR（10.0.0.0/24、fd00::/8）。

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// maxWhitelistEntries 白名單條目上限（防濫用）。
const maxWhitelistEntries = 20

// ValidateIPWhitelistEntry 校驗單條白名單（精確 IP 或 CIDR）。
func ValidateIPWhitelistEntry(entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return fmt.Errorf("條目不能為空")
	}
	if strings.Contains(entry, "/") {
		if _, _, err := net.ParseCIDR(entry); err != nil {
			return fmt.Errorf("%q 不是合法的 CIDR 網段", entry)
		}
		return nil
	}
	if net.ParseIP(entry) == nil {
		return fmt.Errorf("%q 不是合法的 IP 地址", entry)
	}
	return nil
}

// requestClientIP 請求來源 IP（XFF 首段優先——經反代時 RemoteAddr 是代理地址）。
func requestClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipAllowed 白名單判定（空名單放行；非空須命中任一條目）。
func ipAllowed(whitelist []string, r *http.Request) bool {
	if len(whitelist) == 0 {
		return true
	}
	clientIP := requestClientIP(r)
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	for _, entry := range whitelist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if entry == clientIP {
			return true
		}
	}
	return false
}
