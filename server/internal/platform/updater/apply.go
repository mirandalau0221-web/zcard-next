// updater 應用層：rename 舞步（原子替換）+ 狀態機 + 回滾 + 健康門（）。
//
// 磁盤佈局（同目錄，保證 rename 原子性——跨文件系統 rename 退化為 copy 即拒絕）：
//
//	<dir>/zcard 當前二進制（新版本落位後由進程管理器/exec 拉起，方案 ）
//	<dir>/zcard.prev 上一代（回滾位，僅保留一代）
//	<dir>/zcard.new 下載臨時文件（驗證通過後 rename 落位，失敗即刪）
//	<dir>/update.state 狀態機文件（pending → ok）
//	<dir>/backups/ 更新前備份（二進制+DB，MarkOK 後保留最近 N 代）
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// 狀態機取值。
const (
	StatePending = "pending" // 已替換二進制，等待新進程健康門確認
	StateOK      = "ok"      // 更新閉環（或無更新進行中）
)

// ErrNoRollback 無回滾位（update.state 非 pending 且無 zcard.prev）。
var ErrNoRollback = errors.New("updater: 無可回滾版本")

// State 更新狀態文件（serve 啓動讀取；pending 態裝配/遷移失敗自動回滾）。
type State struct {
	Status     string `json:"status"`
	FromVer    string `json:"from_version"`
	ToVer      string `json:"to_version"`
	AppliedAt  string `json:"applied_at"`
	BackupDir  string `json:"backup_dir,omitempty"`
	RolledBack bool   `json:"rolled_back,omitempty"`
}

// BackupFunc 更新前 DB 備份（cmd 層注入方言實現：SQLite VACUUM INTO /
// mysqldump / pg_dump；返回備份目錄）。nil = 跳過（僅告警）。
type BackupFunc func(ctx context.Context, destDir string) error

const (
	newName   = "zcard.new"
	prevName  = "zcard.prev"
	stateName = "update.state"
	backupDir = "backups"
)

// statePath / SidecarPath 狀態文件路徑（binaryPath 所在目錄）。
func statePath(binaryPath string) string {
	return filepath.Join(filepath.Dir(binaryPath), stateName)
}

// LoadState 讀取狀態（不存在 = 無更新進行中，Status=ok）。
func LoadState(binaryPath string) (*State, error) {
	raw, err := os.ReadFile(statePath(binaryPath))
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Status: StateOK}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		// 損壞狀態按無更新處理（fail-open 僅影響回滾位清理，不影響服務）
		return &State{Status: StateOK}, nil
	}
	return &s, nil
}

func saveState(binaryPath string, s *State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(binaryPath), raw, 0o644)
}

