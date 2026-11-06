package supply

// 貨源同步服務（// + S1 同步引擎改造）：
// - 三類 scope：collect（採集：upsert + 庫存 + 刪除對賬）/ price（僅刷價格）/
// status（僅刷上下架 + up_stock）——輕量 scope 走 maintainer 端口不建不刪
// - 狀態增量：支持 IncrementalLister 時按錨點拉取變更；價格/採集全量核價，
// 避免賬號優惠變化卻未更新商品時間時漏同步。
// - 刪除對賬：僅權威快照（全量 + IncludesInactive 回聲）做——seenCodes 對賬
// 把上游已消失商品批量下架；護欄：上游聲稱 total > 實際處理數 → 任務失敗
// 不刪（寧可保守，1.x「不能批量誤刪」紀律）
// - 請求節流：分頁頁間 request_delay（settings.schedule，防上游限流封 IP）；
// 庫存補查分批併發 + 批次間隔 + 600s 限速預算（1.x AcgFakaDriver 同款參數）
// - 任務追蹤：進度/心跳(30s)/統計/取消標誌；失敗 error_context 落庫
// - 庫存失敗保持未知，彙總失敗商品；商品導入完成不等於庫存查詢成功
// - 終態發佈 sync.completed 事件（ 告警 / 對賬數據源）

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	catalogport "github.com/NovaWorks/zcard-next/server/internal/mods/catalog/port"
	"github.com/NovaWorks/zcard-next/server/internal/mods/supply/adapter"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplysynctask"
)

// SyncTaskType 同步任務隊列類型（asynq mux 精確匹配；low 隊列）。
const SyncTaskType = "supply.sync"

// 同步 scope（任務粒度；空 = collect 兼容歷史任務）。
const (
	ScopeCollect = "collect"
	ScopePrice   = "price"
	ScopeStatus  = "status"
	ScopeListing = "listing"
	ScopeStock   = "stock" // 僅補查已映射商品庫存，不寫價格、分類、上下架
)

// 節流/補查默認參數（settings.schedule 可覆蓋；對齊 1.x AcgFakaDriver）。
const (
	defaultPageDelaySec   = 1       // 商品分頁頁間間隔（秒；0=不限）
	defaultStockConc      = 3       // 庫存補查併發（1-10）
	defaultStockBatchMs   = 200     // 補查批次間隔（毫秒）
	stockThrottleBudgetMs = 600_000 // 補查限速預算（600s，超出報錯提示調參）
)

// scheduleSettings 連接級節流參數（ ；S2 自適應節奏器在此基礎上倍增）。
type scheduleSettings struct {
	PageDelay       time.Duration // 商品分頁頁間隔
	StockConc       int           // 庫存補查併發
	StockBatchDelay time.Duration // 補查批次間隔
}

func loadScheduleSettings(conn *ent.SupplyConnection) scheduleSettings {
	var sched map[string]any
	if conn != nil {
		sched, _ = conn.Settings["schedule"].(map[string]any)
	}
	if sched == nil {
		sched = map[string]any{}
	}
	delaySec := toInt(sched["request_delay"], defaultPageDelaySec)
	conc := toInt(sched["stock_concurrency"], defaultStockConc)
	if conc < 1 {
		conc = 1
	}
	if conc > 10 {
		conc = 10
	}
	batchMs := toInt(sched["stock_request_delay_ms"], defaultStockBatchMs)
	return scheduleSettings{
		PageDelay:       time.Duration(delaySec) * time.Second,
		StockConc:       conc,
		StockBatchDelay: time.Duration(batchMs) * time.Millisecond,
	}
}

func toInt(v any, def int) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return def
}

// SyncService 同步執行器。
type SyncService struct {
	stockAdapterFactory func(*ent.SupplyConnection) (adapter.Adapter, error)
	repo                *SupplyRepoImpl
	writer              catalogport.UpstreamProductWriter
	maintainer          catalogport.UpstreamProductMaintainer // 輕量 scope + 刪除對賬（nil=跳過）
	pacer               *Pacer                                // 自適應節奏器（nil=靜態節流，測試用）
	enq                 queue.Enqueuer
	outbox              events.Writer // sync.completed 發佈
	log                 *slog.Logger

	// 上游圖片採集去重緩存（目錄+絕對 URL → 本地 /uploads/ 路徑；僅緩存成功下載）。
	coverMu    sync.Mutex
	coverCache map[string]string
	coverDirs  map[uint64]string // connectionID → 渠道封面目錄名（ensureCoverDir 緩存）
}

// NewSyncService 構造。
func NewSyncService(repo *SupplyRepoImpl, writer catalogport.UpstreamProductWriter, maintainer catalogport.UpstreamProductMaintainer, pacer *Pacer, enq queue.Enqueuer, outbox events.Writer, log *slog.Logger) *SyncService {
	return &SyncService{repo: repo, writer: writer, maintainer: maintainer, pacer: pacer, enq: enq, outbox: outbox, log: log}
}

// StartTask 調度同步任務：有 Redis 入 low 隊列；無 Redis（或未裝配，測試）直接
// 異步執行（降級串行語義）。
func (s *SyncService) StartTask(ctx context.Context, taskID uint64) error {
	payload, err := json.Marshal(map[string]uint64{"task_id": taskID})
	if err != nil {
		return err
	}
	task, err := s.repo.GetSyncTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Scope != ScopeImport && s.enq != nil && s.enq.Enabled() {
		return s.enq.Enqueue(ctx, queue.Task{
			Type:      SyncTaskType,
			Payload:   payload,
			Queue:     queue.QueueLow,
			DedupeKey: SyncTaskType + ":" + strconv.FormatUint(taskID, 10),
		})
	}
	// 降級：進程內異步（失敗由任務終態 failed 落庫，與 asynq 語義一致）
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		if err := s.RunTask(runCtx, payload); err != nil {
			s.log.Error("supply.sync.run_failed", "task_id", taskID, "err", err)
		}
	}()
	return nil
}

