// 在線更新模塊（doc/在線更新方案.md）：更新鏈 service + admin API。
//
// 分層：platform/updater（引擎）← 本模塊（業務編排：源配置/進度/單飛/重啓 hook）
// ← adminv1（HTTP 面）+ cmd CLI（self-update 共用 BackupDatabase）。
package update

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"

	"github.com/go-sql-driver/mysql"
	"path/filepath"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
)

// BackupDatabase 方言感知更新前備份（方案 §7——schema 只前滾，備份是回滾安全網）。
// SQLite VACUUM INTO（WAL 一致快照零外部工具）/ mysqldump / pg_dump；
// 失敗即中止更新（fail-closed，不提供跳過——數據安全優先）。
func BackupDatabase(ctx context.Context, dataCfg *conf.Data, destDir string) error {
	if dataCfg == nil || dataCfg.Database == nil || dataCfg.Database.Driver == "" {
		return fmt.Errorf("配置缺 data.database")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	db := dataCfg.Database
	switch db.Driver {
	case "sqlite":
		d, cleanup, err := data.NewData(dataCfg)
		if err != nil {
			return err
		}
		defer cleanup()
		dest := filepath.Join(destDir, "zcard.db")
		if _, err := d.DB.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
			return fmt.Errorf("VACUUM INTO 失敗: %w", err)
		}
		return nil
	case "mysql":
		return dumpMysql(ctx, filepath.Join(destDir, "zcard.sql"), db.Source)
	case "postgres":
		return dumpCommand(ctx, filepath.Join(destDir, "zcard.sql"), "pg_dump", db.Source)
	default:
		return fmt.Errorf("未知方言 %q", db.Driver)
	}
}

// dumpMysql MySQL 備份：mysqldump 不認 URL/Go DSN（直接傳會當庫名且丟密碼——
// using password: NO 實測），須解析後經 --defaults-extra-file 傳憑據（0600 臨時
// 文件，密碼不進進程參數表 ps 可見面），用後即刪。
func dumpMysql(ctx context.Context, dest, dsn string) error {
	bin, err := exec.LookPath("mysqldump")
	if err != nil {
		return fmt.Errorf("mysqldump 不在 PATH（apt install default-mysql-client 後重試）: %w", err)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("MySQL 連接串解析失敗: %w", err)
	}
	host, port, _ := net.SplitHostPort(cfg.Addr)
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "3306"
	}
	f, err := os.CreateTemp("", "zcard-mysql-*.cnf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	cnf := fmt.Sprintf("[client]\nhost=%s\nport=%s\nuser=%s\npassword=%s\n", host, port, cfg.User, cfg.Passwd)
	if _, err := f.WriteString(cnf); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_ = os.Chmod(f.Name(), 0o600)

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--defaults-extra-file="+f.Name(), cfg.DBName)
	cmd.Stdout = out
	cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if strings.Contains(msg, "Access denied") || strings.Contains(msg, "1045") {
			return fmt.Errorf("mysqldump 失敗: 數據庫認證被拒（檢查 config.yaml / database.yaml 的 data.database 連接賬號密碼）: %s", tail(msg, 300))
		}
		return fmt.Errorf("mysqldump 失敗: %v: %s", err, tail(msg, 400))
	}
	return nil
}

// dumpCommand mysqldump/pg_dump 落盤（外部工具缺失給明確指引；執行失敗
// 捕獲 stderr 回傳——pg_dump 的版本不匹配/認證失敗等原因必須直達面板，
// 不能只留一個 exit status 1 讓用户猜）。
func dumpCommand(ctx context.Context, dest, tool, dsn string) error {
	bin, err := exec.LookPath(tool)
	if err != nil {
		return fmt.Errorf("%s 不在 PATH（%s 後重試）: %w", tool, map[string]string{
			"pg_dump":   "apt install postgresql-client",
			"mysqldump": "apt install default-mysql-client",
		}[tool], err)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, dsn)
	cmd.Stdout = f
	cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf) // 服務日誌留全量，面板收摘要
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		// 常見分類:服務器版本較新——客户端必須同大版本
		if strings.Contains(msg, "server version mismatch") || strings.Contains(msg, "server version") && strings.Contains(msg, "incompatible") {
			return fmt.Errorf("%s 失敗: 服務器 PostgreSQL 版本較新，需安裝同大版本客户端（如 apt install postgresql-client-16 / postgresql-client-17）: %s", tool, tail(msg, 400))
		}
		if strings.Contains(msg, "password authentication") || strings.Contains(msg, "no password supplied") {
			return fmt.Errorf("%s 失敗: 數據庫認證失敗（檢查 config.yaml 的 data.database 連接信息）: %s", tool, tail(msg, 300))
		}
		return fmt.Errorf("%s 失敗: %v: %s", tool, err, tail(msg, 400))
	}
	return nil
}

// tail 截尾部 n 字節（pg_dump 錯誤要點在末尾；防面板超長）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
