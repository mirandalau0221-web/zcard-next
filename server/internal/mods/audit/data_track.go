package audit

// T5 訪問統計明細：page_views（PV/UV 逐請求明細）+ user_sessions（在線心跳）。
// 埋點原則：統計性質數據，寫入失敗一律忽略、絕不阻斷業務請求。

import (
	"context"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/pageview"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/usersession"
	auditport "github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/businessday"
)

// TrackRepo 訪問埋點/統計倉儲。
type TrackRepo struct {
	data *data.Data
}

// NewTrackRepo 構造。
func NewTrackRepo(d *data.Data) *TrackRepo {
	return &TrackRepo{data: d}
}

// RecordVisit 記一次訪問：PV 明細一行 + 登錄用户在線心跳 upsert。
// 失敗忽略（埋點不阻斷請求）。
func (r *TrackRepo) RecordVisit(ctx context.Context, subsite uint64, path string, userID uint64, ip string) {
	now := time.Now().UTC()
	client := data.Client(ctx, r.data)
	_, err := client.PageView.Create().
		SetSubsiteID(subsite).
		SetDay(businessday.Date(now, "20060102")).
		SetPath(path).
		SetUserID(userID).
		SetIP(ip).
		Save(ctx)
	if err != nil || userID == 0 {
		return
	}
	// 在線心跳 upsert（唯一鍵 subsite_id + user_id；created_at 不可變保留）
	_ = client.UserSession.Create().
		SetSubsiteID(subsite).
		SetUserID(userID).
		SetIP(ip).
		SetLastActiveAt(now).
		OnConflictColumns(usersession.FieldSubsiteID, usersession.FieldUserID).
		UpdateNewValues().
		Exec(ctx)
}

// CountOnlineUsers 在線用户數（last_active_at ≥ since 且分站隔離）。
func (r *TrackRepo) CountOnlineUsers(ctx context.Context, subsite uint64, since time.Time) (int64, error) {
	n, err := data.Client(ctx, r.data).UserSession.Query().
		Where(usersession.LastActiveAtGTE(since), usersession.SubsiteID(subsite)).
		Count(ctx)
	return int64(n), err
}

// TrafficByDay 近 N 天 PV/UV（PV=行數，UV=按 ip 去重；缺日補零由調用方補齊）。
func (r *TrackRepo) TrafficByDay(ctx context.Context, subsite uint64, days int) ([]auditport.TrafficDay, error) {
	if days < 1 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	// Use absolute request timestamps to repair old UTC day labels without rewriting history.
	start := businessday.Start(businessday.Now()).In(businessday.Location).AddDate(0, 0, -(days - 1))
	client := data.Client(ctx, r.data)
	out := make([]auditport.TrafficDay, 0, days)
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		end := day.AddDate(0, 0, 1)
		var counts []struct {
			PV int64 `json:"pv"`
			UV int64 `json:"uv"`
		}
		err := client.PageView.Query().Where(pageview.SubsiteID(subsite),
			pageview.DayGTE(day.AddDate(0, 0, -1).Format("20060102")), pageview.DayLTE(day.Format("20060102")),
			pageview.CreatedAtGTE(day.UTC()), pageview.CreatedAtLT(end.UTC())).Aggregate(
			func(s *sql.Selector) string { return sql.As(sql.Count("*"), "pv") },
			func(s *sql.Selector) string { return sql.As(sql.Count(sql.Distinct(s.C(pageview.FieldIP))), "uv") },
		).Scan(ctx, &counts)
		if err != nil {
			return nil, err
		}
		if len(counts) > 0 {
			out = append(out, auditport.TrafficDay{Date: day.Format("20060102"), PV: counts[0].PV, UV: counts[0].UV})
		}
	}

	return out, nil
}

// CleanupVisitData 清理過期數據（cron）：明細保留 90 天，心跳保留 24 小時。
func (r *TrackRepo) CleanupVisitData(ctx context.Context) error {
	client := data.Client(ctx, r.data)
	dayCut := time.Now().UTC().AddDate(0, 0, -90).Format("20060102")
	if _, err := client.PageView.Delete().Where(pageview.DayLT(dayCut)).Exec(ctx); err != nil {
		return err
	}
	activeCut := time.Now().UTC().Add(-24 * time.Hour)
	_, err := client.UserSession.Delete().Where(usersession.LastActiveAtLT(activeCut)).Exec(ctx)
	return err
}
