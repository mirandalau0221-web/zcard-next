package seo

// robots.txt + sitemap.xml 生成（設置 site.url 優先作 URL 基準，未配置回落請求 Host）。

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
)

// SeoService SEO 基礎服務。
type SeoService struct {
	catalog storefrontv1.StoreCatalogServiceServer
	repo    *SeoRepo
	cfg     *settings.RepoImpl
}

// NewSeoService 構造。
func NewSeoService(repo *SeoRepo, cfg *settings.RepoImpl, catalog storefrontv1.StoreCatalogServiceServer) *SeoService {
	return &SeoService{repo: repo, cfg: cfg, catalog: catalog}
}

// siteURL 站點 URL 基準：site.url 優先（去尾斜槓），空則 https://host 兜底。
func (s *SeoService) siteURL(ctx context.Context, host string) string {
	return s.loadSite(ctx).base(host)
}

// robotsDisallows 私有/工具頁默認禁抓（登錄註冊、會員中心、安裝嚮導等——
// 無收錄價值且靜態殼 canonical 指向首頁，放行只會製造重複信號）。
var robotsDisallows = []string{
	"/login", "/register", "/forgot-password",
	"/member", "/cart", "/order", "/payment",
	"/coupons", "/tickets", "/withdraw", "/affiliate", "/fetch", "/install",
}

// RobotsTXT robots.txt：私有頁禁抓 + 自定義規則追加 + Sitemap 指向。
func (s *SeoService) RobotsTXT(ctx context.Context, host string) string {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	for _, p := range robotsDisallows {
		b.WriteString("Disallow: " + p + "\n")
	}
	b.WriteString("Allow: /\n")
	if raw, err := s.cfg.GetDefault(ctx, "site", "robots_custom", nil); err == nil && len(raw) > 0 {
		var v string
		if json.Unmarshal(raw, &v) == nil && v != "" {
			b.WriteString("\n")
			b.WriteString(strings.TrimSpace(v))
			b.WriteString("\n")
		}
	}
	b.WriteString(fmt.Sprintf("\nSitemap: %s/sitemap.xml\n", s.siteURL(ctx, host)))
	return b.String()
}

// sitemapEntry sitemap URL 條目。
type sitemapEntry struct {
	Loc     string `xml:"loc"`
	Lastmod string `xml:"lastmod,omitempty"`
}

type sitemapURLSet struct {
	XMLName xml.Name       `xml:"urlset"`
	Xmlns   string         `xml:"xmlns,attr"`
	URLs    []sitemapEntry `xml:"url"`
}

const sitemapNS = "http://www.sitemaps.org/schemas/sitemap/0.9"

func lastmodOf(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02")
}

// SitemapXML sitemap.xml：靜態頁 + 商品/文章/分類動態條目。
func (s *SeoService) SitemapXML(ctx context.Context, host string) (string, error) {
	base := s.siteURL(ctx, host)
	entries := []sitemapEntry{
		{Loc: base + "/"},
		{Loc: base + "/products"},
		{Loc: base + "/posts"},
		{Loc: base + "/points"},
	}

	products, err := s.repo.ListSitemapProducts(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range products {
		entries = append(entries, sitemapEntry{
			Loc:     fmt.Sprintf("%s/product/%d", base, p.ID),
			Lastmod: lastmodOf(p.UpdatedAt),
		})
	}

	posts, err := s.repo.ListSitemapPosts(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range posts {
		entries = append(entries, sitemapEntry{
			Loc:     fmt.Sprintf("%s/posts/%s", base, url.PathEscape(p.Slug)),
			Lastmod: lastmodOf(p.PublishedAt),
		})
	}
	// 注：分類篩選 URL（/products?category_id=N）不進 sitemap——查詢串不參與
	// 靜態頁匹配，服務端返回與 /products 相同的靜態頁（canonical 也指向 /products），
	// 提交這些 URL 只會製造「重複網頁，未選定規範網頁」的 GSC 報錯。

	out, err := xml.MarshalIndent(sitemapURLSet{Xmlns: sitemapNS, URLs: entries}, "", "  ")
	if err != nil {
		return "", err
	}
	return xml.Header + string(out) + "\n", nil
}
