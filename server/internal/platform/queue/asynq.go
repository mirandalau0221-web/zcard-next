package queue

// asynq 適配器（有 Redis 時啓用；三隊列 weighted 6:3:1 隔離，ADR-D6）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/hibiken/asynq"
)

// AsynqQueue asynq 實現（Enqueuer）。Enabled 恆 true——構造即代表 Redis 已配置。
type AsynqQueue struct {
	client *asynq.Client
}

// NewAsynqQueue 構造（client 生命週期由調用方管理）。
func NewAsynqQueue(client *asynq.Client) *AsynqQueue { return &AsynqQueue{client: client} }

// TaskOf 構造 asynq 任務（導出供 worker 側註冊同一口徑的處理器名）。
func TaskOf(t Task) *asynq.Task {
	typ := t.Type
	if typ == "" {
		typ = "unknown"
	}
	return asynq.NewTask(typ, t.Payload)
}

// Enqueue 入隊：隊列名歸一（空→default）+ DedupeKey → asynq TaskID（唯一約束即冪等）。
func (q *AsynqQueue) Enqueue(ctx context.Context, t Task) error {
	name := t.Queue
	if name == "" {
		name = QueueDefault
	}
	opts := []asynq.Option{asynq.Queue(name)}
	if t.DedupeKey != "" {
		opts = append(opts, asynq.TaskID(t.DedupeKey))
	}
	info, err := q.client.EnqueueContext(ctx, TaskOf(t), opts...)
	if err != nil {
		// 同 TaskID 已存在視為成功（冪等重入）
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("queue: enqueue %q 到 %q 失敗: %w", t.Type, name, err)
	}
	_ = info
	return nil
}

// Enabled 恆 true。
func (*AsynqQueue) Enabled() bool { return true }

// NewRedisClient 按 conf 構造 asynq 底層 Redis 連接。
func NewRedisClient(opt asynq.RedisConnOpt) *asynq.Client {
	return asynq.NewClient(opt)
}
