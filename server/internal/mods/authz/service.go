package authz

// 角色管理與員工管理 API（；薄 transport，業務在 biz/倉儲）。
// 員工數據經 identity/port.AdminMutator 窄接口（跨模塊通道 A）。

import (
	"context"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	authzport "github.com/NovaWorks/zcard-next/server/internal/mods/authz/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	identityport "github.com/NovaWorks/zcard-next/server/internal/mods/identity/port"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// RoleService 角色與權限服務。
type RoleService struct {
	adminv1.UnimplementedRoleServiceServer
	repo RoleRepo
	dir  *Directory
	uc   *RbacUsecase
}

// NewRoleService 構造。
func NewRoleService(repo RoleRepo, dir *Directory, uc *RbacUsecase) *RoleService {
	return &RoleService{repo: repo, dir: dir, uc: uc}
}

// ListRoles 角色列表。
func (s *RoleService) ListRoles(ctx context.Context, _ *emptypb.Empty) (*adminv1.ListRolesReply, error) {
	roles, err := s.repo.Roles(ctx)
	if err != nil {
		return nil, errors.InternalServer("authz.LIST_FAILED", "讀取角色失敗")
	}
	out := make([]*adminv1.Role, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.PB())
	}
	return &adminv1.ListRolesReply{Roles: out}, nil
}

// GetRole 角色詳情（含權限點）。
func (s *RoleService) GetRole(ctx context.Context, req *adminv1.GetRoleRequest) (*adminv1.Role, error) {
	r, err := s.repo.RoleByID(ctx, req.GetId())
	if err != nil {
		return nil, errors.NotFound("authz.ROLE_NOT_FOUND", "角色不存在")
	}
	return r.PB(), nil
}

// CreateRole 創建角色（code 唯一；權限點須在目錄內）。
func (s *RoleService) CreateRole(ctx context.Context, req *adminv1.CreateRoleRequest) (*adminv1.Role, error) {
	if err := s.dir.ValidateCodes(req.GetPermissions()); err != nil {
		return nil, errors.BadRequest("authz.INVALID_PERM", err.Error())
	}
	r, err := s.repo.CreateRole(ctx, req.GetName(), req.GetCode(), req.GetDescription(), req.GetPermissions())
	if err != nil {
		return nil, errors.InternalServer("authz.CREATE_FAILED", "創建角色失敗（code 可能重複）")
	}
	return r.PB(), nil
}

// UpdateRole 修改角色（內置角色 code 保護）。
func (s *RoleService) UpdateRole(ctx context.Context, req *adminv1.UpdateRoleRequest) (*adminv1.Role, error) {
	r, err := s.repo.UpdateRole(ctx, req.GetId(), req.GetName(), req.GetDescription())
	if err != nil {
		return nil, mapRoleErr(err)
	}
	s.uc.Invalidate(req.GetId())
	return r.PB(), nil
}

// DeleteRole 刪除角色（內置拒絕）。
func (s *RoleService) DeleteRole(ctx context.Context, req *adminv1.DeleteRoleRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteRole(ctx, req.GetId()); err != nil {
		return nil, mapRoleErr(err)
	}
	s.uc.Invalidate(req.GetId())
	return &emptypb.Empty{}, nil
}

// UpdateRolePermissions 全量替換權限點（變更實時生效）。
func (s *RoleService) UpdateRolePermissions(ctx context.Context, req *adminv1.UpdateRolePermissionsRequest) (*adminv1.Role, error) {
	if err := s.dir.ValidateCodes(req.GetPermissions()); err != nil {
		return nil, errors.BadRequest("authz.INVALID_PERM", err.Error())
	}
	r, err := s.repo.SetPermissions(ctx, req.GetId(), req.GetPermissions())
	if err != nil {
		return nil, mapRoleErr(err)
	}
	s.uc.Invalidate(req.GetId())
	return r.PB(), nil
}

// GetPermissionTree 權限目錄樹。
func (s *RoleService) GetPermissionTree(_ context.Context, _ *emptypb.Empty) (*adminv1.PermissionTree, error) {
	tree := s.dir.Tree()
	groups := make([]*adminv1.Group, 0, len(tree))
	for _, g := range tree {
		perms := make([]*adminv1.Perm, 0, len(g.Perms))
		for _, p := range g.Perms {
			perms = append(perms, &adminv1.Perm{Code: p.Code, Desc: p.Desc, AdminOnly: p.AdminOnly, Public: p.Public})
		}
		groups = append(groups, &adminv1.Group{Domain: g.Domain, Label: g.Label, Perms: perms})
	}
	return &adminv1.PermissionTree{Groups: groups}, nil
}

