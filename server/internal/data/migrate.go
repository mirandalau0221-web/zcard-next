package data

// 運行時版本化遷移執行器（規劃 §10.4）：啓動時檢測待執行遷移並加鎖串行執行，
// 失敗拒絕啓動。遷移文件 go:embed 內嵌（migrations/<dialect>），生成走
// make migrate-diff（ent NamedDiff + dev-db），禁止手寫 DDL。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"time"

	atlasmigrate "ariga.io/atlas/sql/migrate"
	atlasmysql "ariga.io/atlas/sql/mysql"
	atlaspostgres "ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/sqlclient"
	atlassqlite "ariga.io/atlas/sql/sqlite"

	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	"github.com/NovaWorks/zcard-next/server/migrations"
)

// revisionTable Atlas 遷移版本表（與 atlas CLI 互通）。
const revisionTable = "atlas_schema_revisions"

// ApplyMigrations 應用內嵌遷移：加鎖 → 執行 pending 文件 → 記錄版本。
// 冪等：已應用文件跳過（atlas_schema_revisions 版本比對）。
// dsn 用於 PG：經 sqlclient.OpenURL 打開（URL 注入 search_path 綁定 schema，
// 否則 atlas 的 CheckClean 會因 realm 含 public schema 誤報 not clean）。
func ApplyMigrations(ctx context.Context, handle *sql.DB, d db.Dialect, dsn string) error {
	dirFS, err := migrations.FS(string(d))
	if err != nil {
		return err
	}
	// 無遷移文件（如全新方言線未生成）時靜默跳過——生成流程見 Makefile
	if !hasMigrationFiles(dirFS) {
		return nil
	}

	unlock, err := lockMigrations(ctx, handle, d)
	if err != nil {
		return fmt.Errorf("migrate: 獲取遷移鎖失敗: %w", err)
	}
	defer unlock()

	// PG 經 sqlclient.OpenURL 打開（atlas 需從 URL search_path 綁定 schema）；
	// MySQL/SQLite 直接複用主連接句柄
	var (
		drv     atlasmigrate.Driver
		revConn *sql.DB = handle
		closer  func()
	)
	if d == db.Postgres {
		u, err := url.Parse(dsn)
		if err != nil {
			return fmt.Errorf("migrate: 解析 PG DSN 失敗: %w", err)
		}
		q := u.Query()
		if q.Get("search_path") == "" {
			q.Set("search_path", "public")
			u.RawQuery = q.Encode()
		}
		client, err := sqlclient.OpenURL(ctx, u)
		if err != nil {
			return fmt.Errorf("migrate: 打開 PG 連接失敗: %w", err)
		}
		closer = func() { _ = client.Close() }
		drv = client
		revConn = client.DB
	} else {
		drv, err = atlasDriver(handle, d)
		if err != nil {
			return err
		}
	}
	defer func() {
		if closer != nil {
			closer()
		}
	}()
	rrw, err := newRevisionReadWriter(revConn, d)
	if err != nil {
		return err
	}
	dir, err := embedToMemDir(dirFS)
	if err != nil {
		return err
	}
	ex, err := atlasmigrate.NewExecutor(drv, dir, rrw)
	if err != nil {
		return err
	}
	// ExecuteN(ctx, 0)：應用全部 pending 遷移文件（記錄版本到 atlas_schema_revisions）；
	// 全部已應用時返回 ErrNoPendingFiles，視為成功
	if err := ex.ExecuteN(ctx, 0); err != nil && !errors.Is(err, atlasmigrate.ErrNoPendingFiles) {
		return err
	}
	return nil
}

func hasMigrationFiles(fsys fs.FS) bool {
	found := false
	_ = fs.WalkDir(fsys, ".", func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".sql") || e.Name() == "atlas.sum") {
			found = true
		}
		return nil
	})
	return found
}

