package migratev1

// 1.x settings（group=storefront）→ 2.0 settings(group,key) 首批映射表。
// 與 mods/settings/directory.go 的組定義逐 key 對照；未命中的 key 原樣進
// group=v1_legacy（不丟數據，人工決定去向）；SECRET 類顯式跳過（在 2.0 後台重配）。
// 完整版隨 P1 交付逐 key 補齊（《數據遷移工具開發計劃》附錄 C）。

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// settingRule 單條映射規則：目標組/鍵 + 值規範化類型。
type settingRule struct {
	Group string
	Key   string
	Kind  string // bool | int | string | enum_contact | passthrough
}

// settingsMap 首批映射（鍵 = 1.x settings.key）。
var settingsMap = map[string]settingRule{
	// site
	"site_name":       {Group: "site", Key: "name", Kind: "string"},
	"site_logo":       {Group: "site", Key: "logo", Kind: "string"},
	"seo_title":       {Group: "site", Key: "seo_title", Kind: "string"},
	"seo_keywords":    {Group: "site", Key: "seo_keywords", Kind: "string"},
	"seo_description": {Group: "site", Key: "seo_desc", Kind: "string"},
	// ops
	"maintenance_mode":    {Group: "ops", Key: "maintenance", Kind: "bool"},
	"maintenance_message": {Group: "ops", Key: "announcement", Kind: "string"},
	// trade
	"guest_checkout":       {Group: "trade", Key: "guest_checkout", Kind: "bool"},
	"order_query_password": {Group: "trade", Key: "query_password", Kind: "bool"},
	"order_close_minutes":  {Group: "trade", Key: "order_ttl_minutes", Kind: "int"},
	"require_contact":      {Group: "trade", Key: "contact_required", Kind: "enum_contact"},
	// security
	"trade_captcha":       {Group: "security", Key: "captcha_order", Kind: "bool"},
	"register_open":       {Group: "security", Key: "register_enabled", Kind: "bool"},
	"captcha_register":    {Group: "security", Key: "captcha_register", Kind: "bool"},
	"captcha_login":       {Group: "security", Key: "captcha_login", Kind: "bool"},
	"username_min_length": {Group: "security", Key: "username_min_len", Kind: "int"},
	// template（1.x 外觀列表類）
	"category_nav_style": {Group: "template", Key: "category_nav_style", Kind: "string"},
	"list_default_view":  {Group: "template", Key: "default_view", Kind: "string"},
	"grid_columns":       {Group: "template", Key: "per_row", Kind: "int"},
	"page_size":          {Group: "template", Key: "per_page", Kind: "int"},
}

// settingsSkipped 顯式不遷的 key（SECRET 類 / 1.x 專有加密開關）。
var settingsSkipped = map[string]string{
	"card_encryption_key":       "1.x 卡密鑰匙（僅遷移期使用，絕不寫入 2.0）",
	"card_encryption_enabled":   "1.x 卡密加密開關（2.0 強制加密，無此開關）",
	"mail_password":             "SMTP 密碼（SECRET，請在 2.0 後台「通知設置」重配）",
	"sms_access_key":            "短信 AK（SECRET，請在 2.0 後台重配）",
	"sms_access_secret":         "短信 AS（SECRET，請在 2.0 後台重配）",
	"admin_alert_tg_token":      "管理員 TG 告警 token（SECRET，請在 2.0 後台重配）",
	"admin_alert_wecom_webhook": "管理員企微 webhook（SECRET，請在 2.0 後台重配）",
	"footer_analytics":          "已棄用（1.x 數據已併入 analytics）",
}

// normalizeSettingValue 按 rule.Kind 規範化 1.x value（JSON 編碼，可能存在
// 字符串化標量的歷史形態："false"），輸出 2.0 value 的 JSON 文檔。
func normalizeSettingValue(rule settingRule, raw string) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("settings 值非 JSON: %w", err)
	}
	switch rule.Kind {
	case "bool":
		b, err := asBool(v)
		if err != nil {
			return nil, err
		}
		return json.Marshal(b)
	case "int":
		i, err := asInt(v)
		if err != nil {
			return nil, err
		}
		return json.Marshal(i)
	case "enum_contact":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("contact_required 期望字符串，實際 %T", v)
		}
		switch s {
		case "none", "phone", "email", "qq", "any":
			return json.Marshal(s)
		default:
			return nil, fmt.Errorf("require_contact 取值 %q 無 2.0 對應", s)
		}
	default: // string / passthrough：字符串解引號，其餘原樣
		if s, ok := v.(string); ok {
			return json.Marshal(s)
		}
		return json.Marshal(v)
	}
}

func asBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		if b, err := strconv.ParseBool(x); err == nil {
			return b, nil
		}
	case float64:
		return x != 0, nil
	}
	return false, fmt.Errorf("期望布爾值，實際 %T(%v)", v, v)
}

func asInt(v any) (int64, error) {
	switch x := v.(type) {
	case float64:
		return int64(x), nil
	case string:
		if i, err := strconv.ParseInt(x, 10, 64); err == nil {
			return i, nil
		}
	}
	return 0, fmt.Errorf("期望整數，實際 %T(%v)", v, v)
}
