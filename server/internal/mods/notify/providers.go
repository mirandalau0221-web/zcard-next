package notify

// wire providers（）。
//
// 跨模塊綁定（通道 A）：
// - EmailChannel.settings ← notifyport.SettingsReader（settings.RepoImpl 適配，見下）
// - Dispatcher 實現 notifyport.Sender（業務模塊告警直調）

import (
	"context"

	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"

	"github.com/google/wire"
)

// settingsReaderAdapter settings.RepoImpl → notifyport.SettingsReader 適配。
type settingsReaderAdapter struct{ repo *settings.RepoImpl }

func (a settingsReaderAdapter) GetJSON(ctx context.Context, group, key string) ([]byte, error) {
	raw, err := a.repo.GetDefault(ctx, group, key, nil)
	if err != nil {
		return nil, err // 讀取失敗須由持久化通知重試，不能誤認為已關閉
	}
	return raw, nil
}

// ProviderSet notify providers。
var ProviderSet = wire.NewSet(
	NewNotifyRepo,
	ProvideChannels,
	ProvideSettingsReader, // notifyport.SettingsReader 綁定（order 互斥開關等消費）
	ProvideDispatcher,
	wire.Bind(new(notifyport.Sender), new(*Dispatcher)), // audit Alerter 消費（通道 A）
	NewBroadcastService,
	NewAdminNotifyService,
	NewStoreNotificationService,
)

// ProvideDispatcher 構造分發器並裝配白標解析（BrandResolver 由 reseller 提供，
// 通道 A；nil = 未裝配跳過品牌注入）。
func ProvideDispatcher(repo *NotifyRepo, channels []Channel, brand notifyport.BrandResolver, settingsSvc *settings.AdminSettingsService) *Dispatcher {
	d := NewDispatcher(repo, channels...).WithBrandResolver(brand)
	d.adminPath = settingsSvc.AdminPath
	return d
}

// ProvideSettingsReader settings 適配為通用端口（跨模塊共享，通道 A）。
func ProvideSettingsReader(repo *settings.RepoImpl) notifyport.SettingsReader {
	return settingsReaderAdapter{repo: repo}
}

// ProvideChannels 通道裝配（四通道：Email/Inbox/SMS/Telegram）。
func ProvideChannels(repo *NotifyRepo, settingsRepo *settings.RepoImpl) []Channel {
	reader := settingsReaderAdapter{repo: settingsRepo}
	return []Channel{
		NewEmailChannel(reader),
		NewInboxChannel(repo),
		NewSMSChannel(reader),
		NewTelegramChannel(reader),
	}
}