// atlasDriver 由 *sql.DB 構造 atlas 遷移驅動（不經 sqlclient URL，直連已開句柄）。
func atlasDriver(handle *sql.DB, d db.Dialect) (atlasmigrate.Driver, error) {
	switch d {
	case db.MySQL:
		return atlasmysql.Open(handle)
	case db.Postgres:
		return atlaspostgres.Open(handle)
	case db.SQLite:
		return atlassqlite.Open(handle)
	}
	return nil, fmt.Errorf("migrate: 未知方言 %q", d)
}

// lockMigrations 啓動遷移串行鎖（§10.4）：MySQL GET_LOCK / PG pg_advisory_lock；
// SQLite 單寫者天然串行（BEGIN IMMEDIATE），無需額外鎖。
func lockMigrations(ctx context.Context, handle *sql.DB, d db.Dialect) (func(), error) {
	switch d {
	case db.MySQL:
		var got int
		if err := handle.QueryRowContext(ctx, "SELECT GET_LOCK(?, 60)", d.LockName()).Scan(&got); err != nil || got != 1 {
			return nil, fmt.Errorf("migrate: MySQL GET_LOCK 未獲取（got=%d err=%v）", got, err)
		}
		return func() {
			_, _ = handle.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", d.LockName())
		}, nil
	case db.Postgres:
		if _, err := handle.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", d.LockName()); err != nil {
			return nil, err
		}
		return func() {
			_, _ = handle.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtext($1))", d.LockName())
		}, nil
	}
	return func() {}, nil
}

// embedToMemDir 把內嵌遷移文件複製進 atlas MemDir（跳過 README 等非遷移文件）。
func embedToMemDir(fsys fs.FS) (*atlasmigrate.MemDir, error) {
	dir := &atlasmigrate.MemDir{}
	err := fs.WalkDir(fsys, ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		name := path.Base(p)
		if name == "README.md" || strings.HasPrefix(name, ".") {
			return nil
		}
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return dir.WriteFile(name, content)
	})
	return dir, err
}

// revisionReadWriter DB 版本記錄器（寫 atlas_schema_revisions，與 atlas CLI 互通）。
type revisionReadWriter struct {
	handle  *sql.DB
	dialect db.Dialect
}

func newRevisionReadWriter(handle *sql.DB, d db.Dialect) (*revisionReadWriter, error) {
	// 跨方言通用 DDL：version 用 VARCHAR(191) 作主鍵（MySQL 不允許 TEXT 主鍵，
	// 與 atlas CLI 版本表口徑一致；191 為 utf8mb4 索引安全長度）；operator_version
	// 用 varchar——MySQL 8 嚴格模式禁止 TEXT 列帶字面量默認值（Error 1101/1170）
	const ddl = `CREATE TABLE IF NOT EXISTS ` + revisionTable + ` (
		version            VARCHAR(191) NOT NULL PRIMARY KEY,
		description        TEXT NOT NULL,
		type               INTEGER NOT NULL DEFAULT 1,
		applied            INTEGER NOT NULL DEFAULT 0,
		total              INTEGER NOT NULL,
		executed_at        TIMESTAMP NOT NULL,
		execution_time     BIGINT NOT NULL DEFAULT 0,
		error              TEXT,
		error_stmt         TEXT,
		hash               TEXT NOT NULL,
		partial_hashes     TEXT,
		operator_version   VARCHAR(64) NOT NULL DEFAULT ''
	)`
	if _, err := handle.Exec(ddl); err != nil {
		return nil, fmt.Errorf("migrate: 創建版本表失敗: %w", err)
	}
	return &revisionReadWriter{handle: handle, dialect: d}, nil
}

// Ident 版本表標識。
func (w *revisionReadWriter) Ident() *atlasmigrate.TableIdent {
	return &atlasmigrate.TableIdent{Name: revisionTable}
}

