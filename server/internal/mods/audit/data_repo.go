package audit

// 數據倉儲（）：四表 CRUD + 風控閘門。
// 紀律：審計寫失敗不阻斷業務（1.x SecurityAudit）——所有寫方法錯誤僅日誌。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/auditlog"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/risklockkey"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/securityauditlog"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/visitlog"
	"github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/businessday"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
)

// 閘門閾值（settings.security 可覆蓋——讀取側 接線；默認值與文檔一致）。
const (
	DefaultMaxPendingPerIP  = 3
	DefaultFetchFailLockN   = orderaccess.MaxFailures
	DefaultFetchLockTTL     = orderaccess.LockTTL
	DefaultOrderPerMinPerIP = 10
)

// 哨兵錯誤。
var (
	ErrIPBlacklisted = errors.New("audit: IP 已被列入黑名單")
	ErrPendingExceed = errors.New("audit: 同 IP 待支付訂單超限")
	ErrFreqExceed    = errors.New("audit: 下單頻率超限")
	ErrFetchLocked   = errors.New("audit: 取貨失敗鎖定中（稍後再試）")
)

// AuditRepo 審計倉儲。
type AuditRepo struct {
	data    *data.Data
	log     *slog.Logger
	alerter *Alerter // 告警（nil = 未裝配跳過）

	// 黑名單進程內緩存（admin 變更失效；解析失敗用空名單 fail-open）
	blacklistMu  sync.RWMutex
	blacklist    *ipBlacklist
	blacklistRaw []string

	// 下單頻率窗口（進程內滑動窗口；多實例由 pending 閘門 DB 複查兜底）
	freqMu  sync.Mutex
	freqWin map[string][]time.Time
}

// NewAuditRepo 構造。
func NewAuditRepo(d *data.Data, logger *slog.Logger) *AuditRepo {
	return &AuditRepo{data: d, log: logger, freqWin: map[string][]time.Time{}}
}

// ── 操作審計 ───────────────────────────────────────────

// OpLogInput 操作審計入參。
type OpLogInput struct {
	OperatorType    string // admin | user | system
	OperatorID      uint64
	PermissionPoint string
	Action          string // POST | PUT | DELETE
	Route           string
	Before          map[string]any
	After           map[string]any
	IP              string
	UserAgent       string
}

// WriteOpLog 落操作審計（失敗僅日誌——不阻斷業務）。
func (r *AuditRepo) WriteOpLog(ctx context.Context, in OpLogInput) error {
	create := data.Client(ctx, r.data).AuditLog.Create().
		SetOperatorType(auditlog.OperatorType(in.OperatorType)).
		SetAction(in.Action).
		SetRoute(in.Route)
	if in.OperatorID > 0 {
		create.SetOperatorID(in.OperatorID)
	}
	if in.PermissionPoint != "" {
		create.SetPermissionPoint(in.PermissionPoint)
	}
	if in.Before != nil {
		create.SetBefore(redact(in.Before))
	}
	if in.After != nil {
		create.SetAfter(redact(in.After))
	}
	if in.IP != "" {
		create.SetIP(in.IP)
	}
	if in.UserAgent != "" {
		create.SetUserAgent(in.UserAgent)
	}
	_, err := create.Save(ctx)
	if err != nil {
		r.log.Warn("audit.op_write_failed", "route", in.Route, "err", err)
	}
	return nil
}

