//go:build integration

// Package testint CI 集成測試骨架（， 方言三線矩陣的 MySQL/PG 線）。
//
// 語義：
// - 僅 `-tags=integration` 編譯（make test 單元線零影響）；
// - DSN 走環境變量，未配置自動 Skip（本地無 Docker 不污染）：
// ZCARD_TEST_MYSQL_DSN 管理連接（指向任意可寫庫，如 /mysql）
// ZCARD_TEST_PG_DSN 管理連接（URL 形式，指向任意可寫庫，如 /postgres）
// - 每次運行創建隔離目標（MySQL CREATE DATABASE / PG CREATE SCHEMA，
// 名字含測試名 + 納秒後綴），defer DROP——並行與重跑互不踩踏；
// - 骨架內即跑全量真遷移（data.ApplyMigrations）——每個集成用例
// 天然建立在「遷移後的真實 schema」之上，遷移正確性隨用例持續迴歸。
package testint

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
)

// DSN 環境變量。
const (
	EnvMySQL = "ZCARD_TEST_MYSQL_DSN"
	EnvPG    = "ZCARD_TEST_PG_DSN"
)

// Harness 一次集成測試的隔離環境（真庫 + 真遷移 + 獨立庫/schema）。
type Harness struct {
	T    *testing.T
	Data *data.Data
	DSN  string // 目標 DSN（已含庫名 / search_path，可直接餵給 data.NewData）
	D    db.Dialect

	admin    *sql.DB // 管理連接（建/刪隔離目標）
	adminDSN string
	target   string // 隔離庫名（MySQL）/ schema 名（PG）
}

// MySQL 打開 MySQL 集成環境（EnvMySQL 未配置 → Skip）。
// 環境就緒後自動應用全量遷移（用例建立在真實遷移 schema 之上）。
func MySQL(t *testing.T) *Harness {
	t.Helper()
	return open(t, db.MySQL, envOrSkip(t, EnvMySQL, "root:root@tcp(127.0.0.1:3306)/mysql"), true)
}

// PG 打開 PostgreSQL 集成環境（EnvPG 未配置 → Skip）。語義同 MySQL。
func PG(t *testing.T) *Harness {
	t.Helper()
	return open(t, db.Postgres, envOrSkip(t, EnvPG, "postgres://postgres:pass@127.0.0.1:5432/postgres?sslmode=disable"), true)
}

// MySQLNoMigrate / PGNoMigrate 打開隔離環境但不跑遷移（空 schema）——
// 遷移用例自身驅動 ApplyMigrations 時使用（如衝突拒絕路徑）。
func MySQLNoMigrate(t *testing.T) *Harness {
	t.Helper()
	return open(t, db.MySQL, envOrSkip(t, EnvMySQL, ""), false)
}

func PGNoMigrate(t *testing.T) *Harness {
	t.Helper()
	return open(t, db.Postgres, envOrSkip(t, EnvPG, ""), false)
}

func envOrSkip(t *testing.T, env, example string) string {
	t.Helper()
	v := osGetenv(env)
	if v == "" {
		t.Skipf("集成線：環境變量 %s 未配置（如 %s）", env, example)
	}
	return v
}

func open(t *testing.T, d db.Dialect, adminDSN string, migrate bool) *Harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	target := isoName(t.Name())
	h := &Harness{T: t, D: d, adminDSN: adminDSN, target: target}

	// 1) 管理連接：建隔離目標
	admin, err := d.Open(adminDSN)
	if err != nil {
		t.Fatalf("集成線：打開管理連接失敗: %v", err)
	}
	h.admin = admin
	switch d {
	case db.MySQL:
		if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+target+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			_ = admin.Close()
			t.Fatalf("集成線：建庫失敗: %v", err)
		}
		h.DSN = mysqlWithDB(adminDSN, target)
	case db.Postgres:
		u, err := url.Parse(adminDSN)
		if err != nil {
			_ = admin.Close()
			t.Fatalf("集成線：PG DSN 非法: %v", err)
		}
		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+target); err != nil {
			_ = admin.Close()
			t.Fatalf("集成線：建 schema 失敗: %v", err)
		}
		q := u.Query()
		q.Set("search_path", target)
		u.RawQuery = q.Encode()
		h.DSN = u.String()
	}

	// 2) 目標連接：池上限固定 50（生產拓撲：共享池多連接併發事務；
	// 同時防 PG max_connections=100 默認值被打爆——50 路真鎖競爭已遠超
	// SQLite 單寫者串行路徑的覆蓋面）
	dh, cleanup, err := data.NewData(&conf.Data{Database: &conf.Data_Database{
		Driver: string(d), Source: h.DSN,
		MaxOpenConns: 50, MaxIdleConns: 50,
	}})
	if err != nil {
		h.drop()
		t.Fatalf("集成線：打開目標連接失敗: %v", err)
	}
	if migrate {
		if err := data.ApplyMigrations(ctx, dh.DB, d, h.DSN); err != nil {
			cleanup()
			h.drop()
			t.Fatalf("集成線：全量遷移失敗（方言 %s——遷移 SQL 真跑即驗收）: %v", d, err)
		}
	}
	h.Data = dh

	t.Cleanup(func() {
		cleanup()
		h.drop()
	})
	return h
}

// drop 刪除隔離目標（庫/schema）並關管理連接。
func (h *Harness) drop() {
	if h.admin == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch h.D {
	case db.MySQL:
		_, _ = h.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+h.target)
	case db.Postgres:
		_, _ = h.admin.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+h.target+" CASCADE")
	}
	_ = h.admin.Close()
	h.admin = nil
}

// isoName 測試名 → 合法標識符（小寫、[a-z0-9_]、長度上限 40 + 納秒後綴）。
func isoName(testName string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(path.Base(testName)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= 40 {
			break
		}
	}
	return fmt.Sprintf("zcard_it_%s_%d", b.String(), time.Now().UnixNano()%1_000_000)
}

// mysqlWithDB 替換 MySQL DSN 的庫名段（[user@tcp(addr)/]db[?params]）。
func mysqlWithDB(dsn, dbname string) string {
	query := ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		query = dsn[i:]
		dsn = dsn[:i]
	}
	i := strings.LastIndex(dsn, "/")
	if i < 0 {
		return dsn + "/" + dbname + query
	}
	return dsn[:i+1] + dbname + query
}

func osGetenv(k string) string { return strings.TrimSpace(os.Getenv(k)) }
