package server

// BackgroundServer 後台服務託管（kratos.Server 生命週期）：
//   - Outbox relay：所有模式運行（api 進程也需排水事件；SKIP LOCKED + 消費冪等保證多實例安全）
//   - 進程內 cron：僅 all/worker 模式（多實例部署紀律：無 Redis 多實例 = 需單 worker，ADR-D6）
//
// 無 Redis（SyncQueue）時 sync 隊列經 Dispatcher 直連分發——單進程即完成「寫事件→投遞→消費」閉環。

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"

	"github.com/go-kratos/kratos/v3/log"
)

// RunMode 進程運行模式（main 注入）。
type RunMode string

// 三種模式（規劃 §4.2：api 與 worker 共享全部代碼，啓動參數選擇裝配）。
const (
	ModeAll    RunMode = "all"
	ModeAPI    RunMode = "api"
	ModeWorker RunMode = "worker"
)

// BackgroundServer 後台任務 server。
type BackgroundServer struct {
	relay *data.OutboxRelay
	cron  *queue.Cron
	mode  RunMode
}

// NewBackgroundServer 構造。
func NewBackgroundServer(relay *data.OutboxRelay, cron *queue.Cron, mode RunMode) *BackgroundServer {
	return &BackgroundServer{relay: relay, cron: cron, mode: mode}
}

// Start 啓動 relay（全模式）與 cron（all/worker 模式）。
func (s *BackgroundServer) Start(ctx context.Context) error {
	go s.relay.Run(ctx)
	if s.mode == ModeAll || s.mode == ModeWorker {
		s.cron.Start(log.Default())
	}
	log.Default().Info("background.started", "mode", string(s.mode), "cron", s.mode != ModeAPI)
	return nil
}

// Stop 停止。
func (s *BackgroundServer) Stop(ctx context.Context) error {
	s.relay.Stop()
	s.cron.Stop()
	return nil
}