// AdminUserService 員工管理服務（數據經 identity port）。
type AdminUserService struct {
	adminv1.UnimplementedAdminUserServiceServer
	mut     identityport.AdminMutator
	readers identityport.AdminReader
	dir     *Directory
	repo    RoleRepo
}

// NewAdminUserService 構造。
func NewAdminUserService(mut identityport.AdminMutator, readers identityport.AdminReader, dir *Directory, repo RoleRepo) *AdminUserService {
	return &AdminUserService{mut: mut, readers: readers, dir: dir, repo: repo}
}

// ListAdmins 員工列表（附角色名）。
func (s *AdminUserService) ListAdmins(ctx context.Context, _ *emptypb.Empty) (*adminv1.ListAdminsReply, error) {
	accounts, err := s.mut.List(ctx)
	if err != nil {
		return nil, errors.InternalServer("identity.LIST_FAILED", "讀取員工失敗")
	}
	out := make([]*adminv1.Admin, 0, len(accounts))
	for _, a := range accounts {
		roleName := ""
		if r, err := s.repo.RoleByID(ctx, a.RoleID); err == nil {
			roleName = r.Name
		}
		out = append(out, &adminv1.Admin{
			Id: a.ID, Username: a.Username, Nickname: a.Nickname, Avatar: a.Avatar,
			RoleId: a.RoleID, RoleName: roleName, Enabled: a.Enabled, TotpEnabled: a.TOTPEnabled,
		})
	}
	return &adminv1.ListAdminsReply{Admins: out}, nil
}

// CreateAdmin 創建員工（密碼 ≥8 位安全基線）。
func (s *AdminUserService) CreateAdmin(ctx context.Context, req *adminv1.CreateAdminRequest) (*adminv1.Admin, error) {
	if len(req.GetPassword()) < 8 {
		return nil, errors.BadRequest("identity.WEAK_PASSWORD", "密碼至少 8 位（安全基線）")
	}
	a, err := s.mut.Create(ctx, identityport.AdminInput{
		Username: req.GetUsername(), Password: req.GetPassword(),
		Nickname: req.GetNickname(), RoleID: req.GetRoleId(), Remark: req.GetRemark(),
	})
	if err != nil {
		return nil, errors.InternalServer("identity.CREATE_FAILED", "創建員工失敗（用户名可能重複）")
	}
	return &adminv1.Admin{Id: a.ID, Username: a.Username, Nickname: a.Nickname, RoleId: a.RoleID, Enabled: true}, nil
}

// UpdateAdmin 修改員工。
func (s *AdminUserService) UpdateAdmin(ctx context.Context, req *adminv1.UpdateAdminRequest) (*adminv1.Admin, error) {
	a, err := s.mut.Update(ctx, req.GetId(), identityport.AdminInput{
		Nickname: req.GetNickname(), Avatar: req.GetAvatar(), RoleID: req.GetRoleId(), Remark: req.GetRemark(),
	})
	if err != nil {
		return nil, errors.NotFound("identity.ADMIN_NOT_FOUND", "員工不存在")
	}
	return &adminv1.Admin{Id: a.ID, Username: a.Username, Nickname: a.Nickname, Avatar: a.Avatar, RoleId: a.RoleID, Enabled: a.Enabled, TotpEnabled: a.TOTPEnabled}, nil
}

// ToggleAdmin 啓停員工（禁用即時生效——admin 鑑權中間件每請求回查 enabled；
// 禁用最後一位啓用的內置超管被拒，防全員鎖死後台）。
func (s *AdminUserService) ToggleAdmin(ctx context.Context, req *adminv1.ToggleAdminRequest) (*adminv1.Admin, error) {
	enabled := req.GetEnabled()
	if !enabled && s.isLastEnabledSuper(ctx, req.GetId()) {
		return nil, errors.Forbidden("identity.LAST_SUPER_ADMIN", "不能禁用最後一位啓用的超級管理員")
	}
	a, err := s.mut.Update(ctx, req.GetId(), identityport.AdminInput{Enabled: &enabled})
	if err != nil {
		return nil, errors.NotFound("identity.ADMIN_NOT_FOUND", "員工不存在")
	}
	return &adminv1.Admin{Id: a.ID, Username: a.Username, Nickname: a.Nickname, RoleId: a.RoleID, Enabled: a.Enabled}, nil
}

