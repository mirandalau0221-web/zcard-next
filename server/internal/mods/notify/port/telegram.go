package port

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TelegramTarget identifies a destination; zero TopicID omits message_thread_id.
type TelegramTarget struct {
	ChatID  string `json:"chat_id"`
	TopicID int64  `json:"topic_id,omitempty"`
}

var telegramChatPattern = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z0-9_]{5,})$`)

func NormalizeTelegramTarget(t TelegramTarget) (TelegramTarget, error) {
	t.ChatID = strings.TrimSpace(t.ChatID)
	if len(t.ChatID) > 64 || !telegramChatPattern.MatchString(t.ChatID) {
		return t, fmt.Errorf("Chat ID 須為非零整數或 @用戶名")
	}
	if strings.HasPrefix(t.ChatID, "@") {
		t.ChatID = strings.ToLower(t.ChatID)
	} else {
		id, err := strconv.ParseInt(t.ChatID, 10, 64)
		if err != nil || id == 0 {
			return t, fmt.Errorf("Chat ID 須為非零整數或 @用戶名")
		}
		t.ChatID = strconv.FormatInt(id, 10)
	}
	if t.TopicID < 0 || t.TopicID > 2147483647 {
		return t, fmt.Errorf("Topic ID 須為 1–2147483647 的整數，不使用話題請留空")
	}
	return t, nil
}

func ParseTelegramTargets(raw []byte) ([]TelegramTarget, error) {
	var targets []TelegramTarget
	if len(raw) > 8192 || json.Unmarshal(raw, &targets) != nil || targets == nil {
		return nil, fmt.Errorf("接收位置須為列表")
	}
	if len(targets) > 20 {
		return nil, fmt.Errorf("接收位置最多 20 個")
	}
	seen := map[TelegramTarget]bool{}
	for i, t := range targets {
		normalized, err := NormalizeTelegramTarget(t)
		if err != nil {
			return nil, fmt.Errorf("第 %d 個接收位置：%w", i+1, err)
		}
		if seen[normalized] {
			return nil, fmt.Errorf("第 %d 個接收位置重複，請檢查 Chat ID 和 Topic ID", i+1)
		}
		seen[normalized] = true
		targets[i] = normalized
	}
	return targets, nil
}
