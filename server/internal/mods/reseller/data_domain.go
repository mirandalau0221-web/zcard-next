package reseller

// 域名驗證雙方案（）：
// DNS TXT：_zcard-verify.<domain> TXT "<token>"
// HTTP：http(s)://<domain>/.well-known/zcard-verify.txt 內容 = token
// 安全：HTTP 拉取經 httpx（SSRF）；DNS 解析釘公網 IP（只信公網解析結果——
// rebinding 由 httpx 連接期複核兜底）。
// 驗證通過 → verification_status=verified → tenantMiddleware 生效。

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellersite"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/httpx"
)

// AddSite 登記域名（生成驗證 token；pending）。
func (r *ResellerRepo) AddSite(ctx context.Context, profileID uint64, domain, siteName string, isPrimary bool) (*ent.ResellerSite, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "http://"), "https://")
	domain = strings.Split(domain, "/")[0]
	if domain == "" || !strings.Contains(domain, ".") {
		return nil, fmt.Errorf("reseller.DOMAIN_INVALID")
	}
	token, err := genVerifyToken()
	if err != nil {
		return nil, err
	}
	create := data.Client(ctx, r.data).ResellerSite.Create().
		SetProfileID(profileID).
		SetDomain(domain).
		SetType("custom").
		SetVerificationToken(token).
		SetVerificationStatus(resellersite.VerificationStatusPending).
		SetIsPrimary(isPrimary)
	if siteName != "" {
		create.SetSiteName(siteName)
	}
	s, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, fmt.Errorf("reseller.DOMAIN_TAKEN")
	}
	return s, err
}

// VerifySite 驗證域名（雙方案：DNS 通過即過；DNS 失敗試 HTTP）。
// 返回 (verified, method, err)。
func (r *ResellerRepo) VerifySite(ctx context.Context, siteID uint64) (bool, string, error) {
	client := data.Client(ctx, r.data)
	site, err := client.ResellerSite.Get(ctx, siteID)
	if err != nil {
		return false, "", ErrNotFound
	}
	token := site.VerificationToken
	domain := site.Domain

	// 方案一：DNS TXT
	if ok, err := verifyDNSTXT(ctx, domain, token); err == nil && ok {
		_, _ = client.ResellerSite.UpdateOneID(siteID).
			SetVerificationStatus(resellersite.VerificationStatusVerified).
			Save(ctx)
		return true, "dns", nil
	}
	// 方案二：HTTP well-known（經 httpx SSRF 防護；釘公網解析）
	if ok, err := verifyHTTPWellKnown(ctx, domain, token); err == nil && ok {
		_, _ = client.ResellerSite.UpdateOneID(siteID).
			SetVerificationStatus(resellersite.VerificationStatusVerified).
			Save(ctx)
		return true, "http", nil
	}
	_, _ = client.ResellerSite.UpdateOneID(siteID).
		SetVerificationStatus(resellersite.VerificationStatusFailed).
		Save(ctx)
	return false, "", nil
}

// verifyDNSTXT 查 _zcard-verify.<domain> TXT 記錄匹配 token。
func verifyDNSTXT(ctx context.Context, domain, token string) (bool, error) {
	resolver := &net.Resolver{}
	txts, err := resolver.LookupTXT(ctx, "_zcard-verify."+domain)
	if err != nil {
		return false, err
	}
	for _, txt := range txts {
		if strings.TrimSpace(txt) == token {
			return true, nil
		}
	}
	return false, nil
}

// verifyHTTPWellKnown 拉 /.well-known/zcard-verify.txt 比對 token。
func verifyHTTPWellKnown(ctx context.Context, domain, token string) (bool, error) {
	for _, scheme := range []string{"https", "http"} {
		url := fmt.Sprintf("%s://%s/.well-known/zcard-verify.txt", scheme, domain)
		if err := httpx.ValidateURL(url); err != nil {
			continue // 域名解析到內網：拒絕（不區分探測細節）
		}
		c := httpx.NewSafeClient(10 * time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", httpx.UserAgent)
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		if resp.StatusCode == 200 && strings.TrimSpace(string(buf[:n])) == token {
			return true, nil
		}
	}
	return false, nil
}

// ResolveBrand 站點品牌（白標）解析（notify 端口實現；分站訂單郵件用）。
// 取分站主站名/LOGO；無白標配置返回 ok=false（fail-closed——調用方不得回退主站品牌）。
func (r *ResellerRepo) ResolveBrand(ctx context.Context, subsiteID uint64) (notifyport.Brand, bool) {
	client := data.Client(ctx, r.data)
	site, err := client.ResellerSite.Query().
		Where(resellersite.ProfileID(subsiteID)).
		Order(ent.Desc(resellersite.FieldIsPrimary), ent.Asc(resellersite.FieldID)).
		First(ctx)
	if err != nil || site.SiteName == "" {
		return notifyport.Brand{}, false
	}
	return notifyport.Brand{SiteName: site.SiteName, Logo: site.Logo}, true
}

// ResolveDomain 域名 → subsite_id（verified 域名；profile 必須 approved）。
// 未驗證/未審核返回 0（主站兜底）。
func (r *ResellerRepo) ResolveDomain(ctx context.Context, host string) (uint64, string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return 0, "", nil
	}
	host = strings.Split(host, ":")[0] // 去端口
	client := data.Client(ctx, r.data)
	site, err := client.ResellerSite.Query().
		Where(
			resellersite.DomainEQ(host),
			resellersite.VerificationStatusEQ(resellersite.VerificationStatusVerified),
		).Only(ctx)
	if ent.IsNotFound(err) {
		return 0, "", nil // 未驗證域名 → 主站
	}
	if err != nil {
		return 0, "", err
	}
	profile, err := client.ResellerProfile.Get(ctx, site.ProfileID)
	if err != nil || string(profile.Status) != "approved" {
		return 0, "", nil // 分站被禁/未過審 → 主站兜底
	}
	return profile.ID, site.SiteName, nil
}

func genVerifyToken() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0xf]
	}
	return string(out), nil
}
