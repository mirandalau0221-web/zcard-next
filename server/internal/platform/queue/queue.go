// Package queue 任務隊列抽象（ADR-D6：asynq 可選依賴）。
//
// 有 Redis：asynq 三隊列隔離（critical=交付/採購/補班 6 : default=郵件/通知 3 : low=同步/報表 1），
// 修復友商單隊列互相阻塞問題；無 Redis：同步降級執行 + 失敗落 failed_tasks 表供手動重放，
// 週期任務由進程內 cron 兜底（多實例部署時要求單 worker 模式）。
package queue

import (
	"context"
	"log/slog"
	"time"
)

// 隊列分級（asynq weighted priority 6:3:1）。
const (
	QueueCritical = "critical" // 訂單交付、採購提交、支付補單
	QueueDefault  = "default"  // 郵件/短信/通知/回調轉發
	QueueLow      = "low"      // 庫存價格同步、報表聚合、巡檢
)

// Task 異步任務描述。
type Task struct {
	// Type 任務類型（如 "event:order.paid"、"card:import"）
	Type string
	// Payload 任務載荷
	Payload []byte
	// Queue 目標隊列（空 = default）
	Queue string
	// DedupeKey 冪等鍵（asynq TaskID 語義：同 key 重複入隊直接成功）
	DedupeKey string
}

// Enqueuer 入隊端口。Enabled() 為 false 表示降級模式（無 Redis），
// 調用方據此決定同步執行或走降級路徑（友商 queue.Enabled() 紀律）。
type Enqueuer interface {
	Enqueue(ctx context.Context, task Task) error
	Enabled() bool
}

// FailedTaskWriter 死信落庫端口（無 Redis 降級模式使用）。
// 實現位於 internal/data（failed_tasks 表）；platform 不 import ent。
type FailedTaskWriter interface {
	SaveFailedTask(ctx context.Context, taskType string, payload []byte, errMsg string) error
}

// SyncQueue 無 Redis 時的同步降級實現：任務在獨立 goroutine 順序執行（fire-and-forget），
// 失敗落 failed_tasks（經 FailedTaskWriter；writer 缺失時僅記日誌）。
type SyncQueue struct {
	Log     *slog.Logger
	Handler func(ctx context.Context, task Task) error
	Dead    FailedTaskWriter
}

// Enqueue 同步降級入隊。
func (q *SyncQueue) Enqueue(ctx context.Context, task Task) error {
	if q.Handler == nil {
		return nil
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if err := q.Handler(runCtx, task); err != nil {
			q.recordDead(runCtx, task, err)
		}
	}()
	return nil
}

// Enabled 恆 false：降級模式標識。
func (*SyncQueue) Enabled() bool { return false }

func (q *SyncQueue) recordDead(ctx context.Context, task Task, cause error) {
	if q.Dead != nil {
		if err := q.Dead.SaveFailedTask(ctx, task.Type, task.Payload, cause.Error()); err == nil {
			return
		}
		// 落庫失敗繼續走日誌（不死循環）
	}
	if q.Log != nil {
		q.Log.Error("queue.sync.execute_failed", "type", task.Type, "err", cause)
	}
}
