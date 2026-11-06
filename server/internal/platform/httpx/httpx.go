// Package httpx 出站 HTTP 客户端（規劃 §5.7.3 貨源對接安全）。
//
// 統一強制（架構測試斷言，適配器無感知）：
//  1. 僅允許 http/https；
//  2. 私有地址段黑名單（SSRF 防護：迴環/內網/鏈路本地/ULA）——阻斷「供貨站填內網地址探測元數據」；
//  3. DNS rebinding 防護：連接建立時對實際 IP 複核（防「校驗後解析漂移到內網」）；
//  4. 重定向逐跳重校驗（禁止「白名單 → 302 → 內網」）；
//  5. 憑據與簽名串永不進日誌（RedactURL）。
package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// PrivateCIDRs 私有/保留地址段黑名單（§5.7.3 清單）。
// IPv4-mapped IPv6 由 IsPrivateIP 的 To4 歸一化覆蓋，不單列 CIDR
// （::ffff:0:0/96 經 net.IPNet.Contains 規範化後會誤判所有公網 IPv4）。
var privateCIDRs = mustCIDRs(
	"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"169.254.0.0/16", "0.0.0.0/8", "100.64.0.0/10",
	"::1/128", "fc00::/7", "fe80::/10",
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("httpx: 非法 CIDR " + c)
		}
		out = append(out, n)
	}
	return out
}

// allowPrivate 聯調開關（ZCARD_HTTPX_ALLOW_PRIVATE=1）：放行私有/保留段出站，
// 供本機多服務端到端聯調。生產嚴禁開啓（SSRF 防護旁路）。
var allowPrivate = os.Getenv("ZCARD_HTTPX_ALLOW_PRIVATE") == "1"

// IsPrivateIP 判斷 IP 是否落在私有/保留段（導出供測試與診斷）。
func IsPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range privateCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ErrBlockedAddress 目標地址被 SSRF 防護攔截。
type ErrBlockedAddress struct{ Host string }

func (e *ErrBlockedAddress) Error() string {
	return fmt.Sprintf("httpx: 目標地址被 SSRF 防護攔截（私有/保留段）: %s", e.Host)
}

// ValidateURL 校驗出站 URL：僅 http/https 且 host 不在私有段（按解析結果判定）。
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("httpx: 非法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("httpx: 僅允許 http/https，得到 %q", u.Scheme)
	}
	return checkHost(u.Hostname())
}

func checkHost(host string) error {
	_, err := resolveHost(context.Background(), host, net.DefaultResolver.LookupIPAddr)
	return err
}

type lookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)
type dialContextFunc func(context.Context, string, string) (net.Conn, error)

func resolveHost(ctx context.Context, host string, lookup lookupIPAddrFunc) ([]net.IPAddr, error) {
	var addrs []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		addrs = []net.IPAddr{{IP: ip}}
	} else {
		var err error
		addrs, err = lookup(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("httpx: 解析 %s 失敗: %w", host, err)
		}
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no addresses found", Name: host}
	}
	// Check the whole answer before dialing any member of a mixed answer.
	for _, a := range addrs {
		if !allowPrivate && IsPrivateIP(a.IP) {
			return nil, &ErrBlockedAddress{Host: host + " (" + a.String() + ")"}
		}
	}
	return addrs, nil
}

func safeDialContext(ctx context.Context, network, addr string, lookup lookupIPAddrFunc, dial dialContextFunc) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := resolveHost(ctx, host, lookup)
	if err != nil {
		return nil, err
	}
	candidates := make([]net.IPAddr, 0, len(addrs))
	for _, a := range addrs {
		if (network == "tcp4" && a.IP.To4() == nil) || (network == "tcp6" && a.IP.To4() != nil) {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return nil, &net.DNSError{Err: "no suitable addresses found", Name: host}
	}
	var firstErr error
	for i, a := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attemptCtx, cancel := context.WithCancel(ctx)
		if deadline, ok := ctx.Deadline(); ok && len(candidates)-i > 1 {
			cancel()
			// Share the remaining timeout so one unreachable address cannot
			// consume the entire connection budget and prevent fallback.
			attemptCtx, cancel = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(candidates)-i))
		}
		// Pin the checked IP. Passing the hostname here would resolve again.
		conn, err := dial(attemptCtx, network, net.JoinHostPort(a.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// NewSafeClient 構造安全出站客户端：超時 + 重定向逐跳校驗 + 連接期 IP 複核。
func NewSafeClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialCtx, cancel := context.WithTimeout(ctx, dialer.Timeout)
			defer cancel()
			return safeDialContext(dialCtx, network, addr, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("httpx: 重定向超過 10 跳")
			}
			return ValidateURL(req.URL.String())
		},
	}
}

// UserAgent 統一出站 UA（上游對接可識別來源）。
const UserAgent = "ZCard/2.0"

// Get 簡易 GET（帶 UA + SSRF 校驗）。適配器簽名頭定製請直接使用 NewSafeClient。
func Get(ctx context.Context, c *http.Client, rawURL string) (*http.Response, error) {
	if c == nil {
		c = NewSafeClient(15 * time.Second)
	}
	if err := ValidateURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	return c.Do(req)
}

// RedactURL 日誌脱敏：去除 userinfo（憑據永不進日誌，鐵律 §5.7.3）。
func RedactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return raw
}
