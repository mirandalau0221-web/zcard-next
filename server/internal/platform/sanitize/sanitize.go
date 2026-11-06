// Package sanitize HTML 白名單清洗（規劃 §5.20.5 防 XSS）。
// 商品描述、評價內容、文章內容入庫前必經；卡密前端展示配 textContent（前端紀律）。
package sanitize

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// 允許的基礎標籤（白名單制——不在清單內的一律剝離）
	tags := []string{
		"a", "abbr", "b", "blockquote", "br", "code", "dd", "del", "div",
		"dl", "dt", "em", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "i",
		"img", "ins", "li", "ol", "p", "pre", "q", "s", "small", "span",
		"strong", "sub", "sup", "table", "tbody", "td", "tfoot", "th",
		"thead", "tr", "u", "ul",
	}
	for _, t := range tags {
		p.AllowElements(t)
	}

	// 屬性白名單
	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("src", "alt", "width", "height").OnElements("img")
	p.AllowAttrs("class").OnElements("div", "span", "p", "code", "pre")
	p.AllowAttrs("id").OnElements("div", "span")
	p.AllowAttrs("colspan", "rowspan").OnElements("td", "th")
	p.AllowAttrs("start").OnElements("ol")

	// URL 協議白名單
	p.AllowURLSchemes("http", "https", "mailto")
	// 相對 URL 放行（富文本插圖 = 素材庫同源路徑 /uploads/**；協議白名單對絕對 URL 仍生效，
	// data:/javascript: 一律剝離——wangEditor 輸出對拍見 sanitize_wang_test.go）
	p.AllowRelativeURLs(true)

	// 強制 target=_blank rel=noopener（防釣魚）
	p.AllowAttrs("target").Matching(bluemonday.SpaceSeparatedTokens).OnElements("a")
	p.RequireNoFollowOnLinks(true)
	p.RequireNoFollowOnFullyQualifiedLinks(true)

	return p
}

var policy = newPolicy()

// HTML 清洗（白名單外標籤/屬性/協議全部剝離；script/style/on* 事件一律幹掉）。
func HTML(input string) string {
	return policy.Sanitize(input)
}

// Text 純文本（全部標籤剝離——卡密展示用）。
func Text(input string) string {
	return bluemonday.StrictPolicy().Sanitize(input)
}

// RichHTML is limited to administrator-authored product and article content.
// Other inputs retain the existing image/text policy.
var richPolicy = func() *bluemonday.Policy {
	p := newPolicy()
	p.AllowElements("video", "source")
	p.AllowAttrs("src", "poster").Matching(regexp.MustCompile(`^(https://[^\s]+|/uploads/[^\s]+)$`)).OnElements("video")
	p.AllowAttrs("src").Matching(regexp.MustCompile(`^(https://[^\s]+|/uploads/[^\s]+)$`)).OnElements("source")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^video/mp4$`)).OnElements("source")
	p.AllowAttrs("controls", "playsinline").OnElements("video")
	p.AllowAttrs("preload").Matching(regexp.MustCompile(`^(none|metadata)$`)).OnElements("video")
	p.AllowAttrs("data-w-e-type").Matching(regexp.MustCompile(`^video$`)).OnElements("video")
	p.AllowAttrs("data-w-e-is-void", "data-w-e-is-inline").Matching(regexp.MustCompile(`^true$`)).OnElements("video")
	return p
}()

func RichHTML(input string) string {
	clean := richPolicy.Sanitize(input)
	if !strings.Contains(clean, "<video") {
		return clean
	}
	// Normalize before storage/SSG: browsers may start loading a video before
	// Vue hydrates it, so applying preload=none only on mount is too late.
	z := html.NewTokenizer(strings.NewReader(clean))
	var out strings.Builder
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			token := z.Token()
			if token.Data == "video" {
				attrs := make([]html.Attribute, 0, len(token.Attr)+3)
				for _, attr := range token.Attr {
					if attr.Key != "preload" && attr.Key != "controls" && attr.Key != "playsinline" {
						attrs = append(attrs, attr)
					}
				}
				token.Attr = append(attrs, html.Attribute{Key: "preload", Val: "none"}, html.Attribute{Key: "controls"}, html.Attribute{Key: "playsinline"})
				out.WriteString(token.String())
				continue
			}
		}
		out.Write(z.Raw())
	}
	return out.String()
}
