package settings

// 設置管理 API（薄 transport：校驗 + 裝配，業務在 biz）。
// 權限點：settings:read / settings:update（敏感分組如 security 的寫入 M1 起加二次確認）。

import (
	"context"
	"encoding/json"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/adminpath"
	"regexp"
	"sort"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"

	"github.com/NovaWorks/zcard-next/server/internal/mods/settings/port"
	"github.com/go-kratos/kratos/v3/errors"
)

// 權限點。
const (
	PermRead   = "settings:read"
	PermUpdate = "settings:update"
)

// AdminSettingsService 設置管理服務（實現 adminv1.AdminSettingsService）。
type AdminSettingsService struct {
	adminv1.UnimplementedAdminSettingsServiceServer
	uc            *SettingsUsecase
	adminBasePath string
}

// NewAdminSettingsService 構造。
func NewAdminSettingsService(uc *SettingsUsecase) *AdminSettingsService {
	return &AdminSettingsService{uc: uc}
}

// ListSettings 按分組列出（目錄默認值補齊，未寫入鍵以默認 JSON 展示；
// SECRET 鍵脱敏 ****）。
func (s *AdminSettingsService) ListSettings(ctx context.Context, req *adminv1.ListSettingsRequest) (*adminv1.ListSettingsReply, error) {
	items, err := s.uc.List(ctx, req.GetGroup())
	if err != nil {
		return nil, errors.InternalServer("settings.LIST_FAILED", "讀取設置失敗")
	}
	items = withDefaults(req.GetGroup(), telegramLegacyDefaults(items))
	items = SanitizeGroup(items)
	reply := &adminv1.ListSettingsReply{Items: make([]*adminv1.Setting, 0, len(items))}
	for _, it := range items {
		if it.Group == themeStateGroup {
			continue
		}
		if it.Group == "template" && (it.Key == "mobile_template" || it.Key == activeThemeKey) {
			continue
		}
		if it.Group == "template" && it.Key == "pc_template" {
			it.Value, _ = json.Marshal(s.ActiveTemplate(ctx))
		}
		label := it.Key
		var options []*adminv1.OptionItem
		secret := false
		if g, ok := Group(it.Group); ok {
			if l, has := g.Labels[it.Key]; has {
				label = l
			}
			for v, l := range g.Options[it.Key] {
				options = append(options, &adminv1.OptionItem{Value: v, Label: l})
			}
			sort.Slice(options, func(i, j int) bool { return options[i].Value < options[j].Value })
			secret = g.SecretKeys[it.Key]
		}
		reply.Items = append(reply.Items, &adminv1.Setting{
			Group: it.Group, Key: it.Key, ValueJson: string(it.Value), Label: label,
			Options: options, Secret: secret,
		})
	}
	return reply, nil
}

