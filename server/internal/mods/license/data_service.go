package license

// 管理面 API（M3）：許可證安裝/查詢/清除（license:read 查詢 / license:write 超管專屬）。

import (
	"context"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/platform/license"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminLicenseService 許可證管理。
type AdminLicenseService struct {
	adminv1.UnimplementedAdminLicenseServiceServer
	repo *LicenseRepo
}

// NewAdminLicenseService 構造。
func NewAdminLicenseService(repo *LicenseRepo) *AdminLicenseService {
	return &AdminLicenseService{repo: repo}
}

// GetLicenseStatus 狀態查詢。
func (s *AdminLicenseService) GetLicenseStatus(ctx context.Context, _ *emptypb.Empty) (*adminv1.LicenseStatus, error) {
	return toStatusPB(s.repo.Status(ctx)), nil
}

// InstallLicense 安裝（校驗失敗不落庫）。
func (s *AdminLicenseService) InstallLicense(ctx context.Context, req *adminv1.InstallLicenseRequest) (*adminv1.LicenseStatus, error) {
	if req.GetContent() == "" {
		return nil, errors.BadRequest("license.CONTENT_REQUIRED", "許可證內容必填")
	}
	if err := s.repo.Install(ctx, req.GetContent()); err != nil {
		return nil, errors.BadRequest("license.INVALID", "許可證無效（簽名/綁定/到期任一失敗）")
	}
	return toStatusPB(s.repo.Status(ctx)), nil
}

// ClearLicense 清除（回到社區版）。
func (s *AdminLicenseService) ClearLicense(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if err := s.repo.Clear(ctx); err != nil {
		return nil, errors.InternalServer("license.CLEAR_FAILED", "清除失敗")
	}
	return &emptypb.Empty{}, nil
}

func toStatusPB(st Status) *adminv1.LicenseStatus {
	return &adminv1.LicenseStatus{
		Installed: st.Installed, Valid: st.Valid,
		InstanceId: st.InstanceID, Domain: st.Domain,
		Features: st.Features, ExpiresAt: st.ExpiresAt, Error: st.Error,
	}
}

var _ = license.ErrInvalidLicense
