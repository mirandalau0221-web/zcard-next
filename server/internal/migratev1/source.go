package migratev1

// 源庫（1.x MySQL，只讀）連接與巡查詢。

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql" // 源方言固定 MySQL
)

// Source 1.x 源庫句柄。約定只讀：遷移全程僅 SELECT，建議配只讀賬號。
type Source struct {
	DB *sql.DB
}

// OpenSource 打開源庫（dsn 為 go-sql-driver 格式；由 --dsn-old 或 --old-env 構造）。
func OpenSource(dsn string) (*Source, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打開 1.x 源庫失敗: %w", err)
	}
	// 巡檢/分頁讀無需高併發；限制連接數降低對生產源庫的衝擊
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return &Source{DB: db}, nil
}

// Close 關閉源庫。
func (s *Source) Close() error { return s.DB.Close() }

// ServerVersion 源庫版本（巡檢報告展示）。
func (s *Source) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := s.DB.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// TableExists 表是否存在。
func (s *Source) TableExists(ctx context.Context, table string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ColumnDataType 列的 DATA_TYPE（information_schema；表/列不存在返回錯誤）。
func (s *Source) ColumnDataType(ctx context.Context, table, column string) (string, error) {
	var dt string
	err := s.DB.QueryRowContext(ctx,
		"SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		table, column).Scan(&dt)
	if err != nil {
		return "", fmt.Errorf("探測列 %s.%s 失敗: %w", table, column, err)
	}
	return dt, nil
}

// CountRows 錶行數（巡檢規模概覽；大表 COUNT 可能偏慢，僅在 preflight 使用）。
func (s *Source) CountRows(ctx context.Context, table string) (int64, error) {
	var n sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&n); err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// SampleStrings 取單列前 limit 行（密鑰抽樣自檢用；NULL 跳過）。
func (s *Source) SampleStrings(ctx context.Context, table, column string, limit int) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		fmt.Sprintf("SELECT `%s` FROM `%s` WHERE `%s` IS NOT NULL ORDER BY id LIMIT %d", column, table, column, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SettingValue 讀 settings 表單個 key 的 value（JSON 編碼字符串；不存在返回空串與 false）。
func (s *Source) SettingValue(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE `key` = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}
