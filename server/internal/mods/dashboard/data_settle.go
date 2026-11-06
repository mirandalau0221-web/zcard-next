package dashboard

// 日結任務：daily_stats 落表；歷史查詢按當前可見訂單重新核算。
// 冪等：唯一索引 (subsite_id, stat_date, metric, dimension_key) 重跑覆蓋。
// 調度：每小時檢查 + 當日標記（按北京時間跑昨日聚合，錯過零點仍可補跑）。

import (
	"context"
	"fmt"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/dailystat"
	"github.com/NovaWorks/zcard-next/server/internal/platform/businessday"
)

// settleMetrics 日結指標集（dimension_key 空=總量行）。
var settleMetrics = []string{"orders", "amount", "paid_orders"}

// RunDailySettle 聚合指定日各租户指標並落 daily_stats（重跑覆蓋同維度行）。
func (r *DashboardRepoImpl) RunDailySettle(ctx context.Context, day time.Time) error {
	start := businessday.Start(day)
	end := start.AddDate(0, 0, 1)
	date := businessday.Date(start, "20060102")
	client := data.Client(ctx, r.data)

	// 全部租户（主站 0 + 訂單表中出現過的分站）
	subsites := []uint64{0}
	rows, err := client.Order.Query().Select("subsite_id").All(ctx)
	if err != nil {
		return err
	}
	seen := map[uint64]bool{0: true}
	for _, o := range rows {
		if !seen[o.SubsiteID] {
			seen[o.SubsiteID] = true
			subsites = append(subsites, o.SubsiteID)
		}
	}

	for _, subsite := range subsites {
		m, err := r.metricBetweenSubsite(ctx, subsite, start, end)
		if err != nil {
			return err
		}
		values := map[string]int64{
			"orders": m.Orders, "amount": m.Revenue, "paid_orders": m.PaidOrders,
		}
		for _, metric := range settleMetrics {
			if err := r.upsertDailyStat(ctx, subsite, date, metric, values[metric]); err != nil {
				return err
			}
		}
	}
	return nil
}

// upsertDailyStat 使用唯一索引原子覆蓋，允許日結與歷史查詢併發刷新。
func (r *DashboardRepoImpl) upsertDailyStat(ctx context.Context, subsite uint64, date, metric string, value int64) error {
	return data.Client(ctx, r.data).DailyStat.Create().
		SetSubsiteID(subsite).SetStatDate(date).SetMetric(metric).SetDimensionKey("").SetValue(value).
		OnConflictColumns(dailystat.FieldSubsiteID, dailystat.FieldStatDate, dailystat.FieldMetric, dailystat.FieldDimensionKey).
		Update(func(u *ent.DailyStatUpsert) { u.SetValue(value).UpdateUpdatedAt() }).Exec(ctx)
}

// DailyStatPoint 日結查詢點。
type DailyStatPoint struct {
	Date   string
	Orders int64
	Amount int64
	Paid   int64
}

// GetDailyStats 歷史日結查詢，重算請求範圍並修正過期快照。
func (r *DashboardRepoImpl) GetDailyStats(ctx context.Context, subsiteID uint64, startDate, endDate string) ([]DailyStatPoint, error) {
	// Rebuild requested business dates from source facts, so deleting old invalid
	// orders or receiving late refunds cannot leave cached daily_stats stale.
	start, err := time.ParseInLocation("20060102", startDate, businessday.Location)
	if err != nil {
		return nil, err
	}
	end, err := time.ParseInLocation("20060102", endDate, businessday.Location)
	if err != nil {
		return nil, err
	}
	end = end.AddDate(0, 0, 1)
	if !end.After(start) || end.Sub(start) > 366*24*time.Hour {
		return nil, fmt.Errorf("日期範圍須在 1 至 366 天內")
	}
	l, err := r.loadLedger(ctx, subsiteID, start, end)
	if err != nil {
		return nil, err
	}
	out := []DailyStatPoint{}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		m := l.between(day, day.AddDate(0, 0, 1))
		date := businessday.Date(day, "20060102")
		for metric, value := range map[string]int64{"orders": m.Orders, "amount": m.Revenue, "paid_orders": m.PaidOrders} {
			if err := r.upsertDailyStat(ctx, subsiteID, date, metric, value); err != nil {
				return nil, err
			}
		}
		out = append(out, DailyStatPoint{Date: date, Orders: m.Orders, Amount: m.Revenue, Paid: m.PaidOrders})
	}
	return out, nil
}

// DailySettleCron 日結調度（每小時檢查；按北京時間跑昨日，當日標記防重跑）。
func (r *DashboardRepoImpl) DailySettleCron() func(context.Context) {
	lastRun := ""
	return func(ctx context.Context) {
		now := r.now().In(businessday.Location)
		today := now.Format("20060102")
		if lastRun == today {
			return
		}
		yesterday := now.AddDate(0, 0, -1)
		if err := r.RunDailySettle(ctx, yesterday); err != nil {
			return // 失敗下輪重試（lastRun 不更新）
		}
		lastRun = today
	}
}
