// 管理面 API（doc/在線更新方案.md §9；system:update 超管專屬——權限點在
// authz/permissions.go 預登記段掛 Op，審計經現有 operation 中間件自動覆蓋）。
package update

import (
	context "context"
	"os"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/platform/updater"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminUpdateService 在線更新管理面。
type AdminUpdateService struct {
	adminv1.UnimplementedAdminUpdateServiceServer
	svc *Service
}

// NewAdminUpdateService 構造。
func NewAdminUpdateService(svc *Service) *AdminUpdateService {
	return &AdminUpdateService{svc: svc}
}

// GetUpdateStatus 狀態輪詢（重啓窗口期連接失敗=仍在重啓，前端據此等待）。
func (s *AdminUpdateService) GetUpdateStatus(ctx context.Context, _ *emptypb.Empty) (*adminv1.UpdateStatus, error) {
	return toStatusPB(s.svc.Snapshot(ctx)), nil
}

// CheckUpdate 手動檢查（源探測 + manifest 驗籤）。
func (s *AdminUpdateService) CheckUpdate(ctx context.Context, _ *emptypb.Empty) (*adminv1.UpdateCheckResult, error) {
	if err := s.svc.DisabledErr(); err != nil {
		return nil, errors.Forbidden("update.DISABLED", err.Error())
	}
	res, err := s.svc.Check(ctx)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, errors.Conflict("update.BUSY", err.Error())
		}
		return nil, errors.InternalServer("update.CHECK_FAILED", err.Error())
	}
	return &adminv1.UpdateCheckResult{
		CurrentVersion: res.Current, LatestVersion: res.Latest,
		HasUpdate: res.HasUpdate, Notes: res.Notes, Channel: res.Channel, Source: res.Source,
		History: toHistoryPB(res.History),
	}, nil
}

// ApplyUpdate 觸發更新（單飛；進行中重複調用返回當前態）。
func (s *AdminUpdateService) ApplyUpdate(ctx context.Context, req *adminv1.ApplyUpdateRequest) (*adminv1.UpdateStatus, error) {
	if updater.IsContainer() && os.Getenv("ZCARD_UPDATER_SOCKET") == "" {
		return nil, errors.Forbidden("update.CONTAINER", updater.ErrContainerUpdate.Error())
	}
	if err := s.svc.DisabledErr(); err != nil {
		return nil, errors.Forbidden("update.DISABLED", err.Error())
	}
	if updater.IsContainer() {
		if err := s.svc.applyDocker(ctx, req.GetVersion(), false); err != nil {
			return nil, errors.BadRequest("update.DOCKER_FAILED", err.Error())
		}
		return toStatusPB(s.svc.Snapshot(ctx)), nil
	}
	if err := s.svc.Apply(ctx); err != nil {
		return toStatusPB(s.svc.Snapshot(ctx)), nil // ErrBusy 亦返回當前態（200 攜帶 busy）
	}
	return toStatusPB(s.svc.Snapshot(ctx)), nil
}

// RollbackUpdate 回滾上一版本並重啓。
func (s *AdminUpdateService) RollbackUpdate(ctx context.Context, _ *emptypb.Empty) (*adminv1.UpdateStatus, error) {
	if updater.IsContainer() && os.Getenv("ZCARD_UPDATER_SOCKET") == "" {
		return nil, errors.Forbidden("update.CONTAINER", updater.ErrContainerUpdate.Error())
	}
	if err := s.svc.DisabledErr(); err != nil {
		return nil, errors.Forbidden("update.DISABLED", err.Error())
	}
	if err := s.svc.Rollback(ctx); err != nil {
		if errors.Is(err, ErrBusy) {
			return toStatusPB(s.svc.Snapshot(ctx)), nil // 進行中：返回當前態（與 apply 一致）
		}
		return nil, errors.InternalServer("update.ROLLBACK_FAILED", err.Error())
	}
	return toStatusPB(s.svc.Snapshot(ctx)), nil
}

func toStatusPB(st Status) *adminv1.UpdateStatus {
	checkedAt := ""
	if !st.CheckedAt.IsZero() {
		checkedAt = st.CheckedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return &adminv1.UpdateStatus{
		Phase: st.Phase, CurrentVersion: st.Current, TargetVersion: st.Target,
		ProgressPercent: st.Progress, ErrorMessage: st.Err, Source: st.Source,
		Mode: st.Mode, SupervisorKind: st.Supervisor, HasUpdate: st.HasUpdate,
		Notes: st.Notes, LatestVersion: st.Latest, CheckedAt: checkedAt,
		BackupDir: st.BackupDir, Busy: st.Busy,
		History:     toHistoryPB(st.History),
		PrevVersion: st.Prev, BackupReady: st.BackupReady, BackupHint: st.BackupHint,
		DockerUpdateReady: st.DockerReady, DockerUpdateHint: st.DockerHint, RollbackReady: st.RollbackReady,
	}
}

func toHistoryPB(h []updater.ReleaseNote) []*adminv1.ReleaseNoteEntry {
	if len(h) == 0 {
		return nil
	}
	out := make([]*adminv1.ReleaseNoteEntry, 0, len(h))
	for _, e := range h {
		out = append(out, &adminv1.ReleaseNoteEntry{
			Version: e.Version, Channel: e.Channel, Notes: e.Notes, IssuedAt: e.IssuedAt,
		})
	}
	return out
}
