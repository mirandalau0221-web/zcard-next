package identity

// StoreUserService 用户中心（ 前置）：註冊/登錄/Me（user realm JWT）。
// 歸因鏈：invite_code=<user_id> → l1=邀請人 l2=邀請人.l1 l3=邀請人.l2（環狀拒絕）。

import (
	"context"
	stdErrors "errors"
	"strings"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/memberlevel"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellerprofile"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/mods/captcha"
	"github.com/NovaWorks/zcard-next/server/internal/platform/authn"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// stdErr 便捷別名（本文件 stdErrors 與 kratos errors 雙包共存）

// StoreUserService 用户中心服務。
type StoreUserService struct {
	storefrontv1.UnimplementedStoreUserServiceServer
	repo    *UserRepo
	signer  *authn.Signer
	data    *data.Data
	pwd     *PasswordService      // 自服務（找回/改密/改資料）
	regCode *RegisterCodeService  // 註冊驗證碼（email/phone 通道）
	regCfg  *RegisterCodeSettings // security 組註冊開關/方式
	captcha *captcha.Service      // 圖形驗證碼（scene: login/register/reset）
}

// NewStoreUserService 構造。
func NewStoreUserService(repo *UserRepo, signer *authn.Signer, d *data.Data, pwd *PasswordService, regCode *RegisterCodeService, regCfg *RegisterCodeSettings, cap *captcha.Service) *StoreUserService {
	return &StoreUserService{repo: repo, signer: signer, data: d, pwd: pwd, regCode: regCode, regCfg: regCfg, captcha: cap}
}

// UserRepo 用户倉儲。
type UserRepo struct{ data *data.Data }

// NewUserRepo 構造。
func NewUserRepo(d *data.Data) *UserRepo { return &UserRepo{data: d} }

// Username 按 ID 取用户名（port.UserReader 實現；audit 安全日誌主體富化用）。
func (r *UserRepo) Username(ctx context.Context, id uint64) (string, error) {
	u, err := data.Client(ctx, r.data).User.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// RegisterInput 註冊入參。
type RegisterInput struct {
	Username   string
	Password   string
	Email      string
	Phone      string
	InviteCode string
}

// Register 註冊（歸因鏈寫入；username 唯一）。
func (r *UserRepo) Register(ctx context.Context, in RegisterInput) (*ent.User, error) {
	if len(in.Username) < 3 || len(in.Username) > 30 {
		return nil, stdErrors.New("identity.USERNAME_INVALID: 3-30 字符")
	}
	if len(in.Password) < 6 {
		return nil, stdErrors.New("identity.PASSWORD_TOO_SHORT: 至少 6 位")
	}
	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	// 註冊、邀請鏈和獲贈資格同事務寫入。唯一衝突在完整回滾後重試，兼容 PostgreSQL。
	var result *ent.User
	for attempt := 0; attempt < 2; attempt++ {
		err = data.Tx(ctx, r.data, func(ctx context.Context) error {
			c := data.Client(ctx, r.data)
			var l1, l2, l3, granted uint64
			if strings.TrimSpace(in.InviteCode) != "" {
				inviter, e := r.ResolvePromoCodeChecked(ctx, in.InviteCode)
				if e != nil {
					return e
				}
				if inviter == nil {
					return errors.BadRequest("identity.INVITER_NOT_FOUND", "推薦碼無效，請檢查或清空後重試")
				}
				// 鎖順序與後台配置一致：等級 → 推薦人；拿鎖後使用當前讀重新校驗配置。
				expectedLevel := inviter.InviteLevelID
				if expectedLevel > 0 {
					// MySQL changed-row counts cannot establish existence for a no-op update.
					if r.data.Dialect == db.SQLite {
						if _, e := c.MemberLevel.Update().Where(memberlevel.ID(expectedLevel)).AddSort(0).Save(ctx); e != nil {
							return e
						}
					}
					lq := c.MemberLevel.Query().Where(memberlevel.ID(expectedLevel), memberlevel.Enabled(true))
					if r.data.Dialect != db.SQLite {
						lq = lq.ForUpdate()
					}
					if _, e := lq.Only(ctx); e != nil {
						if !ent.IsNotFound(e) {
							return e
						}
						return errors.BadRequest("identity.INVITE_LEVEL_INVALID", "推薦贈送等級已失效，請聯繫管理員")
					}
				}
				if r.data.Dialect == db.SQLite {
					if _, e := c.User.UpdateOneID(inviter.ID).AddInviteLevelID(0).Save(ctx); e != nil {
						return e
					}
				}
				q := c.User.Query().Where(user.ID(inviter.ID))
				if r.data.Dialect != db.SQLite {
					q = q.ForUpdate()
				}
				inviter, e = q.Only(ctx)
				if e != nil {
					return e
				}
				if inviter.Status != user.StatusActive {
					return errors.BadRequest("identity.INVITER_UNAVAILABLE", "推薦人已停用，請清空推薦碼後重試")
				}
				if inviter.InviteLevelID != expectedLevel {
					return errors.Conflict("identity.INVITE_CHANGED", "推薦權益已調整，請刷新後重試")
				}
				l1, l2, l3, granted = inviter.ID, inviter.InviteL1, inviter.InviteL2, expectedLevel
			}
			create := c.User.Create().SetUsername(in.Username).SetPasswordHash(hash).
				SetStatus(user.StatusActive).SetInviteL1(l1).SetInviteL2(l2).SetInviteL3(l3).
				SetReferralLevelID(granted).SetPromoCode(genPromoCode())
			if in.Email != "" {
				create.SetEmail(in.Email)
			}
			if in.Phone != "" {
				create.SetPhone(in.Phone)
			}
			u, e := create.Save(ctx)
			if e != nil {
				return e
			}
			if granted > 0 {
				e = c.AuditLog.Create().SetOperatorType("system").SetOperatorID(0).
					SetAction("REGISTER").SetRoute("/api/v1/storefront/user/register").
					SetAfter(map[string]any{"user_id": u.ID, "inviter_id": l1, "referral_level_id": granted}).Exec(ctx)
				if e != nil {
					return e
				}
			}
			result = u
			return nil
		})
		if !ent.IsConstraintError(err) {
			return result, err
		}
	}
	return nil, errors.BadRequest("identity.USERNAME_TAKEN", "用户名、郵箱或手機號已被註冊")
}

// FindByUsername 按用户名查。
func (r *UserRepo) FindByUsername(ctx context.Context, username string) (*ent.User, error) {
	u, err := data.Client(ctx, r.data).User.Query().
		Where(user.Username(username)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, stdErrors.New("identity.BAD_CREDENTIALS")
		}
		return nil, err
	}
	return u, nil
}

// FindByLoginIdentifier 登錄混輸（用户名/郵箱/手機號三標識一次查詢——
// acg-faka 同款體驗：單輸入框自動識別）。唯一索引三選一，無歧義。
func (r *UserRepo) FindByLoginIdentifier(ctx context.Context, identifier string) (*ent.User, error) {
	u, err := data.Client(ctx, r.data).User.Query().
		Where(
			user.Or(
				user.Username(identifier),
				user.Email(identifier),
				user.Phone(identifier),
			),
		).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, stdErrors.New("identity.BAD_CREDENTIALS")
		}
		return nil, err
	}
	return u, nil
}

