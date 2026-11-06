package identity

// AdminUserManageService 前台用戶管理（列表/詳情聚合/封禁/新增/重置密碼）。
// 數據域 users 表（storefront 註冊用戶）；員工管理（admin_users）在 authz 模塊。
// 分站隔離：users 表無 subsite 列（全站賬戶），管理面按全站口徑查詢。
//
// 聚合視圖（等級/錢包/積分/優惠券/供貨賬戶/訂單）經共享 ent 層批量查詢
// （與 fulfillment.ProductNames 同模式——模塊紀律禁 import 對方 biz/data，
// 共享 data/ent 的只讀聚合不在此列）；等級判定複用 memberlevel RateResolver。

import (
	"context"
	"strings"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/coupon"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/memberlevel"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/pointaccount"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplieraccount"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/walletaccount"
	memberlevelport "github.com/NovaWorks/zcard-next/server/internal/mods/memberlevel/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AdminUserManageService 實現 adminv1.AdminUserManageService。
type AdminUserManageService struct {
	adminv1.UnimplementedAdminUserManageServiceServer
	repo *UserRepo
	rate memberlevelport.RateResolver // 會員等級解析（0=無等級）
}

// NewAdminUserManageService 構造。
func NewAdminUserManageService(repo *UserRepo, rate memberlevelport.RateResolver) *AdminUserManageService {
	return &AdminUserManageService{repo: repo, rate: rate}
}

// ListUsers 用戶列表（關鍵詞/狀態/供貨商篩選；逐條聚合等級/錢包/訂單/供貨標識）。
func (s *AdminUserManageService) ListUsers(ctx context.Context, req *adminv1.ListUsersRequest) (*adminv1.ListUsersReply, error) {
	q := data.Client(ctx, s.repo.data).User.Query()
	if kw := req.GetKeyword(); kw != "" {
		q = q.Where(user.Or(
			user.UsernameContainsFold(kw),
			user.EmailContainsFold(kw),
		))
	}
	if st := req.GetStatus(); st != "" {
		if !validUserStatus(st) {
			return nil, errors.BadRequest("identity.INVALID_STATUS", "狀態僅支持 active/banned")
		}
		q = q.Where(user.StatusEQ(user.Status(st)))
	}
	client := data.Client(ctx, s.repo.data)
	if req.GetIsSupplier() {
		accts, err := client.SupplierAccount.Query().All(ctx)
		if err != nil {
			return nil, errors.InternalServer("identity.USER_LIST_FAILED", "讀取用戶列表失敗")
		}
		sids := make([]uint64, 0, len(accts))
		for _, a := range accts {
			sids = append(sids, a.OwnerUserID)
		}
		q = q.Where(user.IDIn(sids...))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, errors.InternalServer("identity.USER_LIST_FAILED", "讀取用戶列表失敗")
	}
	page, size := normPage(req.GetPage(), req.GetPageSize())
	rows, err := q.
		Order(ent.Desc(user.FieldID)).
		Offset((page - 1) * size).
		Limit(size).
		All(ctx)
	if err != nil {
		return nil, errors.InternalServer("identity.USER_LIST_FAILED", "讀取用戶列表失敗")
	}
	reply := &adminv1.ListUsersReply{Total: int64(total)}
	items, err := s.enrich(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		reply.Users = append(reply.Users, items[r.ID])
	}
	return reply, nil
}

// GetUser 用戶詳情（聚合：等級/錢包/優惠券/供貨賬戶/邀請關係/最近訂單）。
func (s *AdminUserManageService) GetUser(ctx context.Context, req *adminv1.GetUserRequest) (*adminv1.UserDetail, error) {
	client := data.Client(ctx, s.repo.data)
	row, err := client.User.Get(ctx, req.GetId())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("identity.USER_NOT_FOUND", "用戶不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("identity.USER_GET_FAILED", "讀取用戶失敗")
	}
	items, err := s.enrich(ctx, []*ent.User{row})
	if err != nil {
		return nil, err
	}
	item := items[row.ID]

	detail := &adminv1.UserDetail{User: item}
	// 持有優惠券（最多 50，未使用在前）
	coupons, _ := client.Coupon.Query().
		Where(coupon.UserIDEQ(row.ID)).
		Order(ent.Desc(coupon.FieldID)).
		Limit(50).
		All(ctx)
	for _, c := range coupons {
		detail.Coupons = append(detail.Coupons, &adminv1.UserCouponItem{
			Id: c.ID, Title: c.Name, Status: string(c.Status),
		})
	}
	// 最近訂單 10 條
	orders, err := client.Order.Query().
		Where(order.UserIDEQ(row.ID)).
		Order(ent.Desc(order.FieldID)).
		Limit(10).
		All(ctx)
	if err != nil {
		return nil, errors.InternalServer("identity.ORDERS_FAILED", "讀取最近訂單失敗")
	}
	summaries, err := data.OrderProductSummaries(ctx, s.repo.data, orders)
	if err != nil {
		return nil, errors.InternalServer("identity.ORDER_PRODUCTS_FAILED", "讀取最近訂單商品失敗")
	}
	for _, o := range orders {
		ro := &adminv1.UserRecentOrder{
			OrderNo: o.OrderNo, AmountCents: o.TotalAmount, Status: string(o.Status),
			CreatedAt: o.CreatedAt.Unix(),
		}
		for _, it := range summaries[o.ID] {
			ro.Items = append(ro.Items, &adminv1.AdminOrderItem{ProductId: it.ProductID, SkuId: it.SkuID, Name: it.Name, SkuName: it.SkuName, Quantity: it.Quantity})
		}
		detail.RecentOrders = append(detail.RecentOrders, ro)
	}
	// 邀請關係
	if row.InviteL1 > 0 {
		if inviter, err := client.User.Get(ctx, row.InviteL1); err == nil {
			detail.InviterUsername = inviter.Username
		}
	}
	if n, err := client.User.Query().Where(user.InviteL1EQ(row.ID)).Count(ctx); err == nil {
		detail.InviteesCount = int64(n)
	}
	return detail, nil
}

// SetUserStatus 封禁/解封（deleted 不可經此設置）。
func (s *AdminUserManageService) SetUserStatus(ctx context.Context, req *adminv1.SetUserStatusRequest) (*adminv1.UserItem, error) {
	if !validUserStatus(req.GetStatus()) {
		return nil, errors.BadRequest("identity.INVALID_STATUS", "狀態僅支持 active/banned")
	}
	row, err := data.Client(ctx, s.repo.data).User.UpdateOneID(req.GetId()).
		SetStatus(user.Status(req.GetStatus())).
		Save(ctx)
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("identity.USER_NOT_FOUND", "用戶不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("identity.USER_STATUS_FAILED", "更新用戶狀態失敗")
	}
	items, err := s.enrich(ctx, []*ent.User{row})
	if err != nil {
		return nil, err
	}
	return items[row.ID], nil
}

// CreateUser 後台新增用戶（複用註冊管線：校驗/密碼哈希/推廣碼全同源）。
func (s *AdminUserManageService) CreateUser(ctx context.Context, req *adminv1.CreateUserRequest) (*adminv1.UserItem, error) {
	if req.GetUsername() == "" || req.GetPassword() == "" {
		return nil, errors.BadRequest("identity.CREATE_INPUT", "用戶名與密碼必填")
	}
	if len(req.GetPassword()) < 6 {
		return nil, errors.BadRequest("identity.PASSWORD_TOO_SHORT", "密碼至少 6 位")
	}
	row, err := s.repo.Register(ctx, RegisterInput{
		Username: req.GetUsername(), Password: req.GetPassword(), Email: req.GetEmail(),
	})
	if err != nil {
		return nil, errors.BadRequest("identity.CREATE_FAILED", strings.TrimPrefix(err.Error(), "identity."))
	}
	items, err := s.enrich(ctx, []*ent.User{row})
	if err != nil {
		return nil, err
	}
	return items[row.ID], nil
}

// ResetUserPassword 重置密碼（bcrypt 全新哈希；全部已登錄態不受影響——JWT 無狀態）。
func (s *AdminUserManageService) ResetUserPassword(ctx context.Context, req *adminv1.ResetUserPasswordRequest) (*emptypb.Empty, error) {
	if len(req.GetNewPassword()) < 6 {
		return nil, errors.BadRequest("identity.PASSWORD_TOO_SHORT", "密碼至少 6 位")
	}
	hash, err := crypto.HashPassword(req.GetNewPassword())
	if err != nil {
		return nil, errors.InternalServer("identity.RESET_FAILED", "密碼處理失敗")
	}
	err = data.Client(ctx, s.repo.data).User.UpdateOneID(req.GetId()).
		SetPasswordHash(hash).
		Exec(ctx)
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("identity.USER_NOT_FOUND", "用戶不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("identity.RESET_FAILED", "重置密碼失敗")
	}
	return &emptypb.Empty{}, nil
}

// enrich 批量聚合（一頁用戶 5 類批查 + 等級逐條解析，杜絕 N+1 逐行查詢）：
// 錢包/積分/供貨賬戶 in 查詢；訂單數與消費額 group by；等級名字典批查。
func (s *AdminUserManageService) enrich(ctx context.Context, rows []*ent.User) (map[uint64]*adminv1.UserItem, error) {
	out := make(map[uint64]*adminv1.UserItem, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	client := data.Client(ctx, s.repo.data)
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}

	balances := map[uint64]int64{}
	if ws, err := client.WalletAccount.Query().Where(walletaccount.UserIDIn(ids...)).All(ctx); err == nil {
		for _, w := range ws {
			balances[w.UserID] = w.Available
		}
	}
	points := map[uint64]int64{}
	if ps, err := client.PointAccount.Query().Where(pointaccount.UserIDIn(ids...)).All(ctx); err == nil {
		for _, p := range ps {
			points[p.UserID] = p.Balance
		}
	}
	suppliers := map[uint64]string{}
	if ss, err := client.SupplierAccount.Query().Where(supplieraccount.OwnerUserIDIn(ids...)).All(ctx); err == nil {
		for _, a := range ss {
			suppliers[a.OwnerUserID] = string(a.Status)
		}
	}
	// 訂單聚合:兩趟 group by 批量（杜絕逐用戶 N+1）
	orderCount := map[uint64]int{}
	var cntRows []struct {
		UserID uint64 `json:"user_id"`
		Count  int    `json:"count"`
	}
	_ = client.Order.Query().Where(order.UserIDIn(ids...)).
		GroupBy(order.FieldUserID).
		Aggregate(ent.Count()).
		Scan(ctx, &cntRows)
	for _, r := range cntRows {
		orderCount[r.UserID] = r.Count
	}
	spent, err := data.UserSpending(ctx, client, ids)
	if err != nil {
		return nil, errors.InternalServer("identity.USER_STATS_FAILED", "讀取用戶消費統計失敗")
	}

	// 等級:RateResolver 逐用戶 + 名稱字典批查
	levelIDs := map[uint64]uint64{}
	levelSources := map[uint64]string{}
	for _, r := range rows {
		lid := uint64(0)
		if resolver, ok := s.rate.(memberlevelport.StateResolver); ok {
			_, l, source, err := resolver.EffectiveState(ctx, r.ID)
			if err != nil {
				return nil, errors.InternalServer("identity.USER_LEVEL_FAILED", "讀取客戶等級失敗")
			}
			lid, levelSources[r.ID] = l, source
		} else if s.rate != nil {
			if _, l, err := s.rate.EffectiveRate(ctx, r.ID); err == nil {
				lid = l
			}
		}
		levelIDs[r.ID] = lid
	}
	levelNames := map[uint64]string{}
	distinct := map[uint64]bool{}
	for _, r := range rows {
		if r.ReferralLevelID > 0 {
			distinct[r.ReferralLevelID] = true
		}
		if r.InviteLevelID > 0 {
			distinct[r.InviteLevelID] = true
		}
	}
	for _, lid := range levelIDs {
		if lid > 0 {
			distinct[lid] = true
		}
	}
	if len(distinct) > 0 {
		lids := make([]uint64, 0, len(distinct))
		for lid := range distinct {
			lids = append(lids, lid)
		}
		if ls, err := client.MemberLevel.Query().Where(memberlevel.IDIn(lids...)).All(ctx); err == nil {
			for _, l := range ls {
				levelNames[l.ID] = l.Name
			}
		}
	}

	for _, r := range rows {
		item := &adminv1.UserItem{
			Id: r.ID, Username: r.Username, Email: r.Email, ManualLevelId: r.ManualLevelID,
			ReferralLevelId: r.ReferralLevelID, InviteLevelId: r.InviteLevelID,
			ReferralLevelName: levelNames[r.ReferralLevelID], InviteLevelName: levelNames[r.InviteLevelID],
			PromoCode: r.PromoCode, LevelSource: "none",
			Status: string(r.Status), CreatedAt: r.CreatedAt.Unix(),
			BalanceCents: balances[r.ID],
			Points:       int32(points[r.ID]),
			OrderCount:   int64(orderCount[r.ID]),
			SpentCents:   spent[r.ID],
		}
		if !r.LastLoginAt.IsZero() {
			item.LastLoginAt = r.LastLoginAt.Unix()
		}
		if lid := levelIDs[r.ID]; lid > 0 {
			item.LevelId = lid
			item.LevelName = levelNames[lid]
			item.LevelSource = levelSources[r.ID]
			if item.LevelSource == "" {
				item.LevelSource = "auto"
			}
		}
		if st, ok := suppliers[r.ID]; ok {
			item.IsSupplier = true
			item.SupplierStatus = st
		}
		out[r.ID] = item
	}
	return out, nil
}

func validUserStatus(st string) bool {
	return st == "active" || st == "banned"
}

func normPage(page, size int32) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return int(page), int(size)
}
