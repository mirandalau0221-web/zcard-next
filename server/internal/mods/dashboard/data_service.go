package dashboard

// AdminDashboardService 工作台服務（ v1）。

import (
	"context"
	"encoding/json"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	affiliateport "github.com/NovaWorks/zcard-next/server/internal/mods/affiliate/port"
	auditport "github.com/NovaWorks/zcard-next/server/internal/mods/audit/port"
	dashboardport "github.com/NovaWorks/zcard-next/server/internal/mods/dashboard/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/businessday"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
)

// AdminDashboardService 服務。
type AdminDashboardService struct {
	adminv1.UnimplementedAdminDashboardServiceServer
	repo       *DashboardRepoImpl
	reconciler *Reconciler                    // 貨源對賬（job/item 四態）
	commission affiliateport.CommissionReader // 佣金列表（通道 A）
	settings   dashboardport.SettingsReader   // 低庫存閾值等（通道 A；nil = 默認值）
	traffic    auditport.TrafficReader        // 訪問統計（在線用户/PV/UV，通道 A）
}

// NewAdminDashboardService 構造。
func NewAdminDashboardService(repo *DashboardRepoImpl, reconciler *Reconciler, commission affiliateport.CommissionReader, settings dashboardport.SettingsReader, traffic auditport.TrafficReader) *AdminDashboardService {
	return &AdminDashboardService{repo: repo, reconciler: reconciler, commission: commission, settings: settings, traffic: traffic}
}

// ListCommissions 佣金列表（；port 消費——跨模塊零 ent 依賴）。
func (s *AdminDashboardService) ListCommissions(ctx context.Context, req *adminv1.ListCommissionsRequest) (*adminv1.ListCommissionsReply, error) {
	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	size := int(req.GetPageSize())
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	rows, total, err := s.commission.ListCommissions(ctx, req.GetStatus(), page, size)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", err.Error())
	}
	reply := &adminv1.ListCommissionsReply{Total: total, Page: int32(page), PageSize: int32(size)}
	for _, c := range rows {
		reply.Commissions = append(reply.Commissions, &adminv1.AdminCommission{
			Id: c.ID, OrderId: c.OrderID, BuyerId: c.BuyerID, ReferrerId: c.ReferrerID,
			Tier: c.Tier, Rate: c.Rate, BaseAmount: c.BaseAmount, Amount: c.Amount,
			Status: c.Status, AvailableAt: c.AvailableAt, CreatedAt: c.CreatedAt,
		})
	}
	return reply, nil
}