// validateSettingValue 設置值業務校驗（單鍵/批量共用）：
// base_currency 必須存在、模板鍵必須在清單內。
func (s *AdminSettingsService) validateSettingValue(ctx context.Context, group, key string, value json.RawMessage) error {
	if group == "security" {
		switch key {
		case "captcha_length":
			var n *int
			if json.Unmarshal(value, &n) != nil || n == nil || *n < 4 || *n > 8 {
				return errors.BadRequest("settings.INVALID_VALUE", "驗證碼位數須為 4–8 的整數")
			}
		case "captcha_charset":
			var v string
			g, _ := Group("security")
			if json.Unmarshal(value, &v) != nil || g.Options[key][v] == "" {
				return errors.BadRequest("settings.INVALID_VALUE", "驗證碼字符類型須為純數字、純字母或數字字母混合")
			}
		}
	}
	if group == "supply" && key == "low_stock_alert_enabled" {
		var v *bool
		if json.Unmarshal(value, &v) != nil || v == nil {
			return errors.BadRequest("settings.INVALID_VALUE", "庫存預警開關須為布爾值")
		}
	}
	if group == "supply" && key == "low_stock_threshold" {
		var n *int
		if json.Unmarshal(value, &n) != nil || n == nil || *n < 1 || *n > 1000000 {
			return errors.BadRequest("settings.INVALID_VALUE", "庫存提醒閾值須為 1–1000000 的整數")
		}
	}
	if group == "notify" {
		switch key {
		case "smtp_security", "smtp_auth":
			var v string
			g, _ := Group("notify")
			if json.Unmarshal(value, &v) != nil || g.Options[key][v] == "" {
				return errors.BadRequest("settings.INVALID_VALUE", "請選擇有效的 SMTP 連接或認證方式")
			}
		case "smtp_tls_verify":
			var v *bool
			if json.Unmarshal(value, &v) != nil || v == nil {
				return errors.BadRequest("settings.INVALID_VALUE", "SMTP 證書校驗須為布爾值")
			}
		case "smtp_port":
			var v *int
			if json.Unmarshal(value, &v) != nil || v == nil || *v < 1 || *v > 65535 {
				return errors.BadRequest("settings.INVALID_VALUE", "SMTP 端口須為 1–65535 的整數")
			}
		}
	}
	if group == "notify" && strings.HasPrefix(key, "telegram_") {
		invalid := func() error {
			return errors.BadRequest("settings.INVALID_VALUE", "Telegram 配置格式錯誤，請檢查 Token、Chat ID 或事件選項")
		}
		switch key {
		case "telegram_enabled", "telegram_order_enabled", "telegram_low_stock_enabled":
			var v *bool
			if json.Unmarshal(value, &v) != nil || v == nil {
				return invalid()
			}
		case "telegram_bot_token":
			var v string
			if json.Unmarshal(value, &v) != nil {
				return invalid()
			}
			if len(v) > 512 {
				return invalid()
			}
			if v != "" && v != "****" && !regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{20,}$`).MatchString(v) {
				return invalid()
			}
		case "telegram_chat_ids":
			var v string
			if json.Unmarshal(value, &v) != nil || len(v) > 2000 {
				return invalid()
			}
			ids := strings.Split(v, ",")
			if len(ids) > 20 {
				return invalid()
			}
			for _, id := range ids {
				if id = strings.TrimSpace(id); id != "" && (len(id) > 64 || !regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z0-9_]{5,})$`).MatchString(id)) {
					return invalid()
				}
			}
		case "telegram_targets":
			if _, err := notifyport.ParseTelegramTargets(value); err != nil {
				return errors.BadRequest("settings.INVALID_VALUE", err.Error())
			}
		case "telegram_events":
			var v []string
			if json.Unmarshal(value, &v) != nil {
				return invalid()
			}
			for _, event := range v {
				if event != "order.created" && event != "order.paid" && event != "order.delivered" && event != "order.refunded" {
					return invalid()
				}
			}
		}
	}
	if group == "ticket" && key == "urgent_fee" {
		var fee *int64
		if err := json.Unmarshal(value, &fee); err != nil || fee == nil || *fee < 0 {
			return errors.BadRequest("settings.INVALID_VALUE", "工單加急費須為非負整數，單位分；0 為免費")
		}
	}
	if group == "site" && key == "admin_path" {
		var p string
		if err := json.Unmarshal(value, &p); err != nil || strings.TrimSpace(string(value)) == "null" {
			return errors.BadRequest("settings.INVALID_VALUE", "後台路徑必須是字符串")
		}
		if _, err := adminpath.Normalize(p); err != nil {
			return errors.BadRequest("settings.INVALID_VALUE", err.Error())
		}
	}
	if group == "template" && key == activeThemeKey {
		return errors.BadRequest("settings.INVALID_KEY", "請通過主題選擇器切換默認主題")
	}
	if group == "i18n" {
		if err := validateI18nValue(key, value); err != nil {
			return err
		}
	}
	// 模板鍵必須存在於可用模板清單（防寫不存在的模板 key）。
	if group == "template" && (key == "pc_template" || key == "mobile_template") {
		var tk string
		if err := json.Unmarshal(value, &tk); err != nil || tk == "" {
			return errors.BadRequest("settings.INVALID_VALUE", "模板鍵必須是非空字符串")
		}
		if !templateKeyExists(tk) {
			return errors.BadRequest("settings.TEMPLATE_NOT_FOUND", "模板不存在，請從模板清單中選擇")
		}
	}
	return nil
}

