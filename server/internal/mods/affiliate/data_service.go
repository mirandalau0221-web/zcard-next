package affiliate

// 前台分銷 API（）：推廣碼/團隊/流水。管理側列表經 dashboard 模塊
// （AdminDashboardService.ListCommissions 消費 port.CommissionReader，通道 A）。

import (
	"context"
	"fmt"
	"strings"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/protobuf/types/known/emptypb"
)

// StoreAffiliateService 前台分銷服務。
type StoreAffiliateService struct {
	storefrontv1.UnimplementedStoreAffiliateServiceServer
	settings notifyport.SettingsReader
	repo     *CommissionRepo
	users    *identity.UserRepo // 推廣碼懶生成（通道 A）
}

// NewStoreAffiliateService 構造。
func NewStoreAffiliateService(repo *CommissionRepo, users *identity.UserRepo, settings notifyport.SettingsReader) *StoreAffiliateService {
	return &StoreAffiliateService{repo: repo, users: users, settings: settings}
}

// MyAffiliate 推廣碼 + 統計（invite_url 用請求 Host 拼絕對 URL；推廣碼懶生成）。
func (s *StoreAffiliateService) MyAffiliate(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.MyAffiliateReply, error) {
	userID := currentUID(ctx)
	if userID == 0 {
		return nil, fmt.Errorf("affiliate.UNAUTHORIZED: 請先登錄")
	}
	stats, err := s.repo.StatsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	l1, l2, l3, err := s.repo.TeamCounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	cfg := readAffiliateConfig(ctx, s.settings)
	if cfg.Levels < 3 {
		l3 = 0
	}
	if cfg.Levels < 2 {
		l2 = 0
	}
	// 推廣碼（懶生成——存量用戶首次訪問推廣中心即補）
	promoCode := s.users.EnsurePromoCode(ctx, userID)
	// 推廣鏈接：請求 Host 拼絕對 URL（複製即可分享；無 Host 回退相對路徑）。
	// scheme 判定：本地開發 host 回退 http，其餘一律 https（生產標準）。
	inviteURL := fmt.Sprintf("/?ref=%s", promoCode)
	if host := requestHost(ctx); host != "" {
		scheme := "https"
		if isLocalHost(host) {
			scheme = "http"
		}
		inviteURL = fmt.Sprintf("%s://%s/?ref=%s", scheme, host, promoCode)
	}
	return &storefrontv1.MyAffiliateReply{
		UserId:         userID,
		InviteUrl:      inviteURL,
		PromoCode:      promoCode,
		PendingCents:   stats.PendingCents,
		AvailableCents: stats.AvailableCents,
		WithdrawnCents: stats.WithdrawnCents,
		TotalCents:     stats.TotalCents,
		DebtCents:      stats.DebtCents,
		TeamL1:         l1, TeamL2: l2, TeamL3: l3,
	}, nil
}

// requestHost 從 kratos transport 取請求 Host（拼絕對推廣鏈接）。
// 用 RequestFromServerContext（*Transport 實現 Transporter 小接口；直接斷言
// khttp.Context 巨型接口會失敗——同 Payment 埋點中間件教訓）。
func requestHost(ctx context.Context) string {
	if r, ok := khttp.RequestFromServerContext(ctx); ok {
		h := r.Host
		if h == "" {
			h = r.Header.Get("X-Forwarded-Host")
		}
		return h
	}
	return ""
}

// isLocalHost 本地開發 host 判定（推廣鏈接回退 http scheme）。
func isLocalHost(host string) bool {
	host = strings.Split(host, ":")[0]
	return host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" ||
		len(host) > 6 && host[len(host)-6:] == ".local"
}

// ListTeam 下級列表（用戶名脱敏）。
func (s *StoreAffiliateService) ListTeam(ctx context.Context, req *storefrontv1.ListTeamRequest) (*storefrontv1.ListTeamReply, error) {
	userID := currentUID(ctx)
	if userID == 0 {
		return nil, fmt.Errorf("affiliate.UNAUTHORIZED")
	}
	page, size := pageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListTeam(ctx, userID, int(req.GetTier()), readAffiliateConfig(ctx, s.settings).Levels, page, size)
	if err != nil {
		return nil, err
	}
	reply := &storefrontv1.ListTeamReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, u := range rows {
		tier := 1
		if u.InviteL1 != userID && u.InviteL2 == userID {
			tier = 2
		}
		if u.InviteL1 != userID && u.InviteL2 != userID && u.InviteL3 == userID {
			tier = 3
		}
		reply.Members = append(reply.Members, &storefrontv1.TeamMember{
			UserId: u.ID, UsernameMasked: maskName(u.Username), Tier: int32(tier),
			JoinedAt: u.CreatedAt.Unix(),
		})
	}
	return reply, nil
}

// ListCommissions 佣金流水。
func (s *StoreAffiliateService) ListCommissions(ctx context.Context, req *storefrontv1.ListMyCommissionsRequest) (*storefrontv1.ListMyCommissionsReply, error) {
	userID := currentUID(ctx)
	if userID == 0 {
		return nil, fmt.Errorf("affiliate.UNAUTHORIZED")
	}
	page, size := pageParams(req.GetPage(), req.GetPageSize())
	rows, total, err := s.repo.ListByUser(ctx, userID, page, size)
	if err != nil {
		return nil, err
	}
	reply := &storefrontv1.ListMyCommissionsReply{Total: int64(total), Page: int32(page), PageSize: int32(size)}
	for _, c := range rows {
		item := &storefrontv1.CommissionItem{
			Id: c.ID, OrderId: c.OrderID, Tier: int32(c.Tier),
			BaseAmount: c.BaseAmount, Amount: c.Amount,
			Status: string(c.Status), CreatedAt: c.CreatedAt.Unix(),
		}
		if !c.AvailableAt.IsZero() {
			item.AvailableAt = c.AvailableAt.Unix()
		}
		reply.Commissions = append(reply.Commissions, item)
	}
	return reply, nil
}

// maskName 用戶名脱敏（首尾保留，中間 ***；≤2 位全掩）。
func maskName(name string) string {
	runes := []rune(name)
	if len(runes) <= 2 {
		return "***"
	}
	return string(runes[0]) + "***" + string(runes[len(runes)-1])
}

func currentUID(ctx context.Context) uint64 {
	if claims := identity.ClaimsFromContext(ctx); claims != nil {
		return claims.Subject
	}
	return 0
}

func pageParams(page, pageSize int32) (int, int) {
	p := int(page)
	if p < 1 {
		p = 1
	}
	ps := int(pageSize)
	if ps < 1 {
		ps = 20
	}
	if ps > 100 {
		ps = 100
	}
	return p, ps
}
