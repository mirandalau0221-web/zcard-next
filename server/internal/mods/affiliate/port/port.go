// Package port 為 affiliate 模塊對外契約（零依賴包）。
package port

import "context"

// CommissionStats 佣金統計（dashboard/前台團隊頁消費）。
type CommissionStats struct {
	PendingCents   int64 `json:"pending_cents"`   // 凍結中（pending_confirm）
	AvailableCents int64 `json:"available_cents"` // 可提（已入 wallet 前）
	WithdrawnCents int64 `json:"withdrawn_cents"` // 已提現
	TotalCents     int64 `json:"total_cents"`     // 累計（正佣金合計）
	DebtCents      int64 `json:"debt_cents"`      // 負債（逆向未扣回）
}

// CommissionRow 佣金行（dashboard 列表消費；零依賴 DTO）。
type CommissionRow struct {
	ID          uint64
	OrderID     uint64
	BuyerID     uint64
	ReferrerID  uint64
	Tier        int32
	Rate        float64
	BaseAmount  int64
	Amount      int64
	Status      string
	AvailableAt int64 // unix；0 未設
	CreatedAt   int64
}

// CommissionReader 佣金讀取（dashboard 統計/列表，通道 A）。
type CommissionReader interface {
	StatsByUser(ctx context.Context, userID uint64) (*CommissionStats, error)
	// ListCommissions 佣金列表（status 空=全部）。
	ListCommissions(ctx context.Context, status string, page, size int) (rows []CommissionRow, total int64, err error)
}