// GetDashboard 工作台指標（6 統計窗口 + 趨勢 + 商品/渠道排行 + 待辦）。
func (s *AdminDashboardService) GetDashboard(ctx context.Context, req *adminv1.GetDashboardRequest) (*adminv1.DashboardReply, error) {
	now := s.repo.now().UTC().Truncate(time.Second)
	repo := *s.repo
	repo.now = func() time.Time { return now }
	today, yesterday, last7d, prev7d, last30d, prev30d, err := repo.GetOverview(ctx)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "統計失敗: "+err.Error())
	}
	trendDays := int(req.GetTrendDays())
	trend, err := repo.GetTrend(ctx, trendDays)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "趨勢失敗: "+err.Error())
	}
	top, err := repo.GetTopProducts(ctx, trendDays)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "排行失敗: "+err.Error())
	}
	channels, err := repo.GetTopChannels(ctx, trendDays)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "渠道統計失敗: "+err.Error())
	}
	withdrawals, refunds, fulfilling, err := repo.GetPending(ctx)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "待辦統計失敗: "+err.Error())
	}
	alertsEnabled := true
	if s.settings != nil {
		raw, e := s.settings.GetJSON(ctx, "supply", "low_stock_alert_enabled")
		if e == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &alertsEnabled)
		}
	}
	var lowStock int64
	if alertsEnabled {
		lowStock, err = repo.GetLowStockCount(ctx, s.lowStockThreshold(ctx))
	}
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "庫存統計失敗: "+err.Error())
	}
	openTickets, processingTickets, urgentTickets, err := repo.GetTicketPending(ctx)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "工單統計失敗")
	}
	pendingSupplierApps, err := repo.GetPendingSupplierApplications(ctx)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "對接申請統計失敗")
	}
	onlineUsers := s.onlineUsers(ctx)

	reviews, err := repo.PaymentReviewCount(ctx)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "到賬核對統計失敗")
	}
	start, end := reportWindow(now, trendDays)
	reply := &adminv1.DashboardReply{
		RangeStart: start.Unix(), RangeEnd: end.Unix(), GeneratedAt: now.Unix(), SubsiteId: tenancy.FromContext(ctx).SubsiteID,
		Today:       toStatPB(today),
		Yesterday:   toStatPB(yesterday),
		Last7D:      toStatPB(last7d),
		Prev7D:      toStatPB(prev7d),
		Last30D:     toStatPB(last30d),
		Prev30D:     toStatPB(prev30d),
		OnlineUsers: onlineUsers,
		Pending: &adminv1.DashboardPending{
			PaymentReviews:              reviews,
			OpenTickets:                 openTickets,
			ProcessingTickets:           processingTickets,
			UrgentTickets:               urgentTickets,
			PendingWithdrawals:          withdrawals,
			PendingRefunds:              refunds,
			FulfillingOrders:            fulfilling,
			LowStockProducts:            lowStock,
			PendingSupplierApplications: pendingSupplierApps,
		},
	}
	for _, tp := range trend {
		reply.Trend = append(reply.Trend, &adminv1.DashboardTrendPoint{
			Date: tp.Date, Orders: tp.Orders, Revenue: tp.Revenue,
			PaidCount: tp.PaidCount, Cost: tp.Cost, Profit: tp.Profit, Refunds: tp.Refunds, NetRevenue: tp.NetRevenue,
		})
	}
	for _, p := range top {
		reply.TopProducts = append(reply.TopProducts, &adminv1.DashboardTopProduct{
			ProductId: p.ProductID, Name: p.Name, SoldQty: p.SoldQty, Revenue: p.Revenue,
		})
	}
	for _, c := range channels {
		reply.TopChannels = append(reply.TopChannels, &adminv1.DashboardTopChannel{
			Channel: c.Channel, TotalCount: c.TotalCount, ChannelId: c.ChannelID, Name: c.Name, ChannelState: c.ChannelState, Amount: c.Amount,
			SuccessCount: c.SuccessCount, FailedCount: c.FailedCount,
		})
	}
	return reply, nil
}

// onlineUsers 在線用户數（5 分鐘活躍窗口；失敗回落 0 不阻斷主統計）。
func (s *AdminDashboardService) onlineUsers(ctx context.Context) int64 {
	if s.traffic == nil {
		return 0
	}
	n, err := s.traffic.CountOnlineUsers(ctx, tenancy.FromContext(ctx).SubsiteID, time.Now().UTC().Add(-5*time.Minute))
	if err != nil {
		return 0
	}
	return n
}

// GetTraffic 流量統計（PV/UV 按天；缺日補 0，與趨勢圖同口徑連續日期）。
func (s *AdminDashboardService) GetTraffic(ctx context.Context, req *adminv1.GetTrafficRequest) (*adminv1.GetTrafficReply, error) {
	days := int(req.GetDays())
	if days != 1 && days != 14 && days != 30 {
		days = 7
	}
	if s.traffic == nil {
		return nil, errors.InternalServer("dashboard.TRAFFIC_UNBOUND", "訪問統計未裝配")
	}
	rows, err := s.traffic.TrafficByDay(ctx, tenancy.FromContext(ctx).SubsiteID, days)
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", "流量統計失敗: "+err.Error())
	}
	byDay := make(map[string]*adminv1.TrafficPoint, len(rows))
	for _, r := range rows {
		byDay[r.Date] = &adminv1.TrafficPoint{Date: r.Date, Pv: r.PV, Uv: r.UV}
	}
	reply := &adminv1.GetTrafficReply{}
	start := businessday.Now().AddDate(0, 0, -(days - 1))
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		d := day.Format("2006-01-02")
		// page_views.day uses YYYYMMDD; the chart API returns YYYY-MM-DD.
		if p, ok := byDay[day.Format("20060102")]; ok {
			p.Date = d
			reply.Points = append(reply.Points, p)
		} else {
			reply.Points = append(reply.Points, &adminv1.TrafficPoint{Date: d})
		}
	}
	return reply, nil
}

