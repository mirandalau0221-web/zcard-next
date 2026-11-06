// 更新鏈 service（doc/在線更新方案.md §9）：狀態機 + 單飛 + 進度回調 + 重啓 hook。
//
// 重啓分工：本模塊落位二進制後調用 restartFn（serve 層注入——優雅停機 +
// 三分支：進程管理器拉起 / 裸跑 syscall.Exec）；新進程經 update.state pending
// 健康門確認（updater.HealthGate，main 裝配）。
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
	"github.com/NovaWorks/zcard-next/server/internal/platform/updater"
)

// 階段取值（前端輪詢協議）。
const (
	PhaseIdle        = "idle"
	PhaseChecking    = "checking"
	PhaseBackingUp   = "backing_up"
	PhaseDownloading = "downloading"
	PhaseApplying    = "applying"
	PhaseRestarting  = "restarting"
	PhaseVerifying   = "verifying"   // 新進程健康門中（磁盤 state pending 合成）
	PhaseRolledBack  = "rolled_back" // 最近一次結果為回滾
	PhaseFailed      = "failed"
)

// ErrBusy 更新鏈進行中（apply 單飛互斥）。
var ErrBusy = errors.New("update: 更新正在進行中")

// ErrDisabled 面板更新被禁用（多進程模式，方案 §12——api/worker 分進程會撞
// update.state 且 worker 跑舊代碼+新 schema，須走 CLI 滾動）。
var ErrDisabled = errors.New("update: 當前為多進程部署，面板在線更新已禁用（請用 zcard self-update 滾動各進程）")

// probeCacheTTL auto 源探測緩存（方案 §4.3：10 分鐘）。
const probeCacheTTL = 10 * time.Minute

// Status 狀態快照（admin API 下發；Current/Supervisor 每次現算——重啓後即新值）。
type Status struct {
	Phase         string
	Current       string
	Target        string
	Progress      int32
	Err           string
	Source        string // 生效源展示（github | <accel> | static:<base>）
	Mode          string // 配置模式
	Supervisor    string
	HasUpdate     bool
	Notes         string
	Latest        string
	CheckedAt     time.Time
	BackupDir     string
	Busy          bool
	History       []updater.ReleaseNote // 歷史版本 changelog（manifest 權威源）
	Prev          string                // 上一次版本（最近一次更新的 from——update.state.FromVer 持久保存）
	BackupReady   bool                  // 備份工具就緒（pg_dump/mysqldump 按方言；缺失則更新會被 fail-closed 中止）
	BackupHint    string                // 缺失時的安裝指引（事前警示，不等更新失敗才報）
	DockerReady   bool
	DockerHint    string
	RollbackReady bool
}

// Service 更新編排。
type Service struct {
	mu       sync.Mutex
	busy     bool
	checking bool
	st       Status
	probe    *updater.ProbeOutcome
	probedAt time.Time

	dataCfg   *conf.Data
	uc        *settings.SettingsUsecase
	binPath   string
	restartFn func()
	disabled  string // 非空=禁用原因（多進程模式）
}

// NewService wire 構造。
func NewService(dataCfg *conf.Data, uc *settings.SettingsUsecase) *Service {
	bin := ""
	if p, err := os.Executable(); err == nil {
		if rp, err2 := filepath.EvalSymlinks(p); err2 == nil {
			bin = rp
		} else {
			bin = p
		}
	}
	return &Service{dataCfg: dataCfg, uc: uc, binPath: bin, st: Status{Phase: PhaseIdle}}
}

// SetRestartFn serve 層注入重啓回調（優雅停機 + 三分支；nil=禁用重啓——測試態）。
func (s *Service) SetRestartFn(f func()) { s.restartFn = f }

// Disable 禁用面板更新鏈（serve 層注入：-mode != all 時調用，方案 §12）。
func (s *Service) Disable(reason string) { s.disabled = reason }

