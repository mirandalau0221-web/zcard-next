package data

// 事件/任務消費分發器（ / 消費側）：
// asynq worker 與 SyncQueue 共用同一入口；消費冪等經 processed_events(event_id, consumer)。
// 註冊空目錄（事件消費方 隨交易閉環接入）；進程內處理器供單機模式直連分發。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"
)

// HandlerReg 處理器註冊項。
type HandlerReg struct {
	Consumer string // 消費者標識（processed_events 冪等鍵組成；建議 "模塊.處理器"）
	Type     string // 訂閲的事件類型（如 events.OrderPaid）
	Fn       events.Handler
	// Transactional is for database-only handlers whose repositories join the context transaction.
	Transactional bool
}

// Dispatcher 消費分發器：任務載荷 = events.Envelope JSON。
type Dispatcher struct {
	data *Data
	log  *slog.Logger

	mu       sync.RWMutex
	handlers map[string][]HandlerReg // 事件類型 → 訂閲者
}

// NewDispatcher 構造。
func NewDispatcher(d *Data, log *slog.Logger) *Dispatcher {
	return &Dispatcher{data: d, log: log, handlers: map[string][]HandlerReg{}}
}

// Register 註冊訂閲（冪等：同 Consumer+Type 重複註冊覆蓋）。
func (dp *Dispatcher) Register(regs ...HandlerReg) {
	dp.mu.Lock()
	defer dp.mu.Unlock()
	for _, r := range regs {
		if r.Consumer == "" || r.Type == "" || r.Fn == nil {
			continue
		}
		list := dp.handlers[r.Type]
		replaced := false
		for i := range list {
			if list[i].Consumer == r.Consumer {
				list[i] = r
				replaced = true
			}
		}
		if !replaced {
			dp.handlers[r.Type] = append(list, r)
		}
	}
}

// HandleTask 隊列任務入口（task type 形如 "event:order.paid"）。
func (dp *Dispatcher) HandleTask(ctx context.Context, task queue.Task) error {
	typ := strings.TrimPrefix(task.Type, "event:")
	var env events.Envelope
	if err := json.Unmarshal(task.Payload, &env); err != nil {
		return fmt.Errorf("consumer: 解析事件載荷失敗（type=%s）: %w", task.Type, err)
	}
	if env.Type == "" {
		env.Type = typ
	}
	return dp.Dispatch(ctx, env)
}

// Dispatch 分發到全部訂閲者（逐個冪等；單訂閲者失敗不阻斷其餘，返回聚合錯誤）。
func (dp *Dispatcher) Dispatch(ctx context.Context, env events.Envelope) error {
	dp.mu.RLock()
	subs := append([]HandlerReg(nil), dp.handlers[env.Type]...)
	dp.mu.RUnlock()
	var errs []error
	for _, sub := range subs {
		if err := dp.runOnce(ctx, env, sub); err != nil {
			errs = append(errs, fmt.Errorf("consumer %s: %w", sub.Consumer, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("consumer: %v", errs)
	}
	return nil
}

// runOnce 單訂閲者執行：processed_events 唯一索引冪等（已處理直接返回 nil）。
func (dp *Dispatcher) runOnce(ctx context.Context, env events.Envelope, sub HandlerReg) error {
	if sub.Transactional {
		return dp.runTransactional(ctx, env, sub)
	}

	_, err := Client(ctx, dp.data).ProcessedEvent.Create().
		SetEventID(env.EventID).
		SetConsumer(sub.Consumer).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return nil // 已消費：冪等 ACK
	}
	if err != nil {
		return err
	}
	if err := sub.Fn(ctx, env); err != nil {
		return err // 處理失敗：processed_events 已寫入—— 引入補償刪除或改「先執行後記錄」策略前，以日誌告警
	}
	if dp.log != nil {
		dp.log.Debug("consumer.dispatched", "type", env.Type, "consumer", sub.Consumer, "event_id", env.EventID)
	}
	return nil
}

var errAlreadyProcessed = errors.New("consumer: already processed")

// Retry short database failures even in SyncQueue mode; each attempt starts a
// fresh transaction. Permanent failures reach the queue's failure reporting.
func (dp *Dispatcher) runTransactional(ctx context.Context, env events.Envelope, sub HandlerReg) error {
	for attempt := 0; ; attempt++ {
		err := Tx(ctx, dp.data, func(txCtx context.Context) error {
			_, err := Client(txCtx, dp.data).ProcessedEvent.Create().SetEventID(env.EventID).SetConsumer(sub.Consumer).Save(txCtx)
			if ent.IsConstraintError(err) {
				return errAlreadyProcessed
			}
			if err != nil {
				return err
			}
			return sub.Fn(txCtx, env)
		})
		// A duplicate insert must be rolled back before ACK (PostgreSQL aborts the transaction).
		if errors.Is(err, errAlreadyProcessed) {
			return nil
		}
		if err == nil || attempt >= 2 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
		}
	}
}
