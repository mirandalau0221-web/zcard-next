package supply

// 定時調度器（ S3）：cron 每分鐘掃描 active 連接，按 settings.schedule 派發
// collect/price/status 三類同步任務。
//
// - 到期判定：enabled && interval>0 && 當前時間在窗口內 && 距錨點 ≥ 間隔
// - 熔斷冷卻中的渠道跳過（rate_limit_until 列；節奏器半開探測由出站路徑負責）
// - 防重入：同連接存在 pending/processing 任務時本輪跳過（任務狀態機兜底）
// - 錨點在派發時即回寫（寧可晚一輪也絕不密集雙發；任務失敗下輪重試）
// - 看門狗：心跳超 10 分鐘的 processing 任務置 failed（防僵死餓死調度）
//
// settings.schedule 結構（界面可編輯，對齊 1.x SupplyScheduleService）：
//
//	{
// "enabled": true, "request_delay": 1, "stock_concurrency": 3,
// "stock_request_delay_ms": 200,
// "collect": {"enabled":true,"mode":"incremental","interval":360,"windows":[]},
// "price": {"enabled":true,"interval":30,"windows":[]},
// "status": {"enabled":true,"interval":60,"windows":[]}
//	}
//
// windows 元素 {"start":"09:00","end":"18:00"}；空 = 全天；start>end 視為跨午夜。

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplysynctask"
)

// 調度參數。
const (
	schedStaleAfter = 10 * time.Minute // 看門狗：心跳超時閾值
)

// scope 默認間隔（分鐘；對齊 1.x DEFAULT_INTERVALS——scope 鍵存在但缺 interval 時用）。
var scopeDefaultInterval = map[string]int{
	ScopeCollect: 360,
	ScopePrice:   30,
	ScopeStatus:  60,
}

// timeWindow 執行時間窗（HH:MM；start>end 跨午夜）。
type timeWindow struct{ Start, End string }

// scopePlan 單 scope 計劃。
type scopePlan struct {
	Enabled  bool
	Interval int    // 分鐘
	Mode     string // collect 專用：full | incremental
	Windows  []timeWindow
}

// scheduleConfig schedule 整體配置。
type scheduleConfig struct {
	Enabled bool
	Scopes  map[string]scopePlan
}

// Scheduler 定時調度器（cron 入口 Scan / ReapStaleTasks）。
type Scheduler struct {
	repo *SupplyRepoImpl
	sync *SyncService
	log  *slog.Logger
}

// NewScheduler 構造。
func NewScheduler(repo *SupplyRepoImpl, sync *SyncService, log *slog.Logger) *Scheduler {
	return &Scheduler{repo: repo, sync: sync, log: log}
}

// Scan 每分鐘掃描派發（cron；單輪耗時受連接數約束，失敗僅日誌不 panic）。
func (s *Scheduler) Scan(ctx context.Context) {
	conns, err := s.repo.ListActiveConnections(ctx)
	if err != nil {
		s.log.Warn("scheduler.list_failed", "err", err)
		return
	}
	for _, conn := range conns {
		cfg := loadScheduleConfig(conn)
		// An explicit per-product opt-in enables its channel's full status
		// checks even if general collect/price schedules are disabled.
		hasAuto, e := s.repo.entClient(ctx).Product.Query().Where(product.UpstreamSourceID(conn.ID), product.SubsiteID(0), product.AutoListing(true), product.IsLocked(false), product.StatusGTE(0), product.ProductKindNEQ("sms_channel")).Exist(ctx)
		if e != nil {
			s.log.Warn("scheduler.listing_failed", "err", e)
			continue
		}
		if !cfg.Enabled && !hasAuto {
			continue
		}
		// 熔斷冷卻中：本輪跳過（到期後半開探測成功後自然恢復）
		if !conn.RateLimitUntil.IsZero() && time.Now().UTC().Before(conn.RateLimitUntil) {
			continue
		}
		if has, err := s.repo.HasRunningTask(ctx, conn.ID); err != nil {
			s.log.Warn("scheduler.has_running_failed", "connection_id", conn.ID, "err", err)
			continue
		} else if has {
			continue // 防重入：上輪任務未完結
		}
		if hasAuto && s.dispatchIfDue(ctx, conn, scopePlan{Enabled: true, Interval: 5, Mode: "full"}, ScopeListing) {
			continue
		}
		if !cfg.Enabled {
			continue
		}
		for _, scope := range []string{ScopeCollect, ScopePrice, ScopeStatus} {
			if s.dispatchIfDue(ctx, conn, cfg.Scopes[scope], scope) {
				break // 每連接每輪至多派發一個（錯峯；下輪繼續）
			}
		}
	}
}