// RunTask 隊列 worker 入口（payload = {"task_id": N}）。
func (s *SyncService) RunTask(ctx context.Context, payload []byte) error {
	var req struct {
		TaskID uint64 `json:"task_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("supply.sync: 解析任務載荷失敗: %w", err)
	}
	return s.RunSync(ctx, req.TaskID)
}

// RunSync 執行一次同步（worker 與降級路徑共用；重複入隊由任務狀態冪等）。
func (s *SyncService) runSync(ctx context.Context, taskID uint64) error {
	task, err := s.repo.GetSyncTask(ctx, taskID)
	if err != nil {
		return err
	}
	// 冪等：終態任務直接 ACK（重複投遞不重跑）
	if task.Status != supplysynctask.StatusPending {
		return nil
	}
	scope := task.Scope
	if scope == "" {
		scope = ScopeCollect // 歷史任務兼容
	}
	if scope != ScopeCollect && scope != ScopePrice && scope != ScopeStatus && scope != ScopeListing && scope != ScopeStock {
		_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "INVALID_SCOPE", "scope 必須為 collect|price|status|listing|stock")
		return nil
	}
	conn, err := s.repo.GetConnection(ctx, task.ConnectionID)
	if err != nil {
		return err
	}
	// 熔斷冷卻中：任務直接失敗留痕（定時調度會跳過冷卻中的渠道——這裏兜底
	// 手動觸發/冷卻期內已入隊的任務；不重試）
	if s.pacer != nil && s.pacer.CooldownActive(conn) {
		_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "RATE_LIMITED_COOLDOWN",
			"渠道熔斷冷卻中（上游限流），稍後重試")
		s.publishCompleted(ctx, conn.ID, taskID, "failed")
		return nil
	}
	credsJSON, err := s.repo.OpenCredentials(conn)
	if err != nil {
		_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "CREDENTIALS_DECRYPT_FAILED", err.Error())
		return nil // 憑據損壞不重試（提示重配），避免死循環
	}
	var creds adapter.Credentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "CREDENTIALS_INVALID", err.Error())
		return nil
	}
	a, err := adapter.New(conn.Driver, conn.BaseURL, creds, parseRetryIntervals(conn.RetryIntervals))
	if err != nil {
		_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "ADAPTER_NEW", err.Error())
		return nil
	}

	// 鎖：按連接分組（無 Redis 時串行；任務狀態機本身防併發重入）
	if err := s.repo.SetTaskProcessing(ctx, taskID, 0); err != nil {
		return err
	}

	sched := loadScheduleSettings(conn)
	if scope == ScopeStock {
		err := s.runStockOnly(ctx, task, conn, a, sched)
		if err != nil {
			_ = s.repo.FinishTask(ctx, task.ID, supplysynctask.StatusFailed, "STOCK_QUERY_FAILED", adapter.StockErrorSummary(err))
			s.publishCompleted(ctx, conn.ID, task.ID, "failed")
		}
		return err
	}

	// 列表函數解析：增量（驅動支持 + 有錨點）→ 全量回落。
	// 增量快照不具對賬權威性（未見 ≠ 已刪除）→ authoritative 僅在全量且回聲完整時成立。
	list, incremental := resolveLister(a, task, readSyncAnchor(conn, scope), s.log)
	return s.runLoop(ctx, taskID, task, conn, a, sched, scope, incremental, list)
}

// resolveLister 列表函數決策：
// 驅動實現 IncrementalLister 且錨點有效 → 增量（含下架變更）；否則全量。
func resolveLister(a adapter.Adapter, task *ent.SupplySyncTask, anchor time.Time, log *slog.Logger) (func(ctx context.Context, page, pageSize int) (*adapter.ProductList, error), bool) {
	// Account discounts can change without a product updated_at change. Any scope
	// that writes prices must inspect the full catalog, even when incremental was requested.
	if task.Mode != "incremental" || anchor.IsZero() || task.Scope == ScopePrice || task.Scope == ScopeCollect || task.Scope == "" {
		return func(ctx context.Context, page, pageSize int) (*adapter.ProductList, error) {
			return a.ListProducts(ctx, page, pageSize, true)
		}, false
	}
	if il, ok := a.(adapter.IncrementalLister); ok {
		after := anchor.Add(-1 * time.Minute) // 安全窗（時鐘偏差，dujiao-next 同款）
		log.Info("supply.sync.incremental", "after", after.Format(time.RFC3339))
		return func(ctx context.Context, page, pageSize int) (*adapter.ProductList, error) {
			return il.ListProductsAfter(ctx, page, pageSize, after)
		}, true
	}
	log.Info("supply.sync.incremental_unsupported_fallback_full", "driver", a.Protocol())
	return func(ctx context.Context, page, pageSize int) (*adapter.ProductList, error) {
		return a.ListProducts(ctx, page, pageSize, true)
	}, false
}

// runLoop 分頁主循環（增量/全量共用；incremental 決定是否允許刪除對賬）。
func (s *SyncService) runLoop(ctx context.Context, taskID uint64, task *ent.SupplySyncTask, conn *ent.SupplyConnection, a adapter.Adapter, sched scheduleSettings, scope string, incremental bool, list func(ctx context.Context, page, pageSize int) (*adapter.ProductList, error)) error {
	stats := TaskProgress{Stage: "fetching_products", Page: 1}
	heartbeat := time.Now()

	// 分類映射緩存：upstream_category → local_category_id（僅 collect 寫分類）
	categoryMap := map[string]uint64{}
	if scope == ScopeCollect {
		if err := s.cacheCategoryMappings(ctx, a, conn, categoryMap); err != nil {
			s.log.Warn("supply.sync.categories_failed", "connection_id", conn.ID, "err", err)
		}
	}

	// 對賬狀態：seenCodes（權威快照時收集）；reportedTotal 護欄基準
	authoritative := !incremental
	seen := map[string]bool{}
	reportedTotal := 0
	processed := 0
	stockTotal, stockFailed := 0, 0
	var stockErrors []string

	page := 1
	for {
		if !heartbeat.IsZero() && time.Since(heartbeat) >= 30*time.Second {
			cancel, err := s.repo.TouchTask(ctx, taskID, stats)
			if err != nil {
				s.failAndMaybeRetry(ctx, taskID, conn.ID, "HEARTBEAT_FAILED", err.Error())
				return nil
			}
			if cancel {
				_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusCanceled, "", "")
				s.publishCompleted(ctx, conn.ID, taskID, "canceled")
				return nil
			}
			stats = TaskProgress{}
			heartbeat = time.Now()
		}

		listedAt := time.Now().UTC()
		list0, err := list(adapter.WithCatalogRead(ctx), page, 50)
		if err != nil {
			// fail-open：拉取失敗 → 已處理部分保留，任務失敗留痕（可重跑）。
			// 限流信號反饋節奏器（AIMD 降速/熔斷判據）
			if s.pacer != nil && errors.Is(err, adapter.ErrRateLimited) {
				s.pacer.OnRateLimited(ctx, conn, err.Error())
			}
			s.failAndMaybeRetry(ctx, taskID, conn.ID, "LIST_PRODUCTS_FAILED", err.Error())
			s.publishCompleted(ctx, conn.ID, taskID, "failed")
			return nil
		}
		// 頁成功：反饋節奏器（連續成功間隔回升）+ 動態頁間隔（自適應值 > 配置底線）
		if s.pacer != nil {
			s.pacer.OnSuccess(ctx, conn)
		}
		pageDelay := sched.PageDelay
		if s.pacer != nil {
			pageDelay = s.pacer.Delay(conn)
		}
		if authoritative && !list0.IncludesInactive {
			// 上游未回聲 include_inactive → 快照不完整，禁用刪除對賬（防誤刪）
			authoritative = false
			s.log.Info("supply.sync.reconcile_disabled_no_echo", "task_id", taskID)
		}
		if list0.Total > reportedTotal {
			reportedTotal = list0.Total
		}
		stats.Page = page

		maintainable := make([]adapter.Product, 0, len(list0.Items))
		for _, p := range list0.Items {
			skip, e := s.maintenanceProtected(ctx, conn.ID, p.ID)
			if e != nil {
				return e
			}
			if skip {
				stats.ManualSkipped++
				stats.Processed++
				processed++
				if authoritative {
					seen[p.ID] = true
				}
				continue
			}
			p.StockCheckedAt = listedAt
			maintainable = append(maintainable, p)
		}
		list0.Items = maintainable
		// 採集和狀態同步統一補查；失敗項保持未知並彙總。
		if scope == ScopeCollect || scope == ScopeStatus || scope == ScopeListing {
			if conn.Driver == "acg_faka" {
				for i := range list0.Items {
					list0.Items[i].Stock = -2
				}
			}
			if err := s.backfillStocks(ctx, a, sched, list0.Items, taskID); err != nil {
				if errors.Is(err, errStockCanceled) {
					_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusCanceled, "", "")
					s.publishCompleted(ctx, conn.ID, taskID, "canceled")
					return nil
				}
				_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "STOCK_BACKFILL_BUDGET", err.Error())
				s.publishCompleted(ctx, conn.ID, taskID, "failed")
				return nil
			}
		}

		if scope == ScopeCollect || scope == ScopeStatus || scope == ScopeListing {
			for i := range list0.Items {
				if e := s.listingSKUStock(ctx, a, conn.ID, &list0.Items[i]); e != nil {
					if errors.Is(e, adapter.ErrRateLimited) {
						if s.pacer != nil {
							s.pacer.OnRateLimited(ctx, conn, "庫存檢查限流")
						}
						_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "STOCK_QUERY_FAILED", "規格庫存檢查被限流，請稍後重試")
						return nil
					}
					return e
				}
				p := list0.Items[i]
				if p.ProductKind == "sms_channel" {
					continue // Stock belongs to each offer, never to the channel product.
				}
				stockTotal++
				if p.Stock < -1 {
					stockFailed++
					if len(stockErrors) < 10 {
						stockErrors = append(stockErrors, fmt.Sprintf("%s: %s", p.ID, p.StockError))
					}
				}
			}
		}
		for i := range list0.Items {
			if time.Since(heartbeat) >= 30*time.Second {
				canceled, err := s.repo.TouchTask(ctx, taskID, stats)
				if err != nil {
					return err
				}
				stats = TaskProgress{Stage: "pricing", Page: page}
				heartbeat = time.Now()
				if canceled {
					_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusCanceled, "", "")
					s.publishCompleted(ctx, conn.ID, taskID, "canceled")
					return nil
				}
			}
			if quoter, ok := a.(adapter.AccountQuoter); ok && list0.Items[i].IsActive && (scope == ScopeCollect || scope == ScopePrice) {
				shouldQuote := true
				if scope == ScopePrice {
					_, err := s.repo.GetMapping(ctx, conn.ID, list0.Items[i].ID, "")
					if err == ErrNotFound {
						shouldQuote = false
					} else if err != nil {
						return err
					}
				}
				if shouldQuote {
					quoted, err := quoter.QuoteProduct(ctx, &list0.Items[i])
					if err != nil {
						if s.pacer != nil && errors.Is(err, adapter.ErrRateLimited) {
							s.pacer.OnRateLimited(ctx, conn, err.Error())
						}
						s.failAndMaybeRetry(ctx, taskID, conn.ID, "PRICE_QUOTE_FAILED", "商品 "+list0.Items[i].ID+" 賬號報價失敗，已保留原價格，請重試")
						s.publishCompleted(ctx, conn.ID, taskID, "failed")
						return nil
					}
					list0.Items[i] = *quoted
				}
			}
			cancel, err := s.syncOne(ctx, taskID, task, conn, &list0.Items[i], categoryMap, &stats)
			if err != nil {
				s.failAndMaybeRetry(ctx, taskID, conn.ID, "SYNC_ITEM_FAILED", err.Error())
				s.publishCompleted(ctx, conn.ID, taskID, "failed")
				return nil
			}
			if cancel {
				_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusCanceled, "", "")
				s.publishCompleted(ctx, conn.ID, taskID, "canceled")
				return nil
			}
			processed++
			if authoritative {
				seen[list0.Items[i].ID] = true
			}
		}
		if !list0.HasMore {
			break
		}
		page++
		// 頁間節流（自適應節奏器優先；防上游限流封 IP）
		if err := sleepCtx(ctx, pageDelay); err != nil {
			_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "CTX_CANCELED", err.Error())
			return nil
		}
	}

	// 刪除對賬（僅 collect + 權威快照）：
	// 護欄——上游聲稱總數 > 實際處理數説明分頁不完整，寧可不刪也不能批量誤刪。
	if scope == ScopeCollect && authoritative {
		if reportedTotal > processed {
			_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "RECONCILE_GUARD",
				fmt.Sprintf("上游聲稱 %d 件但僅處理 %d 件（分頁不完整），跳過刪除對賬防誤刪", reportedTotal, processed))
			s.publishCompleted(ctx, conn.ID, taskID, "failed")
			return nil
		}
		if s.maintainer != nil && len(seen) > 0 {
			codes := make([]string, 0, len(seen))
			for c := range seen {
				codes = append(codes, c)
			}
			shelved, err := s.maintainer.ShelveOffMissing(ctx, conn.ID, codes)
			if err != nil {
				_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "RECONCILE_SHELVE_FAILED", err.Error())
				s.publishCompleted(ctx, conn.ID, taskID, "failed")
				return nil
			}
			stats.Deleted += int32(shelved)
		}
	}

	// 收尾：錨點（增量依據）+ last_synced_at（collect 沿用舊列）+ done
	if _, err := s.repo.TouchTask(ctx, taskID, stats); err == nil {
		s.writeSyncAnchor(ctx, conn, scope)
		if scope == ScopeCollect {
			now := time.Now().UTC()
			client := s.repo.entClient(ctx)
			_, _ = client.SupplyConnection.UpdateOneID(conn.ID).SetLastSyncedAt(now).Save(ctx)
		}
	}
	if stockFailed > 0 {
		_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, "STOCK_QUERY_FAILED", fmt.Sprintf("商品同步已完成；庫存成功 %d，失敗 %d。可僅重試失敗庫存。%s", stockTotal-stockFailed, stockFailed, strings.Join(stockErrors, "；")))
		s.publishCompleted(ctx, conn.ID, taskID, "failed")
		return nil
	}
	_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusDone, "", "")
	clearSyncRetry(taskID)
	s.publishCompleted(ctx, conn.ID, taskID, "done")
	return nil
}

// syncOne 同步單個商品（scope 分支）：
// - collect：價格保護 → upsert 商品 → upsert 映射（up_stock 緩存）
// - price：僅已映射商品刷價格（價格保護同口徑；不建不刪不動庫存）
// - status：僅已映射商品刷上下架 + up_stock（不建不刪不動價格）
//
// 返回 (cancelRequested, error)。
func (s *SyncService) syncOne(ctx context.Context, taskID uint64, task *ent.SupplySyncTask, conn *ent.SupplyConnection, p *adapter.Product, categoryMap map[string]uint64, stats *TaskProgress) (bool, error) {
	if skip, e := s.maintenanceProtected(ctx, conn.ID, p.ID); e != nil {
		return false, e
	} else if skip {
		stats.ManualSkipped++
		stats.Processed++
		return false, nil
	}
	// Fetch media before opening the pricing transaction.
	cover := ""
	if task.Scope == "" || task.Scope == ScopeCollect {
		m, err := s.repo.GetMapping(ctx, conn.ID, p.ID, "")
		if err != nil && err != ErrNotFound {
			return false, err
		}
		if p.IsActive {
			cover = s.coverFor(ctx, m, conn, p.Cover)
		}
		mediaCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		prepared := *p
		prepared.Description = s.descriptionFor(mediaCtx, m, conn, p.Description)
		cancel()
		p = &prepared
	}
	next := *stats
	canceled := false
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		if err := s.checkPricingConnection(ctx, conn); err != nil {
			return err
		}
		var err error
		canceled, err = s.syncOneLocked(ctx, taskID, task, conn, p, categoryMap, &next, cover)
		return err
	})
	if data.IsProductLocked(err) || errors.Is(err, data.ErrLocalDeliveryProtected) {
		stats.ManualSkipped++
		stats.Processed++
		return false, nil
	}
	if err == nil {
		*stats = next
	}
	return canceled, err
}

func (s *SyncService) syncOneLocked(ctx context.Context, taskID uint64, task *ent.SupplySyncTask, conn *ent.SupplyConnection, p *adapter.Product, categoryMap map[string]uint64, stats *TaskProgress, preparedCover string) (bool, error) {
	mapping, err := s.repo.GetMapping(ctx, conn.ID, p.ID, "")
	notFound := err == ErrNotFound
	if err != nil && !notFound {
		return false, err
	}

	if !notFound && mapping.LocalProductID > 0 {
		local, e := data.GuardProductWrite(ctx, s.repo.data, mapping.LocalProductID)
		if e != nil {
			return false, e
		}
		if e = data.GuardUpstreamDelivery(ctx, s.repo.entClient(ctx), local); e != nil {
			return false, e
		}
		if e = s.validateListingSKUs(ctx, local.ID, p); e != nil {
			return false, e
		}
	}
	// ── 輕量 scope：無映射（未導入）直接跳過，絕不創建 ──
	if task.Scope == ScopePrice || task.Scope == ScopeStatus || task.Scope == ScopeListing {
		if notFound {
			return false, nil
		}
		if task.Scope == ScopePrice {
			return false, s.syncPriceOnly(ctx, conn, mapping, p, task.ForceReprice, stats)
		}
		return false, s.syncStatusOnly(ctx, conn, mapping, p, stats, task.Scope == ScopeListing)
	}

	if notFound {
		mapping = &ent.SupplyMapping{ConnectionID: conn.ID, UpstreamProduct: p.ID}
	}

	// 狀態語義：上游不可售（手動發貨/預選/停售）→ 本地下架(0)。
	// （曾用隱藏(2)——但隱藏商品會員可見可買，此類品 API 無法履約，必須全員下架）
	// 下架清空採集封面引用；不在事務提交前刪除文件，避免影響其他商品的共享封面。
	// 上游可售(1)不覆蓋本地已下架品——手動下架的運營意圖優先（同步只下架不上架）
	status := int8(1)
	if !p.IsActive {
		status = 0
		p.Cover = ""
	} else if !notFound && s.localProductShelvedOff(ctx, mapping.LocalProductID) {
		status = 0 // 保持本地手動下架（不寫 1 拉回）
	}

	if err := s.lockProductPricing(ctx, mapping); err != nil {
		return false, err
	}
	priceToWrite, skus, override, err := s.productPrices(ctx, conn, mapping, p, task.ForceReprice)
	if err != nil {
		return false, err
	}
	writePrice := priceToWrite >= 0
	if !writePrice && mapping.LocalProductID == 0 {
		status = 0
	}

	// upsert 本地商品（價格 -1 = 不更新）
	write := catalogport.UpstreamProductInput{
		ProductKind: p.ProductKind, DeliveryKind: p.DeliveryKind, SMSProduct: p.SMSProduct, SocialSchema: p.SocialSchema,
		ConnectionID:        conn.ID,
		UpstreamProductCode: p.ID,
		UpstreamSyncedAt:    time.Now().UTC(),
		Name:                p.Name,
		Description:         p.Description,
		DescriptionSet:      p.DescriptionSet,
		Cover:               preparedCover,
		FactoryPrice:        accountCost(conn, p),
		Status:              status,
		AutoOnshelf:         autoOnshelf(conn.Settings),
	}
	if localCat, ok := categoryMap[p.CategoryID]; ok {
		write.CategoryID = localCat
		write.CategorySet = true
	}
	if !writePrice {
		write.Price = -1
		stats.ManualSkipped++
	} else {
		write.Price = priceToWrite
		stats.PriceUpdated++
	}
	write.SKUs = skus

	productID, created, err := s.writeProductCategory(ctx, p.CategoryID, &write)
	if err != nil {
		return false, err
	}
	if created {
		stats.Created++
	} else {
		stats.Updated++
	}
	stats.Processed++

	// Collection may replace the local SKU set in this transaction. Recheck
	// after the upsert before caching stock or restoring availability.
	if err := s.validateListingSKUs(ctx, productID, p); err != nil {
		return false, err
	}

	// upsert 映射（up_stock 緩存 + pricing_override 持久化）
	mapping.LocalProductID = productID
	mapping.UpstreamCategory = p.CategoryID
	if write.CategorySet {
		mapping.LocalCategoryID = write.CategoryID
	}
	mapping.UpStock = p.Stock
	mapping.StockCheckedAt = stockObservationTime(p)
	mapping.PricingOverride = override
	if err := s.saveProductMapping(ctx, mapping); err != nil {
		return false, err
	}

	if exists, e := s.repo.entClient(ctx).Product.Query().Where(product.ID(productID)).Exist(ctx); e != nil {
		return false, e
	} else if exists {
		if err := s.observeListing(ctx, productID, p); err != nil {
			return false, err
		}
	}
	// 取消檢查（每商品粒度太細，放每頁尾部；此處僅任務級取消標誌）
	return false, nil
}

// syncPriceOnly price scope 輕路徑：價格保護 → 僅更新價格 + 基線持久化。
func (s *SyncService) syncPriceOnly(ctx context.Context, conn *ent.SupplyConnection, mapping *ent.SupplyMapping, p *adapter.Product, force bool, stats *TaskProgress) error {
	next := *stats
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		c := s.repo.entClient(ctx)
		if err := s.checkPricingConnection(ctx, conn); err != nil {
			return err
		}
		latest, err := s.repo.GetMapping(ctx, conn.ID, p.ID, "")
		if err != nil {
			return err
		}
		if err := s.lockProductPricing(ctx, latest); err != nil {
			return err
		}
		price, skus, override, err := s.productPrices(ctx, conn, latest, p, force)
		if err != nil {
			return err
		}
		if s.maintainer == nil {
			return nil
		}
		hasPrice := price >= 0
		for _, sk := range skus {
			hasPrice = hasPrice || sk.PriceCents >= 0
		}
		if hasPrice {
			found, err := s.maintainer.UpdateUpstreamPrice(ctx, conn.ID, p.ID, price, skus...)
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
		}

		if price >= 0 {
			next.PriceUpdated++
		} else {
			next.ManualSkipped++
		}
		// Cost is an account quote, independent of local sale-price protection.
		if cost := accountCost(conn, p); cost > 0 {
			if err := c.Product.Update().Where(product.ID(latest.LocalProductID), product.StatusGTE(0)).SetFactoryPrice(cost).Exec(ctx); err != nil {
				return err
			}
		}
		latest.PricingOverride = override
		if err := s.repo.UpsertMapping(ctx, latest); err != nil {
			return err
		}
		next.Processed++
		return nil
	})
	if err == nil {
		*stats = next
	}
	return err
}

// localProductShelvedOff 本地商品當前是否下架(0)——手動下架保持判據。
func (s *SyncService) localProductShelvedOff(ctx context.Context, localProductID uint64) bool {
	if localProductID == 0 {
		return false
	}
	st, err := s.repo.entClient(ctx).Product.Query().
		Where(product.ID(localProductID)).
		Select(product.FieldStatus).
		Int(ctx)
	return err == nil && st == 0
}

// syncStatusOnly uses the normalized stock after upstream backfill (-1 unlimited, -2 unknown).
// 單向語義：只傳導「下架」，不自動上架——運營在後台手動下架的商品不被同步
// 拉回（重新上架走後台操作；上游可售≠本地必須賣）。
func (s *SyncService) syncStatusOnly(ctx context.Context, conn *ent.SupplyConnection, mapping *ent.SupplyMapping, p *adapter.Product, stats *TaskProgress, listingOnly ...bool) error {
	local, localErr := s.repo.entClient(ctx).Product.Get(ctx, mapping.LocalProductID)
	if localErr != nil && !ent.IsNotFound(localErr) {
		return localErr
	}
	if s.maintainer != nil && !p.IsActive && (local == nil || !local.AutoListing) && !(len(listingOnly) > 0 && listingOnly[0]) {
		if _, err := s.maintainer.UpdateUpstreamStatus(ctx, conn.ID, p.ID, 0); err != nil {
			return err
		}
	}
	if p.Stock >= -2 {
		mapping.UpStock = p.Stock
		mapping.StockCheckedAt = stockObservationTime(p)
	}
	if err := s.repo.UpsertMapping(ctx, mapping); err != nil {
		return err
	}
	if local != nil {
		if err := s.observeListing(ctx, local.ID, p); err != nil {
			return err
		}
	}

	stats.Updated++
	stats.Processed++
	return nil
}

// backfillStocks bounds each read independently; failures do not erase references.
var errStockCanceled = errors.New("庫存補查已取消")

func stockObservationTime(p *adapter.Product) time.Time {
	if p.StockCheckedAt.IsZero() {
		return time.Now().UTC()
	}
	return p.StockCheckedAt
}

func (s *SyncService) backfillStocks(ctx context.Context, a adapter.Adapter, cfg scheduleSettings, items []adapter.Product, taskID uint64) error {
	var missing []int
	for i := range items {
		if items[i].ProductKind == "sms_channel" || items[i].ProductKind == "social_service" {
			items[i].Stock = -2
			continue
		}
		// ACG 的目錄庫存由調用方先標為未知；其他協議明確的 0/-1 保持原語義。
		if items[i].Stock < -1 {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	chunks := (len(missing) + cfg.StockConc - 1) / cfg.StockConc
	if throttleMs := int64(chunks-1) * cfg.StockBatchDelay.Milliseconds(); throttleMs > stockThrottleBudgetMs {
		return fmt.Errorf("庫存補查限速配置預計等待 %d 秒（items=%d concurrency=%d batch_delay_ms=%d），請提高併發數或縮短批次間隔",
			(throttleMs+999)/1000, len(missing), cfg.StockConc, cfg.StockBatchDelay.Milliseconds())
	}
	var rateLimited atomic.Bool
	for ci := 0; ci < chunks; ci++ {
		if taskID > 0 {
			canceled, err := s.repo.TouchTask(ctx, taskID, TaskProgress{Stage: "fetching_stock"})
			if err != nil {
				return err
			}
			if canceled {
				return errStockCanceled
			}
		}
		lo := ci * cfg.StockConc
		hi := lo + cfg.StockConc
		if hi > len(missing) {
			hi = len(missing)
		}
		var wg sync.WaitGroup
		for _, i := range missing[lo:hi] {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				items[i].StockCheckedAt = time.Now().UTC()
				bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				st, err := a.GetStock(bounded, items[i].ID, "")
				if err != nil {
					if errors.Is(err, adapter.ErrRateLimited) {
						rateLimited.Store(true)
					}
					items[i].StockError = adapter.StockErrorSummary(err)
					s.log.Warn("supply.sync.stock_backfill_failed", "task_id", taskID, "code", items[i].ID, "reason", items[i].StockError)
					items[i].Stock = -2
					return // 未知庫存不可冒充無限或售罄
				}
				items[i].Stock = st
				if st < -1 {
					items[i].Stock = -2
					items[i].StockError = "上游未返回有效庫存"
				}
			}(i)
		}
		wg.Wait()
		if rateLimited.Load() {
			// Do not continue probing the rest of a catalog after a WAF/429.
			for _, i := range missing[hi:] {
				items[i].Stock = -2
				items[i].StockError = "貨源限流或網關攔截，待稍後補查"
				items[i].StockCheckedAt = time.Now().UTC()
			}
			return nil
		}
		if ci < chunks-1 {
			if err := sleepCtx(ctx, cfg.StockBatchDelay); err != nil {
				return err
			}
		}
	}
	return nil
}

// cacheCategoryMappings 構建 上游分類標識 → 本地分類 id 映射。
// 優先級：連接 settings.category_map（導入彈窗持久化的運營選擇，後續同步
// 自動沿用）→ mapping 行（upstream_category 手工映射）；無映射分類不自動建。
func (s *SyncService) cacheCategoryMappings(ctx context.Context, a adapter.Adapter, conn *ent.SupplyConnection, out map[string]uint64) error {
	cats, err := a.ListCategories(ctx)
	if err != nil {
		return err
	}
	_ = cats
	for k, v := range categoryMapFromSettings(conn.Settings) {
		out[k] = v
	}
	// 本地分類映射表：掃描本連接全部 mapping，收集 upstream_category → local_category_id
	ms, _, err := s.repo.ListMappings(ctx, conn.ID, 1, 100000)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.LocalCategoryID > 0 && m.UpstreamCategory != "" {
			if _, ok := out[m.UpstreamCategory]; !ok {
				out[m.UpstreamCategory] = m.LocalCategoryID
			}
		}
	}
	return nil
}

// categoryMapFromSettings 連接 settings.category_map（JSON {上游分類code: 本地分類id}）。
func categoryMapFromSettings(settings map[string]any) map[string]uint64 {
	out := map[string]uint64{}
	if settings == nil {
		return out
	}
	raw, ok := settings["category_map"].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range raw {
		if id := toInt64(v); id >= 0 && (fmt.Sprint(v) == "0" || id > 0) {
			out[k] = uint64(id)
		}
	}
	return out
}

// currentProductPrice 讀本地商品當前價（價格保護判據）。
func (s *SyncService) currentProductPrice(ctx context.Context, productID uint64) (int64, error) {
	if productID == 0 {
		return 0, fmt.Errorf("supply: 無本地商品")
	}
	client := s.repo.entClient(ctx)
	p, err := client.Product.Get(ctx, productID)
	if err != nil {
		return 0, err
	}
	return p.Price, nil
}

// ensureCoverDir 渠道封面目錄名（連接級緩存 + settings.cover_dir 持久化）：
// - settings.cover_dir 已有 → 沿用（重啓穩定）
// - 否則掃描 uploads/ 一級目錄：渠道名淨化後取首個空閒名（重名加 2/3……），
// 並寫入 settings.cover_dir（落庫失敗僅告警，下次重新解析）
func (s *SyncService) ensureCoverDir(ctx context.Context, conn *ent.SupplyConnection) string {
	s.coverMu.Lock()
	if dir, ok := s.coverDirs[conn.ID]; ok {
		s.coverMu.Unlock()
		return dir
	}
	s.coverMu.Unlock()

	dir := ""
	if conn.Settings != nil {
		if v, ok := conn.Settings["cover_dir"].(string); ok && v != "" {
			dir = v
		}
	}
	if dir == "" {
		dir = allocateCoverDir(listUploadSubDirs(), sanitizeSubDir(conn.Name))
		if dir != "" {
			if saved, err := s.saveCoverDirectory(ctx, conn.ID, dir); err != nil {
				s.log.Warn("supply.cover_dir_save_failed", "connection_id", conn.ID, "err", err)
			} else {
				dir = saved
			}
		}
	}
	s.coverMu.Lock()
	if s.coverDirs == nil {
		s.coverDirs = map[uint64]string{}
	}
	s.coverDirs[conn.ID] = dir
	s.coverMu.Unlock()
	return dir
}

// coverFor 解析封面並落本地：下載（fail-open）→ 與舊 cover 比對，本地舊文件
// 換圖/清空時刪除（防泄漏）。mapping 可為 nil（新建）。
func (s *SyncService) coverFor(ctx context.Context, mapping *ent.SupplyMapping, conn *ent.SupplyConnection, cover string) string {
	if mapping != nil {
		row, err := data.Client(ctx, s.repo.data).Product.Get(ctx, mapping.LocalProductID)
		if err == nil {
			if row.IsLocked || row.CoverProtected {
				return row.Cover
			}
		}
	}
	dir := s.ensureCoverDir(ctx, conn)
	newCover := s.downloadCover(ctx, conn.BaseURL, cover, dir)
	// Reference-aware media cleanup owns deletion after the write commits.
	return newCover
}

// readSyncAnchor 讀取 scope 增量錨點（優先專用列；回退 settings.sync_anchors
// ——S1 過渡期寫入的兼容；collect 再回退舊列 last_synced_at）。
func readSyncAnchor(conn *ent.SupplyConnection, scope string) time.Time {
	if t := scopeAnchor(conn, scope); !t.IsZero() {
		return t
	}
	anchors, _ := conn.Settings["sync_anchors"].(map[string]any)
	if v, ok := anchors[scope].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// writeSyncAnchor 寫 scope 增量錨點（專用列；collect 同時沿用 last_synced_at 舊列）。
func (s *SyncService) writeSyncAnchor(ctx context.Context, conn *ent.SupplyConnection, scope string) {
	_ = s.repo.TouchScopeAnchor(ctx, conn.ID, scope)
	if scope == ScopeCollect {
		_, _ = s.repo.entClient(ctx).SupplyConnection.UpdateOneID(conn.ID).
			SetLastSyncedAt(time.Now().UTC()).Save(ctx)
	}
}

// sleepCtx 可取消 sleep（節流等待期間響應任務取消/超時）。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// publishCompleted 發佈 sync.completed 事件（終態：done/failed/canceled）。
func (s *SyncService) publishCompleted(ctx context.Context, connectionID, taskID uint64, status string) {
	if s.outbox == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"task_id":       taskID,
		"connection_id": connectionID,
		"status":        status,
	})
	if err != nil {
		s.log.Warn("supply.sync.publish_payload_failed", "err", err)
		return
	}
	aggID := "sync:" + strconv.FormatUint(taskID, 10)
	if err := s.outbox.Write(ctx, "supply", events.SyncCompleted, aggID, aggID, payload); err != nil {
		s.log.Warn("supply.sync.publish_failed", "task_id", taskID, "err", err)
	}
}

// autoOnshelf 新建商品自動上架開關（settings.auto_onshelf，默認 true）。
func autoOnshelf(settings map[string]any) bool {
	v, ok := settings["auto_onshelf"]
	if !ok {
		return true
	}
	b, _ := v.(bool)
	return b
}

// ImportOne 單品導入（ D 交互式導入；與 collect 同步同一 upsert 出口）。
// 定價四模式（pricing.go ApplyPricingImport）：pending 不算價不上架（Price=-1
// 不覆蓋既有價，運營補價後手動上架）；導入價寫入基線（後續同步走價格保護）。
func (s *SyncService) ImportOne(ctx context.Context, conn *ent.SupplyConnection, p *adapter.Product, categoryMap map[string]uint64, mode string, markupPercent float64, markupAmount int64) (bool, error) {
	return s.importOne(ctx, conn, p, categoryMap, mode, markupPercent, markupAmount, nil)
}

type importCheckpoint struct {
	categoryID  *uint64
	before      func(context.Context) error
	after       func(context.Context, uint64, bool) error
	holdStock   bool
	cover       string
	description string
}

func (s *SyncService) importOne(ctx context.Context, conn *ent.SupplyConnection, p *adapter.Product, categoryMap map[string]uint64, mode string, markupPercent float64, markupAmount int64, checkpoint *importCheckpoint) (bool, error) {
	rule := productPricingRule{Mode: mode, Percent: markupPercent, Amount: markupAmount}
	importPrice := func(upstream int64) int64 { return rule.price(conn, upstream) }
	price := importPrice(p.Price)
	if p.ProductKind == "social_service" {
		price = 0
	}
	if p.ProductKind == "sms_channel" {
		if conn.Driver != "zcard" || conn.ExchangeRate != 1 || len(p.SKUs) > 0 {
			return false, fmt.Errorf("渠道接碼商品須使用人民幣 ZCard 連接且無 SKU")
		}
		price = 0
	}
	if p.ProductKind != "sms_channel" && p.ProductKind != "social_service" && p.IsActive && mode != PriceModePending && (p.Price <= 0 || price <= 0) {
		return false, fmt.Errorf("商品 %s 報價無效，未導入", p.ID)
	}
	status := int8(1)
	writePrice := price
	if mode == PriceModePending {
		status = 0 // 待定價：導入後不上架
		writePrice = -1
	}
	if !p.IsActive {
		status = 0 // 上游不可售品導入即下架
	}
	// 既有映射（保留鎖定或受保護的封面；新導入為 NotFound）
	mapping, merr := s.repo.GetMapping(ctx, conn.ID, p.ID, "")
	if merr != nil && merr != ErrNotFound {
		return false, merr
	}
	write := catalogport.UpstreamProductInput{
		ProductKind: p.ProductKind, DeliveryKind: p.DeliveryKind, SMSProduct: p.SMSProduct, SocialSchema: p.SocialSchema,
		ConnectionID:        conn.ID,
		UpstreamProductCode: p.ID,
		UpstreamSyncedAt:    time.Now().UTC(),
		Name:                p.Name,
		Description:         s.importDescription(ctx, mapping, conn, p.Description, checkpoint),
		DescriptionSet:      p.DescriptionSet,
		Cover:               s.importCover(ctx, mapping, conn, p.Cover, checkpoint), // 上游圖採集落本地（fail-open；保留舊文件，避免共享引用失效）
		FactoryPrice:        accountCost(conn, p),
		Status:              status,
		AutoOnshelf:         mode != PriceModePending,
		ReimportDeleted:     true,
		Price:               writePrice,
	}
	if localCat, ok := categoryMap[p.CategoryID]; ok {
		write.CategoryID = localCat
		write.CategorySet = true
	}
	if checkpoint != nil && checkpoint.categoryID != nil {
		write.CategoryID = *checkpoint.categoryID
		write.CategorySet = true
		write.CategoryProtected = true
	}
	if checkpoint != nil && mapping != nil && s.localProductShelvedOff(ctx, mapping.LocalProductID) {
		write.Status = 0
		write.AutoOnshelf = false
	}
	if checkpoint != nil && checkpoint.holdStock {
		write.Status = 0
		write.AutoOnshelf = false
	}
	// 手動導入也必須攜帶規格，上游下單使用 SKU ID。
	for _, sk := range p.SKUs {
		skuPrice := importPrice(sk.Price)
		if p.IsActive && mode != PriceModePending && (sk.Price <= 0 || skuPrice <= 0) {
			return false, fmt.Errorf("商品 %s 規格報價無效，未導入", p.ID)
		}
		if mode == PriceModePending {
			skuPrice = -1 // 待定價時保留已有規格價格，不寫零元規格。
		}
		write.SKUs = append(write.SKUs, catalogport.UpstreamSKUInput{
			Code: sk.Code, Name: sk.Name, SpecValues: sk.SpecValues,
			PriceCents: skuPrice,
		})
	}
	// 商品、規格、映射一起提交，失敗時保留原歸檔的同步保護。
	created := false
	err := data.Tx(ctx, s.repo.data, func(ctx context.Context) error {
		if err := s.checkPricingConnection(ctx, conn); err != nil {
			return err
		}
		if checkpoint != nil && checkpoint.before != nil {
			if err := checkpoint.before(ctx); err != nil {
				return err
			}
		}
		productID, wasCreated, err := s.writeProductCategory(ctx, p.CategoryID, &write)
		if err != nil {
			return err
		}
		created = wasCreated
		// 映射 upsert（價格基線：導入價；後續同步據此做運營改價保護）
		override := map[string]any{"rule": rule}
		skuPrices := map[string]any{}
		for _, sk := range write.SKUs {
			if sk.PriceCents >= 0 {
				skuPrices[sk.Code] = sk.PriceCents
			}
		}
		override["sku_prices"] = skuPrices
		if price > 0 {
			override["last_synced_price"] = price
		}
		mapping, err = s.repo.GetMapping(ctx, conn.ID, p.ID, "")
		if err != nil {
			if err != ErrNotFound {
				return err
			}
			mapping = &ent.SupplyMapping{ConnectionID: conn.ID, UpstreamProduct: p.ID}
		}
		mapping.LocalProductID = productID
		mapping.UpstreamCategory = p.CategoryID
		if write.CategorySet {
			mapping.LocalCategoryID = write.CategoryID
		}
		mapping.UpStock = p.Stock
		mapping.StockCheckedAt = stockObservationTime(p)
		mapping.PricingOverride = override
		if err := s.saveProductMapping(ctx, mapping); err != nil {
			return err
		}
		if checkpoint != nil && checkpoint.after != nil {
			return checkpoint.after(ctx, productID, created)
		}
		return nil
	})
	return created, err
}

// ── 失敗自動重試（ 補強：上游暫不可用 15→30→60s 遞進恢復）──
//
// 與請求級 AIMD（pacer）互補：pacer 管單請求間隔（防封 IP），這裏管任務整體的
// 暫時性失敗恢復——重試計數進程內（重啓丟失可接受：定時調度週期兜底重跑）。
// 可重試：上游拉取/單品失敗/心跳等暫時性錯誤；配置類（憑據/參數/限流冷卻/
// 對賬護欄/取消）不重試。

var syncRetryIntervals = []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second}

var syncRetryTracker = struct {
	sync.Mutex
	m map[uint64]int
}{m: map[uint64]int{}}

// retryableSyncCode 可重試錯誤碼判定。
func retryableSyncCode(code string) bool {
	switch code {
	case "LIST_PRODUCTS_FAILED", "SYNC_ITEM_FAILED", "HEARTBEAT_FAILED", "PRICE_QUOTE_FAILED":
		return true
	}
	return false
}

// failAndMaybeRetry 任務失敗落終態；可重試錯誤安排自動重試（error_context 追加
// 可見提示），重試耗盡或不可重試則保持 failed。
func (s *SyncService) failAndMaybeRetry(ctx context.Context, taskID uint64, connectionID uint64, code, errContext string) {
	syncRetryTracker.Lock()
	n := syncRetryTracker.m[taskID]
	if !retryableSyncCode(code) || n >= len(syncRetryIntervals) {
		if retryableSyncCode(code) {
			delete(syncRetryTracker.m, taskID) // 耗盡清理
		}
		syncRetryTracker.Unlock()
		_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, code, errContext)
		return
	}
	delay := syncRetryIntervals[n]
	syncRetryTracker.m[taskID] = n + 1
	syncRetryTracker.Unlock()

	hint := fmt.Sprintf("（%s 後自動重試 %d/%d）", delay, n+1, len(syncRetryIntervals))
	_ = s.repo.FinishTask(ctx, taskID, supplysynctask.StatusFailed, code, errContext+hint)
	s.log.Info("supply.sync.retry_scheduled", "task_id", taskID, "attempt", n+1, "delay", delay.String())

	go func() {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return // 宿主 ctx 結束（進程關停）
		}
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		// 重跑前檢查取消意圖（等待期被用戶取消則不再復活）
		if t, err := s.repo.GetSyncTask(runCtx, taskID); err != nil || !t.CancelRequestedAt.IsZero() {
			return
		}
		if err := s.repo.ResetTaskPending(runCtx, taskID); err != nil {
			return
		}
		if err := s.RunSync(runCtx, taskID); err != nil {
			s.log.Warn("supply.sync.retry_run_failed", "task_id", taskID, "err", err)
		}
	}()
}

// clearSyncRetry 任務成功終態清理重試計數。
func clearSyncRetry(taskID uint64) {
	syncRetryTracker.Lock()
	delete(syncRetryTracker.m, taskID)
	syncRetryTracker.Unlock()
}

func (s *SyncService) importCover(ctx context.Context, m *ent.SupplyMapping, conn *ent.SupplyConnection, url string, cp *importCheckpoint) string {
	if cp != nil {
		return cp.cover
	}
	return s.coverFor(ctx, m, conn, url)
}

func (s *SyncService) importDescription(ctx context.Context, m *ent.SupplyMapping, conn *ent.SupplyConnection, description string, cp *importCheckpoint) string {
	if cp != nil {
		return cp.description
	}
	mediaCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.descriptionFor(mediaCtx, m, conn, description)
}
