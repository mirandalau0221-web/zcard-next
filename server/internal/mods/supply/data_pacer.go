package supply

// 自適應節奏器（ S2）：連接級 AIMD——遇限流乘性降速、連續成功加性回升。
//
// 事件 狀態遷移
// ────────────────────────────────────────────────────────────────────
// 429 / WAF 頁（ErrRateLimited） currentDelay ×2（封頂 60s）
// 連續 2 次限流事件 渠道熔斷：rate_limit_until = now +
// cooldown（5min 起，每次熔斷 ×2，
// 封頂 60min；寫 last_error + 事件）
// 連續 20 次成功 currentDelay ×0.9（回落至配置底線
// schedule.request_delay 為止）
// 熔斷到期 半開：放行一個探測請求；成功 → 解除
// （冷卻時長歸零重來）；再限流 → 冷卻翻倍
//
// 消費方：SyncService（頁間動態節流）與 Gateway（採購出站熔斷判定）。
// 持久化：rate_state JSON（current_delay_ms / success_streak / blocked_count /
// cooldown_sec）——重啓後降速保持、熔斷冷卻由 rate_limit_until 列判定；
// 連續限流計數與半開標誌僅內存態（崩潰丟失可接受：重新積累兩次才熔斷，偏保守）。

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
)

// 節奏器參數（對齊計劃 ；不可配——參數面已足夠，避免過度配置）。
const (
	pacerMaxDelay      = 60 * time.Second // 降速封頂
	pacerCooldownBase  = 5 * time.Minute  // 首次熔斷冷卻
	pacerCooldownMax   = time.Hour        // 冷卻封頂
	pacerTripThreshold = 2                // 連續限流 N 次 → 熔斷
	pacerRecoverStreak = 20               // 連續成功 N 次 → 間隔回落 10%
	pacerRecoverFactor = 0.9
)

// pacerState 持久化狀態（rate_state JSON 的結構）。
type pacerState struct {
	CurrentDelayMs int64 `json:"current_delay_ms"`
	SuccessStreak  int   `json:"success_streak"`
	BlockedCount   int64 `json:"blocked_count"`
	CooldownSec    int64 `json:"cooldown_sec"`
}

// 內存補充態（不持久化）。
type pacerMem struct {
	state         pacerState
	consecutiveRL int  // 連續限流事件（熔斷判據）
	halfOpen      bool // 熔斷到期後已放行探測（等待成敗反饋）
	lastPersisted pacerState
}

// Pacer 自適應節奏器（wire 單例；狀態按連接隔離）。
type Pacer struct {
	repo   *SupplyRepoImpl
	outbox events.Writer
	log    *slog.Logger
	mu     sync.Mutex
	mem    map[uint64]*pacerMem
}

// NewPacer 構造。
func NewPacer(repo *SupplyRepoImpl, outbox events.Writer, log *slog.Logger) *Pacer {
	return &Pacer{repo: repo, outbox: outbox, log: log, mem: map[uint64]*pacerMem{}}
}

// load 連接的內存態（懶加載：首次從 rate_state 列恢復）。
func (p *Pacer) load(conn *ent.SupplyConnection) *pacerMem {
	m, ok := p.mem[conn.ID]
	if ok {
		return m
	}
	m = &pacerMem{}
	if raw, err := json.Marshal(conn.RateState); err == nil && len(conn.RateState) > 0 {
		_ = json.Unmarshal(raw, &m.state)
	}
	m.lastPersisted = m.state
	p.mem[conn.ID] = m
	return m
}

// Delay 出站前應等待的間隔：max(自適應當前間隔, schedule.request_delay 底線)。
func (p *Pacer) Delay(conn *ent.SupplyConnection) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.load(conn)
	base := loadScheduleSettings(conn).PageDelay
	if d := time.Duration(m.state.CurrentDelayMs) * time.Millisecond; d > base {
		return d
	}
	return base
}

// OnSuccess 出站成功反饋：半開探測成功 → 解除熔斷（冷卻歸零）；否則連續成功
// 計數，每 20 次間隔回落 10%（至底線為止）。持久化僅在有變化時寫庫。
func (p *Pacer) OnSuccess(ctx context.Context, conn *ent.SupplyConnection) {
	p.mu.Lock()
	m := p.load(conn)
	m.consecutiveRL = 0
	if m.halfOpen {
		m.halfOpen = false
		m.state.CooldownSec = 0 // 解除熔斷：下次從基礎冷卻重新計
		p.mu.Unlock()
		_ = p.repo.ClearRateLimit(ctx, conn.ID)
		p.log.Info("pacer.recovered", "connection_id", conn.ID)
		return
	}
	m.state.SuccessStreak++
	if m.state.SuccessStreak >= pacerRecoverStreak && m.state.CurrentDelayMs > 0 {
		m.state.SuccessStreak = 0
		base := loadScheduleSettings(conn).PageDelay.Milliseconds()
		next := int64(float64(m.state.CurrentDelayMs) * pacerRecoverFactor)
		if next <= base {
			next = 0 // 回落到底線（0 = 用配置值）
		}
		m.state.CurrentDelayMs = next
	}
	p.persistIfChanged(ctx, conn.ID, m)
	p.mu.Unlock()
}

