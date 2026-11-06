package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/NovaWorks/zcard-next/server/internal/mods/settings/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/theme"
	"github.com/go-kratos/kratos/v3/errors"
)

// Both legacy fields select one responsive storefront theme. The active snapshot
// records its key and revision together without a schema migration.
func isThemeKey(group, key string) bool {
	return group == "template" && (key == "pc_template" || key == "mobile_template")
}

// Uploads and explicit activations share this lock. Readers use one atomic DB
// snapshot; the filesystem pointer only describes the latest installed package.
var themeChanges sync.RWMutex

const activeThemeKey = "active_theme"

func readActiveTheme(ctx context.Context, r Repo) (*theme.Selection, error) {
	raw, err := r.Get(ctx, "template", activeThemeKey)
	if err == ErrSettingNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var selection *theme.Selection
	if err = json.Unmarshal(raw, &selection); err != nil {
		return nil, err
	}
	if selection != nil && selection.Key == "" {
		return nil, fmt.Errorf("invalid active theme")
	}
	return selection, nil
}

func legacySelectedTheme(ctx context.Context, r Repo) (string, error) {
	for _, key := range []string{"pc_template", "mobile_template"} {
		raw, err := r.Get(ctx, "template", key)
		if err != nil {
			if err == ErrSettingNotFound {
				continue
			}
			return "", err
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && value != "" {
			return value, nil
		}
	}
	return "classic", nil
}
func selectedTheme(ctx context.Context, r Repo) string {
	themeChanges.RLock()
	defer themeChanges.RUnlock()
	selection, err := readActiveTheme(ctx, r)
	if err != nil {
		return "classic"
	}
	if selection != nil {
		return selection.Key
	}
	key, err := legacySelectedTheme(ctx, r)
	if err != nil {
		return "classic"
	}
	return key
}
func (s *AdminSettingsService) ActiveTemplate(ctx context.Context) string {
	if s == nil || s.uc == nil {
		return "classic"
	}
	return selectedTheme(ctx, s.uc.repo)
}

// ActiveTheme resolves the pinned revision, with legacy compatibility until the
// first upload or explicit activation. Missing/corrupt packages fall back to Classic.
func (s *AdminSettingsService) ActiveTheme(ctx context.Context) *theme.Theme {
	if r := theme.RuntimeFromContext(ctx); r != nil {
		return r.Theme
	}
	themeChanges.RLock()
	defer themeChanges.RUnlock()
	return s.activeThemeUnlocked(ctx)
}

func (s *AdminSettingsService) activeThemeUnlocked(ctx context.Context) *theme.Theme {
	if s == nil || s.uc == nil {
		return nil
	}
	selection, err := readActiveTheme(ctx, s.uc.repo)
	if err != nil {
		return nil
	}
	var t *theme.Theme
	if selection != nil {
		t, _ = theme.ResolveRevision(selection.Key, selection.Revision)
	} else {
		key, err := legacySelectedTheme(ctx, s.uc.repo)
		if err != nil {
			return nil
		}
		t, _ = theme.Resolve(key)
	}
	return t
}

// Pin the legacy selection BEFORE changing any installed pointer. This also
// prevents uploading a formerly missing theme from silently activating it.
// Caller holds themeChanges through installation.
func (s *AdminSettingsService) freezeLegacyTheme(ctx context.Context) error {
	if s == nil || s.uc == nil {
		return nil
	}
	selection, err := readActiveTheme(ctx, s.uc.repo)
	if err != nil || selection != nil {
		return err
	}
	key, err := legacySelectedTheme(ctx, s.uc.repo)
	if err != nil {
		return err
	}
	selection = &theme.Selection{Key: "classic"}
	if t, err := theme.Resolve(key); err == nil {
		selection = &theme.Selection{Key: t.Key, Revision: t.Revision}
	}
	raw, _ := json.Marshal(selection)
	return s.uc.Put(ctx, "template", activeThemeKey, raw)
}

// normalizeThemeWrites is shared by single/batch updates. Conflicting legacy
// choices are rejected rather than silently depending on request field order.
func normalizeThemeWrites(items []port.Item) ([]port.Item, error) {
	out := make([]port.Item, 0, len(items)+1)
	selected := ""
	for _, it := range items {
		if !isThemeKey(it.Group, it.Key) {
			out = append(out, it)
			continue
		}
		var key string
		if json.Unmarshal(it.Value, &key) != nil || key == "" {
			return nil, errors.BadRequest("settings.INVALID_VALUE", "主題鍵必須是非空字符串")
		}
		if selected != "" && selected != key {
			return nil, errors.BadRequest("settings.TEMPLATE_CONFLICT", "PC 和手機端已合併為一個響應式主題，請選擇同一個主題")
		}
		selected = key
	}
	if selected != "" {
		selection := theme.Selection{Key: selected}
		if selected != "classic" {
			t, err := theme.Resolve(selected)
			if err != nil {
				return nil, errors.BadRequest("settings.TEMPLATE_NOT_FOUND", "主題不存在或已損壞，請重新安裝")
			}
			selection.Revision = t.Revision
		}
		raw, _ := json.Marshal(selection)
		out = append(out, port.Item{Group: "template", Key: activeThemeKey, Value: raw})
		b, _ := json.Marshal(selected)
		for _, key := range []string{"pc_template", "mobile_template"} {
			out = append(out, port.Item{Group: "template", Key: key, Value: b})
		}
	}
	return out, nil
}
