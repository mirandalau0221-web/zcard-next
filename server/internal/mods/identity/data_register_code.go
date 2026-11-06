package identity

// 註冊驗證碼（security.register_method 驅動：username=免驗證 / email=郵箱碼 /
// phone=短信碼）。複用 EmailVerification 表 + PasswordService 同款紀律：
// 6 位碼哈希存儲、10 分鐘有效、60s 冷卻、5 次嘗試上限、一次性 verified。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/emailverification"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
)

// 註冊驗證碼參數（與找回密碼同紀律）。
const (
	regCodeTTL      = 10 * time.Minute
	regCodeCooldown = 60 * time.Second
	regCodeMaxTry   = 5
)

// RegisterCodeService 註冊驗證碼（掛在 StoreUserService；settings 讀通道/模板）。
type RegisterCodeService struct {
	data     *data.Data
	sender   notifyport.Sender
	settings notifyport.SettingsReader // notify 組模板讀取（nil=默認文案）
}

// NewRegisterCodeService 構造。
func NewRegisterCodeService(d *data.Data, sender notifyport.Sender) *RegisterCodeService {
	return &RegisterCodeService{data: d, sender: sender}
}

// SetSettings 裝配期注入 settings（可選；nil 用內置默認文案）。
func (s *RegisterCodeService) SetSettings(r notifyport.SettingsReader) { s.settings = r }

