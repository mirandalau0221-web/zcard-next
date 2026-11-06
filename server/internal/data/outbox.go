package data

// 事務性 Outbox 寫入實現（規劃 通道 C； ）。
//
// 關鍵約束：Write 必須在業務事務內調用——經 data.Tx 攜帶的 *ent.Tx 落庫，
// 業務回滾事件不殘留。dedupe_key 唯一索引兜底：重複發佈冪等返回 nil。

import (
	"context"
	"encoding/json"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent/outboxevent"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"
)

// OutboxWriter events.Writer 實現（wire 綁定）。
type OutboxWriter struct {
	data *Data
}

// NewOutboxWriter 構造。
func NewOutboxWriter(d *Data) *OutboxWriter { return &OutboxWriter{data: d} }

// Write 寫入 outbox 事件（同事務約束見文件頭註釋）。
// dedupe_key 衝突（同事件重複發佈，如訂單重複回調）冪等返回 nil。
// 租戶信息由各模塊事件載荷自帶（outbox_events 表無 subsite_id 列，數據庫架構 ）。
func (w *OutboxWriter) Write(ctx context.Context, module, typ, aggregateID, dedupeKey string, payload json.RawMessage) error {
	return Client(ctx, w.data).OutboxEvent.Create().
		SetModule(module).
		SetType(typ).
		SetAggregateID(aggregateID).
		SetDedupeKey(dedupeKey).
		SetPayload(payload).OnConflictColumns(outboxevent.FieldDedupeKey).Ignore().Exec(ctx)
}

var _ events.Writer = (*OutboxWriter)(nil)

// FailedTaskWriter 實現（無 Redis 降級死信落庫，platform/queue.FailedTaskWriter）。
type FailedTaskWriter struct{ data *Data }

// NewFailedTaskWriter 構造。
func NewFailedTaskWriter(d *Data) *FailedTaskWriter { return &FailedTaskWriter{data: d} }

// SaveFailedTask 落 failed_tasks 表（供 `zcard admin retry-task` 重放）。
func (w *FailedTaskWriter) SaveFailedTask(ctx context.Context, taskType string, payload []byte, errMsg string) error {
	_, err := Client(ctx, w.data).FailedTask.Create().
		SetTaskType(taskType).
		SetPayload(payload).
		SetError(errMsg).
		Save(ctx)
	return err
}

var _ queue.FailedTaskWriter = (*FailedTaskWriter)(nil)
