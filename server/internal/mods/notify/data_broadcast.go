package notify

// 定向羣發（）：目標篩選（全部/活躍/指定會員）+ 定時發送（cron 掃描）+
// 覆蓋人數預估 + 取消（未到定時點）+ 送達統計。
//
// 執行：BroadcastTaskType 任務（default 隊列；降級進程內）；分批發送（100/批）
// 每目標每通道一條 notification_logs（biz_type=broadcast）。
// 站內信直寫 notifications；email/sms/telegram 經通道投遞（失敗計數不阻斷）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/notifybroadcast"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/queue"
)

// BroadcastTaskType 羣發任務類型（default 隊列）。
const BroadcastTaskType = "notify.broadcast"

// 羣發批次大小。
const broadcastBatch = 100

// 哨兵錯誤。
var (
	ErrBroadcastNotFound = errors.New("notify: 羣發任務不存在")
	ErrBroadcastStarted  = errors.New("notify: 羣發已開始，不可取消")
)

// BroadcastService 羣發服務。
type BroadcastService struct {
	repo *NotifyRepo
	disp *Dispatcher
	enq  queue.Enqueuer
}

// NewBroadcastService 構造。
func NewBroadcastService(repo *NotifyRepo, disp *Dispatcher, enq queue.Enqueuer) *BroadcastService {
	return &BroadcastService{repo: repo, disp: disp, enq: enq}
}

// BroadcastInput 創建入參。
type BroadcastInput struct {
	Title       string
	Content     string
	Channels    []string
	TargetType  string // all | active | specified
	TargetIDs   []uint64
	ScheduledAt time.Time // 零值 = 立即
	CreatedBy   uint64
}

// EstimateAudience 覆蓋人數預估（創建前預覽）。
func (s *BroadcastService) EstimateAudience(ctx context.Context, targetType string, targetIDs []uint64) (int64, error) {
	return s.repo.CountBroadcastTargets(ctx, targetType, targetIDs)
}

