package notify

// 事件訂閲分發器（）：
// outbox 事件 → 路由表（事件 → 通道集合）→ 模板渲染（白名單變量 + HTML escape）
// → 逐通道投遞（獨立 enabled；skipped 降級不報錯）→ 每條落 notification_logs。
//
// 冪等：processed_events(event_id, consumer) 由 Dispatcher 統一兜底。
// 白名單變量：模板只允許引用 Variables 裏登記的鍵；未知 {{.xxx}} 渲染為空（防注入：
// 變量值 HTML escape；模板本身後台可編輯——只開放佔位符子集，禁用 tpl 全功能）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"

	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/i18n"
)

// Dispatcher 事件分發器。
type Dispatcher struct {
	adminPath func(context.Context) (string, error)
	repo      *NotifyRepo
	channels  map[string]Channel       // name → channel
	brand     notifyport.BrandResolver // 白標（nil = 未裝配，跳過品牌注入）
}

// NewDispatcher 構造（註冊通道）。
func NewDispatcher(repo *NotifyRepo, channels ...Channel) *Dispatcher {
	m := make(map[string]Channel, len(channels))
	for _, c := range channels {
		m[c.Name()] = c
	}
	return &Dispatcher{repo: repo, channels: m}
}

// WithBrandResolver 裝配白標解析（wire 經 bootstrap 注入；分站訂單郵件品牌隔離）。
func (d *Dispatcher) WithBrandResolver(r notifyport.BrandResolver) *Dispatcher {
	d.brand = r
	return d
}

// 事件 → 默認通道矩陣（模板可覆蓋；通道 enabled 逐個獨立判定）。
var defaultChannels = map[string][]string{
	events.AdminMFAChanged: {"telegram"},
	"order.paid":           {"email", "inbox"},
	"order.shipped":        {"email", "inbox"},
	"order.delivered":      {"email", "inbox"},
	"order.completed":      {"inbox"},
	"order.canceled":       {"email", "inbox"},
	"order.refunded":       {"email", "inbox"},
	"payment.failed":       {"inbox"},
	"recharge.succeeded":   {"email", "inbox"},
	"user.registered":      {"email", "inbox"},
	// ：工單通知（用戶側新回覆；客服側新工單經 telegram 管理員通道）
	"ticket.created": {"inbox"},
	"ticket.replied": {"email", "inbox"},
}

