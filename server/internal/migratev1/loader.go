package migratev1

// 通用遷移引擎：keyset 分批掃描源表 → 每批一個目標事務（行轉換 + v1id_maps 原子寫入）。
// 冪等語義：行級——old_id 已在 v1id_maps 即跳過（重跑=增量，切換窗口模型的基礎）。
// 源端經 *sql.DB 抽象（MySQL 生產 / sqlite 單測雙跑），掃描 SQL 保持雙方言兼容。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
)

// Migrator 一次遷移會話（preflight 通過後構造）。
type Migrator struct {
	Src    *Source
	Client *ent.Client
	IDs    *IDMapper
	RW     *ReportWriter
	Opts   Options

	// 1.x 密鑰（preflight 解析產物）
	AppKey, CardKey []byte
	// 2.0 新密鑰（憑據重加密 DataBox / 卡密與直髮內容 CardCipher 用）
	DataKey    []byte
	NewCardKey []byte

	TZ  *time.Location
	st  *Stats
	dry bool
}

// NewMigrator 構造。
func NewMigrator(src *Source, client *ent.Client, ids *IDMapper, rw *ReportWriter, opts Options, tz string) *Migrator {
	if opts.Batch <= 0 {
		opts.Batch = 1000
	}
	return &Migrator{
		Src:    src,
		Client: client,
		IDs:    ids,
		RW:     rw,
		Opts:   opts,
		TZ:     srcTimezone(tz),
		dry:    opts.DryRun,
		st:     NewStats(),
	}
}

// subsiteFor 1.x merchant → 2.0 subsite_id 解析（分站補遷的核心開關）：
// merchant=1 主站 → (0, true)；merchant>1 且其 reseller_profile 已遷 → (profileID, true)；
// 未遷（主站先行階段）→ (0, false) 調用方跳過計數。P6 遷完 merchants 後重跑即補上。
func (m *Migrator) subsiteFor(ctx context.Context, merchantID int64) (uint64, bool) {
	if merchantID == 1 {
		return 0, true
	}
	if merchantID <= 0 {
		return 0, false
	}
	sid, ok := m.IDs.Get(ctx, "merchants", uint64(merchantID))
	return sid, ok
}

// productSubsite 查已遷商品的 subsite_id（卡密 Seal 的 AAD 必須與商品一致）。
func (m *Migrator) productSubsite(ctx context.Context, productID uint64) uint64 {
	p, err := m.Client.Product.Get(ctx, productID)
	if err != nil {
		return 0
	}
	return p.SubsiteID
}

// Stats 遷移統計（報告用）。
func (m *Migrator) Stats() *Stats { return m.st }

// Stats 逐表計數。
type Stats struct {
	Tables map[string]*TableStat
}

// TableStat 單表統計。Planned 僅 dry-run 填充（源行數預估）。
type TableStat struct {
	Migrated      int64 `json:"migrated"`
	SkippedExists int64 `json:"skipped_exists"` // 冪等跳過（已在 v1id_maps / 唯一鍵存在）
	Failed        int64 `json:"failed"`
	Planned       int64 `json:"planned,omitempty"`
}

// NewStats 構造。
func NewStats() *Stats { return &Stats{Tables: map[string]*TableStat{}} }

func (s *Stats) table(name string) *TableStat {
	t, ok := s.Tables[name]
	if !ok {
		t = &TableStat{}
		s.Tables[name] = t
	}
	return t
}

// Record 記一筆。
func (s *Stats) Record(table string, outcome string) {
	t := s.table(table)
	switch outcome {
	case "migrated":
		t.Migrated++
	case "skip":
		t.SkippedExists++
	case "fail":
		t.Failed++
	}
}

// scanTable keyset 分批掃描。dest 每行調用一次返回 scan 目標切片（首列必須是 id *int64）；
// fn 每行調用一次做轉換寫入（返回 error 計入失敗行）。dry 模式只計數不調 fn。
func (m *Migrator) scanTable(ctx context.Context, table string, cols []string,
	dest func() []any, fn func(id int64) error) error {

	t := m.st.table(table)
	if m.dry {
		var n int64
		if err := m.Src.DB.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM `%s`", table)).Scan(&n); err != nil {
			return err
		}
		t.Migrated = 0
		t.SkippedExists = 0
		t.Failed = 0
		t.Planned = n
		return nil
	}

	colList := make([]string, len(cols))
	for i, c := range cols {
		colList[i] = "`" + c + "`"
	}
	query := fmt.Sprintf("SELECT %s FROM `%s` WHERE `id` > ? ORDER BY `id` LIMIT ?",
		strings.Join(colList, ", "), table)
	var cursor int64
	for {
		rows, err := m.Src.DB.QueryContext(ctx, query, cursor, m.Opts.Batch)
		if err != nil {
			return fmt.Errorf("掃描 %s 失敗: %w", table, err)
		}
		var batchLast int64
		count := 0
		for rows.Next() {
			targets := dest()
			if err := rows.Scan(targets...); err != nil {
				rows.Close()
				return fmt.Errorf("掃描 %s 行失敗: %w", table, err)
			}
			id := *targets[0].(*int64)
			batchLast = id
			count++
			if err := fn(id); err != nil {
				if m.Opts.OnError == "abort" {
					rows.Close()
					return fmt.Errorf("表 %s 行 %d 遷移失敗（abort）: %w", table, id, err)
				}
				t.Failed++
				m.RW.AddError(table, uint64(id), err.Error())
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if count == 0 {
			return nil
		}
		cursor = batchLast
		if count < m.Opts.Batch {
			return nil
		}
	}
}

// scanAll 無 id 主鍵的小表全量掃描（如 currencies：1.x 主鍵是 code，無自增 id）。
// dest/fn 語義同 scanTable；表必須有界（字典級數據）。
func (m *Migrator) scanAll(ctx context.Context, table string, cols []string,
	dest func() []any, fn func() error) error {

	t := m.st.table(table)
	if m.dry {
		var n int64
		if err := m.Src.DB.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM `%s`", table)).Scan(&n); err != nil {
			return err
		}
		t.Planned = n
		return nil
	}
	colList := make([]string, len(cols))
	for i, c := range cols {
		colList[i] = "`" + c + "`"
	}
	rows, err := m.Src.DB.QueryContext(ctx,
		fmt.Sprintf("SELECT %s FROM `%s`", strings.Join(colList, ", "), table))
	if err != nil {
		return fmt.Errorf("掃描 %s 失敗: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		targets := dest()
		if err := rows.Scan(targets...); err != nil {
			return fmt.Errorf("掃描 %s 行失敗: %w", table, err)
		}
		if err := fn(); err != nil {
			if m.Opts.OnError == "abort" {
				return fmt.Errorf("表 %s 行遷移失敗（abort）: %w", table, err)
			}
			t.Failed++
		}
	}
	return rows.Err()
}

// ErrNotDelivered 未交付階段的佔位錯誤。
var ErrNotDelivered = fmt.Errorf("該階段尚未交付（按計劃 P2 起逐階段實現）")

// nullStr sql.NullString 取值（NULL → 空串；時間列 NULL 即空串語義）。
func nullStr(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

// nullInt sql.NullInt64 取值（NULL → 0）。
func nullInt(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
