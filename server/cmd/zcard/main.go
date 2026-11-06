// zcard 入口（規劃 / –10.3）。
//
// 用法：
//
//	zcard serve [-conf configs] [-mode all|api|worker] # 啓動服務（默認 all）
//	zcard migrate [-conf configs] # 應用待執行遷移後退出
//	zcard admin create|list|reset-password ... # 運維子命令（同二進制，容器免工具）
//	zcard install # 安裝嚮導（）
//	zcard version # 版本信息
//
// 運行模式：api 與 worker 共享全部代碼，靠 -mode 選擇裝配的 server（worker 模式
// 的 asynq 消費者與週期任務 隨交易閉環交付； 僅 api/all）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/admincmd"
	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/mods/affiliate"
	"github.com/NovaWorks/zcard-next/server/internal/mods/catalog"
	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	couponport "github.com/NovaWorks/zcard-next/server/internal/mods/coupon/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/fulfillment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/memberlevel"
	"github.com/NovaWorks/zcard-next/server/internal/mods/notify"
	"github.com/NovaWorks/zcard-next/server/internal/mods/order"
	orderport "github.com/NovaWorks/zcard-next/server/internal/mods/order/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/procurement"
	"github.com/NovaWorks/zcard-next/server/internal/mods/reseller"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supplier"
	"github.com/NovaWorks/zcard-next/server/internal/mods/update"
	"github.com/NovaWorks/zcard-next/server/internal/mods/wallet"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/updater"
	"github.com/NovaWorks/zcard-next/server/internal/server"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
	"github.com/go-kratos/kratos/v3/log"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"go.uber.org/automaxprocs/maxprocs"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	// Name 服務名。
	Name = "zcard"
	// Version 構建注入版本。
	Version string

	id, _ = os.Hostname()

	// appMode 運行模式（runServe 解析 -mode 後賦值，wire 經 provideRunMode 消費）
	appMode = string(server.ModeAll)
)

// provideRunMode wire provider（server 裝配按模式分流）。
func provideRunMode() server.RunMode { return server.RunMode(appMode) }

