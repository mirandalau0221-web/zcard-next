package data

// Outbox relay（規劃 ； ）：500ms 掃描 publishing 事件 →
// 入隊（asynq 或進程內同步分發）→ 標記 published。
//
// 多實例安全：MySQL/PG 認領用 FOR UPDATE SKIP LOCKED；SQLite 單寫者事務天然串行
// （BATCH 內一次事務認領+投遞後標記，崩潰由至少一次投遞 + 消費冪等兜底）。

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/outboxevent"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"
)

const (
	relayInterval = 500 * time.Millisecond
	relayBatch    = 100
	relayMaxTry   = 3 // 連續投遞失敗次數上限，超過置 failed + 告警日誌
)

// OutboxRelay outbox 投遞器（kratos server 生命週期託管，見 internal/server/background.go）。
type OutboxRelay struct {
	data  *Data
	queue queue.Enqueuer
	log   *slog.Logger

	mu      sync.Mutex
	attempt map[uint64]int // 內存重試計數（進程重啓歸零，由消費冪等兜底）
	stop    chan struct{}
	stopOne sync.Once
}

// NewOutboxRelay 構造。
func NewOutboxRelay(d *Data, q queue.Enqueuer, log *slog.Logger) *OutboxRelay {
	return &OutboxRelay{
		data: d, queue: q, log: log,
		attempt: map[uint64]int{},
		stop:    make(chan struct{}),
	}
}

// Run 阻塞循環（調用方以 goroutine 託管）；ctx 取消或 Stop 後返回。
func (r *OutboxRelay) Run(ctx context.Context) {
	ticker := time.NewTicker(relayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			if err := r.tick(ctx); err != nil && r.log != nil {
				r.log.Error("outbox.relay.tick_failed", "err", err)
			}
		}
	}
}

// Stop 冪等停止。
func (r *OutboxRelay) Stop() { r.stopOne.Do(func() { close(r.stop) }) }

// tick 單輪：認領一批 publishing → 逐條投遞 → 投遞成功即標記 published。
func (r *OutboxRelay) tick(ctx context.Context) error {
	rows, err := r.claim(ctx)
	if err != nil || len(rows) == 0 {
		return err
	}
	for _, row := range rows {
		if err := r.deliver(ctx, row); err != nil {
			return nil // 本條失敗不阻斷後續 tick；計數與置 failed 在 deliver 內處理
		}
	}
	return nil
}

// claim 認領一批：MySQL/PG 行鎖 SKIP LOCKED（多實例不重複認領）；SQLite 普通查詢
// （單寫者串行；崩潰場景由至少一次語義 + 消費冪等兜底）。
func (r *OutboxRelay) claim(ctx context.Context) ([]*ent.OutboxEvent, error) {
	q := Client(ctx, r.data).OutboxEvent.Query().
		Where(outboxevent.StatusEQ(outboxevent.StatusPublishing)).
		Order(ent.Asc(outboxevent.FieldID)).
		Limit(relayBatch)
	if c := r.data.Dialect.Capabilities(); c.SupportsSkipLocked {
		q = q.ForUpdate(entsql.WithLockAction(sql.SkipLocked))
	}
	return q.All(ctx)
}

// deliver 投遞單條：入隊成功 → published；失敗 → 計數，連續 relayMaxTry 次置 failed。
func (r *OutboxRelay) deliver(ctx context.Context, row *ent.OutboxEvent) error {
	env := events.Envelope{
		EventID:     row.ID,
		Type:        row.Type,
		AggregateID: row.AggregateID,
		Payload:     row.Payload, // 事件載荷必須隨信封投遞（消費方按載荷解析業務字段）
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return r.markFailed(ctx, row, err)
	}
	task := queue.Task{
		Type:    "event:" + row.Type,
		Payload: payload,
		Queue:   queue.QueueCritical, // 事件扇出（交付/採購/補單）默認 critical；細分由訂閲方任務類型承接
	}
	if row.Module == "notify" {
		task.Queue = queue.QueueDefault // 通知類事件降級 default
	}
	if err := r.queue.Enqueue(ctx, task); err != nil {
		r.mu.Lock()
		r.attempt[row.ID]++
		n := r.attempt[row.ID]
		r.mu.Unlock()
		if n >= relayMaxTry {
			return r.markFailed(ctx, row, err)
		}
		if r.log != nil {
			r.log.Warn("outbox.relay.enqueue_retry", "event_id", row.ID, "type", row.Type, "attempt", n, "err", err)
		}
		return nil
	}
	r.mu.Lock()
	delete(r.attempt, row.ID)
	r.mu.Unlock()
	_, err = Client(ctx, r.data).OutboxEvent.UpdateOne(row).
		SetStatus(outboxevent.StatusPublished).
		SetPublishedAt(time.Now().UTC()).
		Save(ctx)
	return err
}

func (r *OutboxRelay) markFailed(ctx context.Context, row *ent.OutboxEvent, cause error) error {
	if r.log != nil {
		r.log.Error("outbox.relay.event_failed", "event_id", row.ID, "type", row.Type, "err", cause)
	}
	_, err := Client(ctx, r.data).OutboxEvent.UpdateOne(row).
		SetStatus(outboxevent.StatusFailed).
		Save(ctx)
	return err
}

var _ = db.SQLite // 保持 db 包引用（能力判定見 claim）