// Register API（security.register_method 驅動：username=免驗證 / email=郵箱碼 / phone=短信碼）。
func (s *StoreUserService) Register(ctx context.Context, req *storefrontv1.RegisterRequest) (*storefrontv1.RegisterReply, error) {
	// 圖形驗證碼（captcha_register 開啓時前置）
	if s.captcha != nil {
		if err := s.captcha.VerifyScene(ctx, captcha.SceneRegister, req.GetCaptchaId(), req.GetCaptchaCode()); err != nil {
			return nil, err
		}
	}
	// 註冊開關（security.register_enabled）
	if s.regCfg != nil && !s.regCfg.RegisterEnabled(ctx) {
		return nil, stdErrors.New("identity.REGISTER_DISABLED: 站點暫未開放註冊")
	}
	// 註冊方式（多選）：email/phone 通道啓用時對應目標必填 + 驗碼（一次性）
	if s.regCfg != nil {
		if s.regCfg.MethodEnabled(ctx, "email") {
			if req.GetEmail() == "" {
				return nil, stdErrors.New("identity.EMAIL_REQUIRED: 請填寫郵箱並完成驗證")
			}
			if err := s.regCode.VerifyRegisterCode(ctx, req.GetEmail(), req.GetCode(), "email"); err != nil {
				return nil, err
			}
		}
		if s.regCfg.MethodEnabled(ctx, "phone") {
			if req.GetPhone() == "" {
				return nil, stdErrors.New("identity.PHONE_REQUIRED: 請填寫手機號並完成驗證")
			}
			if err := s.regCode.VerifyRegisterCode(ctx, req.GetPhone(), req.GetCode(), "phone"); err != nil {
				return nil, err
			}
		}
	}
	u, err := s.repo.Register(ctx, RegisterInput{
		Username: req.GetUsername(), Password: req.GetPassword(),
		Email: req.GetEmail(), Phone: req.GetPhone(), InviteCode: req.GetInviteCode(),
	})
	if err != nil {
		return nil, err
	}
	token, exp, err := s.signer.Issue(authn.RealmUser, u.ID, u.Username, 0)
	if err != nil {
		return nil, err
	}
	return &storefrontv1.RegisterReply{UserId: u.ID, Token: token, ExpiresAt: exp.Unix(), PromoCode: u.PromoCode}, nil
}