func main() {
	// 容器 CPU 限額感知（GOMAXPROCS 修正）
	_, _ = maxprocs.Set(maxprocs.Logger(func(f string, a ...any) { slog.Debug(fmt.Sprintf(f, a...)) }))

	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "migrate":
		err = runMigrate(args)
	case "admin":
		err = admincmd.Run(args)
	case "reencrypt-cards":
		err = admincmd.RunReencrypt(args)
	case "migrate-from-v1":
		err = admincmd.RunMigrateV1(args)
	case "install":
		err = runInstall(args)
	case "dbtest":
		err = runDBTest(args)
	case "self-update":
		err = runSelfUpdate(args)
	case "docker-backup-check":
		var bc *conf.Bootstrap
		bc, err = loadBootstrap("/app/configs")
		if err == nil {
			var dialect string
			dialect, err = update.DockerBackupDialect(bc.Data)
			if err == nil {
				fmt.Println(dialect)
			}
		}
	case "version":
		fmt.Printf("%s %s (%s)\n", Name, Version, id)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %q\n\n", cmd)
		printUsage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "zcard: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`zcard —— ZCard 2.0 單二進制服務

用法:
  zcard serve  [-conf <dir>] [-mode all|api|worker]   啓動服務（默認 all）
  zcard migrate [-conf <dir>]                          應用待執行遷移後退出
  zcard admin  create|list|reset-password              運維子命令
  zcard reencrypt-cards --new-key <hex>                卡密密鑰輪換
  zcard migrate-from-v1 --old-env <dir> [--dry-run]   1.x 數據遷移（先跑預檢報告）
  zcard self-update [--check|--rollback]               在線更新（ed25519 驗籤；--check 只查）
  zcard install                                        安裝嚮導（M1 交付）
  zcard version                                        版本信息

通用 flag:
  -conf <dir>   配置目錄（默認 configs；含 config.yaml）
`)
}

// loadBootstrap 裝載配置（file source；運行時業務開關在 settings 表，鐵律 7）。
func loadBootstrap(confDir string) (*conf.Bootstrap, error) {
	if confDir == "" {
		confDir = "configs"
	}
	c := config.New(config.WithSource(file.NewSource(confDir)))
	defer c.Close()
	if err := c.Load(); err != nil {
		return nil, fmt.Errorf("讀取配置失敗（目錄 %s）: %w", confDir, err)
	}
	bc := &conf.Bootstrap{}
	if err := c.Scan(bc); err != nil {
		return nil, fmt.Errorf("解析配置失敗: %w", err)
	}
	if bc.Security == nil {
		bc.Security = &conf.Security{}
	}
	return bc, nil
}

// newLogger slog 構造（text/json + 級別；結構化：事件名 + 關鍵 ID，）。
func newLogger(bc *conf.Bootstrap) *slog.Logger {
	level := slog.LevelInfo
	format := "text"
	if bc.Log != nil {
		switch strings.ToLower(bc.Log.Level) {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		format = bc.Log.Format
	}
	hOpts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stdout, hOpts)
	if strings.ToLower(format) == "json" {
		h = slog.NewJSONHandler(os.Stdout, hOpts)
	}
	return slog.New(h).With(slog.String("service.id", id), slog.String("service.name", Name), slog.String("service.version", Version))
}

// applyMigrationsIfEnabled 啓動遷移（：加鎖串行，失敗拒絕啓動）。
func applyMigrationsIfEnabled(ctx context.Context, bc *conf.Bootstrap) error {
	if bc.Server != nil && !bc.Server.MigrateOnStart {
		return nil
	}
	d, cleanup, err := data.NewData(bc.Data)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := data.ApplyMigrations(ctx, d.DB, d.Dialect, bc.Data.Database.Source); err != nil {
		return fmt.Errorf("啓動遷移失敗（拒絕啓動，規劃 §10.4）: %w", err)
	}
	return data.UpgradeProductMediaRefs(ctx, d)
}

// completePendingInstall 在線安裝接力（庫切換重啓後）：待裝憑據存在 → 新庫安裝。
func completePendingInstall(ctx context.Context, bc *conf.Bootstrap) error {
	d, cleanup, err := data.NewData(bc.Data)
	if err != nil {
		return err
	}
	defer cleanup()
	return settings.CompletePendingInstall(ctx, d)
}

// ensureSeeds 啓動補種（冪等；獨立於遷移開關——表已存在即可）：基礎貨幣
// 缺失時寫入（老庫升級自動補 CNY，新庫由 Install 事務同源寫入）。
func ensureSeeds(ctx context.Context, bc *conf.Bootstrap) error {
	d, cleanup, err := data.NewData(bc.Data)
	if err != nil {
		return err
	}
	defer cleanup()
	return settings.EnsureDefaultCurrencies(ctx, d)
}

// runServe 啓動服務。
func runServe(args []string) (err error) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	mode := fs.String("mode", "all", "運行模式 all|api|worker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	settings.SetConfDir(*confDir)
	// 零配置首啓：配置缺失時生成 SQLite 引導配置（僅作嚮導載體，Web 裏再選庫）
	if created, cerr := ensureBootstrapConfig(*confDir); cerr != nil {
		return cerr
	} else if created {
		abs, _ := filepath.Abs(*confDir)
		fmt.Printf("zcard: 未發現配置，已生成引導配置 %s/config.yaml（0600，含隨機密鑰）\n", abs)
		fmt.Printf("zcard: 瀏覽器打開 http://<本機IP>:8000/install 選擇數據庫（PG 推薦/MySQL/SQLite）完成安裝\n")
	}
	bc, err := loadBootstrap(*confDir)
	if err != nil {
		return err
	}
	ensureSQLiteDir(bc)
	logger := newLogger(bc)
	log.SetDefault(logger)

	// 更新 pending 態：serve 任何啓動失敗（配置/遷移/裝配）自動回滾二進制，
	// systemd 重啓即舊版本（ 自動回滾；DB 只前滾，靠備份恢復）。
	binPath := currentBinaryPath()
	pending := false
	if binPath != "" {
		if st, lerr := updater.LoadState(binPath); lerr == nil && st.Status == updater.StatePending {
			pending = true
			logger.Warn("update.pending", "from", st.FromVer, "to", st.ToVer)
			defer func() {
				if err != nil && updater.RollbackOnBootFailure(binPath) {
					logger.Error("update.rollback.applied", "reason", "boot_failure", "from", st.FromVer)
				}
			}()
		}
	}

	if err := applyMigrationsIfEnabled(context.Background(), bc); err != nil {
		return err
	}
	// 在線安裝·庫切換接力：待裝憑據存在 → 在新庫上補裝（遷移已完成）
	if err := completePendingInstall(context.Background(), bc); err != nil {
		return err
	}
	if err := ensureSeeds(context.Background(), bc); err != nil {
		return err
	}
	switch *mode {
	case string(server.ModeAll), string(server.ModeAPI), string(server.ModeWorker):
		appMode = *mode
	default:
		return fmt.Errorf("未知運行模式 %q（all|api|worker）", *mode)
	}
	logger.Info("zcard.starting", "mode", appMode, "version", Version)

	supplier.ServerVersion = orDev(Version)
	settings.SetServerVersion(orDev(Version))
	server.Version = orDev(Version) // /health 下發真實構建版本（此前恆 dev 的根因）
	deps, cleanup, err := wireApp(bc.Server, bc.Data, bc.Security, logger)
	if err != nil {
		return err
	}
	app, updateSvc := deps.App, deps.Update
	// 多進程形態禁用面板更新（方案 ）：api/worker 分進程會撞 update.state，
	// 且面板只重啓 api 進程 → worker 舊代碼 + 新 schema。滾動更新走 CLI。
	if appMode != string(server.ModeAll) && updateSvc != nil {
		updateSvc.Disable(fmt.Sprintf("多進程模式（%s）面板在線更新已禁用——請用 zcard self-update 逐進程滾動", appMode))
	}
	// cleanup 單次化：重啓路徑 exec 前顯式收口，進程退出路徑 defer 兜底
	var cleanupOnce sync.Once
	doCleanup := func() { cleanupOnce.Do(cleanup) }
	defer doCleanup()

	// 更新重啓 hook（三分支）：置位標記 + 異步優雅停機；
	// app.Run 返回後按 supervisor 分流——管理器環境 exit 0 交拉起，裸跑 exec 自替換
	var restarting atomic.Bool
	var supAtRestart atomic.Value // 停機前預取的 supervisor 判定（doCleanup 關 DB 後配置讀不到）
	supervisorOf := func() string {
		if v := supAtRestart.Load(); v != nil {
			return v.(string)
		}
		s := updater.DetectSupervisor()
		if updateSvc != nil {
			s = updateSvc.SupervisorKind(context.Background())
		}
		return s
	}
	if updateSvc != nil {
		updateSvc.SetRestartFn(func() {
			supAtRestart.Store(supervisorOf()) // 停機前預取（此刻 DB 仍活）
			restarting.Store(true)
			go func() {
				// kratos App.Stop 無參（內部 5s 優雅停機）；hook 只負責觸發
				app.Stop()
			}()
		})
	}

	// 更新健康門（）：HTTP 就緒 + DB 連通自檢通過 → pending 轉 ok；
	// 超時 → 回滾並優雅退出，systemd 重啓舊版本。
	if pending {
		go func() {
			herr := updater.HealthGate(context.Background(), healthURL(bc), binPath, 3*time.Minute)
			if herr == nil {
				logger.Info("update.healthgate.ok", "version", Version)
				return
			}
			logger.Error("update.healthgate.timeout", "error", herr)
			if updater.RollbackOnBootFailure(binPath) {
				logger.Error("update.rollback.applied", "reason", "healthgate_timeout")
			}
			_ = app.Stop()
		}()
	}
	runErr := app.Run()
	if restarting.Load() {
		doCleanup() // exec 前顯式收口（exec 不走 defer 鏈）
		if err := restartAfterUpdate(binPath, logger, supervisorOf()); err != nil {
			// exec 失敗：舊進程內存映像仍在服務，磁盤已是新版——復位更新態允許重試，
			// 返回 nil 保持 exit 0（非零會誤觸 systemd OnFailure 回滾掉剛落位的新版）
			logger.Error("update.restart.failed", "error", err)
			if updateSvc != nil {
				updateSvc.OnRestartFailed(err)
			}
		}
		return nil
	}
	return runErr
}

// restartAfterUpdate 更新後重啓分流（方案 ）：僅 systemd 走 exit 0 交拉起
// （Restart=always 由 install.sh 保證）；supervisord/寶塔/pm2/裸跑一律 exec 同進程
// 替換（PID 不變管理器零感知，規避 autorestart 配置不可控）。sup 由調用方在
// 停機前預取（doCleanup 關 DB 後配置讀不到，回落探測會產生意圖漂移）。
func restartAfterUpdate(binPath string, logger *slog.Logger, sup string) error {
	if binPath == "" {
		return fmt.Errorf("無法定位二進制路徑")
	}
	// 僅 systemd 走「exit 0 交拉起」——Restart=always 由 install.sh 保證。
	// supervisord/寶塔守護的 autorestart 配置不可控（unexpected 語義下 exit 0
	// 不觸發重啓 = 更新即停服），pm2 同理——一律 exec 同進程替換（PID 不變，
	// 管理器零感知，對兩者完全兼容）。
	if sup == "systemd" {
		logger.Info("update.restart.exit_to_supervisor", "supervisor", sup)
		return nil
	}
	logger.Info("update.restart.exec_self", "supervisor", sup, "bin", binPath)
	return updater.RestartSelf(binPath)
}

// currentBinaryPath 當前二進制絕對路徑（失敗返回空串，更新態檢查跳過）。
func currentBinaryPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return p
}

// healthURL 健康門探測地址（HTTP 監聽口 → 127.0.0.1 本機自檢）。
func healthURL(bc *conf.Bootstrap) string {
	addr := ""
	if bc.Server != nil && bc.Server.Http != nil {
		addr = bc.Server.Http.Addr
	}
	if addr == "" {
		addr = "0.0.0.0:8000"
	}
	parts := strings.Split(addr, ":")
	return "http://127.0.0.1:" + parts[len(parts)-1] + "/health"
}

// runDBTest 安裝腳本配套：目標庫/Redis 連接校驗（庫不存在自動創建；成功 exit 0）。
func runDBTest(args []string) error {
	fs := flag.NewFlagSet("dbtest", flag.ExitOnError)
	dialect := fs.String("dialect", "", "postgres | mysql")
	host := fs.String("host", "127.0.0.1", "數據庫主機")
	port := fs.Int("port", 0, "數據庫端口（0=按方言默認 5432/3306）")
	user := fs.String("user", "", "數據庫用户")
	password := fs.String("password", "", "數據庫密碼")
	name := fs.String("name", "", "數據庫名")
	redisAddr := fs.String("redis", "", "Redis 地址（mysql/postgres 必填）")
	redisPassword := fs.String("redis-password", "", "Redis 密碼")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == 0 {
		if *dialect == "mysql" {
			*port = 3306
		} else {
			*port = 5432
		}
	}
	if err := settings.ValidateSwitchInput(*dialect, *host, int32(*port), *user, *password, *name, *redisAddr, *redisPassword); err != nil {
		return err
	}
	fmt.Println("連接成功：數據庫與 Redis 驗證通過（庫已就緒）")
	return nil
}

// runMigrate 僅執行遷移。
func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	if err := fs.Parse(args); err != nil {
		return err
	}
	bc, err := loadBootstrap(*confDir)
	if err != nil {
		return err
	}
	if err := applyMigrationsIfEnabled(context.Background(), bc); err != nil {
		return err
	}
	fmt.Println("遷移完成（無待執行項或已全部應用）")
	return nil
}

// runInstall 安裝嚮導（：CLI 交互式；Web /install 前端補齊）。
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := ensureBootstrapConfig(*confDir); err != nil {
		return err
	}
	bc, err := loadBootstrap(*confDir)
	if err != nil {
		return err
	}
	ensureSQLiteDir(bc)
	// 全新庫先遷移（serve 啓動鏈同款）——否則 CLI 直裝報 no such table
	if err := applyMigrationsIfEnabled(context.Background(), bc); err != nil {
		return err
	}
	d, cleanup, err := data.NewData(bc.Data)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := settings.RunInstallCLI(context.Background(), d); err != nil {
		return err
	}
	fmt.Println("安裝完成：瀏覽器打開服務地址即可使用（後台 /admin）")
	return nil
}

// newApp kratos.App 裝配：按模式選擇 server 組合（規劃 單進程多角色）。
//
//	all = HTTP + gRPC + worker + 後台（默認，單機形態）
//	api = HTTP + gRPC + 後台relay（多實例 api，cron 不註冊）
//	worker = worker + 後台（消費與週期任務，多實例 asynq 競爭消費）
func newApp(logger *slog.Logger, hs *khttp.Server, gs *kgrpc.Server, ws *server.WorkerServer, bs *server.BackgroundServer, dp *data.Dispatcher, procureSvc *procurement.ProcureService, notifyDisp *notify.Dispatcher, affiliateSvc *affiliate.AffiliateService, resellerSettleSvc *reseller.SettleService, fulfillRepo *fulfillment.DeliveryRepoImpl, pointsSvc *memberlevel.PointsService, orderUC *order.OrderUsecase, payRepo *payment.PaymentRepoImpl, walletRepo *wallet.WalletRepoImpl, stockGate orderport.UpstreamStockGate, catalogSvc *catalog.StoreCatalogService, storeOrderSvc *order.StoreOrderService) *kratos.App {
	if reader, ok := orderUC.Flash.(couponport.FlashReader); ok {
		catalogSvc.SetFlashReader(reader)
	}
	// 破環點：order 超時取消慢通道順延探測 ← payment 實現
	// （wire 環 OrderUsecase ↔ PaymentRepoImpl，裝配期手工注入——同 dp.Register 模式）
	orderUC.SetSlowPaymentChecker(payRepo)
	// 破環點：上游代發項下單前實時庫存預檢 ← supply 網關實現
	orderUC.SetStockGate(stockGate)
	storeOrderSvc.SetRecoverySender(notifyDisp)
	if lookup, ok := stockGate.(catalogport.StockLookup); ok {
		catalogSvc.SetStockLookup(lookup)
	}
	// 佣金提現打通：打款 FIFO 消耗佣金（affiliate → wallet 注入，裝配期一次）
	walletRepo.SetCommissionConsumer(affiliateSvc.Repo())
	// 事件訂閲註冊（）：order.paid → 採購（wire 破環點，見 bootstrap/queue.go 註釋）
	dp.Register(data.HandlerReg{
		Consumer: "procurement.order_paid",
		Type:     events.OrderPaid,
		Fn:       procureSvc.OnOrderPaid,
	})
	// 事件訂閲註冊（）：order.paid → 三級佣金入賬；order.refunded → 逆向扣回
	dp.Register(data.HandlerReg{Consumer: "affiliate.settle", Type: events.OrderPaid, Fn: affiliateSvc.OnOrderPaid, Transactional: true})
	dp.Register(data.HandlerReg{Consumer: "affiliate.reversal", Type: events.OrderRefunded, Fn: affiliateSvc.OnOrderRefunded, Transactional: true})
	// 事件訂閲註冊（）：order.paid → 分站利潤入賬（訂單快照 subsite_profit/profit_eligible）
	dp.Register(data.HandlerReg{Consumer: "reseller.settle", Type: events.OrderPaid, Fn: resellerSettleSvc.OnOrderPaid, Transactional: true})
	// 事件訂閲註冊（ ）：order.paid → 自動交付（reserved→used/即刪 + 交付記錄；冪等由 FulfillOrder 兜底）
	dp.Register(data.HandlerReg{Consumer: "fulfillment.deliver", Type: events.OrderPaid, Fn: fulfillRepo.OnOrderPaid, Transactional: true})
	// 事件訂閲註冊（）：order.refunded → 分站利潤扣回（refund_deduct 負行/負債態）
	dp.Register(data.HandlerReg{Consumer: "reseller.reversal", Type: events.OrderRefunded, Fn: resellerSettleSvc.OnOrderRefunded, Transactional: true})
	// 事件訂閲註冊（）：order.paid → 積分產生（等級 points_rule；冪等鍵 points:<orderID>）
	dp.Register(data.HandlerReg{Consumer: "memberlevel.points_earn", Type: events.OrderPaid, Fn: pointsSvc.OnOrderPaid, Transactional: true})
	dp.Register(data.HandlerReg{Consumer: "memberlevel.points_refund", Type: events.OrderRefunded, Fn: pointsSvc.OnOrderRefunded, Transactional: true})
	// 事件訂閲註冊（）：交易事件 → 通知分發（email/inbox 按模板逐通道投遞）
	for _, typ := range notify.SubscribedEvents() {
		t := typ
		dp.Register(data.HandlerReg{
			Consumer: "notify.dispatcher",
			Type:     t,
			Fn:       notifyDisp.HandleEvent,
		})
	}
	for _, typ := range notify.TelegramEvents() {
		dp.Register(data.HandlerReg{Consumer: "notify.telegram_orders", Type: typ, Fn: notifyDisp.EnqueueTelegram, Transactional: true})
	}
	var servers []transport.Server
	// gRPC 可選（配置 addr 為空時不裝配）
	withGRPC := func(list []transport.Server) []transport.Server {
		if gs == nil {
			return list
		}
		return append([]transport.Server{gs}, list...)
	}
	switch server.RunMode(appMode) {
	case server.ModeAPI:
		servers = withGRPC([]transport.Server{hs, bs})
	case server.ModeWorker:
		servers = []transport.Server{ws, bs}
	default:
		servers = withGRPC([]transport.Server{hs, ws, bs})
	}
	return kratos.New(
		kratos.ID(id),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(map[string]string{}),
		kratos.Logger(logger),
		kratos.Server(servers...),
	)
}

func orDev(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}