// OnRateLimited 限流反饋：間隔倍增（封頂 60s）；連續 2 次 → 熔斷（冷卻指數遞增）。
// 返回 tripped 表示本次觸發了熔斷。
func (p *Pacer) OnRateLimited(ctx context.Context, conn *ent.SupplyConnection, reason string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.load(conn)
	// 間隔倍增
	d := time.Duration(m.state.CurrentDelayMs) * time.Millisecond
	if d <= 0 {
		d = loadScheduleSettings(conn).PageDelay
	}
	d *= 2
	if d > pacerMaxDelay {
		d = pacerMaxDelay
	}
	m.state.CurrentDelayMs = d.Milliseconds()
	m.state.SuccessStreak = 0
	m.consecutiveRL++
	// 半開中再限流：冷卻翻倍（不重置 consecutiveRL 計數——直接再熔斷）
	if m.consecutiveRL >= pacerTripThreshold || m.halfOpen {
		cooldown := time.Duration(m.state.CooldownSec) * time.Second
		if cooldown <= 0 {
			cooldown = pacerCooldownBase
		} else {
			cooldown *= 2
			if cooldown > pacerCooldownMax {
				cooldown = pacerCooldownMax
			}
		}
		m.state.CooldownSec = int64(cooldown.Seconds())
		m.state.BlockedCount++
		m.halfOpen = false
		m.consecutiveRL = 0
		until := time.Now().UTC().Add(cooldown)
		_ = p.repo.TripRateLimit(ctx, conn.ID, until, m.state.toMap(), "上游限流熔斷至 "+until.Local().Format("2006-01-02 15:04:05")+"："+reason)
		p.publishRateLimited(ctx, conn.ID, until, reason)
		p.log.Warn("pacer.tripped", "connection_id", conn.ID, "cooldown", cooldown, "reason", reason)
		return true
	}
	p.persistIfChanged(ctx, conn.ID, m)
	return false
}

// CooldownActive 熔斷冷卻判定：未到期 → true；到期 → 標記半開（放行一個探測）
// 並清內存期截止（DB 列由探測成敗的 OnSuccess/OnRateLimited 收尾）。
func (p *Pacer) CooldownActive(conn *ent.SupplyConnection) bool {
	if conn.RateLimitUntil.IsZero() {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().UTC().Before(conn.RateLimitUntil) {
		return true
	}
	m := p.load(conn)
	if !m.halfOpen {
		m.halfOpen = true
		p.log.Info("pacer.half_open_probe", "connection_id", conn.ID)
	}
	return false
}

// Reset 重置節奏器（管理界面按鈕）：清內存與持久狀態、解除熔斷。
func (p *Pacer) Reset(ctx context.Context, connID uint64) error {
	p.mu.Lock()
	delete(p.mem, connID)
	p.mu.Unlock()
	if err := p.repo.UpdateRateState(ctx, connID, map[string]any{}); err != nil {
		return err
	}
	return p.repo.ClearRateLimit(ctx, connID)
}

// PacerSnapshot 節奏器狀態快照（管理界面展示）。
type PacerSnapshot struct {
	CurrentDelay  time.Duration // 當前自適應間隔（0 = 用配置底線）
	SuccessStreak int
	BlockedCount  int64
	CooldownUntil time.Time // 熔斷截止（零值 = 未熔斷）
	CooldownSec   int64     // 下次熔斷將使用的冷卻時長
	HalfOpen      bool
}

// Snapshot 讀取狀態快照。
func (p *Pacer) Snapshot(conn *ent.SupplyConnection) PacerSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.load(conn)
	return PacerSnapshot{
		CurrentDelay:  time.Duration(m.state.CurrentDelayMs) * time.Millisecond,
		SuccessStreak: m.state.SuccessStreak,
		BlockedCount:  m.state.BlockedCount,
		CooldownUntil: conn.RateLimitUntil,
		CooldownSec:   m.state.CooldownSec,
		HalfOpen:      m.halfOpen,
	}
}

// persistIfChanged 狀態有變化才寫庫（成功計數高頻變化不落盤——streak 只在內存）。
// 調用方持鎖。
func (p *Pacer) persistIfChanged(ctx context.Context, connID uint64, m *pacerMem) {
	if m.state.CurrentDelayMs == m.lastPersisted.CurrentDelayMs &&
		m.state.BlockedCount == m.lastPersisted.BlockedCount &&
		m.state.CooldownSec == m.lastPersisted.CooldownSec {
		return
	}
	m.lastPersisted = m.state
	if err := p.repo.UpdateRateState(ctx, connID, m.state.toMap()); err != nil {
		p.log.Warn("pacer.persist_failed", "connection_id", connID, "err", err)
	}
}

// publishRateLimited 熔斷告警事件（ 告警/界面紅點數據源）。
func (p *Pacer) publishRateLimited(ctx context.Context, connID uint64, until time.Time, reason string) {
	if p.outbox == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"connection_id": connID, "cooldown_until": until.Unix(), "reason": reason,
	})
	if err != nil {
		return
	}
	agg := "supply_conn:" + strconv.FormatUint(connID, 10)
	_ = p.outbox.Write(ctx, "supply", events.SupplyRateLimited, agg, agg, payload)
}

// toMap / fromMap：ent JSON 列是 map[string]any（SuccessStreak 序列化後是 float64）。
func (s pacerState) toMap() map[string]any {
	return map[string]any{
		"current_delay_ms": s.CurrentDelayMs,
		"success_streak":   s.SuccessStreak,
		"blocked_count":    s.BlockedCount,
		"cooldown_sec":     s.CooldownSec,
	}
}
