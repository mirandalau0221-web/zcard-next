package dashboard

// wire providers。

import (
	"context"

	dashboardport "github.com/NovaWorks/zcard-next/server/internal/mods/dashboard/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"

	"github.com/google/wire"
)

// settingsReaderAdapter settings.RepoImpl → dashboardport.SettingsReader 適配
// （低庫存閾值讀取；讀失敗走默認值，不阻斷工作台統計）。
type settingsReaderAdapter struct{ repo *settings.RepoImpl }

func (a settingsReaderAdapter) GetJSON(ctx context.Context, group, key string) ([]byte, error) {
	raw, err := a.repo.GetDefault(ctx, group, key, nil)
	if err != nil {
		return nil, nil
	}
	return raw, nil
}

// ProvideSettingsReader settings 適配為通用端口（通道 A）。
func ProvideSettingsReader(repo *settings.RepoImpl) dashboardport.SettingsReader {
	return settingsReaderAdapter{repo: repo}
}

// ProviderSet dashboard providers。
var ProviderSet = wire.NewSet(
	NewDashboardRepoImpl,
	NewReconciler,
	NewAdminDashboardService,
	ProvideSettingsReader, // dashboardport.SettingsReader 綁定（低庫存閾值等）
)