// ListOpLogs 操作審計查詢。
func (r *AuditRepo) ListOpLogs(ctx context.Context, operatorID uint64, page, size int) ([]*ent.AuditLog, int, error) {
	q := data.Client(ctx, r.data).AuditLog.Query().Order(ent.Desc(auditlog.FieldID))
	if operatorID > 0 {
		q = q.Where(auditlog.OperatorID(operatorID))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ── 安全審計（port.Auditor 實現）──────────────────────

// Security 安全事件埋點（寫失敗不阻斷——1.x 紀律）。
func (r *AuditRepo) Security(ctx context.Context, e port.SecurityEntry) {
	// 主體缺省時從請求身份補齊（調用方位於鑑權中間件下游，ctx 內 claims 可用；
	// 顯式 guest/system 的條目不覆蓋）。
	if e.ActorID == 0 && (e.ActorType == "" || e.ActorType == "admin" || e.ActorType == "user") {
		if claims := identity.ClaimsFromContext(ctx); claims != nil {
			e.ActorID = claims.Subject
			e.ActorType = string(claims.Realm)
		}
	}
	create := data.Client(ctx, r.data).SecurityAuditLog.Create().
		SetActorType(securityauditlog.ActorType(orDefault(e.ActorType, "system"))).
		SetAction(e.Action)
	if e.ActorID > 0 {
		create.SetActorID(e.ActorID)
	}
	if e.IP != "" {
		create.SetIP(e.IP)
	}
	if e.Metadata != nil {
		create.SetMetadata(e.Metadata)
	}
	if _, err := create.Save(ctx); err != nil {
		r.log.Warn("audit.security_write_failed", "action", e.Action, "err", err)
	}
	// 告警閾值計數（達到閾值經 notify 管理員通道告警；去重窗口防護）
	if r.alerter != nil {
		r.alerter.Count(ctx, e.Action, e.IP,
			"[ZCard 安全告警] "+e.Action,
			"動作: "+e.Action+"\n來源 IP: "+e.IP+"\n10 分鐘窗口內達到閾值，請關注。")
	}
}

// SetAlerter 注入告警器（wire 裝配後調用）。
func (r *AuditRepo) SetAlerter(a *Alerter) { r.alerter = a }

// ListSecurityLogs 安全審計查詢。
func (r *AuditRepo) ListSecurityLogs(ctx context.Context, action string, page, size int) ([]*ent.SecurityAuditLog, int, error) {
	q := data.Client(ctx, r.data).SecurityAuditLog.Query().Order(ent.Desc(securityauditlog.FieldID))
	if action != "" {
		q = q.Where(securityauditlog.ActionContains(action))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ── 風控閘門（port.RiskGate 實現）──────────────────────

// SetBlacklist 更新黑名單緩存（admin CRUD 後調用）。
func (r *AuditRepo) SetBlacklist(entries []string) {
	r.blacklistMu.Lock()
	r.blacklistRaw = entries
	r.blacklist = parseBlacklist(entries)
	r.blacklistMu.Unlock()
}

// BlacklistRaw 當前黑名單配置。
func (r *AuditRepo) BlacklistRaw() []string {
	r.blacklistMu.RLock()
	defer r.blacklistMu.RUnlock()
	return r.blacklistRaw
}

// Check 下單閘門：黑名單 → pending 數（事務內複查語義：本方法在下單事務內調用）→ 頻率。
func (r *AuditRepo) Check(ctx context.Context, in port.GateInput) error {
	ip := port.NormalizeIP(in.RiskIP)

	// 1) 黑名單
	r.blacklistMu.RLock()
	bl := r.blacklist
	r.blacklistMu.RUnlock()
	if bl.contains(ip) {
		return ErrIPBlacklisted
	}

	// 2) pending 閘門（DB 複查——併發 50 單同 IP 不穿透：計數查詢在同一事務，
	// order 模塊在下單事務內調用本方法）
	count, err := data.Client(ctx, r.data).Order.Query().
		Where(order.RiskIPEQ(ip), order.StatusEQ(order.StatusPendingPayment)).
		Count(ctx)
	if err != nil {
		return nil // 查詢失敗 fail-open（可用性優先；黑名單仍生效）
	}
	if count >= DefaultMaxPendingPerIP {
		return ErrPendingExceed
	}

	// 3) 頻率限流（進程內滑動窗口）
	if r.freqExceed(ip) {
		return ErrFreqExceed
	}
	return nil
}

// freqExceed 滑動窗口頻率檢查（每 IP 每分鐘）。
func (r *AuditRepo) freqExceed(ip string) bool {
	now := time.Now()
	r.freqMu.Lock()
	defer r.freqMu.Unlock()
	win := r.freqWin[ip]
	kept := win[:0]
	for _, t := range win {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= DefaultOrderPerMinPerIP {
		r.freqWin[ip] = kept
		return true
	}
	r.freqWin[ip] = append(kept, now)
	return false
}

// FetchPasswordState reads durable counters shared by all application instances.
func (r *AuditRepo) FetchPasswordState(ctx context.Context, key string) (orderaccess.State, error) {
	row, err := data.Client(ctx, r.data).RiskLockKey.Query().Where(risklockkey.KeyHash(hashKey(key))).Only(ctx)
	if ent.IsNotFound(err) {
		return orderaccess.State{}, nil
	}
	if err != nil {
		return orderaccess.State{}, err
	}
	if !row.ExpiresAt.After(time.Now()) {
		return orderaccess.State{}, nil
	}
	return orderaccess.State{Failures: row.FailureCount, ExpiresAt: row.ExpiresAt}, nil
}

// RecordFetchFailure uses compare-and-swap so concurrent requests cannot lose increments.
// Each failed attempt refreshes the counting window; the fifth starts the lock TTL.
// Requests during a lock leave its expiry unchanged.
func (r *AuditRepo) RecordFetchFailure(ctx context.Context, key string) (orderaccess.State, error) {
	client := data.Client(ctx, r.data)
	hash := hashKey(key)
	for attempt := 0; attempt < 16; attempt++ {
		now := time.Now().UTC().Truncate(time.Millisecond)
		row, err := client.RiskLockKey.Query().Where(risklockkey.KeyHash(hash)).Only(ctx)
		if ent.IsNotFound(err) {
			row, err = client.RiskLockKey.Create().SetKeyHash(hash).SetFailureCount(1).SetExpiresAt(now.Add(DefaultFetchLockTTL)).Save(ctx)
			if ent.IsConstraintError(err) {
				continue
			}
			if err != nil {
				return orderaccess.State{}, err
			}
			return orderaccess.State{Failures: row.FailureCount, ExpiresAt: row.ExpiresAt}, nil
		}
		if err != nil {
			return orderaccess.State{}, err
		}
		state := orderaccess.State{Failures: row.FailureCount, ExpiresAt: row.ExpiresAt}
		if state.Locked() {
			return state, nil
		}
		failures := row.FailureCount + 1
		expiryCondition := risklockkey.ExpiresAtGT(now)
		if !row.ExpiresAt.After(now) {
			failures = 1
			expiryCondition = risklockkey.ExpiresAtLTE(now)
		}
		expiry := now.Add(DefaultFetchLockTTL)
		n, err := client.RiskLockKey.Update().Where(risklockkey.ID(row.ID), risklockkey.FailureCount(row.FailureCount), expiryCondition).SetFailureCount(failures).SetExpiresAt(expiry).Save(ctx)
		if err != nil {
			return orderaccess.State{}, err
		}
		if n == 0 {
			continue
		}
		if failures == DefaultFetchFailLockN {
			r.Security(ctx, port.SecurityEntry{ActorType: "guest", Action: "fetch.locked", Metadata: map[string]any{"key_prefix": prefixOf(key)}})
		}
		return orderaccess.State{Failures: failures, ExpiresAt: expiry}, nil
	}
	return orderaccess.State{}, fmt.Errorf("audit: concurrent fetch counter update; retry")
}

// ResetFetchFailures clears successful attempts without unlocking a concurrent lock.
func (r *AuditRepo) ResetFetchFailures(ctx context.Context, key string) error {
	_, err := data.Client(ctx, r.data).RiskLockKey.Delete().Where(risklockkey.KeyHash(hashKey(key)), risklockkey.Or(risklockkey.FailureCountLT(DefaultFetchFailLockN), risklockkey.ExpiresAtLTE(time.Now().UTC()))).Exec(ctx)
	return err
}

// CleanupExpiredLocks 過期鎖清理（cron）。
func (r *AuditRepo) CleanupExpiredLocks(ctx context.Context) error {
	_, err := data.Client(ctx, r.data).RiskLockKey.Delete().
		Where(risklockkey.ExpiresAtLT(time.Now().UTC())).
		Exec(ctx)
	return err
}

// ── 訪問統計 ───────────────────────────────────────────

// VisitCounter 進程內聚合計數器（批量落庫——不逐請求寫）。
type VisitCounter struct {
	mu    sync.Mutex
	rows  map[string]*visitRow // key: date|hour|path
	total int64
	r     *AuditRepo
}

type visitRow struct {
	subsiteID uint64
	date      string
	hour      int8
	path      string
	pv        int64
}

// NewVisitCounter 構造。
func NewVisitCounter() *VisitCounter { return &VisitCounter{rows: map[string]*visitRow{}} }

// Record 記一次訪問（內存）。
func (c *VisitCounter) Record(subsiteID uint64, path string) {
	now := businessday.Now()
	key := fmt.Sprintf("%s|%d|%s", now.Format("20060102"), now.Hour(), path)
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[key]
	if !ok {
		row = &visitRow{subsiteID: subsiteID, date: now.Format("20060102"), hour: int8(now.Hour()), path: path}
		c.rows[key] = row
	}
	row.pv++
	c.total++
	// 聚合閾值批量落庫
	if c.total >= 100 {
		c.flushLocked()
	}
}

// Bind 綁定倉儲（wire 裝配後調用一次）。
func (c *VisitCounter) Bind(r *AuditRepo) { c.r = r }

// Flush 強制落庫（cron 週期調用兜底）。
func (c *VisitCounter) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}

// flushLocked 落庫（調用方持鎖）。使用背景上下文（計數線程無請求上下文）。
func (c *VisitCounter) flushLocked() {
	if c.r == nil || len(c.rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := data.Client(ctx, c.r.data)
	for _, row := range c.rows {
		// UPSERT：UNIQUE(subsite_id, stat_date, stat_hour, path) 命中則累加
		existing, err := client.VisitLog.Query().
			Where(
				visitlog.SubsiteID(row.subsiteID),
				visitlog.StatDate(row.date),
				visitlog.StatHour(row.hour),
				visitlog.Path(row.path),
			).
			Only(ctx)
		if err == nil {
			if _, err := client.VisitLog.UpdateOneID(existing.ID).
				AddPv(row.pv).
				Save(ctx); err == nil {
				continue
			}
		}
		_, _ = client.VisitLog.Create().
			SetSubsiteID(row.subsiteID).
			SetStatDate(row.date).
			SetStatHour(row.hour).
			SetPath(row.path).
			SetPv(row.pv).
			SetUv(row.pv). // 輕量口徑：無會話標識時 uv≈pv（ 細化）
			Save(ctx)
	}
	c.rows = map[string]*visitRow{}
	c.total = 0
}

// ── 工具 ──────────────────────────────────────────────────

func hashKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func prefixOf(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ListVisitStats 訪問統計查詢。
func (r *AuditRepo) ListVisitStats(ctx context.Context, statDate string, page, size int) ([]*ent.VisitLog, int, error) {
	q := data.Client(ctx, r.data).VisitLog.Query().
		Where(visitlog.StatDate(statDate)).
		Order(ent.Desc(visitlog.FieldPv))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// securityEntryOf 快捷構造。
func securityEntryOf(action string, metadata map[string]any) port.SecurityEntry {
	return port.SecurityEntry{ActorType: "admin", Action: action, Metadata: metadata}
}