// HandleEvent outbox 消費入口（Dispatcher Register）。
func (d *Dispatcher) HandleEvent(ctx context.Context, env events.Envelope) error {
	if env.Type == events.AdminMFAChanged {
		var payload struct {
			AdminID    uint64 `json:"admin_id"`
			OperatorID uint64 `json:"operator_id"`
			Action     string `json:"action"`
			At         string `json:"at"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return err
		}
		return d.Send(ctx, notifyport.Message{EventType: env.Type, Channel: "telegram", Locale: "zh_CN", Subject: "ZCard 後台兩步驗證變更", Body: fmt.Sprintf("管理員 ID：%d\n操作：%s\n操作者 ID：%d（0 為服務器命令行）\n時間：%s\n若非預期操作，請立即檢查服務器與管理員賬號。", payload.AdminID, payload.Action, payload.OperatorID, payload.At), BizType: "admin_security", BizID: payload.AdminID})
	}
	channels := defaultChannels[env.Type]
	if len(channels) == 0 {
		return nil // 無訂閲事件（管理員告警類由業務模塊顯式 Send）
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(env.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("notify: 解析 %s 載荷失敗: %w", env.Type, err)
	}
	// SMS results remain in the authenticated order view, never card delivery mail.
	if env.Type == "order.delivered" && payload["delivery_kind"] == "sms_activation" {
		return nil
	}
	// 白名單變量（事件載荷扁平化；值統一字符串化 + HTML escape 在渲染期）
	vars := FlattenVars(payload)
	if env.Type == "order.paid" || env.Type == "order.delivered" {
		if err := d.repo.orderEmailVars(ctx, env, vars); err != nil {
			return err
		}
	}
	// 品牌隔離 fail-closed：分站上下文郵件注入分站白標；
	// 無白標 → 品牌變量留空（絕不回退主站品牌）
	if env.SubsiteID > 0 && d.brand != nil {
		if brand, ok := d.brand.ResolveBrand(ctx, env.SubsiteID); ok {
			vars["site_name"] = brand.SiteName
			if brand.Logo != "" {
				vars["site_logo"] = brand.Logo
			}
		} else {
			vars["site_name"] = ""
			vars["site_logo"] = ""
		}
	}
	locale := string(i18n.FromContext(ctx))
	templateEvent := env.Type
	if env.Type == "order.delivered" && payload["goods_type"] == "physical" {
		templateEvent = "order.received"
	}

	for _, ch := range channels {
		channel, ok := d.channels[ch]
		if !ok {
			continue // 通道未註冊（SMS/Telegram ）
		}
		// 模板（事件 × 通道 × 語言；無模板 → 該通道跳過——不算錯誤）
		tpl, err := d.repo.Template(ctx, templateEvent, ch, locale)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		msg := notifyport.Message{
			EventType: env.Type,
			Channel:   ch,
			Locale:    locale,
			Subject:   RenderTemplate(tpl.SubjectTpl, vars),
			Body:      RenderTemplate(tpl.BodyTpl, vars),
			Variables: vars,
		}
		// 收件人解析（email → 載荷 email 字段；inbox → user_id）
		msg.Recipient = vars["email"]
		msg.UserID = parseUint(vars["user_id"])
		msg.BizType = "order"
		msg.BizID = parseUint64(vars["order_id"])
		if ch == "email" && msg.Recipient == "" {
			_ = d.repo.WriteLog(ctx, logOf(msg, "skipped", "訂單未留有效郵箱，未發送郵件"))
			continue
		}
		if ch == "email" && tpl.ID == 0 && vars["fetch_url"] == "" && (env.Type == "order.paid" || env.Type == "order.delivered") {
			_ = d.repo.WriteLog(ctx, logOf(msg, "failed", "店鋪網址未配置或無效，無法生成訂單查詢鏈接"))
			continue
		}

		if err := channel.Deliver(ctx, msg); err != nil {
			if err == ErrSkipped {
				_ = d.repo.WriteLog(ctx, logOf(msg, "skipped", "通道未配置"))
				continue // 降級：不算失敗不重試
			}
			_ = d.repo.WriteLog(ctx, logOf(msg, "failed", err.Error()))
			continue // 單通道失敗不阻斷其餘（重試由管理員日誌重發）
		}
		_ = d.repo.WriteLog(ctx, logOf(msg, "sent", ""))
	}
	return nil
}

// FlattenVars 載荷扁平化為白名單變量（嵌套 key 以 . 連接；值字符串化）。
func FlattenVars(payload map[string]any) map[string]string {
	out := make(map[string]string)
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				walk(key, vv)
			}
		case []any:
			// 數組不展開（變量粒度到對象字段；數組場景業務側顯式拼接進字符串字段）
		case string:
			out[prefix] = x
		case float64:
			out[prefix] = trimFloat(x)
		case bool:
			out[prefix] = fmt.Sprintf("%v", x)
		case nil:
			out[prefix] = ""
		default:
			out[prefix] = fmt.Sprintf("%v", x)
		}
	}
	for k, v := range payload {
		walk(k, v)
	}
	return out
}

// placeholderRe 佔位符子集：{{.key}}（僅字母數字下劃線點；無函數/管道——防注入）。
var placeholderRe = regexp.MustCompile(`{{\s*\.([A-Za-z0-9_.]+)\s*}}`)

// RenderTemplate 渲染模板：白名單變量替換 + 值 HTML escape（防注入雙保險）。
// 未知變量渲染為空字符串。
func RenderTemplate(tpl string, vars map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := placeholderRe.FindStringSubmatch(m)[1]
		if v, ok := vars[key]; ok {
			return html.EscapeString(v)
		}
		return ""
	})
}

// ValidateTemplate 後台模板編輯校驗：佔位符必須在白名單內（預覽前攔截）。
func ValidateTemplate(tpl string, allowed []string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = true
	}
	for _, m := range placeholderRe.FindAllStringSubmatch(tpl, -1) {
		if !allowedSet[m[1]] {
			return fmt.Errorf("notify.TPL_VAR_NOT_ALLOWED: %q 不在白名單變量內", m[1])
		}
	}
	return nil
}

func logOf(msg notifyport.Message, status, errMsg string) LogInput {
	// 敏感類型（驗證碼郵件）：正文/變量不落日誌——庫內無明文紀律
	// （郵件照發，僅審計留檔脱敏；BizType 契約見調用方）
	body, vars := msg.Body, msg.Variables
	if msg.BizType == "password_reset" || msg.BizType == "order_access_recovery" || msg.BizType == "register_code" {
		body = "[驗證碼郵件：內容不落日誌]"
		vars = map[string]string{"masked": "true"}
	}
	return LogInput{
		EventType: msg.EventType, BizType: msg.BizType, BizID: msg.BizID,
		Channel: msg.Channel, Recipient: msg.Recipient, Locale: msg.Locale,
		Subject: msg.Subject, Body: body, Status: status,
		ErrorMessage: errMsg, Variables: vars,
	}
}

func parseUint(s string) uint64 { return parseUint64(s) }

func parseUint64(s string) uint64 {
	var v uint64
	_, _ = fmt.Sscanf(strings.TrimSpace(s), "%d", &v)
	return v
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%v", f)
}

// Send 顯式發送（管理員告警等業務直調；實現 notifyport.Sender）。
// ChannelReady 通道是否就緒（配置齊全）。驗證碼等「必須送達」的場景應在發送前
// 調用並 fail-fast——Send 對未配置通道按降級語義返回成功（營銷通知不阻斷業務），
// 驗證碼靜默 skipped = 用戶點了發送卻永遠收不到（實測踩坑）。
func (d *Dispatcher) ChannelReady(ctx context.Context, channel string) bool {
	c, ok := d.channels[channel]
	if !ok {
		return false
	}
	return c.Ready(ctx)
}

func (d *Dispatcher) Send(ctx context.Context, msg notifyport.Message) error {
	channel, ok := d.channels[msg.Channel]
	if !ok {
		return fmt.Errorf("notify: 未知通道 %q", msg.Channel)
	}
	if err := channel.Deliver(ctx, msg); err != nil {
		if err == ErrSkipped {
			_ = d.repo.WriteLog(ctx, logOf(msg, "skipped", "通道未配置"))
			if msg.BizType == "order_access_recovery" {
				return ErrSkipped
			}
			return nil
		}
		_ = d.repo.WriteLog(ctx, logOf(msg, "failed", err.Error()))
		return err
	}
	_ = d.repo.WriteLog(ctx, logOf(msg, "sent", ""))
	return nil
}

// SubscribedEvents 訂閲事件清單（main.go 註冊用；與 defaultChannels 鍵一致）。
func SubscribedEvents() []string {
	out := make([]string, 0, len(defaultChannels))
	for k := range defaultChannels {
		out = append(out, k)
	}
	return out
}
