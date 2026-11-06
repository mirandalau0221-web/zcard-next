// Package i18n 多語言文案（規劃 §5.15）。
//
// M0：最小實現（zh_TW 默認 + en 骨架，內存 bundle）；M3 交付 embed bundle 完整版
// 與 DB 覆蓋層（後台可改文案）。錯誤 message 走 i18n，前端按 reason 做文案與
// 重定向、不解析 message（§7.2）。
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Locale 語言標識（zh_TW / en；架構支持任意擴展）。
type Locale string

// 支持的語言。
const (
	ZhCN Locale = "zh_CN"
	ZhTW Locale = "zh_TW"
	En   Locale = "en"
)

// Default 默認語言（鐵律 9：文案默認繁體中文）。
const Default = ZhTW

//go:embed locales/*.json
var localeFS embed.FS

// Bundle 文案包（只讀；M3 增加 DB 覆蓋層）。
type Bundle struct {
	messages map[Locale]map[string]string
}

var messageOnce sync.Once
var messageBundle *Bundle

// Message selects the request language for a business message, keeping the caller's fallback for unknown keys.
func Message(ctx context.Context, key, fallback string) string {
	messageOnce.Do(func() { messageBundle, _ = Load() })
	if messageBundle != nil {
		if value := messageBundle.T(FromContext(ctx), key); value != key {
			return value
		}
	}
	return fallback
}

// Load 從 embed FS 裝載全部語言。
func Load() (*Bundle, error) {
	b := &Bundle{messages: map[Locale]map[string]string{}}
	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		return nil, fmt.Errorf("i18n: 讀取語言包失敗: %w", err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		data, err := localeFS.ReadFile("locales/" + e.Name())
		if err != nil {
			return nil, err
		}
		msgs := map[string]string{}
		if err := json.Unmarshal(data, &msgs); err != nil {
			return nil, fmt.Errorf("i18n: 解析 %s 失敗: %w", e.Name(), err)
		}
		b.messages[Locale(name)] = msgs
	}
	return b, nil
}

// T 取文案：locale 缺失或 key 缺失時回退 zh_TW，再回退 key 本身（絕不返回空）。
func (b *Bundle) T(locale Locale, key string) string {
	if msgs, ok := b.messages[locale]; ok {
		if v, ok := msgs[key]; ok {
			return v
		}
	}
	if msgs, ok := b.messages[Default]; ok {
		if v, ok := msgs[key]; ok {
			return v
		}
	}
	return key
}

// FromContext 從上下文取語言（請求頭 Accept-Language 的解析由 transport 中間件注入；
// worker/定時任務走 settings 站點默認語言）。
func FromContext(ctx context.Context) Locale {
	if v, ok := ctx.Value(localeKey{}).(Locale); ok && v != "" {
		return v
	}
	return Default
}

type localeKey struct{}

// WithLocale 注入語言。
func WithLocale(ctx context.Context, l Locale) context.Context {
	return context.WithValue(ctx, localeKey{}, l)
}
