package settings

// 在線安裝服務（Web /install 表單後端）：複用 Install()（單事務種子 + 管理員）。
// Public 端點——僅未安裝時可寫；已安裝後 status 只讀、install 冪等拒絕（409）。

import (
	"context"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// serverVersion 構建注入（http.server 同源 Version；鏈接期由 main 設置）。
var serverVersion = "dev"

// SetServerVersion 供啓動鏈注入構建版本（cmd main 調用）。
func SetServerVersion(v string) { serverVersion = v }

// ServerVersion 當前構建版本（update 模塊 status 面用——進程重啓後自然為新值）。
func ServerVersion() string { return serverVersion }

// AdminInstallService 在線安裝服務。
type AdminInstallService struct {
	adminv1.UnimplementedAdminInstallServiceServer
	data *data.Data
}

// NewAdminInstallService 構造。
func NewAdminInstallService(d *data.Data) *AdminInstallService {
	return &AdminInstallService{data: d}
}

// GetInstallStatus 安裝狀態 + 環境自檢（公開只讀）。
func (s *AdminInstallService) GetInstallStatus(ctx context.Context, _ *emptypb.Empty) (*adminv1.InstallStatusReply, error) {
	installed := Installed(ctx, s.data)
	reply := &adminv1.InstallStatusReply{
		Installed:    installed,
		Dialect:      string(s.data.Dialect),
		Version:      serverVersion,
		DatabaseOk:   s.data.DB != nil,
		MigrationsOk: true, // 遷移失敗啓動即拒絕（fail-fast），能應答即已應用
	}
	if installed {
		if ts, err := installedAt(ctx, s.data); err == nil {
			reply.InstalledAt = ts
		}
	}
	return reply, nil
}

// PerformInstall 執行安裝（未安裝可寫；已安裝 409 冪等拒絕）。
//
// dialect=sqlite（或缺省）：當前庫直裝（零依賴體驗不變）；
// dialect=mysql/postgres：校驗連接（庫不存在自動建）→ 寫覆蓋配置 + 待裝憑據
// → 響應 restart_required 並自重啓（新庫遷移後補裝完成；前端輪詢至 installed）。
func (s *AdminInstallService) PerformInstall(ctx context.Context, req *adminv1.PerformInstallRequest) (*adminv1.InstallStatusReply, error) {
	if Installed(ctx, s.data) {
		return nil, errors.Conflict("settings.ALREADY_INSTALLED", "系統已安裝（如需重裝請清空數據庫）")
	}
	admin := InstallInput{
		AdminUsername: req.GetAdminUsername(),
		AdminPassword: req.GetAdminPassword(),
		SiteName:      req.GetSiteName(),
		SiteURL:       req.GetSiteUrl(),
	}
	// 庫切換分支：mysql/postgres → 校驗 + 落盤 + 自重啓（不在當前庫安裝）
	if d := req.GetDialect(); d == "mysql" || d == "postgres" {
		sw := dbSwitchInput{
			Dialect: d, Host: req.GetDbHost(), Port: req.GetDbPort(),
			User: req.GetDbUser(), Password: req.GetDbPassword(), Name: req.GetDbName(),
			RedisAddr: req.GetRedisAddr(), RedisPassword: req.GetRedisPassword(),
		}
		if err := validateSwitch(ctx, sw); err != nil {
			return nil, errors.BadRequest("settings.DB_SWITCH_INVALID", err.Error())
		}
		// 管理員入參先行校驗（弱密碼不落盤、不重啓）
		if len(admin.AdminPassword) < 8 {
			return nil, errors.BadRequest("settings.WEAK_PASSWORD", "管理員密碼至少 8 位")
		}
		if err := writeSwitchFiles(sw, admin); err != nil {
			return nil, errors.InternalServer("settings.DB_SWITCH_FAILED", err.Error())
		}
		scheduleSelfRestart(800 * time.Millisecond)
		return &adminv1.InstallStatusReply{Installed: false, RestartRequired: true,
			Dialect: d, Version: serverVersion, DatabaseOk: true, MigrationsOk: true}, nil
	}
	if err := Install(ctx, s.data, admin); err != nil {
		return nil, errors.BadRequest("settings.INSTALL_FAILED", err.Error())
	}
	return s.GetInstallStatus(ctx, nil)
}

// TestInstallConnection 測試目標庫/Redis 連通（嚮導「測試連接」；庫不存在自動建）。
func (s *AdminInstallService) TestInstallConnection(ctx context.Context, req *adminv1.TestInstallConnectionRequest) (*adminv1.TestInstallConnectionReply, error) {
	sw := dbSwitchInput{
		Dialect: req.GetDialect(), Host: req.GetDbHost(), Port: req.GetDbPort(),
		User: req.GetDbUser(), Password: req.GetDbPassword(), Name: req.GetDbName(),
		RedisAddr: req.GetRedisAddr(), RedisPassword: req.GetRedisPassword(),
	}
	if err := validateSwitch(ctx, sw); err != nil {
		return &adminv1.TestInstallConnectionReply{Ok: false, Message: err.Error()}, nil
	}
	return &adminv1.TestInstallConnectionReply{Ok: true, Message: "數據庫與 Redis 連接成功"}, nil
}