// Apply 原子替換（內存版薄包裝——小產物/測試用；生產更新鏈走 ApplyFile，
// 124MB 大二進制禁止整包進內存，方案 ）。
func Apply(binaryPath, fromVer, toVer string, newBin []byte) error {
	tmp := filepath.Join(filepath.Dir(binaryPath), newName)
	if err := os.WriteFile(tmp, newBin, 0o755); err != nil {
		return fmt.Errorf("updater: 寫入臨時文件失敗: %w", err)
	}
	if err := ApplyFile(binaryPath, fromVer, toVer, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ApplyFile 原子替換（rename 舞步，落盤版）：downloaded 為已通過哈希校驗的
// 新二進制文件（調用方直接下載到 <dir>/zcard.new，邊寫邊校驗零內存）。
// chmod → current 改名 .prev → downloaded 落位 current → 寫 pending 態。
// 任何一步失敗磁盤可恢復（downloaded 可刪，未到 rename 階段 current 未動）。
// downloaded 必須與 binaryPath 同目錄（同文件系統，rename 原子性前提）。
func ApplyFile(binaryPath, fromVer, toVer, downloaded string) error {
	dir := filepath.Dir(binaryPath)
	if filepath.Dir(downloaded) != dir {
		return fmt.Errorf("updater: 下載臨時文件與二進制不同目錄（rename 原子性前提）")
	}
	if err := os.Chmod(downloaded, 0o755); err != nil {
		return fmt.Errorf("updater: 臨時文件加執行位失敗: %w", err)
	}
	prev := filepath.Join(dir, prevName)
	_ = os.Remove(prev) // 上一代殘留（上次更新未閉環）直接覆蓋
	if err := os.Rename(binaryPath, prev); err != nil {
		return fmt.Errorf("updater: 保留回滾位失敗: %w", err)
	}
	if err := os.Rename(downloaded, binaryPath); err != nil {
		// 舞步中斷：回放 current（.prev 尚在）
		_ = os.Rename(prev, binaryPath)
		return fmt.Errorf("updater: 落位新版本失敗: %w", err)
	}
	return saveState(binaryPath, &State{
		Status: StatePending, FromVer: fromVer, ToVer: toVer,
		AppliedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// Rollback 顯式回滾：.prev swap 回 current，狀態清理。
// 無 .prev 返回 ErrNoRollback（OnFailure 單元隨機崩潰不誤傷）。
func Rollback(binaryPath string) error {
	dir := filepath.Dir(binaryPath)
	prev := filepath.Join(dir, prevName)
	if _, err := os.Stat(prev); err != nil {
		return ErrNoRollback
	}
	if err := copyOrRemoveBad(binaryPath); err != nil {
		return err
	}
	if err := os.Rename(prev, binaryPath); err != nil {
		return fmt.Errorf("updater: 回滾落位失敗: %w", err)
	}
	s, _ := LoadState(binaryPath)
	s.Status = StateOK
	s.RolledBack = true
	return saveState(binaryPath, s)
}

// copyOrRemoveBad 回滾前處理損壞的 current：改名留證（failed-<ts>）便於排查。
func copyOrRemoveBad(binaryPath string) error {
	if _, err := os.Stat(binaryPath); err != nil {
		return nil // current 不存在（啓動即崩場景）
	}
	dir := filepath.Dir(binaryPath)
	keep := filepath.Join(dir, "zcard.failed-"+time.Now().UTC().Format("20060102T150405"))
	_ = os.Rename(binaryPath, keep) // 失敗即刪，不阻塞回滾
	return nil
}

// RollbackOnBootFailure serve 啓動失敗路徑：pending 態才回滾（隨機崩潰不動二進制）。
// 返回是否執行了回滾（日誌用）。
func RollbackOnBootFailure(binaryPath string) bool {
	s, err := LoadState(binaryPath)
	if err != nil || s.Status != StatePending {
		return false
	}
	if err := Rollback(binaryPath); err != nil {
		return false
	}
	return true
}

// MarkOK 健康門通過：pending → ok，清過期備份（保留最近 keep 代）。
func MarkOK(binaryPath string, keep int) error {
	s, err := LoadState(binaryPath)
	if err != nil {
		return err
	}
	if s.Status != StatePending {
		return nil
	}
	s.Status = StateOK
	if err := saveState(binaryPath, s); err != nil {
		return err
	}
	pruneBackups(filepath.Join(filepath.Dir(binaryPath), backupDir), keep)
	return nil
}

func pruneBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	// 目錄名含時間戳，字典序即時間序
	for _, e := range entries[:len(entries)-keep] {
		if e.IsDir() {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// HealthGate 啓動後自檢：輪詢 /health 直到 database:true 或超時。
// 通過 → MarkOK；超時 → 返回錯誤（調用方決定回滾/退出）。
func HealthGate(ctx context.Context, healthURL, binaryPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if pingHealth(healthURL) {
			return MarkOK(binaryPath, 3)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("updater: 健康門超時（%s 未就緒，%s）", healthURL, timeout)
}

func pingHealth(url string) bool {
	cli := &http.Client{Timeout: 3 * time.Second}
	resp, err := cli.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Status struct {
			Server   bool `json:"server"`
			Database bool `json:"database"`
		} `json:"status"`
	}
	return json.NewDecoder(resp.Body).Decode(&body) == nil && body.Status.Database
}

// BackupPath 構造備份目錄（pre-<版本>-<ts>；BackupFunc 落盤目標）。
func BackupPath(binaryPath, version string) string {
	return filepath.Join(filepath.Dir(binaryPath), backupDir,
		"pre-"+version+"-"+time.Now().UTC().Format("20060102T150405"))
}

// RestartService 重啓服務：systemd 優先（安裝態），失敗返回錯誤提示手動重啓
// （裸跑/容器態；容器內 self-update 本就被部署矩陣排除）。
// inProcess=true（admin 面內觸發）時由 serve 層重啓三分支處理，不走本函數。
func RestartService(unit string) error {
	if unit == "" {
		unit = "zcard"
	}
	systemctl := "/bin/systemctl"
	if !fileExists(systemctl) {
		systemctl = "/usr/bin/systemctl"
	}
	if fileExists(systemctl) {
		cmd := exec.Command(systemctl, "restart", unit+".service")
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			_ = out
		}
	}
	return errors.New("updater: 無法自動重啓（非 systemd 環境）——請手動重啓服務進程")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// CheckDiskSpace 磁盤預檢（方案 ）：dir 所在文件系統可用空間不足 need 即報錯
// （124MB 下載 + GB 級 DB 備份，寫一半 ENOSPC 遠劣於提前拒絕）。
func CheckDiskSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return fmt.Errorf("updater: 磁盤預檢失敗: %w", err)
	}
	avail := int64(st.Bavail) * int64(st.Bsize)
	if avail < need {
		return fmt.Errorf("updater: 磁盤空間不足（需 %dMB，%s 可用 %dMB）", need>>20, dir, avail>>20)
	}
	return nil
}

// DetectSupervisor 探測進程管理器（方案 重啓三分支依據），判據按可靠度排序：
// 1. ZCARD_SUPERVISOR（install.sh unit 顯式聲明）
// 2. systemd 運行時注入 env（INVOCATION_ID）
// 3. supervisord 標準注入 env（SUPERVISOR_*）
// 4. 父進程鏈 comm（寶塔「進程守護管理器」等封裝不透傳標準 env——實測有
// env 缺失場景；父鏈出現 supervisord/pm2 是強判據，不可能誤報）
// 5. /proc/self/cgroup 含 supervisord（supervisord 被 systemd 託管時子進程
// 同 cgroup 的兜底）
//
// 注意 cgroup 仍不能用於判 systemd——systemd 系統上 nohup 裸跑進程同樣落在
// systemd slice 內（誤判即「優雅退出等拉起」而實際無人拉起，服務死透）。
// 探測盡力的盲區由 settings system/update 的 supervisor 字段顯式覆蓋（service 層）。
func DetectSupervisor() string {
	if IsContainer() {
		return "docker"
	}
	if v := os.Getenv("ZCARD_SUPERVISOR"); v != "" {
		return v
	}
	if os.Getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	if os.Getenv("SUPERVISOR_ENABLED") == "1" || os.Getenv("SUPERVISOR_SERVER_URL") != "" {
		return "supervisord"
	}
	if v := detectAncestorManager(); v != "" {
		return v
	}
	if cgroup, err := os.ReadFile("/proc/self/cgroup"); err == nil && strings.Contains(string(cgroup), "supervisord") {
		return "supervisord"
	}
	return "none"
}

// detectAncestorManager 向上父進程鏈找進程管理器（supervisord / PM2 God Daemon /
// 寶塔守護等——按 comm 名匹配；/proc 不可讀（非 Linux）即跳過）。
func detectAncestorManager() string {
	pid := os.Getppid()
	for i := 0; i < 12 && pid > 1; i++ {
		if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
			name := strings.ToLower(strings.TrimSpace(string(comm)))
			switch {
			case strings.Contains(name, "supervisor"):
				return "supervisord"
			case strings.HasPrefix(name, "pm2"), name == "god":
				return "pm2"
			}
		}
		next, ok := parentPID(pid)
		if !ok {
			return ""
		}
		pid = next
	}
	return ""
}

// parentPID /proc/<pid>/stat 的 ppid（comm 含空格時括號定位）。
func parentPID(pid int) (int, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	s := string(raw)
	if idx := strings.LastIndex(s, ")"); idx >= 0 && idx+2 <= len(s) {
		fields := strings.Fields(s[idx+2:])
		if len(fields) >= 2 { // state ppid
			var pp int
			if _, err := fmt.Sscanf(fields[1], "%d", &pp); err == nil {
				return pp, true
			}
		}
	}
	return 0, false
}

// RestartSelf 裸跑降級路徑：syscall.Exec 同進程映像替換（方案 ）。
// PID/父子關係/env/cwd 全保留——systemd 主進程無感、nohup 掛載關係不變；
// 成功不返回；失敗則舊進程內存映像仍在（磁盤已換新，返回錯誤提示手動重啓）。
// 調用前必須已完成優雅停機（serve 層 app.Stop 後調用）。
func RestartSelf(binaryPath string) error {
	if _, err := os.Stat(binaryPath); err != nil {
		return fmt.Errorf("updater: exec 目標不存在: %w", err)
	}
	return syscall.Exec(binaryPath, os.Args, os.Environ())
}