// SendRegisterCode API（發碼：60s 冷卻 + 已註冊拒絕 + 通道格式校驗）。
func (s *StoreUserService) SendRegisterCode(ctx context.Context, req *storefrontv1.SendRegisterCodeRequest) (*storefrontv1.SendRegisterCodeReply, error) {
	// 圖形驗證碼（captcha_register 場景——發碼前防機器人，acg-faka 同款紀律）
	if s.captcha != nil {
		if err := s.captcha.VerifyScene(ctx, captcha.SceneRegister, req.GetCaptchaId(), req.GetCaptchaCode()); err != nil {
			return nil, err
		}
	}
	if err := s.regCode.SendRegisterCode(ctx, req.GetTarget(), req.GetChannel()); err != nil {
		return nil, err
	}
	return &storefrontv1.SendRegisterCodeReply{}, nil
}

// mapStoreAuthErr storefront 認證錯誤映射（登錄/找回密碼/改密）：
// 普通 error → kratos 帶狀態碼與中文 message（前端直接展示 message）。
func mapStoreAuthErr(err error) error {
	msg := err.Error()
	switch {
	case msg == "identity.BAD_CREDENTIALS":
		return errors.Unauthorized("identity.BAD_CREDENTIALS", "用户名或密碼錯誤")
	case msg == "identity.USER_DISABLED":
		return errors.Forbidden("identity.USER_DISABLED", "賬號已被禁用，請聯繫管理員")
	case msg == "identity.USERNAME_TAKEN":
		return errors.BadRequest("identity.USERNAME_TAKEN", "用户名已被註冊")
	case msg == "identity.PASSWORD_TOO_SHORT" || strings.HasPrefix(msg, "identity.PASSWORD_TOO_SHORT"):
		return errors.BadRequest("identity.PASSWORD_TOO_SHORT", "密碼至少 6 位")
	case msg == "identity.UNAUTHORIZED" || msg == "identity.SESSION_INVALID":
		return errors.Unauthorized("identity.UNAUTHORIZED", "登錄狀態已失效，請重新登錄")
	case msg == "identity.USER_NOT_FOUND":
		return errors.NotFound("identity.USER_NOT_FOUND", "賬號不存在")
	case msg == "identity.RESET_CODE_INVALID" || msg == "identity.CODE_INVALID":
		return errors.BadRequest("identity.CODE_INVALID", "驗證碼錯誤或已過期")
	default:
		// 已是 kratos errors（驗證碼等）原樣透傳
		if se, ok := err.(*errors.Error); ok {
			return se
		}
		return errors.InternalServer("identity.FAILED", msg)
	}
}

// Login API（username 字段混輸：用户名/郵箱/手機號）。
func (s *StoreUserService) Login(ctx context.Context, req *storefrontv1.LoginRequest) (*storefrontv1.LoginReply, error) {
	// 圖形驗證碼（captcha_login 開啓時前置；未啓用直接放行）
	if s.captcha != nil {
		if err := s.captcha.VerifyScene(ctx, captcha.SceneLogin, req.GetCaptchaId(), req.GetCaptchaCode()); err != nil {
			return nil, mapStoreAuthErr(err)
		}
	}
	u, err := s.repo.FindByLoginIdentifier(ctx, req.GetUsername())
	if err != nil {
		return nil, mapStoreAuthErr(err)
	}
	if !crypto.VerifyPassword(u.PasswordHash, req.GetPassword()) {
		return nil, mapStoreAuthErr(stdErrors.New("identity.BAD_CREDENTIALS"))
	}
	if string(u.Status) != "active" {
		return nil, mapStoreAuthErr(stdErrors.New("identity.USER_DISABLED"))
	}
	token, exp, err := s.signer.Issue(authn.RealmUser, u.ID, u.Username, 0)
	if err != nil {
		return nil, mapStoreAuthErr(err)
	}
	return &storefrontv1.LoginReply{
		AccessToken: token, TokenType: "Bearer", ExpiresAt: exp.Unix(),
	}, nil
}

