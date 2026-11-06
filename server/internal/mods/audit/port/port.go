// Package port 為 audit 模塊對外契約（零依賴包）。
package port

import (
	"context"
	"github.com/NovaWorks/zcard-next/server/internal/platform/orderaccess"
	"net"
	"strings"
	"time"
)

// SecurityEntry 安全審計條目（埋點入參）。
type SecurityEntry struct {
	ActorType string // admin | user | guest | system
	ActorID   uint64
	Action    string // 事件名（identity.login_failed / card.decrypt / ...）
	IP        string
	Metadata  map[string]any // 關鍵 ID（明文卡密/憑據絕不入內）
}

// Auditor 安全審計端口（identity/inventory/authz 等模塊消費，通道 A）。
// 實現紀律：寫失敗不阻斷業務（1.x SecurityAudit 紀律）。
type Auditor interface {
	Security(ctx context.Context, e SecurityEntry)
}

// GateInput 風控閘門入參。
type GateInput struct {
	RiskIP string // 規範化 IP（IPv6 已按 /64 聚合）
	UserID uint64 // 0 = 遊客
	// FetchFail 取貨失敗鎖定鍵（非空時走失敗計數鎖定而非下單閘門）
	FetchFailKey string
}

// RiskGate 風控閘門端口（order 下單 / fulfillment 取貨消費，通道 A）。
type RiskGate interface {
	// Check 下單前閘門：IP 黑名單 → pending 閘門 → 頻率限流（事務內複查防穿透）。
	Check(ctx context.Context, in GateInput) error
	orderaccess.Gate
}

// NormalizeIP IP 規範化：IPv4 原樣；IPv6 按 /64 聚合（orders.risk_ip 寫入口統一）。
func NormalizeIP(ip string) string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	masked := parsed.Mask(net.CIDRMask(64, 128))
	return masked.String()
}

// TrafficDay 單日流量（PV/UV）。
type TrafficDay struct {
	Date string
	PV   int64
	UV   int64
}

// TrafficReader 訪問統計讀取端口（dashboard 工作台消費，通道 A）。
type TrafficReader interface {
	// CountOnlineUsers 在線用户數（最近 since 內有活躍心跳；分站隔離）。
	CountOnlineUsers(ctx context.Context, subsite uint64, since time.Time) (int64, error)
	// TrafficByDay 近 N 天 PV/UV（不含缺日補零——調用方補齊日期序列）。
	TrafficByDay(ctx context.Context, subsite uint64, days int) ([]TrafficDay, error)
}
