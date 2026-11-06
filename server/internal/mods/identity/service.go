package identity

// Kratos transport 薄層：登錄/登出/刷新/TOTP 管理。

import (
	"context"
	"net"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	authzport "github.com/NovaWorks/zcard-next/server/internal/mods/authz/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/captcha"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/transport"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminAuthService 管理認證服務。
type AdminAuthService struct {
	adminv1.UnimplementedAdminAuthServiceServer
	uc  *IdentityUsecase
	az  authzport.Authorizer
	cap *captcha.Service // 後台登錄圖形驗證碼（captcha_admin_login 場景）
}

// NewAdminAuthService 構造（az 用於 profile 下發權限點清單，前端動態路由消費）。
func NewAdminAuthService(uc *IdentityUsecase, az authzport.Authorizer, cap *captcha.Service) *AdminAuthService {
	return &AdminAuthService{uc: uc, az: az, cap: cap}
}

// Login 管理員登錄（captcha_admin_login 開啓時前置圖形驗證碼；未開啓放行）。
func (s *AdminAuthService) Login(ctx context.Context, req *adminv1.LoginRequest) (*adminv1.LoginReply, error) {
	if err := s.uc.CheckLoginIP(ctx, clientIP(ctx)); err != nil {
		return nil, mapLoginErr(err)
	}
	if s.cap != nil && req.GetChallenge() == "" {
		if err := s.cap.VerifyScene(ctx, captcha.SceneAdminLogin, req.GetCaptchaId(), req.GetCaptchaCode()); err != nil {
			return nil, mapLoginErr(err)
		}
	}
	noCache(ctx)
	var res *AdminLoginResult
	var err error
	if req.GetChallenge() != "" {
		res, err = s.uc.FinishLogin(ctx, req.GetChallenge(), req.GetTotpCode(), clientIP(ctx))
	} else {
		res, err = s.uc.StartLogin(ctx, req.GetUsername(), req.GetPassword(), clientIP(ctx))
	}
	if err != nil {
		return nil, mapLoginErr(err)
	}
	return &adminv1.LoginReply{
		RequiresTotp: res.Challenge != "", Challenge: res.Challenge, RecoveryTicket: res.RecoveryTicket,
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    tokenExpiry(res.ExpiresAt),
		Admin:        toAdminProfile(res.Admin),
	}, nil
}

// GetCaptchaImage 登錄圖形驗證碼（免鑑權）。
func (s *AdminAuthService) GetCaptchaImage(ctx context.Context, _ *emptypb.Empty) (*adminv1.CaptchaImageReply, error) {
	img, err := s.cap.Get(ctx)
	if err != nil {
		return nil, errors.InternalServer("identity.CAPTCHA_GEN_FAILED", "生成驗證碼失敗")
	}
	return &adminv1.CaptchaImageReply{CaptchaId: img.ID, ImageBase64: img.ImageBase64, Length: uint32(img.Length), Charset: img.Charset}, nil
}

// GetCaptchaConfig 登錄驗證碼開關（免鑑權；登錄頁條件渲染）。
func (s *AdminAuthService) GetCaptchaConfig(ctx context.Context, _ *emptypb.Empty) (*adminv1.CaptchaConfigReply, error) {
	return &adminv1.CaptchaConfigReply{Enabled: s.cap.SceneEnabledFor(ctx, captcha.SceneAdminLogin)}, nil
}

// Logout 登出（吊銷 refresh session）。
func (s *AdminAuthService) Logout(ctx context.Context, req *adminv1.LogoutRequest) (*emptypb.Empty, error) {
	if req.GetRefreshToken() != "" {
		_ = s.uc.Logout(ctx, req.GetRefreshToken())
	}
	return &emptypb.Empty{}, nil
}

// RefreshToken 用 refresh 換新令牌對。
func (s *AdminAuthService) RefreshToken(ctx context.Context, req *adminv1.RefreshTokenRequest) (*adminv1.LoginReply, error) {
	noCache(ctx)
	res, err := s.uc.RefreshAccess(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, mapLoginErr(err)
	}
	return &adminv1.LoginReply{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    tokenExpiry(res.ExpiresAt),
		Admin:        toAdminProfile(res.Admin),
	}, nil
}

func noCache(ctx context.Context) {
	if tr, ok := transport.FromServerContext(ctx); ok {
		tr.ReplyHeader().Set("Cache-Control", "no-store")
		tr.ReplyHeader().Set("Pragma", "no-cache")
	}
}
func tokenExpiry(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
func (s *AdminAuthService) EnableTOTP(ctx context.Context, req *adminv1.ConfirmTOTPRequest) (*adminv1.EnableTOTPReply, error) {
	noCache(ctx)
	c := ClaimsFromContext(ctx)
	if c == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	secret, url, err := s.uc.SetupMFA(ctx, c.Subject, c.AuthVersion, req.GetPassword(), req.GetCode(), req.GetRecoveryTicket())
	if err != nil {
		return nil, mapMFAErr(err)
	}
	return &adminv1.EnableTOTPReply{Secret: secret, OtpauthUrl: url}, nil
}
func (s *AdminAuthService) ConfirmTOTP(ctx context.Context, req *adminv1.ConfirmTOTPRequest) (*adminv1.RecoveryCodesReply, error) {
	noCache(ctx)
	c := ClaimsFromContext(ctx)
	if c == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	codes, err := s.uc.ConfirmMFA(ctx, c.Subject, c.AuthVersion, req.GetCode())
	if err != nil {
		return nil, mapMFAErr(err)
	}
	return &adminv1.RecoveryCodesReply{RecoveryCodes: codes}, nil
}
func (s *AdminAuthService) DisableTOTP(ctx context.Context, req *adminv1.ConfirmTOTPRequest) (*emptypb.Empty, error) {
	noCache(ctx)
	c := ClaimsFromContext(ctx)
	if c == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	if err := s.uc.DisableMFA(ctx, c.Subject, c.AuthVersion, req.GetPassword(), req.GetCode()); err != nil {
		return nil, mapMFAErr(err)
	}
	return &emptypb.Empty{}, nil
}
func (s *AdminAuthService) ResetTOTP(ctx context.Context, req *adminv1.ResetTOTPRequest) (*emptypb.Empty, error) {
	noCache(ctx)
	c := ClaimsFromContext(ctx)
	if c == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	if !s.az.Allowed(ctx, c.RoleID, "identity:admin_totp_reset") {
		return nil, errors.Forbidden("authz.PERMISSION_DENIED", "權限不足")
	}
	if err := s.uc.ResetOtherMFA(ctx, c.Subject, req.GetAdminId(), c.AuthVersion, req.GetPassword(), req.GetCode(), req.GetReason()); err != nil {
		return nil, mapMFAErr(err)
	}
	return &emptypb.Empty{}, nil
}
func mapMFAErr(err error) error {
	switch {
	case errors.Is(err, ErrIPLimited):
		return errors.New(429, "identity.IP_LIMITED", "請求過於頻繁，請稍後重試")
	case errors.Is(err, ErrMFAExpired):
		return errors.BadRequest("identity.MFA_EXPIRED", "驗證已過期，請重新開始")
	case errors.Is(err, ErrLocked):
		return errors.BadRequest("identity.ACCOUNT_LOCKED", "失敗次數過多，請 15 分鐘後重試")
	case errors.Is(err, ErrLoginFailed):
		return errors.BadRequest("identity.LOGIN_FAILED", "密碼錯誤")
	case errors.Is(err, ErrTOTPInvalid), errors.Is(err, ErrTOTPRequired):
		return errors.BadRequest("identity.TOTP_INVALID", "動態碼或恢復碼無效；已使用的動態碼請等待下一組")
	case errors.Is(err, ErrSessionInvalid):
		return errors.Unauthorized("identity.SESSION_INVALID", "安全設置已變更，請重新登錄")
	case errors.Is(err, ErrMFAConflict):
		return errors.BadRequest("identity.MFA_CONFLICT", "狀態已變化或操作參數無效，請刷新後重試")
	default:
		return errors.InternalServer("identity.MFA_FAILED", "操作失敗，請重試或聯繫管理員")
	}
}

// GetProfile 當前管理員信息。
func (s *AdminAuthService) GetProfile(ctx context.Context, _ *emptypb.Empty) (*adminv1.GetProfileReply, error) {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	u, err := s.uc.repo.FindByUsername(ctx, claims.Username)
	if err != nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	// 權限點清單下發（auth.proto GetProfileReply.permissions——前端據此生成菜單與按鈕）。
	// 讀取失敗時下發空清單（fail-closed：後端中間件每請求仍獨立校驗，不受此快照影響）。
	perms, _ := s.az.PermissionsOf(ctx, claims.RoleID)
	if perms == nil {
		perms = []string{}
	}
	return &adminv1.GetProfileReply{Admin: toAdminProfile(*u), Permissions: perms}, nil
}

func mapLoginErr(err error) error {
	switch {
	case errors.Is(err, ErrIPLimited):
		return errors.New(429, "identity.IP_LIMITED", "請求過於頻繁，請稍後重試")
	case errors.Is(err, ErrMFAExpired), errors.Is(err, ErrTOTPInvalid), errors.Is(err, ErrMFAConflict):
		return mapMFAErr(err)
	case errors.Is(err, captcha.ErrCaptchaRequired):
		return errors.BadRequest("identity.CAPTCHA_REQUIRED", "請輸入圖形驗證碼")
	case errors.Is(err, captcha.ErrCaptchaInvalid):
		return errors.BadRequest("identity.CAPTCHA_INVALID", "圖形驗證碼錯誤或已過期")
	case errors.Is(err, ErrAdminDisabled):
		return errors.Forbidden("identity.ADMIN_DISABLED", "賬號已禁用")
	case errors.Is(err, ErrLocked):
		return errors.Forbidden("identity.ACCOUNT_LOCKED", "登錄失敗次數過多，賬號已臨時鎖定")
	case errors.Is(err, ErrTOTPRequired):
		return errors.Unauthorized("identity.TOTP_REQUIRED", "需要兩步驗證碼")
	case errors.Is(err, ErrTOTPInvalid):
		return errors.Unauthorized("identity.TOTP_INVALID", "兩步驗證碼錯誤")
	case errors.Is(err, ErrSessionInvalid):
		return errors.Unauthorized("identity.SESSION_INVALID", "會話無效或已過期")
	default:
		return errors.Unauthorized("identity.LOGIN_FAILED", "賬號或密碼錯誤")
	}
}

func toAdminProfile(u AdminUser) *adminv1.AdminProfile {
	p := &adminv1.AdminProfile{
		TotpBoundAt: u.TOTPBoundAt,
		Id:          u.ID,
		Username:    u.Username,
		Nickname:    u.Nickname,
		Avatar:      u.Avatar,
		RoleId:      u.RoleID,
		TotpEnabled: len(u.TOTPSecretEnc) > 0,
		LastLoginIp: u.LastLoginIP,
	}
	if !u.LastLoginAt.IsZero() {
		p.LastLoginAt = timestamppb.New(u.LastLoginAt)
	}
	return p
}

func clientIP(ctx context.Context) string {
	if r, ok := khttp.RequestFromServerContext(ctx); ok {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			return host
		}
		return r.RemoteAddr
	}
	return ""

}

func (s *AdminAuthService) CancelTOTP(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	c := ClaimsFromContext(ctx)
	if c == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "未登錄")
	}
	if err := s.uc.CancelMFA(ctx, c.Subject, c.AuthVersion); err != nil {
		return nil, mapMFAErr(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *AdminAuthService) ChangePassword(ctx context.Context, req *adminv1.ChangeAdminPasswordRequest) (*emptypb.Empty, error) {
	noCache(ctx)
	c := ClaimsFromContext(ctx)
	if c == nil {
		return nil, errors.Unauthorized("identity.UNAUTHORIZED", "請先登錄")
	}
	err := s.uc.ChangeOwnPassword(ctx, c.Subject, c.AuthVersion, req.GetCurrentPassword(), req.GetNewPassword(), req.GetConfirmPassword())
	switch {
	case err == nil:
		return &emptypb.Empty{}, nil
	case errors.Is(err, ErrPasswordInput):
		return nil, errors.BadRequest("identity.PASSWORD_INPUT", "請檢查新密碼長度（至少 6 個字符）、兩次輸入是否一致，以及是否與當前密碼相同")
	case errors.Is(err, ErrLoginFailed):
		return nil, errors.BadRequest("identity.CURRENT_PASSWORD_INVALID", "當前密碼不正確")
	case errors.Is(err, ErrLocked):
		return nil, errors.New(429, "identity.ACCOUNT_LOCKED", "密碼驗證失敗次數過多，請稍後重試")
	case errors.Is(err, ErrSessionInvalid), errors.Is(err, ErrAdminDisabled):
		return nil, mapLoginErr(err)
	case errors.Is(err, ErrMFAConflict):
		return nil, errors.Conflict("identity.SECURITY_CONFLICT", "賬號安全狀態已變化，請重新登錄後再試")
	default:
		return nil, errors.InternalServer("identity.PASSWORD_CHANGE_FAILED", "修改密碼失敗，請稍後重試")
	}
}