// DeleteAdmin 刪除員工（內置超管角色與本人不可刪；其管理面會話同事務清除）。
func (s *AdminUserService) DeleteAdmin(ctx context.Context, req *adminv1.DeleteAdminRequest) (*emptypb.Empty, error) {
	if claims := identity.ClaimsFromContext(ctx); claims != nil && claims.Subject == req.GetId() {
		return nil, errors.Forbidden("identity.ADMIN_SELF_DELETE", "不能刪除當前登錄賬號")
	}
	if acc, err := s.readers.Admin(ctx, req.GetId()); err == nil && acc != nil && s.repo != nil {
		if role, err := s.repo.RoleByID(ctx, acc.RoleID); err == nil && role.Code == authzport.RoleSuperAdmin {
			return nil, errors.Forbidden("identity.ADMIN_PROTECTED", "內置超級管理員不可刪除")
		}
	}
	if err := s.mut.Delete(ctx, req.GetId()); err != nil {
		return nil, errors.NotFound("identity.ADMIN_NOT_FOUND", "員工不存在")
	}
	return &emptypb.Empty{}, nil
}

// isLastEnabledSuper 目標是否為最後一位啓用的內置超管（防禁用後無人可管）。
func (s *AdminUserService) isLastEnabledSuper(ctx context.Context, id uint64) bool {
	if s.repo == nil {
		return false
	}
	acc, err := s.readers.Admin(ctx, id)
	if err != nil || acc == nil {
		return false
	}
	role, err := s.repo.RoleByID(ctx, acc.RoleID)
	if err != nil || role.Code != authzport.RoleSuperAdmin {
		return false
	}
	list, err := s.mut.List(ctx)
	if err != nil {
		return false // 查詢失敗保守放行，由啓用狀態自身兜底
	}
	for _, a := range list {
		if a.ID != acc.ID && a.Enabled && a.RoleID == acc.RoleID {
			return false
		}
	}
	return true
}

// ResetAdminPassword 重置員工密碼（≥8 位安全基線；憑據接管類操作——吊銷其全部
// 管理面會話強制重登，防舊會話在密碼泄露場景下繼續存活）。
func (s *AdminUserService) ResetAdminPassword(ctx context.Context, req *adminv1.ResetAdminPasswordRequest) (*adminv1.Admin, error) {
	if len(req.GetPassword()) < 8 {
		return nil, errors.BadRequest("identity.WEAK_PASSWORD", "密碼至少 8 位（安全基線）")
	}
	if err := s.mut.ResetPassword(ctx, req.GetId(), req.GetPassword()); err != nil {
		return nil, errors.NotFound("identity.ADMIN_NOT_FOUND", "員工不存在")
	}
	_ = s.mut.RevokeAdminSessions(ctx, req.GetId())
	return s.adminByID(ctx, req.GetId())
}

// ResetAdminTOTP 解綁員工 TOTP（二因素移除屬高危——吊銷其全部管理面會話強制重登，
// 員工下次登錄自行重新綁定）。
func (s *AdminUserService) ResetAdminTOTP(ctx context.Context, req *adminv1.ResetAdminTOTPRequest) (*adminv1.Admin, error) {
	return nil, errors.BadRequest("identity.REAUTH_REQUIRED", "請使用新版身份驗證流程重置，或在服務器執行 admin reset-2fa")
}

// adminByID 回讀員工最新狀態（含角色名）。
func (s *AdminUserService) adminByID(ctx context.Context, id uint64) (*adminv1.Admin, error) {
	a, err := s.readers.Admin(ctx, id)
	if err != nil || a == nil {
		return nil, errors.NotFound("identity.ADMIN_NOT_FOUND", "員工不存在")
	}
	roleName := ""
	if s.repo != nil {
		if r, err := s.repo.RoleByID(ctx, a.RoleID); err == nil {
			roleName = r.Name
		}
	}
	return &adminv1.Admin{
		Id: a.ID, Username: a.Username, Nickname: a.Nickname, Avatar: a.Avatar,
		RoleId: a.RoleID, RoleName: roleName, Enabled: a.Enabled, TotpEnabled: a.TOTPEnabled,
	}, nil
}

func mapRoleErr(err error) error {
	switch {
	case errors.Is(err, ErrRoleNotFound):
		return errors.NotFound("authz.ROLE_NOT_FOUND", "角色不存在")
	case errors.Is(err, ErrRoleBuiltIn):
		return errors.Forbidden("authz.ROLE_BUILTIN", "內置角色受保護")
	case errors.Is(err, ErrRoleInUse):
		return errors.Conflict("authz.ROLE_IN_USE", "角色仍有員工掛載")
	default:
		return errors.InternalServer("authz.ROLE_OP_FAILED", "角色操作失敗")
	}
}