func toStatPB(m Metric) *adminv1.DashboardStat {
	return &adminv1.DashboardStat{
		Orders: m.Orders, Revenue: m.Revenue, PaidOrders: m.PaidOrders,
		Cost: m.Cost, Profit: m.Profit, NewUsers: m.NewUsers, Refunds: m.Refunds, NetRevenue: m.NetRevenue, UnknownCostOrders: m.UnknownCostOrders,
	}
}

// lowStockThreshold 庫存預警閾值（settings.supply.low_stock_threshold；默認 5）。
func (s *AdminDashboardService) lowStockThreshold(ctx context.Context) int {
	if s.settings == nil {
		return 5
	}
	raw, err := s.settings.GetJSON(ctx, "supply", "low_stock_threshold")
	if err != nil || len(raw) == 0 {
		return 5
	}
	var v int
	if json.Unmarshal(raw, &v) != nil || v < 1 {
		return 5
	}
	return v
}

// GetDailyStats 歷史日結（分站視角自動隔離：subsite 取自 tenancy 上下文）。
func (s *AdminDashboardService) GetDailyStats(ctx context.Context, req *adminv1.GetDailyStatsRequest) (*adminv1.GetDailyStatsReply, error) {
	subsite := tenancy.FromContext(ctx).SubsiteID
	start, end := req.GetStartDate(), req.GetEndDate()
	if start == "" || end == "" {
		now := businessday.Now()
		start = now.AddDate(0, 0, -6).Format("20060102")
		end = now.Format("20060102")
	}
	points, err := s.repo.GetDailyStats(ctx, subsite, start, end)
	if err != nil {
		return nil, errors.InternalServer("dashboard.DAILY_FAILED", "讀取日結失敗")
	}
	reply := &adminv1.GetDailyStatsReply{}
	for _, p := range points {
		reply.Points = append(reply.Points, &adminv1.DailyStatPoint{
			Date: p.Date, Orders: p.Orders, AmountCents: p.Amount, PaidOrders: p.Paid,
		})
	}
	return reply, nil
}

// CreateReconciliationJob 創建對賬任務。
func (s *AdminDashboardService) CreateReconciliationJob(ctx context.Context, req *adminv1.CreateReconciliationJobRequest) (*adminv1.ReconciliationJobItem, error) {
	if s.reconciler == nil {
		return nil, errors.InternalServer("dashboard.RECONCILE_UNBOUND", "對賬引擎未裝配")
	}
	job, err := s.reconciler.CreateJob(ctx, req.GetConnectionId(),
		time.Unix(req.GetStart(), 0).UTC(), time.Unix(req.GetEnd(), 0).UTC())
	if err != nil {
		return nil, errors.BadRequest("dashboard.RANGE_INVALID", "時間窗非法（或超過 31 天）")
	}
	return toJobPB(job), nil
}

// GetReconciliationJob 任務詳情。
func (s *AdminDashboardService) GetReconciliationJob(ctx context.Context, req *adminv1.GetReconciliationJobRequest) (*adminv1.ReconciliationJobItem, error) {
	job, err := s.reconciler.GetJob(ctx, req.GetId())
	if err != nil {
		return nil, errors.NotFound("dashboard.JOB_NOT_FOUND", "對賬任務不存在")
	}
	return toJobPB(job), nil
}

