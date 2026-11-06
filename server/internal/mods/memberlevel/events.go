package memberlevel

// 積分產生：訂閲 order.paid → 按用戶當前等級 points_rule（消費 X 分產 Y 分）
// 入積分賬本（wallet.Points 端口，通道 A）；冪等鍵 points:<orderID>（重複投遞直接 ACK）。
// 口徑：積分兌換單 total=0 不產生；取消/未支付訂單不觸發（事件只在 paid 後發佈）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
)

// orderPaidPointsPayload order.paid 載荷（與 order 模塊發佈結構對齊——只取所需字段）。
type orderPaidPointsPayload struct {
	OrderID    uint64 `json:"order_id"`
	UserID     uint64 `json:"user_id"`
	TotalCents int64  `json:"total_cents"`
}

// PointsService 積分產生服務（Dispatcher 註冊）。
type PointsService struct {
	repo   *MemberLevelRepoImpl
	points walletport.Points
	log    *slog.Logger
}

// NewPointsService 構造。
func NewPointsService(repo *MemberLevelRepoImpl, points walletport.Points, log *slog.Logger) *PointsService {
	return &PointsService{repo: repo, points: points, log: log}
}

// OnOrderPaid 訂閲 order.paid：等級 points_rule → 積分入賬（冪等）。
func (s *PointsService) onOrderPaid(ctx context.Context, env events.Envelope) error {
	var payload orderPaidPointsPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		// 載荷損壞不重投（人工排查）；口徑與 procurement 一致
		s.log.Error("memberlevel.points_payload_invalid", "err", err)
		return nil
	}
	if payload.UserID == 0 || payload.TotalCents <= 0 {
		return nil // 遊客/積分兌換單（total=0）不產生積分
	}
	lv, err := s.repo.EffectiveLevelOf(ctx, payload.UserID)
	if err != nil || lv == nil {
		return err
	}
	spend, per := PointsRuleOf(lv)
	if spend <= 0 || per <= 0 {
		return nil // 當前等級未配置積分規則
	}
	earned := payload.TotalCents / spend * per
	if earned <= 0 {
		return nil
	}
	err = s.points.PointCreditInTx(ctx, walletport.PointEntry{
		UserID:    payload.UserID,
		Direction: "in",
		Type:      "earn_consume",
		Amount:    earned,
		Reference: fmt.Sprintf("points:%d", payload.OrderID),
		OrderID:   payload.OrderID,
		Remark:    "消費產生積分",
	})
	if err != nil {
		s.log.Error("memberlevel.points_credit_failed", "order_id", payload.OrderID, "err", err)
	}
	return err // 失敗交由隊列重試；積分流水與消費標記在同一事務提交。
}