// OnRestartFailed 重啓失敗復位（serve 層 exec 失敗分支調用）：新版本已落盤但進程
// 未能重啓——恢復 busy 允許重試，phase 轉 failed 附手動指引（磁盤已是新版）。
func (s *Service) OnRestartFailed(err error) {
	s.mu.Lock()
	s.busy = false
	s.st.Phase = PhaseFailed
	s.st.Err = fmt.Sprintf("重啓失敗（新版本已落盤，請手動重啓服務生效）: %v", err)
	s.mu.Unlock()
}

// DisabledErr 禁用態錯誤（空=啓用）。
func (s *Service) DisabledErr() error {
	if s.disabled == "" {
		return nil
	}
	return ErrDisabled
}

// sourceConfig settings system/update → updater.SourceConfig。
func (s *Service) sourceConfig(ctx context.Context) updater.SourceConfig {
	var cfg updater.SourceConfig
	if s.uc != nil {
		_ = settings.GetStruct(ctx, s.uc, "system", "update", &cfg)
	}
	return cfg.Normalize()
}

// resolveClient 源解析（auto 結果緩存 10 分鐘）。
func (s *Service) resolveClient(ctx context.Context, cfg updater.SourceConfig) (*updater.Client, *updater.ProbeOutcome, error) {
	if cfg.Mode != "auto" {
		return updater.ResolveSource(ctx, cfg, 5*time.Second) // 釘死模式無網絡探測
	}
	s.mu.Lock()
	if s.probe != nil && time.Since(s.probedAt) < probeCacheTTL {
		out := s.probe
		s.mu.Unlock()
		c := updater.NewClient(out.Mode, cfg.Repo, out.Accel, cfg.StaticBase)
		return c, out, nil
	}
	s.mu.Unlock()
	c, out, err := updater.ResolveSource(ctx, cfg, 5*time.Second)
	if err != nil {
		// 探測失敗清緩存：auto 判定可能基於過期網絡快照，下輪重探
		s.mu.Lock()
		s.probe = nil
		s.mu.Unlock()
		return nil, nil, err
	}
	s.mu.Lock()
	s.probe, s.probedAt = out, time.Now()
	s.mu.Unlock()
	return c, out, nil
}

// CheckResult 檢查結果。
type CheckResult struct {
	Current   string
	Latest    string
	HasUpdate bool
	Notes     string
	Channel   string
	Source    string
	History   []updater.ReleaseNote // 歷史版本 changelog（manifest 權威源）
}

// Check 手動檢查（源解析 + manifest 驗籤；結果緩存進 status 供彈窗展示）。
// 更新鏈進行中拒絕重入：Check 的 phase 遷移會打斷下載/應用狀態展示，
// 等待模式被破壞後前端彈窗退回確認態——另一標籤頁自動檢查即觸發（線上實錄）。
func (s *Service) Check(ctx context.Context) (*CheckResult, error) {
	if updater.IsContainer() && os.Getenv("ZCARD_UPDATER_SOCKET") != "" {
		if st, err := dockerCall(ctx, "/status", nil); err == nil && st.Busy {
			return nil, ErrBusy
		}
	}
	s.mu.Lock()
	if s.busy || s.checking || s.st.Phase == PhaseChecking || s.pendingTargetLocked() {
		s.mu.Unlock()
		return nil, ErrBusy
	}
	// Reserve the operation before releasing the lock: Apply/Rollback must
	// not start while a slow Check can still write its result or failure.
	s.checking = true
	s.st.Phase = PhaseChecking
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.checking = false
		if s.st.Phase == PhaseChecking {
			s.st.Phase, s.st.Err = PhaseIdle, ""
		}
	}()

	cfg := s.sourceConfig(ctx)
	cli, outcome, err := s.resolveClient(ctx, cfg)
	if err != nil {
		s.fail(fmt.Errorf("源解析失敗: %w", err))
		return nil, err
	}
	pub, err := updater.PublicKey(updater.DefaultPublicKeyHex)
	if err != nil {
		return nil, err
	}
	m, _, err := cli.FetchManifest(ctx, cfg.Channel, pub)
	if err != nil {
		s.fail(fmt.Errorf("獲取清單失敗: %w", err))
		return nil, err
	}
	res := &CheckResult{
		Current: cur(), Latest: m.Version,
		HasUpdate: updater.HasUpdate(m.Version, cur()),
		Notes:     m.Notes, Channel: m.Channel, Source: outcome.SourceDesc(),
		History: m.History,
	}
	s.mu.Lock()
	s.st.HasUpdate, s.st.Notes, s.st.Latest = res.HasUpdate, res.Notes, res.Latest
	s.st.Source, s.st.CheckedAt = res.Source, time.Now()
	s.st.History = res.History
	s.mu.Unlock()
	return res, nil
}