// Me API（登錄態；推廣碼懶生成——存量用户首次訪問即補）。
func (s *StoreUserService) Me(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.MeReply, error) {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return nil, stdErrors.New("identity.UNAUTHORIZED")
	}
	u, err := data.Client(ctx, s.data).User.Get(ctx, claims.Subject)
	if err != nil {
		return nil, stdErrors.New("identity.UNAUTHORIZED")
	}
	promoCode := u.PromoCode
	if promoCode == "" {
		promoCode = s.repo.EnsurePromoCode(ctx, u.ID)
	}
	reply := &storefrontv1.MeReply{
		UserId: u.ID, Username: u.Username, Email: u.Email,
		CreatedAt: u.CreatedAt.Unix(), PromoCode: promoCode,
	}
	// 分站主身份（approved）
	if p, err := data.Client(ctx, s.data).ResellerProfile.Query().
		Where(resellerprofile.UserID(u.ID), resellerprofile.StatusEQ(resellerprofile.StatusApproved)).
		Only(ctx); err == nil {
		reply.IsReseller = true
		reply.ResellerProfileId = p.ID
	}
	return reply, nil
}

// ── 自服務 API（薄 transport：委託 PasswordService）──────────

// ForgotPassword 發送找回密碼驗證碼（防枚舉：任何輸入都成功）。
func (s *StoreUserService) ForgotPassword(ctx context.Context, req *storefrontv1.ForgotPasswordRequest) (*storefrontv1.ForgotPasswordReply, error) {
	// 圖形驗證碼（captcha_reset 開啓時前置——發碼前防爆破）
	if s.captcha != nil {
		if err := s.captcha.VerifyScene(ctx, captcha.SceneReset, req.GetCaptchaId(), req.GetCaptchaCode()); err != nil {
			return nil, err
		}
	}
	if err := s.pwd.ForgotPassword(ctx, req.GetEmail()); err != nil {
		return nil, mapStoreAuthErr(err)
	}
	return &storefrontv1.ForgotPasswordReply{}, nil
}

// ResetPassword 驗碼重置（一次性；吊銷 session；重置即登錄）。
func (s *StoreUserService) ResetPassword(ctx context.Context, req *storefrontv1.ResetPasswordRequest) (*storefrontv1.ResetPasswordReply, error) {
	u, err := s.pwd.ResetPassword(ctx, req.GetEmail(), req.GetCode(), req.GetNewPassword())
	if err != nil {
		return nil, mapStoreAuthErr(err)
	}
	token, exp, err := s.signer.Issue(authn.RealmUser, u.ID, u.Username, 0)
	if err != nil {
		return nil, err
	}
	return &storefrontv1.ResetPasswordReply{Token: token, ExpiresAt: exp.Unix()}, nil
}

// ChangePassword 登錄態改密（新 token 保當前會話）。
func (s *StoreUserService) ChangePassword(ctx context.Context, req *storefrontv1.ChangePasswordRequest) (*storefrontv1.ChangePasswordReply, error) {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return nil, stdErrors.New("identity.UNAUTHORIZED")
	}
	u, err := s.pwd.ChangePassword(ctx, claims.Subject, req.GetOldPassword(), req.GetNewPassword())
	if err != nil {
		return nil, mapStoreAuthErr(err)
	}
	token, exp, err := s.signer.Issue(authn.RealmUser, u.ID, u.Username, 0)
	if err != nil {
		return nil, err
	}
	return &storefrontv1.ChangePasswordReply{Token: token, ExpiresAt: exp.Unix()}, nil
}

// UpdateProfile 改郵箱（Me 語義響應）。
func (s *StoreUserService) UpdateProfile(ctx context.Context, req *storefrontv1.UpdateProfileRequest) (*storefrontv1.MeReply, error) {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return nil, stdErrors.New("identity.UNAUTHORIZED")
	}
	if err := s.pwd.UpdateProfile(ctx, claims.Subject, req.GetEmail()); err != nil {
		return nil, err
	}
	return s.Me(ctx, nil)
}
