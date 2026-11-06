package update

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/platform/updater"
	"github.com/go-sql-driver/mysql"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type dockerStatus struct {
	Ready           bool   `json:"ready"`
	Protocol        int    `json:"protocol"`
	RollbackReady   bool   `json:"rollback_ready"`
	RollbackVersion string `json:"rollback_version"`
	Phase           string `json:"phase"`
	Busy            bool   `json:"busy"`
	Target          string `json:"target"`
	Previous        string `json:"previous"`
	Error           string `json:"error"`
	BackupDir       string `json:"backup_dir"`
	Progress        int32  `json:"progress"`
}

// Browser input cannot select a socket, command, repository or Compose project.
func dockerCall(ctx context.Context, path string, body any) (*dockerStatus, error) {
	socket, token := os.Getenv("ZCARD_UPDATER_SOCKET"), os.Getenv("ZCARD_UPDATER_TOKEN")
	if socket == "" || token == "" {
		return nil, updater.ErrContainerUpdate
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader, method = bytes.NewReader(raw), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://updater"+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Docker 升級助手未連接，請檢查 updater 服務")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, fmt.Errorf("Docker 升級助手響應無效")
	}
	var st dockerStatus
	if json.Unmarshal(raw, &st) != nil {
		return nil, fmt.Errorf("Docker 升級助手響應無效")
	}
	if resp.StatusCode != http.StatusOK {
		if st.Error == "" {
			st.Error = "請求被拒絕"
		}
		return nil, fmt.Errorf("Docker 升級助手：%s", st.Error)
	}
	return &st, nil
}

func (s *Service) dockerDialect() (string, error) {
	if s.dataCfg == nil || s.dataCfg.Database == nil {
		return "", fmt.Errorf("缺少數據庫配置")
	}
	db := s.dataCfg.Database
	switch db.Driver {
	case "sqlite":
		path := strings.SplitN(strings.TrimPrefix(db.Source, "file:"), "?", 2)[0]
		abs, err := filepath.Abs(path)
		if err == nil {
			if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
				abs = resolved
			}
		}
		if err != nil || !strings.HasPrefix(abs, "/app/") {
			return "", fmt.Errorf("SQLite 數據必須保存在 /app 數據卷中")
		}
		return "sqlite", nil
	case "mysql":
		cfg, err := mysql.ParseDSN(db.Source)
		if err == nil && cfg.Net == "tcp" && cfg.Addr == "mysql:3306" && cfg.DBName == "zcard" && cfg.User == "zcard" {
			return "mysql", nil
		}
	}
	return "", fmt.Errorf("Docker 在線更新目前支持 /app 內 SQLite 或官方 Compose 的 mysql:3306/zcard；其他數據庫請手動升級")
}

// DockerBackupDialect inspects configuration without opening the database.
func DockerBackupDialect(cfg *conf.Data) (string, error) {
	return (&Service{dataCfg: cfg}).dockerDialect()
}

func (s *Service) dockerSnapshot(ctx context.Context, st Status) Status {
	st.DockerHint = "請先備份數據，在原部署目錄執行 bash deploy/docker-install.sh --online，接入在線更新助手。"
	st.BackupReady = false
	if _, err := s.dockerDialect(); err != nil {
		st.DockerHint = err.Error()
		return st
	}
	d, err := dockerCall(ctx, "/status", nil)
	if err != nil {
		if os.Getenv("ZCARD_UPDATER_SOCKET") != "" {
			st.DockerHint = err.Error()
		}
		return st
	}
	if !d.Ready || d.Protocol != 1 {
		st.DockerHint = "升級助手協議不兼容，請更新部署工具"
		return st
	}
	st.DockerReady, st.BackupReady, st.RollbackReady = true, true, d.RollbackReady
	st.DockerHint = "通過獨立助手更新鏡像；切換時會短暫停機，更新前自動備份數據庫和應用數據。"
	st.BackupHint = "由 Docker 升級助手執行備份，失敗時中止更新"
	if d.Target != "" {
		st.Phase, st.Target, st.Prev, st.Busy = d.Phase, d.Target, d.Previous, d.Busy
		st.Progress, st.Err, st.BackupDir = d.Progress, d.Error, d.BackupDir
	}
	return st
}

func (s *Service) applyDocker(ctx context.Context, version string, rollback bool) error {
	if err := s.DisabledErr(); err != nil {
		return err
	}
	if os.Getenv("ZCARD_UPDATER_SOCKET") == "" {
		return updater.ErrContainerUpdate
	}
	dialect, err := s.dockerDialect()
	if err != nil {
		return err
	}
	if !updater.IsSemver(version) {
		return fmt.Errorf("請先檢查更新並確認目標版本")
	}
	path := "/apply"
	if rollback {
		path = "/rollback"
	}
	_, err = dockerCall(ctx, path, map[string]string{"version": version, "current": cur(), "dialect": dialect})
	return err
}