// dispatchIfDue 到期則派發；返回是否已派發。
func (s *Scheduler) dispatchIfDue(ctx context.Context, conn *ent.SupplyConnection, plan scopePlan, scope string) bool {
	if !plan.Enabled || plan.Interval <= 0 {
		return false
	}
	if !inWindows(plan.Windows, time.Now()) {
		return false
	}
	anchor := scopeAnchor(conn, scope)
	if !anchor.IsZero() && time.Since(anchor) < time.Duration(plan.Interval)*time.Minute {
		return false
	}
	mode := plan.Mode
	if mode == "" {
		mode = "incremental" // 首跑錨點為零 → 引擎自動全量
	}
	task, err := s.repo.CreateSyncTask(ctx, conn.ID, mode, scope, false)
	if err != nil {
		s.log.Warn("scheduler.create_task_failed", "connection_id", conn.ID, "scope", scope, "err", err)
		return false
	}
	if err := s.sync.StartTask(ctx, task.ID); err != nil {
		_ = s.repo.FinishTask(ctx, task.ID, supplysynctask.StatusFailed, "ENQUEUE_FAILED", err.Error())
		s.log.Warn("scheduler.enqueue_failed", "connection_id", conn.ID, "scope", scope, "err", err)
		return false
	}
	_ = s.repo.TouchScopeAnchor(ctx, conn.ID, scope)
	s.log.Info("scheduler.dispatched", "connection_id", conn.ID, "scope", scope, "mode", mode, "task_id", task.ID)
	return true
}

// ReapStaleTasks 看門狗：心跳超時 的 processing 任務置 failed（防僵死）。
func (s *Scheduler) ReapStaleTasks(ctx context.Context) {
	rows, err := s.repo.ListStaleProcessing(ctx, time.Now().UTC().Add(-schedStaleAfter))
	if err != nil {
		s.log.Warn("scheduler.reap_list_failed", "err", err)
		return
	}
	for _, t := range rows {
		if t.Scope == ScopeImport {
			continue
		} // durable item checkpoints resume through ResumeTasks
		reason := "任務心跳超時（>" + schedStaleAfter.String() + "），看門狗回收——可手動重跑"
		if err := s.repo.FinishTask(ctx, t.ID, supplysynctask.StatusFailed, "STALE_HEARTBEAT", reason); err != nil {
			s.log.Warn("scheduler.reap_failed", "task_id", t.ID, "err", err)
			continue
		}
		s.log.Warn("scheduler.reaped_stale_task", "task_id", t.ID, "connection_id", t.ConnectionID)
	}
}

// scopeAnchor scope → 調度錨點列（collect 兼容舊列 last_synced_at）。
func scopeAnchor(conn *ent.SupplyConnection, scope string) time.Time {
	switch scope {
	case ScopeCollect:
		if !conn.LastCollectAt.IsZero() {
			return conn.LastCollectAt
		}
		return conn.LastSyncedAt
	case ScopePrice:
		return conn.LastPriceSyncAt
	case ScopeStatus, ScopeListing:
		return conn.LastStatusSyncAt
	}
	return time.Time{}
}

// loadScheduleConfig 解析 settings.schedule（結構不存在 = 定時調度關閉）。
func loadScheduleConfig(conn *ent.SupplyConnection) scheduleConfig {
	sched, _ := conn.Settings["schedule"].(map[string]any)
	cfg := scheduleConfig{Scopes: map[string]scopePlan{}}
	if sched == nil {
		return cfg
	}
	cfg.Enabled, _ = sched["enabled"].(bool)
	for _, scope := range []string{ScopeCollect, ScopePrice, ScopeStatus} {
		raw, ok := sched[scope].(map[string]any)
		if !ok {
			continue // scope 未配置 = 不啓用（顯式為準）
		}
		cfg.Scopes[scope] = parseScopePlan(raw, scope)
	}
	return cfg
}

// parseScopePlan 單 scope 配置歸一化（interval 缺省用 scope 默認間隔）。
func parseScopePlan(raw map[string]any, scope string) scopePlan {
	plan := scopePlan{Mode: "incremental"}
	plan.Enabled, _ = raw["enabled"].(bool)
	switch v := raw["interval"].(type) {
	case float64:
		plan.Interval = int(v)
	case int:
		plan.Interval = v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			plan.Interval = n
		}
	}
	if plan.Interval <= 0 {
		plan.Interval = scopeDefaultInterval[scope]
	}
	if m, ok := raw["mode"].(string); ok && (m == "full" || m == "incremental") {
		plan.Mode = m
	}
	if ws, ok := raw["windows"].([]any); ok {
		for _, w := range ws {
			m, ok := w.(map[string]any)
			if !ok {
				continue
			}
			start, _ := m["start"].(string)
			end, _ := m["end"].(string)
			if start != "" && end != "" {
				plan.Windows = append(plan.Windows, timeWindow{Start: start, End: end})
			}
		}
	}
	return plan
}

// inWindows 當前時間是否落在任一窗口內（空 = 全天；start>end 跨午夜）。
func inWindows(windows []timeWindow, now time.Time) bool {
	if len(windows) == 0 {
		return true
	}
	minutes := now.Hour()*60 + now.Minute()
	for _, w := range windows {
		s, ok1 := parseHHMM(w.Start)
		e, ok2 := parseHHMM(w.End)
		if !ok1 || !ok2 {
			continue
		}
		if s <= e {
			if minutes >= s && minutes < e {
				return true
			}
			continue
		}
		// 跨午夜：22:00-06:00 = [22:00,24:00) ∪ [0:00,6:00)
		if minutes >= s || minutes < e {
			return true
		}
	}
	return false
}

// parseHHMM "HH:MM" → 當日分鐘數。
func parseHHMM(s string) (int, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
