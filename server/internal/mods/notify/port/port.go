// Package port 為 notify 模塊對外契約（零依賴包）。
package port

import "context"

// Message 通知消息（通道無關）。
type Message struct {
	EventType string // 事件類型（order.paid 等）
	Channel   string // email | inbox | sms | telegram
	Recipient string // 收件人（郵箱/用户ID字符串/手機號/chat_id）
	Locale    string // zh_CN 默認
	Subject   string
	Body      string
	// Variables 白名單變量（渲染後隨日誌留檔）
	Variables map[string]string
	// BizRef 業務引用（notification_logs.biz_type/biz_id）
	BizType string
	BizID   uint64
	// UserID 站內信目標（inbox 通道）
	UserID uint64
}

// Sender 通知發送端口（業務模塊消費，通道 A）。
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Brand 站點品牌（白標解析結果）。
type Brand struct {
	SiteName string
	Logo     string
}

// BrandResolver 站點品牌解析（reseller 實現；分站訂單按 subsite_id 解析白標）。
// fail-closed：解析不到返回 ok=false——調用方不得回退主站品牌（鐵律：分站郵件
// 絕不暴露主站品牌）。
type BrandResolver interface {
	ResolveBrand(ctx context.Context, subsiteID uint64) (brand Brand, ok bool)
}

// SettingsReader 通道配置讀取（notify 模塊消費 settings，通道 A）。
// SMTP 等通道運行時讀取——配置變更不重啓生效（1.x MailService 紀律）。
type SettingsReader interface {
	// GetJSON 讀配置組鍵（不存在返回 nil, nil——調用方走降級）。
	GetJSON(ctx context.Context, group, key string) ([]byte, error)
}

// SMTPConfig SMTP 通道配置（settings notify 組）。
type SMTPConfig struct {
	Security  string `json:"security"` // auto | plain | tls | starttls
	Auth      string `json:"auth"`     // auto | plain | login | cram_md5 | none
	TLSVerify bool   `json:"tls_verify"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	From      string `json:"from"`
	FromName  string `json:"from_name"`
	Enabled   bool   `json:"enabled"`
}
