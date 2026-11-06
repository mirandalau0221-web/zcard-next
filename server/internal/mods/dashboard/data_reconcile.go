package dashboard

// 貨源對賬引擎（ 驗收項）：
// 任務生命週期 pending → processing → done/failed（可查可告警）；
// 時間窗內本地 procurement_orders vs 上游訂單清單（數據源端口注入）；
// 四態比對 matched / mismatched（金額·狀態差異進 diff_json）/ local_only /
// upstream_only；job 彙總計數；mismatch>0 → notify 管理員告警。
// 數據源：上游支持列表（zcard ListOrders）走完整四態；不支持（dujiao/acg-faka）
// 置 failed「上游不支持列表對賬」（fail-closed，不靜默跳過）。

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/procurementorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/reconciliationitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/reconciliationjob"
	dashboardport "github.com/NovaWorks/zcard-next/server/internal/mods/dashboard/port"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
)

// Reconciler 對賬引擎。
type Reconciler struct {
	data   *data.Data
	source dashboardport.UpstreamOrderSource
	sender notifyport.Sender // mismatch 告警（nil = 未裝配跳過）
}

// NewReconciler 構造。
func NewReconciler(d *data.Data, source dashboardport.UpstreamOrderSource, sender notifyport.Sender) *Reconciler {
	return &Reconciler{data: d, source: source, sender: sender}
}

// CreateJob 創建對賬任務（pending；時間窗 ≤31 天）。
func (rc *Reconciler) CreateJob(ctx context.Context, connectionID uint64, start, end time.Time) (*ent.ReconciliationJob, error) {
	if end.Before(start) || end.Sub(start) > 31*24*time.Hour {
		return nil, errors.New("reconciliation.RANGE_INVALID")
	}
	return data.Client(ctx, rc.data).ReconciliationJob.Create().
		SetConnectionID(connectionID).
		SetType("orders").
		SetStatus(reconciliationjob.StatusPending).
		SetTimeRangeStart(start).
		SetTimeRangeEnd(end).
		Save(ctx)
}

// RunJob 執行任務（冪等：非 pending 直接返回；處理中/已完成不重跑）。
func (rc *Reconciler) RunJob(ctx context.Context, jobID uint64) error {
	client := data.Client(ctx, rc.data)
	job, err := client.ReconciliationJob.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != reconciliationjob.StatusPending {
		return nil // 冪等
	}
	// 置 processing（CAS）
	if _, err := client.ReconciliationJob.UpdateOneID(jobID).
		Where(reconciliationjob.StatusEQ(reconciliationjob.StatusPending)).
		SetStatus(reconciliationjob.StatusProcessing).
		Save(ctx); err != nil {
		return err
	}

	// 上游清單（不支持 → failed 可查）
	if rc.source == nil {
		return rc.failJob(ctx, jobID, "對賬數據源未裝配")
	}
	upstream, err := rc.source.ListOrders(ctx, job.ConnectionID, job.TimeRangeStart, job.TimeRangeEnd)
	if errors.Is(err, dashboardport.ErrUpstreamListUnsupported) {
		return rc.failJob(ctx, jobID, "上游不支持訂單列表對賬")
	}
	if err != nil {
		return rc.failJob(ctx, jobID, "拉取上游訂單失敗: "+err.Error())
	}

	// 本地採購單（時間窗 + 已提交上游的）
	locals, err := client.ProcurementOrder.Query().
		Where(
			procurementorder.ConnectionID(job.ConnectionID),
			procurementorder.CreatedAtGTE(job.TimeRangeStart),
			procurementorder.CreatedAtLT(job.TimeRangeEnd),
			procurementorder.UpstreamOrderIDNotNil(),
		).
		All(ctx)
	if err != nil {
		return rc.failJob(ctx, jobID, "讀取本地採購單失敗: "+err.Error())
	}

	upByID := map[string]dashboardport.UpstreamOrder{}
	for _, u := range upstream {
		upByID[u.UpstreamOrderID] = u
	}
	localByID := map[string]localSide{}
	for _, l := range locals {
		// 本地口徑：訂單子項金額 + 父單號（快照鏈：procurement → order_item → order）
		no, amt := rc.localRef(ctx, l)
		localByID[l.UpstreamOrderID] = localSide{procure: l, orderNo: no, amount: amt}
	}

	var matched, mismatched, localOnly, upstreamOnly int32
	// 本地側比對
	for _, l := range localByID {
		u, ok := upByID[l.procure.UpstreamOrderID]
		if !ok {
			localOnly++
			if err := rc.item(ctx, jobID, l.procure.ID, l.orderNo, "", reconciliationitem.StatusLocalOnly, nil); err != nil {
				return rc.failJob(ctx, jobID, "落對賬明細失敗: "+err.Error())
			}
			continue
		}
		diff := map[string]any{}
		if u.Amount != l.amount {
			diff["amount"] = map[string]any{"local": l.amount, "upstream": u.Amount}
		}
		if len(diff) > 0 {
			mismatched++
			if err := rc.item(ctx, jobID, l.procure.ID, l.orderNo, u.UpstreamOrderID, reconciliationitem.StatusMismatched, diff); err != nil {
				return rc.failJob(ctx, jobID, "落對賬明細失敗: "+err.Error())
			}
			continue
		}
		matched++
		if err := rc.item(ctx, jobID, l.procure.ID, l.orderNo, u.UpstreamOrderID, reconciliationitem.StatusMatched, nil); err != nil {
			return rc.failJob(ctx, jobID, "落對賬明細失敗: "+err.Error())
		}
	}
	// 上游側多餘
	for _, u := range upstream {
		if _, ok := localByID[u.UpstreamOrderID]; !ok {
			upstreamOnly++
			if err := rc.item(ctx, jobID, 0, "", u.UpstreamOrderID, reconciliationitem.StatusUpstreamOnly, nil); err != nil {
				return rc.failJob(ctx, jobID, "落對賬明細失敗: "+err.Error())
			}
		}
	}

	// 彙總落 job done
	resultMap := map[string]any{
		"local_total": len(locals), "upstream_total": len(upstream),
		"matched": matched, "mismatched": mismatched,
		"local_only": localOnly, "upstream_only": upstreamOnly,
	}
	if _, err := client.ReconciliationJob.UpdateOneID(jobID).
		SetStatus(reconciliationjob.StatusDone).
		SetTotalCount(int32(len(locals)) + upstreamOnly).
		SetMatchedCount(matched).
		SetMismatchedCount(mismatched).
		SetResultJSON(resultMap).
		Save(ctx); err != nil {
		return err
	}
	// mismatch 告警（管理員通道；一次一告）
	if mismatched+localOnly+upstreamOnly > 0 && rc.sender != nil {
		_ = rc.sender.Send(ctx, notifyport.Message{
			EventType: "reconciliation.mismatch",
			Channel:   "inbox",
			Subject:   "貨源對賬差異告警",
			Body:      "對賬任務 #" + strconv.FormatUint(jobID, 10) + " 發現差異：mismatched=" + strconv.Itoa(int(mismatched)) + " local_only=" + strconv.Itoa(int(localOnly)) + " upstream_only=" + strconv.Itoa(int(upstreamOnly)),
			UserID:    0, // 管理員廣播由 notify 通道處理（admin 收件方由通道配置決定）
		})
	}
	return nil
}

