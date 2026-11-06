// Package id 雪花 ID 生成器（規劃 §4.11.4 ID 策略）。
//
// 對外業務單號（訂單/工單/退款單）必須不可枚舉（防 IDOR 取貨，鐵律 12），
// 且跨庫導出/合併不衝突；內部主鍵保持數據庫自增，二者嚴格分離。
package id

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	epochMs    int64 = 1735689600000 // 2025-01-01T00:00:00Z（毫秒）
	workerBits uint  = 10            // 1024 實例
	seqBits    uint  = 12            // 單機每毫秒 4096 個
	maxWorker  int64 = (1 << workerBits) - 1
	maxSeq     int64 = (1 << seqBits) - 1
)

// Generator 雪花 ID 生成器（41 位毫秒 + 10 位機器 + 12 位序列，併發安全）。
type Generator struct {
	mu       sync.Mutex
	workerID int64
	lastMs   int64
	seq      int64
}

// NewGenerator 構造。workerID 取值 [0, 1023]；Schema/Database 模式下由實例 ID 派生，
// 保證跨實例唯一（§4.11.4）。
func NewGenerator(workerID int64) (*Generator, error) {
	if workerID < 0 || workerID > maxWorker {
		return nil, fmt.Errorf("id: workerID %d 超出 [0,%d]", workerID, maxWorker)
	}
	return &Generator{workerID: workerID}, nil
}

// Next 生成下一個 ID；時鐘回撥在同一毫秒內由序列位吸收，跨毫秒回撥返回錯誤（拒絕生成重複號）。
func (g *Generator) Next() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := time.Now().UnixMilli()
	if ms < g.lastMs {
		return 0, errors.New("id: 時鐘回撥，拒絕生成（防重複單號）")
	}
	if ms == g.lastMs {
		g.seq++
		if g.seq > maxSeq {
			// 本毫秒序列耗盡：等待下一毫秒
			for ms <= g.lastMs {
				ms = time.Now().UnixMilli()
			}
			g.seq = 0
		}
	} else {
		g.seq = 0
	}
	g.lastMs = ms

	ts := ms - epochMs
	return ts<<(workerBits+seqBits) | g.workerID<<seqBits | g.seq, nil
}

// FormatNo 拼接對外單號：prefix + 雪花 ID（如訂單 "S1234567890123456789"，前綴可配）。
func FormatNo(prefix string, snowflakeID int64) string {
	return fmt.Sprintf("%s%d", prefix, snowflakeID)
}
