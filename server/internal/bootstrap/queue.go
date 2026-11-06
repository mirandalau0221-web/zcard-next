package bootstrap

// 隊列/事件/週期任務裝配（ // 的 wiring）：
// - Enqueuer：Redis 配置存在 → AsynqQueue；否則 SyncQueue（Dispatcher 直連 + 死信落庫）
// - Dispatcher：消費分發（processed_events 冪等）； 處理器目錄為空， 隨交易閉環註冊
// - Cron：週期任務註冊表（ 空目錄；訂單超時取消/佣金確認等隨 + 在此追加）

import (
	"context"
	"log/slog"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/mods/affiliate"
	"github.com/NovaWorks/zcard-next/server/internal/mods/audit"
	"github.com/NovaWorks/zcard-next/server/internal/mods/notify"
	"github.com/NovaWorks/zcard-next/server/internal/mods/order"
	"github.com/NovaWorks/zcard-next/server/internal/mods/procurement"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supplier"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply"
	"github.com/NovaWorks/zcard-next/server/internal/mods/ticket"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"

	"github.com/google/wire"
	"github.com/hibiken/asynq"
)

var queueProviderSet = wire.NewSet(
	NewEnqueuer,
	NewDispatcher,
	NewOutboxRelay,
	NewCron,
)

// NewEnqueuer 隊列決策（ADR-D6 降級矩陣）：conf.Redis.Addr 非空 → asynq；否則同步降級。
func NewEnqueuer(c *conf.Data, dp *data.Dispatcher, dead *data.FailedTaskWriter, logger *slog.Logger) (queue.Enqueuer, func(), error) {
	if c != nil && c.Redis != nil && c.Redis.Addr != "" {
		opt := asynq.RedisClientOpt{Addr: c.Redis.Addr}
		if c.Redis.Password != "" {
			opt.Password = c.Redis.Password
		}
		if c.Redis.Db > 0 {
			opt.DB = int(c.Redis.Db)
		}
		client := asynq.NewClient(opt)
		return queue.NewAsynqQueue(client), func() { _ = client.Close() }, nil
	}
	logger.Warn("bootstrap.queue.sync_mode", "msg", "未配置 Redis：隊列同步降級（失敗落 failed_tasks），週期任務走進程內 cron")
	sq := &queue.SyncQueue{
		Log:     logger,
		Handler: dp.HandleTask,
		Dead:    dead,
	}
	return sq, func() {}, nil
}

// NewDispatcher 消費分發器（事件處理器註冊點）。
// 訂閲註冊在 cmd/zcard newApp 裝配（wire 破環：Dispatcher → ProcureService → Enqueuer → Dispatcher）。
func NewDispatcher(d *data.Data, logger *slog.Logger) *data.Dispatcher {
	return data.NewDispatcher(d, logger)
}

// NewOutboxRelay relay 構造（生命週期由 server.BackgroundServer 託管）。
func NewOutboxRelay(d *data.Data, q queue.Enqueuer, logger *slog.Logger) *data.OutboxRelay {
	return data.NewOutboxRelay(d, q, logger)
}

// NewCron 進程內週期任務（註冊表）。
func NewCron(catalogPreview *supply.AdminSupplyService, notifyDisp *notify.Dispatcher, supplySync *supply.SyncService, supplyScheduler *supply.Scheduler, procure *procurement.ProcureService, supplierRepo *supplier.SupplierRepoImpl, auditRepo *audit.AuditRepo, visitCounter *audit.VisitCounter, trackRepo *audit.TrackRepo, broadcastSvc *notify.BroadcastService, ticketAdmin *ticket.AdminTicketService, affiliateSvc *affiliate.AffiliateService, orderUC *order.OrderUsecase) *queue.Cron {
	c := queue.NewCron()
	// 訂單超時取消（每分鐘掃 pending_payment 到期單；慢支付順延在其內）
	c.AddEvery("order.expire_pending", time.Minute, func(ctx context.Context) {
		if _, err := orderUC.ExpireOrder(ctx); err != nil {
			slog.ErrorContext(ctx, "order.expiry.failed", "error", err)
		}
	})
	// ：貨源連接週期探活（健康度累計 → M4 供應商評分基礎數據，）
	c.AddEvery("supply.health_ping", 5*time.Minute, func(ctx context.Context) {
		supplySync.PingAllActive(ctx)
	})
	// S3：定時同步調度（每分鐘掃描 settings.schedule 到期任務）+ 看門狗（僵死任務回收）
	c.AddEvery("supply.catalog_snapshots", 5*time.Second, catalogPreview.ResumeCatalogSnapshots)
	c.AddEvery("supply.resume_tasks", 5*time.Second, supplySync.ResumeTasks)
	c.AddEvery("supply.schedule_scan", time.Minute, supplyScheduler.Scan)
	c.AddEvery("supply.low_stock", time.Minute, supplySync.ScanLowStock)
	c.AddEvery("notify.low_stock", time.Minute, notifyDisp.ScanLowStock)
	c.AddEvery("supply.sync_reap_stale", 10*time.Minute, supplyScheduler.ReapStaleTasks)
	// ：採購巡檢兜底（每 30 分鐘拉 polling/submitted 單查上游；24h 卡死轉人工）
	c.AddEvery("procurement.reusable", 5*time.Second, func(ctx context.Context) {
		if err := procure.ResumeReusable(ctx); err != nil {
			slog.ErrorContext(ctx, "procurement.reusable.failed", "error", err)
		}
	})
	c.AddEvery("procurement.sms", 5*time.Second, procure.RunSMS)
	c.AddEvery("procurement.social", 5*time.Second, procure.RunSocial)
	c.AddEvery("procurement.patrol", 30*time.Minute, procure.Patrol)
	// ：供貨 nonce 過期清理（每小時）
	c.AddEvery("supplier.nonce_cleanup", time.Hour, func(ctx context.Context) {
		_ = supplierRepo.CleanupExpiredNonces(ctx)
	})
	// ：風控鎖過期清理（每 5 分鐘）+ 訪問統計批量落庫（每分鐘）
	c.AddEvery("audit.lock_cleanup", 5*time.Minute, func(ctx context.Context) {
		_ = auditRepo.CleanupExpiredLocks(ctx)
	})
	c.AddEvery("audit.visit_flush", time.Minute, func(ctx context.Context) {
		visitCounter.Flush()
	})
	// ：訪問明細/在線心跳清理（每小時——明細保留 90 天，心跳保留 24 小時）
	c.AddEvery("audit.visit_cleanup", time.Hour, func(ctx context.Context) {
		_ = trackRepo.CleanupVisitData(ctx)
	})
	// ：定時羣發掃描（到期 pending → 入隊）
	c.AddEvery("notify.broadcast_scan", time.Minute, broadcastSvc.ScanDue)
	c.AddEvery("notify.telegram_delivery", 10*time.Second, notifyDisp.ScanTelegram)
	// ：工單 resolved 超 7 天自動關閉
	c.AddEvery("ticket.autoclose", time.Hour, ticketAdmin.AutoCloseResolved)
	// ：佣金到期確認（凍結期過 → wallet 入賬；負債行重試抵扣）
	c.AddEvery("affiliate.confirm", time.Hour, affiliateSvc.ConfirmDue)
	return c
}