// pendingTargetLocked includes the new process's persisted health gate.
// Completed and failed attempts must not permanently disable future checks.
func (s *Service) pendingTargetLocked() bool {
	if s.st.Phase == PhaseFailed || s.st.Phase == PhaseRolledBack {
		return false
	}
	if s.st.Target != "" && s.st.Target != cur() {
		return true
	}
	if s.binPath != "" {
		state, err := updater.LoadState(s.binPath)
		return err == nil && state.Status == updater.StatePending
	}
	return false
}

// Apply 觸發更新（後台 goroutine 執行鏈；重複調用 ErrBusy）。
// 鏈：磁盤預檢 → DB 備份 → 落盤下載（進度）→ 原子替換 → 重啓 hook。
func (s *Service) Apply(ctx context.Context) error {
	if updater.IsContainer() {
		s.mu.Lock()
		version := s.st.Latest
		s.mu.Unlock()
		return s.applyDocker(ctx, version, false)
	}
	s.mu.Lock()
	if s.busy || s.checking || s.st.Phase == PhaseChecking {
		s.mu.Unlock()
		return ErrBusy
	}
	s.busy = true
	s.st.Phase, s.st.Target, s.st.Err = PhaseChecking, "", ""
	s.mu.Unlock()
	go s.run()
	return nil
}

func (s *Service) run() {
	// 獨立 ctx：apply 接口即時返回，請求 ctx 不能帶入後台鏈
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	fail := func(err error) {
		s.fail(err)
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}
	if s.binPath == "" {
		fail(errors.New("無法定位當前二進制"))
		return
	}

	cfg := s.sourceConfig(ctx)
	cli, outcome, err := s.resolveClient(ctx, cfg)
	if err != nil {
		fail(fmt.Errorf("源解析失敗: %w", err))
		return
	}
	pub, err := updater.PublicKey(updater.DefaultPublicKeyHex)
	if err != nil {
		fail(err)
		return
	}
	m, _, err := cli.FetchManifest(ctx, cfg.Channel, pub)
	if err != nil {
		fail(fmt.Errorf("獲取清單失敗: %w", err))
		return
	}
	// 版本單調（防降級攻擊；CLI 同款約束）
	if !updater.IsSemver(m.Version) || updater.CompareSemver(m.Version, cur()) <= 0 {
		fail(fmt.Errorf("%w（當前 %s ↔ 目標 %s）", updater.ErrRefuseUpdate, cur(), m.Version))
		return
	}
	s.mu.Lock()
	s.st.Target = m.Version
	s.mu.Unlock()
	assetName := "zcard-" + runtime.GOOS + "-" + runtime.GOARCH
	var assetSize int64
	for _, f := range m.Files {
		if f.Name == assetName {
			assetSize = f.Size
		}
	}
	dir := filepath.Dir(s.binPath)
	// 磁盤預檢（方案 §8）：產物 + 64MB 餘量
	if err := updater.CheckDiskSpace(dir, assetSize+64<<20); err != nil {
		fail(err)
		return
	}

	// 備份（DB + 舊二進制；失敗中止——安全網優先）
	s.setPhase(PhaseBackingUp)
	backupDir := updater.BackupPath(s.binPath, cur())
	if err := BackupDatabase(ctx, s.dataCfg, backupDir); err != nil {
		fail(fmt.Errorf("備份失敗，中止更新: %w", err))
		return
	}
	_ = copyBinary(s.binPath, filepath.Join(backupDir, "zcard.old"))

	// 落盤下載（流式哈希；進度回調）。源失敗自動換下一個候選從頭重試（方案 §4.3：
	// 首選源（auto 探測/釘死）→ 其餘加速器逐個兜底，beta 通道排除加速器）
	s.mu.Lock()
	s.st.Phase, s.st.Progress, s.st.BackupDir = PhaseDownloading, 0, backupDir
	s.mu.Unlock()
	newPath := filepath.Join(dir, "zcard.new")
	candidates := s.downloadCandidates(cfg, cli)
	var dlErr error
	for i, cand := range candidates {
		if i > 0 {
			s.mu.Lock()
			s.st.Progress = 0
			s.mu.Unlock()
		}
		if err := downloadTo(ctx, cand, m, assetName, newPath, func(received, total int64) {
			if total > 0 {
				s.mu.Lock()
				s.st.Progress = int32(received * 100 / total)
				s.mu.Unlock()
			}
		}); err != nil {
			_ = os.Remove(newPath)
			dlErr = fmt.Errorf("下載失敗（源 %s）: %w", cand.Desc(), err)
			continue
		}
		dlErr = nil
		s.mu.Lock()
		s.st.Source = cand.Desc()
		s.mu.Unlock()
		break
	}
	if dlErr != nil {
		fail(dlErr)
		return
	}

	// 原子替換（rename 舞步 + pending 態）
	s.setPhase(PhaseApplying)
	if err := updater.ApplyFile(s.binPath, cur(), m.Version, newPath); err != nil {
		_ = os.Remove(newPath)
		fail(err)
		return
	}
	s.mu.Lock()
	s.st.Phase, s.st.Target, s.st.Source = PhaseRestarting, m.Version, outcome.SourceDesc()
	s.mu.Unlock()

	// 重啓 hook（serve 層三分支；本 goroutine 就此讓位——進程即將被停機/替換）
	if s.restartFn != nil {
		s.restartFn()
	}
}

