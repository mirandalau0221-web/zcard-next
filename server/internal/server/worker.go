package server

// WorkerServer 隊列消費者 server（kratos.Server 包裝 asynq.Server）。
// 三隊列 weighted 6:3:1（critical/default/low，ADR-D6 修復友商單隊列互相阻塞）。
// 無 Redis（降級模式）不裝配 asynq——消費經 SyncQueue 在進程內完成（佔位即就緒）。
// asynq mux 按任務類型精確匹配（無通配），事件類任務按 events.All() 目錄逐一註冊，
// 統一進入 Dispatcher 路由；非事件任務（card:import 等）隨模塊在 mux 上追加註冊。

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/mods/notify"
	"github.com/NovaWorks/zcard-next/server/internal/mods/procurement"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supplier"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"

	"github.com/go-kratos/kratos/v3/log"
	"github.com/hibiken/asynq"
)

// NewWorkerServer 構造（Redis 未配置時返回降級佔位）。
func NewWorkerServer(c *conf.Data, enq queue.Enqueuer, dp *data.Dispatcher, supplySync *supply.SyncService, procureSvc *procurement.ProcureService, supplierAPI *supplier.SupplyAPIService, broadcastSvc *notify.BroadcastService) *WorkerServer {
	w := &WorkerServer{dp: dp}
	if c == nil || c.Redis == nil || c.Redis.Addr == "" || !enq.Enabled() {
		log.Default().Info("worker.sync_mode", "msg", "無 Redis：SyncQueue 進程內消費，asynq 消費者未裝配")
		return w
	}
	concurrency := int(c.WorkerConcurrency)
	if concurrency <= 0 {
		concurrency = 10
	}
	opt := asynq.RedisClientOpt{Addr: c.Redis.Addr}
	if c.Redis.Password != "" {
		opt.Password = c.Redis.Password
	}
	if c.Redis.Db > 0 {
		opt.DB = int(c.Redis.Db)
	}
	w.srv = asynq.NewServer(opt, asynq.Config{
		Concurrency: concurrency,
		Queues: map[string]int{
			queue.QueueCritical: 6,
			queue.QueueDefault:  3,
			queue.QueueLow:      1,
		},
	})
	w.mux = asynq.NewServeMux()
	// 事件目錄逐一註冊到統一入口（asynq mux 無通配）
	for _, typ := range events.All() {
		t := "event:" + typ
		w.mux.HandleFunc(t, func(ctx context.Context, task *asynq.Task) error {
			return dp.HandleTask(ctx, queue.Task{Type: task.Type(), Payload: task.Payload()})
		})
	}
	// 非事件任務（M2）：貨源同步（low 隊列；payload {"task_id": N}）
	w.mux.HandleFunc(supply.SyncTaskType, func(ctx context.Context, task *asynq.Task) error {
		return supplySync.RunTask(ctx, task.Payload())
	})
	// M2：採購輪詢（critical 隊列；payload {"procurement_id": N}）
	w.mux.HandleFunc(procurement.PollTaskType, func(ctx context.Context, task *asynq.Task) error {
		return procureSvc.RunPollTask(ctx, task.Payload())
	})
	// M2：下游回調轉發（default 隊列；payload {"supply_order_id": N}）
	w.mux.HandleFunc(supplier.CallbackTaskType, func(ctx context.Context, task *asynq.Task) error {
		return supplierAPI.RunCallbackTask(ctx, task.Payload())
	})
	// M3：通知羣發（default 隊列；payload {"broadcast_id": N}）
	w.mux.HandleFunc(notify.BroadcastTaskType, func(ctx context.Context, task *asynq.Task) error {
		return broadcastSvc.RunBroadcastTask(ctx, task.Payload())
	})
	return w
}

// WorkerServer asynq 消費者包裝。
type WorkerServer struct {
	srv *asynq.Server
	mux *asynq.ServeMux
	dp  *data.Dispatcher
}

// Start 啓動消費者（降級模式空操作）。
func (w *WorkerServer) Start(_ context.Context) error {
	if w.srv == nil {
		return nil
	}
	go func() {
		if err := w.srv.Run(w.mux); err != nil {
			log.Default().Error("worker.asynq_run_failed", "err", err)
		}
	}()
	log.Default().Info("worker.started", "queues", "critical:6 default:3 low:1")
	return nil
}

// Stop 優雅停止。
func (w *WorkerServer) Stop(_ context.Context) error {
	if w.srv != nil {
		w.srv.Shutdown()
	}
	return nil
}
