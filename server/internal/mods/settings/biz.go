// Package settings 設置中心（）：settings 表 = 運行時業務開關真理源。
//
// 分組目錄（規劃 ）：site（基礎）/template（模板）/trade（交易）/security（安全）/
// ops（運維）/notify（郵件短信）/recharge（充值提現）/points（積分）/affiliate（分銷）/
// supply（貨源）/i18n（語言貨幣）。安裝嚮導 /install 交付。
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/NovaWorks/zcard-next/server/internal/mods/settings/port"
)

// ErrSettingNotFound 設置項不存在。
var ErrSettingNotFound = errors.New("settings.NOT_FOUND")

// Repo 設置倉儲（模塊內端口，實現於 data.go）。
type Repo interface {
	Get(ctx context.Context, group, key string) (json.RawMessage, error)
	List(ctx context.Context, group string) ([]port.Item, error)
	Put(ctx context.Context, group, key string, value json.RawMessage) error
	// Currencies 啓用貨幣視圖（前台列表）。
	Currencies(ctx context.Context) ([]CurrencyView, error)
	// CurrencyExists 貨幣是否存在（i18n.base_currency 寫入校驗）。
	CurrencyExists(ctx context.Context, code string) (bool, error)
	// PutMany 批量寫入（事務原子）。
	PutMany(ctx context.Context, items []port.Item) error
}

// SettingsUsecase 設置用例。
type SettingsUsecase struct {
	repo Repo
}

// NewSettingsUsecase 構造。
func NewSettingsUsecase(repo Repo) *SettingsUsecase { return &SettingsUsecase{repo: repo} }

// CurrencyExists 轉發（service 層 base_currency 寫入校驗）。
func (uc *SettingsUsecase) CurrencyExists(ctx context.Context, code string) (bool, error) {
	return uc.repo.CurrencyExists(ctx, code)
}

// Get 讀取單項。
func (uc *SettingsUsecase) Get(ctx context.Context, group, key string) (json.RawMessage, error) {
	return uc.repo.Get(ctx, group, key)
}

// GetDefault 讀取單項，不存在返回默認值。
func (uc *SettingsUsecase) GetDefault(ctx context.Context, group, key string, def json.RawMessage) (json.RawMessage, error) {
	v, err := uc.repo.Get(ctx, group, key)
	if errors.Is(err, ErrSettingNotFound) {
		return def, nil
	}
	return v, err
}

// List 按分組列出（group 為空列出全部）。
func (uc *SettingsUsecase) List(ctx context.Context, group string) ([]port.Item, error) {
	return uc.repo.List(ctx, group)
}

// Put 更新單項（鍵經目錄校驗 + JSON 合法性；SECRET 鍵回寫 **** 視為未修改跳過）。
func (uc *SettingsUsecase) Put(ctx context.Context, group, key string, value json.RawMessage) error {
	if err := ValidateKey(group, key); err != nil {
		return err
	}
	if !json.Valid(value) {
		return errors.New("settings.INVALID_VALUE")
	}
	if IsSecret(group, key) && (string(value) == `"****"` || (group == "notify" && key == "telegram_bot_token" && string(value) == `""`)) {
		return nil // 脱敏回寫 = 未修改
	}
	return uc.PutMany(ctx, []port.Item{{Group: group, Key: key, Value: value}})
}

// PutMany 批量更新（表單級保存；單事務原子寫入——任一項失敗整體回滾）。
// 校驗語義與 Put 一致；SECRET 鍵 **** 回寫跳過。
func (uc *SettingsUsecase) PutMany(ctx context.Context, items []port.Item) error {
	if len(items) == 0 {
		return nil
	}
	valid := make([]port.Item, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		name := it.Group + "." + it.Key
		if seen[name] {
			return fmt.Errorf("settings.DUPLICATE_KEY: %s", name)
		}
		seen[name] = true
		if it.Group == "i18n" {
			if err := validateI18nValue(it.Key, it.Value); err != nil {
				return err
			}
		}
		if err := ValidateKey(it.Group, it.Key); err != nil {
			return err
		}
		if !json.Valid(it.Value) {
			return errors.New("settings.INVALID_VALUE")
		}
		if IsSecret(it.Group, it.Key) && (string(it.Value) == `"****"` || (it.Group == "notify" && it.Key == "telegram_bot_token" && string(it.Value) == `""`)) {
			continue
		}
		valid = append(valid, it)
	}
	if len(valid) == 0 {
		return nil
	}
	if hasI18nWrites(valid) {
		current, err := uc.repo.List(ctx, "i18n")
		if err != nil {
			return err
		}
		if _, err := mergedI18n(current, valid); err != nil {
			return err
		}
	}
	return uc.repo.PutMany(ctx, valid)
}

// GetStruct 泛型讀取（）：JSON 綁定到調用方結構體，DB 缺省回落目錄默認值。
// 用法：var cfg TradeConfig; uc.GetStruct(ctx, "trade", "guest_checkout", &cfg)
func GetStruct[T any](ctx context.Context, uc *SettingsUsecase, group, key string, out *T) error {
	raw, err := uc.GetDefault(ctx, group, key, nil)
	if err != nil {
		return err
	}
	if raw == nil {
		// DB 無值 → 用目錄默認值
		g, ok := Group(group)
		if !ok {
			return fmt.Errorf("settings: 未知分組 %q", group)
		}
		def, has := g.DefaultJSON(key)
		if !has {
			return fmt.Errorf("settings: 分組 %q 內未知鍵 %q", group, key)
		}
		raw = json.RawMessage(def)
	}
	return json.Unmarshal(raw, out)
}

// SanitizeGroup 脱敏分組（列表/詳情接口用）：SECRET 鍵值替換 ****。
func SanitizeGroup(items []port.Item) []port.Item {
	out := make([]port.Item, 0, len(items))
	for _, it := range items {
		if IsSecret(it.Group, it.Key) && !(it.Group == "notify" && it.Key == "telegram_bot_token" && string(it.Value) == `""`) {
			out = append(out, port.Item{Group: it.Group, Key: it.Key, Value: json.RawMessage(`"****"`)})
			continue
		}
		out = append(out, it)
	}
	return out
}

// withDefaults 目錄默認值補齊（admin 列表用）：DB 已寫入的鍵優先，
// 未寫入的鍵以目錄默認 JSON 展示——保證每個分組全量可見
// （與前台 GetPublicConfig 的回落邏輯同源；保存後即落 DB 行）。
func withDefaults(groupFilter string, items []port.Item) []port.Item {
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[it.Group+"."+it.Key] = true
	}
	var extra []port.Item
	for _, gname := range GroupsSorted() {
		if groupFilter != "" && gname != groupFilter {
			continue
		}
		g, ok := Group(gname)
		if !ok {
			continue
		}
		for k := range g.Defaults {
			if have[gname+"."+k] {
				continue
			}
			if def, has := g.DefaultJSON(k); has {
				extra = append(extra, port.Item{Group: gname, Key: k, Value: json.RawMessage(def)})
			}
		}
	}
	if len(extra) == 0 {
		return items
	}
	out := append(items, extra...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Key < out[j].Key
	})
	return out
}