// GetSetting 讀取單項（SECRET 鍵脱敏）。
func (s *AdminSettingsService) GetSetting(ctx context.Context, req *adminv1.GetSettingRequest) (*adminv1.Setting, error) {
	if err := ValidateKey(req.GetGroup(), req.GetKey()); err != nil || (req.GetGroup() == "template" && req.GetKey() == activeThemeKey) {
		return nil, errors.NotFound("settings.NOT_FOUND", "設置項不存在或不在目錄內")
	}
	if isThemeKey(req.GetGroup(), req.GetKey()) {
		value, _ := json.Marshal(s.ActiveTemplate(ctx))
		return &adminv1.Setting{Group: req.GetGroup(), Key: req.GetKey(), ValueJson: string(value)}, nil
	}
	v, err := s.uc.Get(ctx, req.GetGroup(), req.GetKey())
	if err != nil {
		return nil, errors.NotFound("settings.NOT_FOUND", "設置項不存在")
	}
	val := string(v)
	if IsSecret(req.GetGroup(), req.GetKey()) {
		val = `"****"`
	}
	return &adminv1.Setting{Group: req.GetGroup(), Key: req.GetKey(), ValueJson: val}, nil
}

// UpdateSetting 更新單項。
func (s *AdminSettingsService) UpdateSetting(ctx context.Context, req *adminv1.UpdateSettingRequest) (*adminv1.Setting, error) {
	themeChanges.Lock()
	defer themeChanges.Unlock()
	value := json.RawMessage(req.GetValueJson())
	if err := s.validateSettingValue(ctx, req.GetGroup(), req.GetKey(), value); err != nil {
		return nil, err
	}
	items, err := normalizeThemeWrites([]port.Item{{Group: req.GetGroup(), Key: req.GetKey(), Value: value}})
	if err != nil {
		return nil, err
	}
	if err := s.uc.PutMany(ctx, items); err != nil {
		return nil, settingWriteError(err, "設置值必須是合法 JSON")
	}
	if IsSecret(req.GetGroup(), req.GetKey()) {
		value = json.RawMessage(`"****"`)
	}
	return &adminv1.Setting{Group: req.GetGroup(), Key: req.GetKey(), ValueJson: string(value)}, nil
}

// UpdateSettings 批量更新（表單級保存；單事務原子寫入，任一項失敗整體回滾）。
func (s *AdminSettingsService) UpdateSettings(ctx context.Context, req *adminv1.UpdateSettingsRequest) (*adminv1.UpdateSettingsReply, error) {
	themeChanges.Lock()
	defer themeChanges.Unlock()
	items := make([]port.Item, 0, len(req.GetItems()))
	for _, it := range req.GetItems() {
		value := json.RawMessage(it.GetValueJson())
		if err := s.validateSettingValue(ctx, it.GetGroup(), it.GetKey(), value); err != nil {
			return nil, err
		}
		items = append(items, port.Item{Group: it.GetGroup(), Key: it.GetKey(), Value: value})
	}
	items, err := normalizeThemeWrites(items)
	if err != nil {
		return nil, err
	}
	if err := s.uc.PutMany(ctx, items); err != nil {
		return nil, settingWriteError(err, "設置值必須是合法 JSON 或鍵不在目錄內")
	}
	reply := &adminv1.UpdateSettingsReply{Updated: int32(len(items))}
	for _, it := range items {
		if it.Group == themeStateGroup {
			continue
		}
		if it.Group == "site" && it.Key == "admin_path" {
			// The value was validated before writing; avoid a second DB read after committing.
			var configured string
			_ = json.Unmarshal(it.Value, &configured)
			reply.AdminBasePath, _ = adminpath.Normalize(configured)
			if reply.AdminBasePath == "" {
				reply.AdminBasePath = s.adminBasePath
				if reply.AdminBasePath == "" {
					reply.AdminBasePath = "/admin"
				}
			}
		}
	}
	return reply, nil
}

// Preserve actionable validation failures from transactional configuration writes.
func settingWriteError(err error, fallback string) error {
	if e := errors.FromError(err); e.Code == 400 || e.Code == 409 {
		return err
	}
	return errors.BadRequest("settings.INVALID_VALUE", fallback)
}