// Rollback 回滾 .prev 並重啓（新版不健康時的面板逃生口）。
func (s *Service) Rollback(ctx context.Context) error {
	if updater.IsContainer() {
		st, err := dockerCall(ctx, "/status", nil)
		if err != nil {
			return err
		}
		if !st.RollbackReady {
			return fmt.Errorf("沒有數據庫結構兼容的上一版本可回退")
		}
		return s.applyDocker(ctx, st.RollbackVersion, true)
	}
	if s.binPath == "" {
		return errors.New("update: 無法定位當前二進制")
	}
	// 單飛守衞：更新鏈進行中（下載/落位中）同時回滾 = 磁盤狀態競態破壞
	s.mu.Lock()
	if s.busy || s.checking || s.st.Phase == PhaseChecking {
		s.mu.Unlock()
		return ErrBusy
	}
	s.mu.Unlock()
	if err := updater.Rollback(s.binPath); err != nil {
		return err
	}
	s.setPhase(PhaseRestarting)
	if s.restartFn != nil {
		go s.restartFn()
	}
	return nil
}

// Snapshot 狀態快照（Current/Supervisor/Mode 現算；磁盤 state 合成 verifying/rolled_back）。
func (s *Service) Snapshot(ctx context.Context) Status {
	s.mu.Lock()
	st := s.st
	busy := s.busy
	s.mu.Unlock()
	st.Current = cur()
	st.Busy = busy
	st.Supervisor = s.supervisorKind(ctx)
	if updater.IsContainer() {
		return s.dockerSnapshot(ctx, st)
	}
	st.BackupReady, st.BackupHint = s.backupToolStatus()
	if s.binPath != "" {
		if state, err := updater.LoadState(s.binPath); err == nil && state != nil {
			// 新進程延續目標版本（exec 重啓後內存態清零，磁盤 state 是唯一延續源——
			// 前端「等待恢復」以 current==target 判成功，target 斷檔會假性超時）
			if !busy && st.Target == "" && state.ToVer != "" {
				st.Target = state.ToVer
			}
			// 上一次版本號（from 持久保留：更新閉環後仍是最近一次升級起點）
			if state.FromVer != "" {
				st.Prev = state.FromVer
			}
			if st.Phase == PhaseIdle {
				if state.Status == updater.StatePending {
					st.Phase = PhaseVerifying
				} else if state.RolledBack {
					st.Phase = PhaseRolledBack
				}
			}
		}
	}
	if st.Mode == "" {
		st.Mode = s.sourceConfig(ctx).Mode
	}
	return st
}