// Create 創建羣發任務（定時 → cron 掃描觸發；立即 → 直接入隊）。
func (s *BroadcastService) Create(ctx context.Context, in BroadcastInput) (*ent.NotifyBroadcast, error) {
	if in.Title == "" || in.Content == "" {
		return nil, errors.New("notify.TITLE_CONTENT_REQUIRED")
	}
	if len(in.Channels) == 0 {
		return nil, errors.New("notify.CHANNELS_REQUIRED")
	}
	if in.TargetType == "specified" && len(in.TargetIDs) == 0 {
		return nil, errors.New("notify.TARGET_IDS_REQUIRED")
	}
	// 預估覆蓋人數（創建即回填）
	audience, err := s.EstimateAudience(ctx, in.TargetType, in.TargetIDs)
	if err != nil {
		return nil, err
	}
	b, err := s.repo.CreateBroadcast(ctx, in, audience)
	if err != nil {
		return nil, err
	}
	// 立即發送：入隊；定時：cron 掃描（ScanDueBroadcasts）
	if in.ScheduledAt.IsZero() || !in.ScheduledAt.After(time.Now().UTC()) {
		if err := s.enqueueBroadcast(ctx, b.ID); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// Cancel 取消（僅 pending；已開始不可取消）。
func (s *BroadcastService) Cancel(ctx context.Context, id uint64) (*ent.NotifyBroadcast, error) {
	b, err := s.repo.GetBroadcast(ctx, id)
	if err != nil {
		return nil, err
	}
	if string(b.Status) != "pending" {
		return nil, ErrBroadcastStarted
	}
	return s.repo.SetBroadcastStatus(ctx, id, "canceled", time.Time{})
}

// ScanDue 定時羣發掃描（cron 每分鐘）：到期 pending → 入隊。
func (s *BroadcastService) ScanDue(ctx context.Context) {
	ids, err := s.repo.ListDueBroadcasts(ctx, time.Now().UTC())
	if err != nil {
		return
	}
	for _, id := range ids {
		if err := s.enqueueBroadcast(ctx, id); err != nil {
			continue
		}
	}
}

// RunBroadcastTask 隊列任務入口（payload {"broadcast_id": N}）。
func (s *BroadcastService) RunBroadcastTask(ctx context.Context, payload []byte) error {
	var req struct {
		BroadcastID uint64 `json:"broadcast_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("notify.broadcast: 解析載荷失敗: %w", err)
	}
	if req.BroadcastID == 0 {
		return nil
	}
	return s.Execute(ctx, req.BroadcastID)
}

// Execute 執行羣發：鎖定 sending → 分批投遞 → 統計回填 → done。
func (s *BroadcastService) Execute(ctx context.Context, broadcastID uint64) error {
	b, err := s.repo.GetBroadcast(ctx, broadcastID)
	if err != nil {
		return err
	}
	// 冪等：非 pending 直接 ACK（重複投遞）
	if string(b.Status) != "pending" {
		return nil
	}
	b, err = s.repo.SetBroadcastStatus(ctx, broadcastID, "sending", time.Now().UTC())
	if err != nil {
		return err
	}

	var sent, failed int64
	var lastID uint64
	for {
		// 取消檢查（發送中允許取消：批次間檢查，剩餘目標不再投遞）
		cur, err := s.repo.GetBroadcast(ctx, broadcastID)
		if err == nil && string(cur.Status) == "canceled" {
			break
		}
		targets, err := s.repo.BroadcastTargets(ctx, string(b.TargetType), b.TargetIds, lastID, broadcastBatch)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			break
		}
		for _, u := range targets {
			lastID = u.ID
			sentOne, failOne := s.deliverOne(ctx, b, u)
			sent += sentOne
			failed += failOne
		}
		// 統計增量回填（進度可觀測）
		_ = s.repo.UpdateBroadcastProgress(ctx, broadcastID, sent, failed)
		if len(targets) < broadcastBatch {
			break
		}
	}
	_, err = s.repo.FinishBroadcast(ctx, broadcastID, sent, failed)
	return err
}

// deliverOne 單目標投遞（多通道逐個；inbox 直寫，其餘經 Dispatcher）。
func (s *BroadcastService) deliverOne(ctx context.Context, b *ent.NotifyBroadcast, u *ent.User) (sent, failed int64) {
	for _, ch := range b.Channels {
		msg := broadcastMessage(b, ch, u)
		if ch == "inbox" {
			if err := s.repo.CreateInbox(ctx, u.ID, b.Title, plainText(b.Content), "broadcast", b.ID); err != nil {
				failed++
				_ = s.repo.WriteLog(ctx, logOf(msg, "failed", err.Error()))
				continue
			}
			sent++
			_ = s.repo.WriteLog(ctx, logOf(msg, "sent", ""))
			continue
		}
		if err := s.disp.Send(ctx, msg); err != nil {
			failed++
			continue
		}
		sent++
	}
	return sent, failed
}

func broadcastMessage(b *ent.NotifyBroadcast, channel string, u *ent.User) notifyport.Message {
	msg := notifyport.Message{
		EventType: "broadcast", Channel: channel, Locale: "zh_CN",
		Subject: b.Title, Body: b.Content,
		BizType: "broadcast", BizID: b.ID, UserID: u.ID,
		Variables: map[string]string{"user_id": fmt.Sprint(u.ID), "username": u.Username},
	}
	if channel == "email" {
		msg.Recipient = u.Email // 收件人按通道解析
	}
	return msg
}

// enqueueBroadcast 入隊（有 Redis 走隊列；降級進程內異步）。
func (s *BroadcastService) enqueueBroadcast(ctx context.Context, id uint64) error {
	payload, _ := json.Marshal(map[string]uint64{"broadcast_id": id})
	if s.enq == nil || !s.enq.Enabled() {
		go func() {
			runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			if err := s.Execute(runCtx, id); err != nil {
				_ = err
			}
		}()
		return nil
	}
	return s.enq.Enqueue(ctx, queue.Task{
		Type:      BroadcastTaskType,
		Payload:   payload,
		Queue:     queue.QueueDefault,
		DedupeKey: BroadcastTaskType + ":" + fmt.Sprint(id),
	})
}

var _ = data.Client
var _ = user.FieldID
var _ = notifybroadcast.FieldID