// RunReconciliationJob 執行任務（冪等：非 pending 直接返回當前狀態）。
func (s *AdminDashboardService) RunReconciliationJob(ctx context.Context, req *adminv1.RunReconciliationJobRequest) (*adminv1.ReconciliationJobItem, error) {
	if err := s.reconciler.RunJob(ctx, req.GetId()); err != nil {
		return nil, errors.InternalServer("dashboard.RECONCILE_FAILED", "對賬執行失敗: "+err.Error())
	}
	job, err := s.reconciler.GetJob(ctx, req.GetId())
	if err != nil {
		return nil, errors.NotFound("dashboard.JOB_NOT_FOUND", "對賬任務不存在")
	}
	return toJobPB(job), nil
}

// ListReconciliationItems 明細分頁。
func (s *AdminDashboardService) ListReconciliationItems(ctx context.Context, req *adminv1.ListReconciliationItemsRequest) (*adminv1.ListReconciliationItemsReply, error) {
	page, size := int(req.GetPage()), int(req.GetPageSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 50
	}
	rows, total, err := s.reconciler.ListItems(ctx, req.GetId(), req.GetStatus(), page, size)
	if err != nil {
		return nil, errors.InternalServer("dashboard.ITEMS_FAILED", "讀取明細失敗")
	}
	reply := &adminv1.ListReconciliationItemsReply{Total: total}
	for _, it := range rows {
		item := &adminv1.ReconciliationItemDetail{
			Id: it.ID, Status: string(it.Status),
			ProcurementOrderId: it.ProcurementOrderID,
			LocalOrderNo:       it.LocalOrderNo, UpstreamOrderNo: it.UpstreamOrderNo,
			CreatedAt: it.CreatedAt.Unix(),
		}
		if len(it.DiffJSON) > 0 {
			if raw, err := json.Marshal(it.DiffJSON); err == nil {
				item.DiffJson = string(raw)
			}
		}
		reply.Items = append(reply.Items, item)
	}
	return reply, nil
}

func toJobPB(job *ent.ReconciliationJob) *adminv1.ReconciliationJobItem {
	out := &adminv1.ReconciliationJobItem{
		Id: job.ID, ConnectionId: job.ConnectionID, Status: string(job.Status),
		TimeRangeStart: job.TimeRangeStart.Unix(), TimeRangeEnd: job.TimeRangeEnd.Unix(),
		TotalCount: job.TotalCount, MatchedCount: job.MatchedCount, MismatchedCount: job.MismatchedCount,
	}
	if !job.CreatedAt.IsZero() {
		out.CreatedAt = job.CreatedAt.Unix()
	}
	if len(job.ResultJSON) > 0 {
		if raw, err := json.Marshal(job.ResultJSON); err == nil {
			out.ResultJson = string(raw)
		}
	}
	return out
}

// GetReconciliation 對賬總覽（）。
func (s *AdminDashboardService) GetReconciliation(ctx context.Context, req *adminv1.GetReconciliationRequest) (*adminv1.GetReconciliationReply, error) {
	sum, err := s.repo.GetReconciliation(ctx, req.GetDate())
	if err != nil {
		return nil, errors.InternalServer("dashboard.QUERY_FAILED", err.Error())
	}
	return &adminv1.GetReconciliationReply{
		Summary: &adminv1.ReconciliationSummary{
			Date:                sum.Date,
			OrderPaidTotal:      sum.OrderPaidTotal,
			PaymentSuccessTotal: sum.PaymentSuccessTotal,
			WalletRechargeTotal: sum.WalletRechargeTotal,
			CommissionTotal:     sum.CommissionTotal,
			OrderCount:          sum.OrderCount,
			MismatchCount:       sum.MismatchCount,
		},
	}, nil
}
