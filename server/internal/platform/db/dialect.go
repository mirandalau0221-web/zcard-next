// Package db 是全倉唯一允許觸碰數據庫方言的包（ADR-D20）。
//
// 三個方向釘死（規劃 §3.7）：
//  1. 運行時能力開關（Detect），不是 build tag——同一二進制三方言；
//  2. 能力降級是顯式分支（Capabilities），禁止「假裝支持然後運行時炸 SQL」；
//  3. report 層裸 SQL 必須經 SQL 原語構造；mods/* 禁止 import 驅動特定包（架構測試強制）。
//
// 業務代碼 95% 走 Ent builder 天然跨方言；本包只服務：
//   - 啓動裝配（按 DSN 打開對應驅動的 *sql.DB）；
//   - internal/data/report 的跨方言裸 SQL 構造；
//   - 方言能力判定（如 SQLite 無 FOR UPDATE 時走 BEGIN IMMEDIATE + CAS）。
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dialect 數據庫方言。
type Dialect string

const (
	MySQL    Dialect = "mysql"
	Postgres Dialect = "postgres"
	SQLite   Dialect = "sqlite"
)

// Detect 從配置的 driver 名檢測方言（config.yaml data.database.driver）。
// 兼容常見別名（mariadb/postgresql/sqlite3）。
func Detect(driver string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "mysql", "mariadb":
		return MySQL, nil
	case "postgres", "postgresql", "pgx":
		return Postgres, nil
	case "sqlite", "sqlite3":
		return SQLite, nil
	}
	return "", fmt.Errorf("db: unsupported driver %q (supported: sqlite | mysql | postgres)", driver)
}

// DriverName 返回 database/sql 註冊的驅動名。
// SQLite 使用純 Go 驅動 modernc.org/sqlite（無 CGO，保單二進制交叉編譯，ADR-D19）。
func (d Dialect) DriverName() string {
	switch d {
	case MySQL:
		return "mysql"
	case Postgres:
		return "pgx" // jackc/pgx/v5/stdlib
	case SQLite:
		return "sqlite" // modernc.org/sqlite
	}
	return string(d)
}

// Open 按 DSN 打開連接池（統一入口：驅動選擇收口在本包，mods 禁 import 驅動包）。
// SQLite 自動啓用 WAL + busy_timeout（ADR-D19 技術要點 2），並確保父目錄存在；
// MySQL 自動補 parseTime/loc（驅動返回 time.Time 的必要參數，漏配是常見接入錯誤）。
func (d Dialect) Open(dsn string) (*sql.DB, error) {
	switch d {
	case SQLite:
		dsn = ensureSQLitePragmas(dsn)
		if err := ensureSQLiteDir(dsn); err != nil {
			return nil, err
		}
	case MySQL:
		dsn = ensureMySQLParams(dsn)
	}
	handle, err := sql.Open(d.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", d, err)
	}
	return handle, nil
}

// ensureMySQLParams 補齊驅動必需參數：parseTime=True（掃描 time 列）與 loc=UTC
// （統一 UTC，§3.4）；用戶已配置則不覆蓋（如自定義 loc 的多租戶場景）。
func ensureMySQLParams(dsn string) string {
	// DSN 形如 user:pass@tcp(host:port)/db?params
	if !strings.Contains(dsn, "parseTime=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "parseTime=True&loc=UTC"
	}
	return dsn
}

func ensureSQLitePragmas(dsn string) string {
	if strings.Contains(dsn, "_pragma") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}

// ensureSQLiteDir SQLite 文件父目錄自動創建（file:data/zcard.db → mkdir data/）。
func ensureSQLiteDir(dsn string) error {
	path := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexAny(path, "?"); i >= 0 {
		path = path[:i]
	}
	if path == "" || path == ":memory:" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("db: 創建 SQLite 目錄 %s 失敗: %w", dir, err)
	}
	return nil
}

// LockName 返回 Dialect 對應的遷移鎖名（啓動遷移串行化，規劃 §10.4）。
func (d Dialect) LockName() string { return "zcard_migrate" }