func (s *Service) setPhase(p string) {
	s.mu.Lock()
	s.st.Phase = p
	if p == PhaseIdle {
		s.st.Err = ""
	}
	s.mu.Unlock()
}

func (s *Service) fail(err error) {
	s.mu.Lock()
	s.st.Phase, s.st.Err = PhaseFailed, err.Error()
	s.mu.Unlock()
}

// backupToolStatus 備份工具就緒檢測（方言感知）：更新前強制 DB 備份依賴
// pg_dump/mysqldump——缺失時提前在面板警示安裝指引，而非等更新失敗才暴露。
func (s *Service) backupToolStatus() (bool, string) {
	if s.dataCfg == nil || s.dataCfg.Database == nil {
		return true, ""
	}
	switch s.dataCfg.Database.Driver {
	case "postgres":
		if _, err := exec.LookPath("pg_dump"); err != nil {
			return false, "當前數據庫為 PostgreSQL，服務器缺少 pg_dump——在線更新將因無法備份數據庫而中止。請在服務器執行：apt install -y postgresql-client"
		}
	case "mysql":
		if _, err := exec.LookPath("mysqldump"); err != nil {
			return false, "當前數據庫為 MySQL，服務器缺少 mysqldump——在線更新將因無法備份數據庫而中止。請在服務器執行：apt install -y default-mysql-client"
		}
	}
	return true, ""
}

// cur 當前版本（settings 注入的內存值——重啓後自然為新版本號）。
func cur() string { return settings.ServerVersion() }

// SupervisorKind 進程管理器判定（配置顯式覆蓋 > 自動探測——寶塔等封裝環境
// env 探測有盲區，方案 §5）。main 的重啓三分支與 status 下發共用本口徑，
// 保證「顯示的」與「實際分流的」一致。
func (s *Service) SupervisorKind(ctx context.Context) string { return s.supervisorKind(ctx) }

// supervisorKind 配置顯式覆蓋 > 自動探測（寶塔等封裝環境 env 探測有盲區，
// 方案 §5——重啓三分支正確分流是更新安全前提，配置是權威出口）。
func (s *Service) supervisorKind(ctx context.Context) string {
	if updater.IsContainer() {
		return "docker"
	}
	cfg := s.sourceConfig(ctx)
	switch cfg.Supervisor {
	case "systemd", "supervisord", "pm2", "none":
		return cfg.Supervisor
	}
	return updater.DetectSupervisor()
}

// downloadCandidates 下載候選源序列：首選源 + 其餘加速器兜底（beta 排除——
// 加速鏡像不支持 beta；static/github 釘死模式同樣給加速器兜底，網絡突變時多一條生路）。
func (s *Service) downloadCandidates(cfg updater.SourceConfig, primary *updater.Client) []*updater.Client {
	out := []*updater.Client{primary}
	if cfg.Channel == "beta" {
		return out
	}
	for _, acc := range cfg.Accels {
		acc = strings.TrimRight(acc, "/")
		if acc == "" || acc == primary.Accel {
			continue
		}
		out = append(out, updater.NewClient(updater.SourceAccel, cfg.Repo, acc, ""))
	}
	if len(out) > 3 { // 最多試 3 個源（重試成本可控）
		out = out[:3]
	}
	return out
}

// downloadTo 流式落盤（DownloadAsset 邊寫邊哈希校驗；失敗文件由調用方刪）。
func downloadTo(ctx context.Context, cli *updater.Client, m *updater.Manifest, name, path string, onProgress updater.ProgressFunc) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := cli.DownloadAsset(ctx, m, name, f, onProgress); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func copyBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
