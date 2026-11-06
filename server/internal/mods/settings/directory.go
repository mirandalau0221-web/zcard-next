package settings

// 設置分組目錄（）：每分組強類型默認值 + SECRET/PUBLIC 雙清單 + 鍵校驗。
// 運行時業務開關的真理源在 settings 表（鐵律 7）；本目錄是「鍵合法性 + 默認值 + 脱敏」的唯一裁決。

import (
	"encoding/json"
	"fmt"
	"sort"
)

// GroupDef 分組定義。
type GroupDef struct {
	Name string
	Desc string
	// Defaults 默認值（key → JSON 值）；GetStruct 缺省回落
	Defaults map[string]any
	// Labels 業務顯示名稱（key → 中文名；admin 列表/表單渲染，缺失回落 key 本身）
	Labels map[string]string
	// Options 枚舉選項（key → value → 展示名；admin 表單渲染下拉）
	Options map[string]map[string]string
	// SecretKeys 敏感鍵：讀接口脱敏 ****（寫回時 **** 視為未修改），前台絕不下發
	SecretKeys map[string]bool
	// PublicKeys 前台白名單鍵（GetPublicConfig 僅下發這些；不在其中的鍵絕不外泄）
	PublicKeys map[string]bool
}

// groups 全部分組目錄（ 清裁）。
var groups = map[string]*GroupDef{
	"ticket": {Name: "ticket", Desc: "工單 / TG訂單通知", Labels: map[string]string{"urgent_fee": "加急費（分，0 為免費）"}, Defaults: map[string]any{"urgent_fee": 0}},
	"site": {
		Name: "site", Desc: "站點基礎",
		Labels: map[string]string{
			"name": "站點名稱", "logo": "站點 Logo", "url": "站點地址",
			"seo_title": "SEO 標題", "seo_keywords": "SEO 關鍵詞", "seo_desc": "SEO 描述",
			"verification_google": "Google 站長驗證碼", "verification_bing": "Bing 站長驗證碼",
			"robots_custom": "robots.txt 自定義規則",
			"admin_path":    "後台安全路徑", "top_button": "頂部自定義按鈕",
		},
		Defaults: map[string]any{
			"name":                "ZCard 演示站",
			"logo":                "",
			"url":                 "",
			"seo_title":           "",
			"seo_keywords":        "",
			"seo_desc":            "",
			"verification_google": "",
			"verification_bing":   "",
			"robots_custom":       "",
			"admin_path":          "",
			"top_button":          nil, // 頂部自定義按鈕 {text,url}
		},
		PublicKeys: map[string]bool{"name": true, "logo": true, "url": true, "seo_title": true, "seo_keywords": true, "seo_desc": true, "verification_google": true, "verification_bing": true, "robots_custom": true, "top_button": true},
	},
	"template": {
		Name: "template", Desc: "模板",
		Labels: map[string]string{
			"pc_template": "商城主題（PC / 手機）", "mobile_template": "商城主題（兼容舊設置）", "bg_image": "電腦背景圖", "bg_image_mobile": "手機背景圖",
			"category_nav_style": "分類導航樣式", "default_view": "商品默認視圖",
			"per_row": "每行商品數", "per_page": "每頁商品數", "sort_by": "默認排序方式",
			"show_stock": "顯示庫存", "show_sales": "顯示銷量", "show_reviews": "顯示評價",
		},
		Options: map[string]map[string]string{
			"category_nav_style": {"list": "列表", "grid": "網格"},
			"default_view":       {"list": "列表", "grid": "網格", "big": "大圖"},
			"sort_by": {
				"default":    "綜合排序（默認）",
				"sales":      "銷量優先",
				"newest":     "最新上架",
				"price_asc":  "價格從低到高",
				"price_desc": "價格從高到低",
			},
		},
		Defaults: map[string]any{
			"active_theme":       nil, // Internal atomic key + revision snapshot; never editable/public.
			"pc_template":        "classic",
			"mobile_template":    "classic",
			"bg_image":           "",
			"bg_image_mobile":    "",
			"category_nav_style": "list", // list | grid
			"default_view":       "grid", // list | grid | big
			"per_row":            4,
			"per_page":           20,
			"sort_by":            "default",
			"show_stock":         true,
			"show_sales":         true,
			"show_reviews":       true,
		},
		PublicKeys: map[string]bool{"pc_template": true, "mobile_template": true, "bg_image": true, "bg_image_mobile": true, "category_nav_style": true, "default_view": true, "per_row": true, "per_page": true, "sort_by": true, "show_stock": true, "show_sales": true, "show_reviews": true},
	},
	"footer": {
		Name: "footer", Desc: "頁腳",
		Labels: map[string]string{
			"about": "頁腳關於", "nav": "頁腳導航", "agreement": "用户協議",
			"contact": "聯繫方式", "social": "社交鏈接", "icp": "ICP 備案號",
		},
		Defaults: map[string]any{
			"about":     "",
			"nav":       nil, // [{text,url}]
			"agreement": "",
			"contact":   "",
			"social":    nil, // [{icon,url}]
			"icp":       "",
		},
		PublicKeys: map[string]bool{"about": true, "nav": true, "agreement": true, "contact": true, "social": true, "icp": true},
	},
	"promo": {
		Name: "promo", Desc: "推薦位（落地行為 content/banners）",
		Labels: map[string]string{"top_banner_enabled": "頂部橫幅開關", "nav_recommend": "導航推薦位"},
		Defaults: map[string]any{
			"top_banner_enabled": true,
			"nav_recommend":      nil,
		},
		PublicKeys: map[string]bool{"top_banner_enabled": true, "nav_recommend": true},
	},
	// 客户代碼（service）：第三方 JS 嵌入——客服代碼（懸浮球）與統計代碼（頁面底部）
	"service": {
		Name: "service", Desc: "客服與統計代碼",
		Labels: map[string]string{
			"widget_script": "客服代碼（Chatwoot/Crisp 等嵌入代碼）",
			"stats_script":  "統計代碼（百度/GA/51la，頁面底部注入）",
		},
		Defaults: map[string]any{
			"widget_script": "", // 客服嵌入代碼（含 <script> 標籤）；非空時前台顯示第三方客服懸浮球
			"stats_script":  "", // 統計代碼（body 末尾註入；百度/GA/51la 等）
		},
		PublicKeys: map[string]bool{
			"widget_script": true, "stats_script": true,
		},
	},
	"trade": {
		Name: "trade", Desc: "交易",
		Labels: map[string]string{
			"guest_checkout": "遊客下單", "contact_required": "聯繫方式要求", "contact_scope": "聯繫方式適用買家（積分兑換除外）", "query_password": "訂單查詢密碼",
			"order_ttl_minutes": "訂單超時（分鐘）", "cart_enabled": "購物車功能", "api_order_enabled": "API 下單",
		},
		Options: map[string]map[string]string{
			"contact_scope":    {"guest": "僅遊客", "all": "所有買家"},
			"contact_required": {"none": "不要求", "phone": "手機號", "email": "郵箱", "qq": "QQ", "any": "任意一種"},
		},
		Defaults: map[string]any{
			"guest_checkout":    true,
			"contact_scope":     "guest",
			"contact_required":  "any", // none | phone | email | qq | any
			"query_password":    true,
			"order_ttl_minutes": 30,
			"cart_enabled":      true,
			"api_order_enabled": false,
		},
	},
	"security": {
		Name: "security", Desc: "安全",
		Labels: map[string]string{
			"register_enabled": "開放註冊", "register_method": "註冊方式（多選）",
			"captcha_register": "註冊驗證碼", "captcha_login": "登錄驗證碼", "captcha_order": "下單驗證碼（僅遊客）",
			"captcha_reset": "重置密碼驗證碼", "captcha_admin_login": "後台登錄驗證碼",
			"captcha_length": "驗證碼位數", "captcha_charset": "驗證碼字符類型",
			"username_min_len":   "用户名最小長度",
			"max_pending_per_ip": "單 IP 待付款訂單上限", "risk_enabled": "風控攔截",
		},
		Options: map[string]map[string]string{
			// 多選（數組）：勾選的通道全部可用；至少含 username 才能免驗證註冊
			"register_method": {"username": "用户名", "email": "郵箱驗證", "phone": "手機驗證"},
			"captcha_charset": {"digit": "純數字", "letter": "純字母", "mixed": "數字字母混合"},
		},
		Defaults: map[string]any{
			"register_enabled":    true,
			"register_method":     []string{"username"},
			"captcha_register":    true,
			"captcha_login":       false,
			"captcha_order":       false,
			"captcha_reset":       true,
			"captcha_admin_login": false,
			"captcha_length":      4,
			"captcha_charset":     "digit",
			"username_min_len":    3,
			"max_pending_per_ip":  3,
			"risk_enabled":        true,
		},
	},
	"ops": {
		Name: "ops", Desc: "運維",
		Labels: map[string]string{
			"maintenance": "維護模式", "maintenance_style": "維護提示樣式",
			"maintenance_modal_freq": "維護彈窗頻率",
			"announcement_type":      "公告類型", "announcement": "公告內容",
			"installed_at": "安裝時間（只讀）",
		},
		Options: map[string]map[string]string{
			"maintenance_style": {"modal": "彈窗", "banner": "頂部橫幅"},
			// 彈窗樣式下的自動彈出頻率（banner 樣式不生效）
			"maintenance_modal_freq": {"every": "每次進入都彈", "daily": "24 小時內只彈一次"},
			"announcement_type":      {"text": "文本", "image": "圖片", "carousel": "輪播"},
		},
		Defaults: map[string]any{
			"maintenance":            false,
			"maintenance_style":      "modal", // modal | banner
			"maintenance_modal_freq": "every", // every | daily（僅 modal 樣式生效）
			"announcement_type":      "text",  // text | image | carousel
			"announcement":           "",
			"installed_at":           nil, // 安裝時間（install 寫入，業務只讀）
		},
		PublicKeys: map[string]bool{
			"maintenance": true, "maintenance_style": true, "maintenance_modal_freq": true,
			"announcement_type": true, "announcement": true,
		},
	},
	"recharge": {
		Name: "recharge", Desc: "充值",
		Labels: map[string]string{
			"enabled": "充值功能", "min_amount": "最小充值金額（分）",
			"max_amount": "最大充值金額（分）", "gift_tiers": "充值贈送檔位",
		},
		Defaults: map[string]any{
			"enabled":    true,
			"min_amount": 1000, // 分
			"max_amount": 500000,
			"gift_tiers": nil, // [{amount,gift_balance,gift_points}]
		},
		// 前台公開：充值頁檔位/贈送規則展示（；金額裁決仍在服務端）
		PublicKeys: map[string]bool{"enabled": true, "min_amount": true, "max_amount": true, "gift_tiers": true},
	},
	"supplier_recharge": {
		Name: "supplier_recharge", Desc: "供貨充值",
		Labels: map[string]string{
			"enabled": "供貨充值功能", "min_amount": "最小充值金額（分）",
			"max_amount": "最大充值金額（分）",
			"gift_tiers": "充值贈送檔位",
		},
		Defaults: map[string]any{
			"enabled":    true,
			"min_amount": 1000, // 分（獨立於錢包充值檔位）
			"max_amount": 500000,
			"gift_tiers": nil, // [{amount,gift_balance}] 充滿 amount 分贈 gift_balance 分供貨餘額
		},
		// 前台公開：對接賬户充值彈窗限額與贈送檔位展示（金額裁決仍在服務端）
		PublicKeys: map[string]bool{"enabled": true, "min_amount": true, "max_amount": true, "gift_tiers": true},
	},
	"license": {
		Name: "license", Desc: "訂閲許可證",
		Labels: map[string]string{
			"file": "許可證內容", "pubkey": "許可證公鑰", "domain": "綁定域名",
			"instance_id": "實例 ID", "purchase_monthly_cents": "月付價格（分）",
			"purchase_yearly_cents": "年付價格（分）", "purchase_privkey": "購買簽發私鑰",
		},
		Defaults: map[string]any{
			"file":        "", // 許可證內容（JSON；安裝時寫入）
			"pubkey":      "", // ed25519 公鑰（base64；發行側配置）
			"domain":      "", // 主站域名（許可證綁定校驗；空=跳過）
			"instance_id": "", // 實例 ID（首次讀取時生成並持久化）
			// 在線購買（發行側部署配置；「專業套餐可購買激活」）
			"purchase_monthly_cents": 300,  // 月付 3U
			"purchase_yearly_cents":  3000, // 年付 30U
			"purchase_privkey":       "",   // ed25519 簽發私鑰（base64；空=不開通在線購買）
		},
		SecretKeys: map[string]bool{"purchase_privkey": true},
	},
	"withdraw": {
		Name: "withdraw", Desc: "提現",
		Labels: map[string]string{
			"enabled": "提現功能", "min_amount": "最小提現金額（分）",
			"fee_type": "手續費方式", "fee_value": "手續費值", "methods": "收款方式白名單",
		},
		Options: map[string]map[string]string{
			"fee_type": {"fixed": "固定金額", "percent": "按比例"},
		},
		Defaults: map[string]any{
			"enabled":    false,
			"min_amount": 1000,
			"fee_type":   "fixed", // fixed | percent
			"fee_value":  0,
			"methods": []map[string]string{
				{"type": "alipay", "name": "支付寶"},
				{"type": "wechat", "name": "微信"},
				{"type": "usdt_trc20", "name": "USDT TRC20"},
			}, // 白名單 [{type,name}]
		},
	},
	"points": {
		Name: "points", Desc: "積分",
		Labels: map[string]string{
			"enabled": "積分功能", "deduct_rate": "積分抵現比例", "max_deduct_pct": "最大抵扣比例（%）",
		},
		Defaults: map[string]any{
			"enabled":        true,
			"deduct_rate":    100, // X 積分抵 1 分
			"max_deduct_pct": 50,  // 單單最大抵扣比例
		},
	},
	"affiliate": {
		Name: "affiliate", Desc: "分銷",
		PublicKeys: map[string]bool{"enabled": true, "levels": true, "base": true},
		Labels: map[string]string{
			"enabled": "分銷功能", "levels": "分銷層級",
			"rate_l1": "L1 佣金（萬分比）", "rate_l2": "L2 佣金（萬分比）", "rate_l3": "L3 佣金（萬分比）",
			"base": "佣金基數", "confirm_days": "確認收貨天數", "self_buy": "允許自購",
		},
		Options: map[string]map[string]string{
			"base": {"amount": "訂單金額", "profit": "利潤"},
		},
		Defaults: map[string]any{
			"enabled":      false,
			"levels":       3,
			"rate_l1":      500, // 萬分比
			"rate_l2":      200,
			"rate_l3":      100,
			"base":         "amount", // amount | profit
			"confirm_days": 7,
			"self_buy":     false,
		},
	},
	"supply": {
		Name: "supply", Desc: "貨源",
		Labels: map[string]string{
			"import_max_lines": "導入最大行數", "low_stock_alert_enabled": "啓用庫存預警", "low_stock_threshold": "庫存低於此數量時提醒",
			"sync_interval_minutes": "同步間隔（分鐘）",
		},
		Defaults: map[string]any{
			"import_max_lines":        100000,
			"low_stock_threshold":     5,
			"low_stock_alert_enabled": true,
			"sync_interval_minutes":   30,
		},
	},
	"notify": {
		Name: "notify", Desc: "郵件短信",
		Labels: map[string]string{
			"telegram_enabled": "Telegram 通知通道", "telegram_order_enabled": "Telegram 主站訂單通知", "telegram_low_stock_enabled": "Telegram 低庫存通知",
			"telegram_bot_token": "Telegram Bot Token", "telegram_chat_ids": "Telegram 接收 Chat ID",
			"telegram_events": "Telegram 訂單通知事件", "telegram_targets": "Telegram 接收位置",
			"smtp_security": "SMTP 連接方式", "smtp_auth": "SMTP 認證方式", "smtp_tls_verify": "校驗 SMTP 服務器證書",
			"smtp_host": "SMTP 服務器", "smtp_port": "SMTP 端口", "smtp_user": "SMTP 用户名",
			"smtp_password": "SMTP 密碼", "smtp_from": "發件郵箱", "smtp_name": "發件人名稱",
			"sms_provider": "短信服務商", "sms_key": "短信 AccessKey",
			"sms_secret":            "短信 SecretKey",
			"sms_sign":              "短信簽名",
			"sms_sdk_app_id":        "短信 SDK AppID",
			"sms_template_code":     "短信模板 ID",
			"sms_template_register": "註冊驗證碼短信模板（內容需與通道模板一致；變量 {code}{minutes}{site}）",
			"sms_template_reset":    "找回密碼短信模板（內容需與通道模板一致；變量 {code}{minutes}{site}）",
		},
		Options: map[string]map[string]string{
			"telegram_events": {"order.created": "新訂單", "order.paid": "付款成功", "order.delivered": "發貨完成", "order.refunded": "退款成功"},
			"smtp_security":   {"auto": "自動（兼容舊配置）", "plain": "普通 SMTP（不加密）", "tls": "SSL/TLS（通常 465）", "starttls": "STARTTLS（通常 587）"},
			"smtp_auth":       {"auto": "自動", "plain": "PLAIN", "login": "LOGIN", "cram_md5": "CRAM-MD5", "none": "無需認證（中繼）"},
			"sms_provider":    {"aliyun": "阿里雲短信", "tencent": "騰訊雲短信", "qiniu": "七牛短信"},
		},
		Defaults: map[string]any{
			"telegram_enabled": false, "telegram_order_enabled": false, "telegram_low_stock_enabled": false,
			"telegram_targets": nil, "telegram_bot_token": "", "telegram_chat_ids": "", "telegram_events": []string{"order.paid"},
			"smtp_security":   "auto",
			"smtp_auth":       "auto",
			"smtp_tls_verify": true,
			"smtp_host":       "",
			"smtp_port":       465,
			"smtp_user":       "",
			"smtp_password":   "",
			"smtp_name":       "",
			"smtp_from":       "",
			"sms_provider":    "aliyun",
			"sms_key":         "",
			"sms_secret":      "",
			"sms_sign":        "",
			"sms_sdk_app_id":  "",
			// 模板變量：{code} 驗證碼 {minutes} 有效分鐘 {site} 站點名
			"sms_template_code":     "",
			"sms_template_register": "【{site}】您的註冊驗證碼：{code}，{minutes} 分鐘內有效。",
			"sms_template_reset":    "【{site}】您正在重置密碼，驗證碼：{code}，{minutes} 分鐘內有效。",
		},
		SecretKeys: map[string]bool{"smtp": true, "telegram": true, "telegram_bot_token": true, "smtp_password": true, "sms_key": true, "sms_secret": true},
	},
	"i18n": {
		Name: "i18n", Desc: "語言貨幣",
		Labels: map[string]string{
			"default_locale": "默認語言", "enabled_locales": "啓用語言列表", "base_currency": "基礎貨幣 / 默認結算貨幣",
			"display_currency": "前端顯示貨幣",
		},
		Defaults: map[string]any{
			"default_locale":   "zh_TW",
			"enabled_locales":  []string{"zh_TW"},
			"base_currency":    "CNY",
			"display_currency": "", // 空=跟隨基礎貨幣；多幣種站可設 USD 等（展示層換算，結算仍以基礎幣）
		},
		PublicKeys: map[string]bool{"default_locale": true, "enabled_locales": true, "base_currency": true, "display_currency": true},
	},
	// 在線更新（）：源配置運行時可改——mode 四態
	// （auto=直連探測不通走加速，語義「大陸才加速」）、repo/加速器列表/靜態基址/通道。
	"system": {
		Name: "system", Desc: "系統更新",
		Labels: map[string]string{
			"update": "在線更新源配置",
		},
		Defaults: map[string]any{
			"update": map[string]any{
				"mode":         "auto", // auto | github | accel | static
				"repo":         "NovaWorks/zcard-next",
				"accelerators": []string{"https://gh-proxy.com", "https://ghfast.top", "https://ghproxy.net"},
				"static_base":  "",       // source=static 必填
				"channel":      "stable", // beta 僅 github 直連/靜態源
				"supervisor":   "auto",   // auto=探測 | systemd | supervisord | pm2 | none（探測盲區手動指定）
			},
		},
	},
}