// ph 第 i 個佔位符（PG $i / MySQL、SQLite ?）。
func (w *revisionReadWriter) ph(i int) string {
	if w.dialect == db.Postgres {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// ReadRevisions 讀取全部已應用版本。
func (w *revisionReadWriter) ReadRevisions(ctx context.Context) ([]*atlasmigrate.Revision, error) {
	rows, err := w.handle.QueryContext(ctx, `SELECT version, description, type, applied, total,
		executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version
		FROM `+revisionTable+` ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revs []*atlasmigrate.Revision
	for rows.Next() {
		r := &atlasmigrate.Revision{}
		var (
			execAt      time.Time
			execTime    int64
			errText     sql.NullString
			errStmt     sql.NullString
			partialHash sql.NullString
		)
		if err := rows.Scan(&r.Version, &r.Description, &r.Type, &r.Applied, &r.Total,
			&execAt, &execTime, &errText, &errStmt, &r.Hash, &partialHash, &r.OperatorVersion); err != nil {
			return nil, err
		}
		r.ExecutedAt = execAt
		r.ExecutionTime = time.Duration(execTime) * time.Millisecond
		r.Error = errText.String
		r.ErrorStmt = errStmt.String
		if partialHash.Valid && partialHash.String != "" {
			_ = json.Unmarshal([]byte(partialHash.String), &r.PartialHashes)
		}
		revs = append(revs, r)
	}
	return revs, rows.Err()
}

// ReadRevision 讀單條。
func (w *revisionReadWriter) ReadRevision(ctx context.Context, version string) (*atlasmigrate.Revision, error) {
	revs, err := w.ReadRevisions(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range revs {
		if r.Version == version {
			return r, nil
		}
	}
	return nil, atlasmigrate.ErrRevisionNotExist
}

// WriteRevision 寫版本記錄。atlas executor 協議是 UPSERT 語義：
// 執行前先寫入（標記 started）、每條語句後更新進度（partial hashes/applied 計數）、
// 完成或失敗後終寫（error/error_stmt）——純 INSERT 會在第二條語句後撞唯一鍵。
func (w *revisionReadWriter) WriteRevision(ctx context.Context, r *atlasmigrate.Revision) error {
	cols := []string{"version", "description", "type", "applied", "total",
		"executed_at", "execution_time", "error", "error_stmt", "hash", "partial_hashes", "operator_version"}
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = w.ph(i + 1)
	}
	partialJSON, err := json.Marshal(r.PartialHashes)
	if err != nil {
		return err
	}
	// 衝突時全量更新（進度推進/錯誤覆蓋/完成態清理 partial）
	var conflict string
	switch w.dialect {
	case db.MySQL:
		conflict = ` ON DUPLICATE KEY UPDATE description=VALUES(description), type=VALUES(type),
			applied=VALUES(applied), total=VALUES(total), executed_at=VALUES(executed_at),
			execution_time=VALUES(execution_time), error=VALUES(error), error_stmt=VALUES(error_stmt),
			hash=VALUES(hash), partial_hashes=VALUES(partial_hashes), operator_version=VALUES(operator_version)`
	default: // PG / SQLite
		conflict = ` ON CONFLICT (version) DO UPDATE SET description=EXCLUDED.description, type=EXCLUDED.type,
			applied=EXCLUDED.applied, total=EXCLUDED.total, executed_at=EXCLUDED.executed_at,
			execution_time=EXCLUDED.execution_time, error=EXCLUDED.error, error_stmt=EXCLUDED.error_stmt,
			hash=EXCLUDED.hash, partial_hashes=EXCLUDED.partial_hashes, operator_version=EXCLUDED.operator_version`
	}
	_, err = w.handle.ExecContext(ctx,
		`INSERT INTO `+revisionTable+` (`+strings.Join(cols, ",")+`) VALUES (`+strings.Join(placeholders, ",")+`)`+conflict,
		r.Version, r.Description, r.Type, r.Applied, r.Total,
		r.ExecutedAt.UTC(), r.ExecutionTime.Milliseconds(), r.Error, r.ErrorStmt, r.Hash, string(partialJSON), r.OperatorVersion)
	return err
}

// DeleteRevision 刪除版本記錄。
func (w *revisionReadWriter) DeleteRevision(ctx context.Context, version string) error {
	_, err := w.handle.ExecContext(ctx,
		`DELETE FROM `+revisionTable+` WHERE version = `+w.ph(1), version)
	return err
}

var _ atlasmigrate.RevisionReadWriter = (*revisionReadWriter)(nil)