// siteName 站點名（settings.site.name；缺失/讀取失敗回落 "ZCard"）。
func (s *RegisterCodeService) siteName(ctx context.Context) string {
	if s.settings != nil {
		if raw, err := s.settings.GetJSON(ctx, "site", "name"); err == nil && len(raw) > 0 {
			var v string
			if json.Unmarshal(raw, &v) == nil && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return "ZCard"
}

// renderSmsTemplate 短信模板渲染（{code}/{minutes}/{site} 佔位；讀取失敗用 dflt）。
func (s *RegisterCodeService) renderSmsTemplate(ctx context.Context, key, dflt, code string) string {
	tpl := dflt
	if s.settings != nil {
		if raw, err := s.settings.GetJSON(ctx, "notify", key); err == nil && len(raw) > 0 {
			var v string
			if json.Unmarshal(raw, &v) == nil && strings.TrimSpace(v) != "" {
				tpl = v
			}
		}
	}
	r := strings.NewReplacer(
		"{code}", code,
		"{minutes}", fmt.Sprintf("%d", int(regCodeTTL.Minutes())),
		"{site}", s.siteName(ctx),
	)
	return r.Replace(tpl)
}

// smsTemplateParams 短信模板變量（與 renderSmsTemplate 佔位同源；Body 發送給通道）。
// 各通道按自身模板變量規範轉換（阿里雲/七牛鍵值對象；騰訊雲按變量名排序轉數組）。
func (s *RegisterCodeService) smsTemplateParams(ctx context.Context, code string) map[string]string {
	return map[string]string{
		"code":    code,
		"minutes": fmt.Sprintf("%d", int(regCodeTTL.Minutes())),
		"site":    s.siteName(ctx),
	}
}

// SendRegisterCode 發碼：target（郵箱或手機號）+ channel（email|phone，空自動判定）。
// 目標已被註冊時拒絕（註冊場景無需防枚舉——註冊地址本來就不存在）。
func (s *RegisterCodeService) SendRegisterCode(ctx context.Context, target, channel string) error {
	target = strings.TrimSpace(target)
	if channel == "" {
		channel = detectChannel(target)
	}
	if channel == "email" {
		target = strings.ToLower(target)
		if !strings.Contains(target, "@") || !strings.Contains(target, ".") {
			return errors.New("identity.EMAIL_INVALID: 郵箱格式不正確")
		}
	} else if channel == "phone" {
		if !isPhoneFormat(target) {
			return errors.New("identity.PHONE_INVALID: 手機號格式不正確")
		}
	} else {
		return errors.New("identity.CHANNEL_INVALID")
	}

	client := data.Client(ctx, s.data)
	// 目標已註冊拒絕（含軟刪除域：username/email/phone 唯一索引兜底）
	exist, _ := client.User.Query().Where(
		user.Or(user.Email(target), user.Phone(target)),
	).Exist(ctx)
	if exist {
		return errors.New("identity.TARGET_TAKEN: 該郵箱/手機號已註冊")
	}

	purpose := emailverification.PurposeRegister
	notifyChannel := "email"
	if channel == "phone" {
		purpose = emailverification.PurposePhoneRegister
		notifyChannel = "sms"
	}
	// 通道就緒前置校驗（fail-fast）：驗證碼是「必須送達」場景——Send 對未配置通道
	// 按營銷降級語義返回成功，用戶卻永遠收不到碼（實測踩坑），必須在發碼前攔下。
	if cr, ok := s.sender.(interface {
		ChannelReady(ctx context.Context, channel string) bool
	}); ok && !cr.ChannelReady(ctx, notifyChannel) {
		if notifyChannel == "sms" {
			return errors.New("identity.SMS_NOT_READY: 短信通道未配置——請聯繫管理員在後台「設置 → 郵件短信」配置短信服務商")
		}
		return errors.New("identity.EMAIL_NOT_READY: 郵件通道未配置——請聯繫管理員在後台「設置 → 郵件短信」配置 SMTP 發件賬號")
	}

	// 60s 冷卻
	latest, err := client.EmailVerification.Query().
		Where(
			emailverification.Email(target),
			emailverification.PurposeEQ(purpose),
			emailverification.VerifiedAtIsNil(),
		).
		Order(ent.Desc(emailverification.FieldCreatedAt)).
		First(ctx)
	if err == nil && time.Since(latest.CreatedAt) < regCodeCooldown {
		return errors.New("identity.CODE_COOLDOWN: 發送過於頻繁，請 1 分鐘後再試")
	}

	code, err := randomCode()
	if err != nil {
		return err
	}
	if _, err := client.EmailVerification.Create().
		SetEmail(target).
		SetPurpose(purpose).
		SetCodeHash(codeHash(code)).
		SetExpiresAt(time.Now().Add(regCodeTTL)).
		Save(ctx); err != nil {
		return err
	}
	// 消息體：短信 Body = 模板變量 JSON（通道按自身規範轉換；渲染文本入 Subject 留檔），郵件用 HTML
	subject := "ZCard 註冊驗證碼"
	var body string
	if channel == "phone" {
		params, _ := json.Marshal(s.smsTemplateParams(ctx, code))
		body = string(params)
		subject = s.renderSmsTemplate(ctx, "sms_template_register",
			"【ZCard】您的註冊驗證碼：{code}，{minutes} 分鐘內有效。", code)
	} else {
		body = fmt.Sprintf(
			"<p>您的註冊驗證碼：<b style=\"font-size:20px\">%s</b>（%d 分鐘內有效）。</p><p>若非本人操作請忽略。</p>",
			code, int(regCodeTTL.Minutes()))
	}
	return s.sender.Send(ctx, notifyport.Message{
		EventType: "user.register_code",
		Channel:   notifyChannel,
		Recipient: target,
		Locale:    "zh_CN",
		Subject:   subject,
		Body:      body,
		BizType:   "register_code",
	})
}

// VerifyRegisterCode 驗碼（一次性 verified；嘗試計數；錯滿作廢）。
// 返回 nil 表示通過。
func (s *RegisterCodeService) VerifyRegisterCode(ctx context.Context, target, code, channel string) error {
	target = strings.TrimSpace(target)
	if channel == "email" {
		target = strings.ToLower(target)
	}
	purpose := emailverification.PurposeRegister
	if channel == "phone" {
		purpose = emailverification.PurposePhoneRegister
	}
	client := data.Client(ctx, s.data)
	v, err := client.EmailVerification.Query().
		Where(
			emailverification.Email(target),
			emailverification.PurposeEQ(purpose),
		).
		Order(ent.Desc(emailverification.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		return errors.New("identity.CODE_INVALID: 請先獲取驗證碼")
	}
	if !v.VerifiedAt.IsZero() {
		return errors.New("identity.CODE_USED: 驗證碼已使用")
	}
	if time.Now().After(v.ExpiresAt) {
		return errors.New("identity.CODE_EXPIRED: 驗證碼已過期")
	}
	if v.AttemptCount >= regCodeMaxTry {
		return errors.New("identity.CODE_MAX_TRIES: 錯誤次數過多，請重新獲取")
	}
	if v.CodeHash != codeHash(strings.TrimSpace(code)) {
		_, _ = client.EmailVerification.UpdateOne(v).
			SetAttemptCount(v.AttemptCount + 1).Save(ctx)
		return errors.New("identity.CODE_INVALID: 驗證碼錯誤")
	}
	_, _ = client.EmailVerification.UpdateOne(v).
		SetVerifiedAt(time.Now()).Save(ctx)
	return nil
}

// detectChannel 按 target 格式自動判定通道。
func detectChannel(target string) string {
	if strings.Contains(target, "@") {
		return "email"
	}
	if isPhoneFormat(target) {
		return "phone"
	}
	return ""
}

// isPhoneFormat 手機號格式（1 開頭 11 位——寬鬆口徑，詳細校驗在短信通道側）。
func isPhoneFormat(s string) bool {
	if len(s) != 11 || s[0] != '1' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