// trade 組 PublicKeys 在 init 內補充（同文件上方字段初始化保持簡潔）
func init() {
	groups["trade"].PublicKeys = map[string]bool{
		"guest_checkout": true, "query_password": true, "contact_required": true, "contact_scope": true, "cart_enabled": true,
	}
	// withdraw 組公開鍵（提現頁表單驅動：開關/最低額/手續費/白名單）
	groups["withdraw"].PublicKeys = map[string]bool{
		"enabled": true, "min_amount": true, "fee_type": true, "fee_value": true, "methods": true,
	}
	// security 組公開鍵：註冊頁動態表單渲染（開關/方式）+ 圖形驗證碼場景開關（前端條件渲染）
	groups["security"].PublicKeys = map[string]bool{
		"register_enabled": true, "register_method": true,
		"captcha_register": true, "captcha_login": true, "captcha_order": true, "captcha_reset": true,
	}
}

// mustJSON 序列化（目錄默認值為字面量，序列化不會失敗）。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// Group 取分組定義。
func Group(name string) (*GroupDef, bool) {
	g, ok := groups[name]
	return g, ok
}

// GroupsSorted 全部分組名（穩定排序）。
func GroupsSorted() []string {
	out := make([]string, 0, len(groups))
	for k := range groups {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateKey 鍵校驗：分組存在 + 鍵在默認值目錄內（未知鍵拒絕，防髒數據）。
func ValidateKey(group, key string) error {
	g, ok := groups[group]
	if !ok {
		return fmt.Errorf("settings: 未知分組 %q（合法：%v）", group, GroupsSorted())
	}
	if _, ok := g.Defaults[key]; !ok {
		return fmt.Errorf("settings: 分組 %q 內未知鍵 %q", group, key)
	}
	return nil
}

// IsSecret 是否敏感鍵。
func IsSecret(group, key string) bool {
	g, ok := groups[group]
	return ok && g.SecretKeys[key]
}

// IsPublic 是否前台白名單鍵。
func IsPublic(group, key string) bool {
	g, ok := groups[group]
	return ok && g.PublicKeys[key]
}

// PublicKeysOf 分組前台鍵清單（測試快照用）。
func PublicKeysOf(group string) []string {
	g, ok := groups[group]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(g.PublicKeys))
	for k := range g.PublicKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DefaultJSON 分組默認值整體 JSON（GetStruct 兜底與 install 初始寫入用）。
func (g *GroupDef) DefaultJSON(key string) (string, bool) {
	v, ok := g.Defaults[key]
	if !ok {
		return "", false
	}
	return mustJSON(v), true
}