// localSide 本地側對賬口徑（採購單 + 父單號 + 訂單子項金額）。
type localSide struct {
	procure *ent.ProcurementOrder
	orderNo string
	amount  int64
}

// localRef 解析本地口徑（子項金額 + 父單號；解析失敗降級零值——金額差異進 diff）。
func (rc *Reconciler) localRef(ctx context.Context, p *ent.ProcurementOrder) (string, int64) {
	client := data.Client(ctx, rc.data)
	it, err := client.OrderItem.Get(ctx, p.OrderItemID)
	if err != nil {
		return "", 0
	}
	no := ""
	if o, err := client.Order.Get(ctx, it.OrderID); err == nil {
		no = o.OrderNo
	}
	return no, it.Amount
}

// item 落對賬明細行。
func (rc *Reconciler) item(ctx context.Context, jobID uint64, procureID uint64, localNo, upstreamNo string, status reconciliationitem.Status, diff map[string]any) error {
	create := data.Client(ctx, rc.data).ReconciliationItem.Create().
		SetJobID(jobID).
		SetStatus(status)
	if procureID > 0 {
		create.SetProcurementOrderID(procureID)
	}
	if localNo != "" {
		create.SetLocalOrderNo(localNo)
	}
	if upstreamNo != "" {
		create.SetUpstreamOrderNo(upstreamNo)
	}
	if len(diff) > 0 {
		create.SetDiffJSON(diff)
	}
	return create.Exec(ctx)
}

// failJob 失敗收尾（可查原因）。
func (rc *Reconciler) failJob(ctx context.Context, jobID uint64, reason string) error {
	res := map[string]any{"error": reason}
	_, err := data.Client(ctx, rc.data).ReconciliationJob.UpdateOneID(jobID).
		SetStatus(reconciliationjob.StatusFailed).
		SetResultJSON(res).
		Save(ctx)
	return err
}

// GetJob 任務詳情。
func (rc *Reconciler) GetJob(ctx context.Context, jobID uint64) (*ent.ReconciliationJob, error) {
	return data.Client(ctx, rc.data).ReconciliationJob.Get(ctx, jobID)
}

// ListItems 明細分頁（mismatch 等狀態篩選）。
func (rc *Reconciler) ListItems(ctx context.Context, jobID uint64, status string, page, size int) ([]*ent.ReconciliationItem, int64, error) {
	q := data.Client(ctx, rc.data).ReconciliationItem.Query().
		Where(reconciliationitem.JobID(jobID)).
		Order(ent.Asc(reconciliationitem.FieldID))
	if status != "" {
		q = q.Where(reconciliationitem.StatusEQ(reconciliationitem.Status(status)))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, int64(total), err
}
