package audit

// 告警閾值接線（ 收尾）：異常檢測閾值（settings.security）→
// 安全事件計數超閾值時經 notify 管理員通道告警（telegram/email）。
//
// 閾值項（settings security 組 alert 子鍵；缺省用默認值）：
// fetch_fail_per_ip 同 IP 取貨失敗次數/窗口（默認 10/10min）
// login_fail_per_ip 同 IP 登錄失敗（默認 20/10min）
// decrypt_per_admin 管理員解密頻率（默認 30/10min）
// 去重窗口：同 key 告警 30min 內只發一次（告警風暴防護）。
//
// 埋點接入：Security() 內部順帶計數——業務方零改動。

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
)

// AlertConfig 告警閾值配置（settings security 組 alert 鍵）。
type AlertConfig struct {
	Enabled bool `json:"enabled"`
	// 閾值（次/10 分鐘窗口）
	FetchFailPerIP  int `json:"fetch_fail_per_ip"`
	LoginFailPerIP  int `json:"login_fail_per_ip"`
	DecryptPerAdmin int `json:"decrypt_per_admin"`
	// 告警通道（notify 通道名，如 telegram/email）
	Channel   string `json:"channel"`
	Recipient string `json:"recipient"` // telegram chat_id / 管理員郵箱
}

// 默認閾值（配置缺省回退）。
func defaultAlertConfig() AlertConfig {
	return AlertConfig{
		Enabled:         true,
		FetchFailPerIP:  10,
		LoginFailPerIP:  20,
		DecryptPerAdmin: 30,
		Channel:         "telegram",
	}
}

// alertWindow 計數窗口。
const alertWindow = 10 * time.Minute

// alertDedup 同 key 告警去重窗口。
const alertDedup = 30 * time.Minute

// Alerter 告警器（閾值計數 + 去重 + 通道投遞）。
type Alerter struct {
	settings notifyport.SettingsReader
	sender   notifyport.Sender

	mu        sync.Mutex
	counts    map[string][]time.Time // key → 窗口內事件時間
	lastAlert map[string]time.Time   // key → 上次告警時間
}

// NewAlerter 構造。
func NewAlerter(settings notifyport.SettingsReader, sender notifyport.Sender) *Alerter {
	return &Alerter{
		settings:  settings,
		sender:    sender,
		counts:    map[string][]time.Time{},
		lastAlert: map[string]time.Time{},
	}
}

// alertKey 事件計數鍵（閾值判定的維度）。
func alertKey(action, ip string) string {
	return action + "|" + ip
}

// Count 計數並判定：達到閾值 → 觸發告警（去重窗口內只發一次）。
// 業務方調用點：Security() 內部（fetch 失敗/登錄失敗/解密）。
func (a *Alerter) Count(ctx context.Context, action, ip string, subject, body string) {
	if a == nil {
		return
	}
	cfg := a.config(ctx)
	if !cfg.Enabled {
		return
	}
	threshold := a.thresholdOf(cfg, action)
	if threshold <= 0 {
		return // 該維度未配置閾值（不計數不告警）
	}

	key := alertKey(action, ip)
	now := time.Now()

	a.mu.Lock()
	// 滑動窗口計數
	win := a.counts[key]
	kept := win[:0]
	for _, t := range win {
		if now.Sub(t) < alertWindow {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	a.counts[key] = kept
	count := len(kept)
	// 去重窗口
	if last, ok := a.lastAlert[key]; ok && now.Sub(last) < alertDedup {
		a.mu.Unlock()
		return
	}
	if count < threshold {
		a.mu.Unlock()
		return
	}
	a.lastAlert[key] = now
	// 達到閾值後重置計數（下一窗口重新累計）
	a.counts[key] = nil
	a.mu.Unlock()

	if a.sender == nil {
		return
	}
	_ = a.sender.Send(ctx, notifyport.Message{
		EventType: "security.alert", Channel: cfg.Channel,
		Recipient: cfg.Recipient, Locale: "zh_CN",
		Subject: subject,
		Body:    body,
		Variables: map[string]string{
			"action": action, "ip": ip, "count": itoa(count), "window": "10m",
		},
	})
}

// config 運行時讀閾值配置（缺省回退默認值）。
func (a *Alerter) config(ctx context.Context) AlertConfig {
	cfg := defaultAlertConfig()
	if a.settings == nil {
		return cfg
	}
	raw, err := a.settings.GetJSON(ctx, "security", "alert")
	if err != nil || len(raw) == 0 {
		return cfg
	}
	// 寬鬆解析：零值字段保留默認
	parsed := cfg
	_ = jsonUnmarshalAlert(raw, &parsed)
	if parsed.FetchFailPerIP == 0 {
		parsed.FetchFailPerIP = cfg.FetchFailPerIP
	}
	if parsed.LoginFailPerIP == 0 {
		parsed.LoginFailPerIP = cfg.LoginFailPerIP
	}
	if parsed.DecryptPerAdmin == 0 {
		parsed.DecryptPerAdmin = cfg.DecryptPerAdmin
	}
	if parsed.Channel == "" {
		parsed.Channel = cfg.Channel
	}
	return parsed
}

// thresholdOf 按事件名取閾值（未覆蓋的維度返回 0 = 不計數）。
func (a *Alerter) thresholdOf(cfg AlertConfig, action string) int {
	switch {
	case containsStr(action, "fetch"):
		return cfg.FetchFailPerIP
	case containsStr(action, "login_failed"):
		return cfg.LoginFailPerIP
	case containsStr(action, "decrypt"), containsStr(action, "view_content"):
		return cfg.DecryptPerAdmin
	}
	return 0
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// jsonUnmarshalAlert 告警配置解析（寬鬆：失敗保留默認值）。
func jsonUnmarshalAlert(raw []byte, v *AlertConfig) error {
	return json.Unmarshal(raw, v)
}
