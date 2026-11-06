package procurement

// 三通道結果獲取（友商標準照搬 + 間隔表）：
// 1. 輪詢：指數退避間隔表 [30s×2, 1m×2, 2m×2, 5m×2, 10m]（約 30 分鐘）；
// 耗盡不標記失敗，移交巡檢（cron 30min）
// 2. 巡檢：拉 polling/submitted 單查上游；超 24h 卡死 → manual + 告警事件
// 3. 回調：上游主動通知（ 自家協議回調）；三通道匯聚同一
// confirmResult 入口，狀態機 + dedupe 冪等（併發到達同一終態只生效一次）
//
// PollTaskType 任務類型（critical 隊列；worker mux 註冊）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"
)

// PollTaskType 輪詢任務類型（asynq mux 精確匹配）。
const PollTaskType = "procurement.poll"

// pollIntervals 指數退避間隔表（秒）：30s×2, 1m×2, 2m×2, 5m×2, 10m×1。
var pollIntervals = []int{30, 30, 60, 60, 120, 120, 300, 300, 600}

// PollOne 輪詢單張採購單（退避表按 retry_count 取間隔；耗盡移交巡檢不標記失敗）。
func (s *ProcureService) PollOne(ctx context.Context, poID uint64) error {
	po, err := s.repo.Get(ctx, poID)
	if err != nil {
		return err
	}
	switch string(po.Status) {
	case "rejected", "refunding", "refunded", "manual":
		return nil // 終態
	case "pending":
		// 提交尚未受理（上游排隊中）——按退避繼續
	}

	receipt, err := s.repo.ReceivedContent(ctx, poID)
	if err != nil {
		return err
	}
	if len(receipt) > 0 || string(po.Status) == "fulfilled" {
		return s.deliverReceipt(ctx, poID, 0)
	}
	if po.UpstreamOrderID == "" {
		return s.repo.MarkManual(ctx, poID, "上游單號缺失，請核實上游扣款和出貨結果後人工補發")
	}

	// 三通道匯聚：查詢上游 → 結果確認
	info, err := s.gw.Query(ctx, po.ConnectionID, po.UpstreamOrderID)
	if err != nil {
		return s.handlePollError(ctx, po.ID, err)
	}
	return s.confirmResult(ctx, po.ID, info.Status, info.Cards, info.Amount)
}

// confirmResult 三通道統一結果入口（輪詢/巡檢/回調匯聚）：
// 併發到達同一終態只生效一次（狀態機 CAS）。
func (s *ProcureService) confirmResult(ctx context.Context, poID uint64, status string, cards []string, amount int64) error {
	switch status {
	case "delivered":
		po, err := s.repo.Get(ctx, poID)
		if err != nil {
			return err
		}
		oi, err := s.repo.OrderItemInfo(ctx, po.OrderItemID)
		if err != nil {
			return err
		}
		return s.finalizeDelivered(ctx, po.ID, oi.OrderID, po.OrderItemID, oi.ProductID, oi.SubsiteID, cards, amount)
	case "rejected", "failed":
		return s.handleSubmitError(ctx, poID, errors.New("上游返回終態失敗"))
	default:
		// pending：推進輪詢檔位
		po, err := s.repo.Get(ctx, poID)
		if err != nil {
			return err
		}
		idx := int(po.RetryCount)
		if idx >= len(pollIntervals) {
			// 間隔表耗盡：不標記失敗，移交巡檢（30min cron 兜底）；
			// 已是 polling 則不再遷移（冪等）
			if string(po.Status) != "polling" {
				if err := s.repo.MarkPolling(ctx, poID); err != nil {
					return err
				}
			}
			s.log.Info("procurement.poll_exhausted_patrol", "po_id", poID)
			return nil
		}
		delay := time.Duration(pollIntervals[idx]) * time.Second
		if err := s.repo.BumpRetry(ctx, poID, time.Now().UTC().Add(delay), "pending"); err != nil {
			return err
		}
		return s.schedulePoll(ctx, poID, delay)
	}
}

// handlePollError 輪詢查詢失敗：可重試退避；永久錯誤 → rejected 分流。
func (s *ProcureService) handlePollError(ctx context.Context, poID uint64, err error) error {
	if errors.Is(err, supplyport.ErrUpstreamDeleted) || errors.Is(err, supplyport.ErrUpstreamUnavailable) {
		reason := err.Error()
		if merr := s.repo.MarkRejected(ctx, poID, reason); merr != nil {
			return merr
		}
		return s.applyFailStrategy(ctx, poID, reason)
	}
	po, gerr := s.repo.Get(ctx, poID)
	if gerr != nil {
		return gerr
	}
	idx := int(po.RetryCount)
	if idx >= len(pollIntervals) {
		if err := s.repo.MarkPolling(ctx, poID); err != nil {
			return err
		}
		return nil // 移交巡檢
	}
	delay := time.Duration(pollIntervals[idx]) * time.Second
	if err := s.repo.BumpRetry(ctx, poID, time.Now().UTC().Add(delay), err.Error()); err != nil {
		return err
	}
	return s.schedulePoll(ctx, poID, delay)
}

// Patrol 巡檢：拉 polling/submitted 單逐個查上游；超 24h 卡死 → manual + 事件。
// cron 每 30 分鐘註冊（bootstrap.NewCron）。
func (s *ProcureService) Patrol(ctx context.Context) {
	if err := s.repo.reconcileMissing(ctx); err != nil {
		s.log.Warn("procurement.missing_reconcile_failed", "err", err)
	}
	rows, err := s.repo.ListPollable(ctx, 100)
	if err != nil {
		s.log.Warn("procurement.patrol_list_failed", "err", err)
		return
	}
	now := time.Now().UTC()
	for _, po := range rows {
		// 24h 卡死檢測（last_poll_at 距今 > 24h）
		if !po.LastPollAt.IsZero() && now.Sub(po.LastPollAt) > 24*time.Hour {
			reason := fmt.Sprintf("採購單 %d 超過 24h 未推進，轉人工處理", po.ID)
			if err := s.repo.MarkManual(ctx, po.ID, reason); err != nil {
				s.log.Warn("procurement.patrol_manual_failed", "po_id", po.ID, "err", err)
				continue
			}
			s.publish(ctx, events.ProcurementFailed, po.ID, map[string]any{
				"procurement_id": po.ID, "reason": reason, "strategy": "manual_24h_stale",
			})
			continue
		}
		ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.PollOne(ctx2, po.ID)
		cancel()
		if err != nil {
			s.log.Warn("procurement.patrol_poll_failed", "po_id", po.ID, "err", err)
		}
	}
}

// RunPollTask 隊列任務入口（payload {"procurement_id": N}）。
func (s *ProcureService) RunPollTask(ctx context.Context, payload []byte) error {
	var req struct {
		ProcurementID uint64 `json:"procurement_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("procurement.poll: 解析任務載荷失敗: %w", err)
	}
	if req.ProcurementID == 0 {
		return nil
	}
	return s.PollOne(ctx, req.ProcurementID)
}
