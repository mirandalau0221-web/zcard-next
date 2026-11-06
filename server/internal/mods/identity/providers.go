package identity

// wire providers（模塊自裝配；跨模塊綁定在 bootstrap）。

import (
	"context"
	"encoding/json"

	"github.com/NovaWorks/zcard-next/server/internal/mods/identity/port"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"

	"github.com/google/wire"
)

// ProviderSet identity providers。
var ProviderSet = wire.NewSet(
	NewIdentityUsecase,
	NewAdminUserRepoImpl,
	wire.Bind(new(AdminUserRepo), new(*AdminUserRepoImpl)),
	wire.Bind(new(port.AdminMutator), new(*AdminUserRepoImpl)),
	wire.Bind(new(port.AdminReader), new(*AdminUserRepoImpl)),
	NewUserRepo,
	wire.Bind(new(port.UserReader), new(*UserRepo)),
	NewAdminAuthService, NewPasswordService,
	NewRegisterCodeService,
	ProvideRegisterCodeSettings, // 註冊開關/方式讀取（複用 notifyport.SettingsReader 綁定）
	NewStoreUserService,
	NewAdminUserManageService,
)

// RegisterCodeSettings security 組設置讀取（通道 A：settings 適配的
// notifyport.SettingsReader——避免 identity→settings 直接依賴產生環）。
type RegisterCodeSettings struct {
	read notifyport.SettingsReader
}

// ProvideRegisterCodeSettings 構造。
func ProvideRegisterCodeSettings(read notifyport.SettingsReader) *RegisterCodeSettings {
	return &RegisterCodeSettings{read: read}
}

// RegisterMethods 註冊方式（多選數組：["username","email","phone"]；兼容舊單值字符串）。
// 返回勾選的通道列表；空/解析失敗回落 ["username"]。
func (s *RegisterCodeSettings) RegisterMethods(ctx context.Context) []string {
	raw, err := s.read.GetJSON(ctx, "security", "register_method")
	if err != nil || len(raw) == 0 {
		return []string{"username"}
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return arr
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one} // 舊單值兼容
	}
	return []string{"username"}
}

// MethodEnabled 指定通道是否啓用（多選語義）。
func (s *RegisterCodeSettings) MethodEnabled(ctx context.Context, m string) bool {
	for _, v := range s.RegisterMethods(ctx) {
		if v == m {
			return true
		}
	}
	return false
}

// AdminLoginCaptcha 後台登錄驗證碼（security.captcha_admin_login；默認 false）。
func (s *RegisterCodeSettings) AdminLoginCaptcha(ctx context.Context) bool {
	raw, err := s.read.GetJSON(ctx, "security", "captcha_admin_login")
	if err != nil || len(raw) == 0 {
		return false
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	return v
}

// RegisterEnabled 開放註冊（security.register_enabled；默認 true）。
func (s *RegisterCodeSettings) RegisterEnabled(ctx context.Context) bool {
	raw, err := s.read.GetJSON(ctx, "security", "register_enabled")
	if err != nil || len(raw) == 0 {
		return true
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return true
	}
	return v
}
