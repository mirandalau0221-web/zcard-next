package settings

// 在線安裝·數據庫切換（安裝嚮導選擇 mysql/postgres 時）：
//   校驗連接（庫不存在自動建）→ 寫 <confDir>/database.yaml 覆蓋配置（data 段）
//   → 寫待安裝憑據 data/.install-pending.json（0600，裝完即刪）→ 自重啓
//   → 新庫啓動鏈（遷移 → CompletePendingInstall 補裝管理員/種子 → 正常服務）。
//
// SQLite 路徑不落盤任何文件——當前庫直裝（零依賴體驗不變）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/redis/go-redis/v9"
)

// confDir 在線安裝寫覆蓋配置的目標目錄（runServe 注入，-conf 值）。
var confDir = "configs"

// SetConfDir 注入配置目錄（main runServe 調用）。
func SetConfDir(dir string) {
	if dir != "" {
		confDir = dir
	}
}

// pendingInstallFile 待安裝憑據落點（工作目錄 data/ 下——SQLite 同址，重啓後可尋）。
func pendingInstallFile() string { return filepath.Join("data", ".install-pending.json") }

// overrideConfigFile 數據庫覆蓋配置（Kratos 目錄合併：database.yaml 字典序在
// config.yaml 之後，同名鍵覆蓋——僅覆蓋 data 段，其餘配置不動）。
func overrideConfigFile() string { return filepath.Join(confDir, "database.yaml") }

// dbSwitchInput 庫切換參數（嚮導收集）。
type dbSwitchInput struct {
	Dialect       string
	Host          string
	Port          int32
	User          string
	Password      string
	Name          string
	RedisAddr     string
	RedisPassword string
}

// ValidateSwitchInput 校驗目標庫/Redis（安裝腳本 dbtest 子命令複用；庫不存在自動創建）。
func ValidateSwitchInput(dialect, host string, port int32, user, password, name, redisAddr, redisPassword string) error {
	return validateSwitch(context.Background(), dbSwitchInput{
		Dialect: dialect, Host: host, Port: port, User: user, Password: password,
		Name: name, RedisAddr: redisAddr, RedisPassword: redisPassword,
	})
}

// buildDSN 按方言拼 DSN（與 config.example 口徑一致）。
func buildDSN(in dbSwitchInput) (string, error) {
	switch in.Dialect {
	case "mysql":
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=True&loc=UTC&charset=utf8mb4",
			in.User, in.Password, in.Host, in.Port, in.Name), nil
	case "postgres":
		u := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(in.User, in.Password),
			Host:     fmt.Sprintf("%s:%d", in.Host, in.Port),
			Path:     "/" + in.Name,
			RawQuery: "sslmode=disable",
		}
		return u.String(), nil
	}
	return "", fmt.Errorf("不支持的方言 %q", in.Dialect)
}

// validateSwitch 測試目標庫 + Redis 連通（庫不存在自動創建；庫名缺失/憑據錯誤
// 返回可讀錯誤——嚮導「測試連接」與正式安裝共用）。
func validateSwitch(ctx context.Context, in dbSwitchInput) error {
	if in.Host == "" || in.Port <= 0 || in.User == "" || in.Name == "" {
		return fmt.Errorf("數據庫主機/端口/用户/庫名不能為空")
	}
	if in.RedisAddr == "" {
		return fmt.Errorf("選擇 MySQL/PostgreSQL 必須配置 Redis（SQLite 模式可省略）")
	}
	// Redis 連通
	rdb := redis.NewClient(&redis.Options{
		Addr: in.RedisAddr, Password: in.RedisPassword,
		DialTimeout: 3 * time.Second,
	})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis 連接失敗（%s）: %v", in.RedisAddr, err)
	}
	// 數據庫：先連服務級（無庫名）→ 建庫（不存在時）→ 再按 DSN 驗證
	switch in.Dialect {
	case "mysql":
		server := fmt.Sprintf("%s:%s@tcp(%s:%d)/?parseTime=True&loc=UTC",
			in.User, in.Password, in.Host, in.Port)
		db, err := sql.Open("mysql", server)
		if err != nil {
			return fmt.Errorf("MySQL 連接失敗: %v", err)
		}
		defer db.Close()
		if _, err := db.ExecContext(ctx,
			fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci", in.Name)); err != nil {
			return fmt.Errorf("創建數據庫失敗（檢查賬號權限）: %v", err)
		}
	case "postgres":
		u := url.URL{Scheme: "postgres", User: url.UserPassword(in.User, in.Password),
			Host: fmt.Sprintf("%s:%d", in.Host, in.Port), Path: "/postgres", RawQuery: "sslmode=disable"}
		db, err := sql.Open("postgres", u.String())
		if err != nil {
			return fmt.Errorf("PostgreSQL 連接失敗: %v", err)
		}
		defer db.Close()
		var exists bool
		if err := db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", in.Name).Scan(&exists); err != nil {
			return fmt.Errorf("查詢庫失敗: %v", err)
		}
		if !exists {
			// PG 建庫不支持參數佔位（標識符）
			if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, in.Name)); err != nil {
				return fmt.Errorf("創建數據庫失敗（檢查賬號權限）: %v", err)
			}
		}
	default:
		return fmt.Errorf("不支持的方言 %q", in.Dialect)
	}
	// 按最終 DSN 複驗（含庫名）
	dsn, _ := buildDSN(in)
	db, err := sql.Open(in.Dialect, dsn)
	if err != nil {
		return fmt.Errorf("數據庫連接失敗: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("數據庫連接失敗: %v", err)
	}
	// 建表權限探測：能連 ≠ 能建表（PG 15+ 默認 public schema 不開放 CREATE，
	// MySQL 用户也可能無 CREATE）——早報錯優於切庫自重啓後遷移失敗
	return probeCreateTable(ctx, db, in.Dialect, in.User, in.Name)
}

// probeCreateTable 建表權限探測：建一張隨機名臨時表再刪除（不留痕跡）。
// 失敗返回帶授權指引的錯誤——「測試連接」階段即暴露權限問題。
func probeCreateTable(ctx context.Context, db *sql.DB, dialect, user, dbName string) error {
	name := fmt.Sprintf("zcard_perm_probe_%d", time.Now().UnixNano())
	var create, drop string
	switch dialect {
	case "postgres":
		create = fmt.Sprintf(`CREATE TABLE %s (id bigint)`, name)
		drop = fmt.Sprintf(`DROP TABLE %s`, name)
	case "mysql":
		create = fmt.Sprintf("CREATE TABLE `%s` (`id` bigint)", name)
		drop = fmt.Sprintf("DROP TABLE `%s`", name)
	default:
		return nil
	}
	if _, err := db.ExecContext(ctx, create); err != nil {
		if strings.Contains(err.Error(), "denied") || strings.Contains(err.Error(), "permission") {
			var hint string
			if dialect == "postgres" {
				hint = fmt.Sprintf("GRANT ALL ON SCHEMA public TO \"%s\"", user)
			} else {
				hint = fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'", dbName, user)
			}
			return fmt.Errorf("數據庫賬號無建表權限（%v）——請使用數據庫超級用户/owner 賬號，或執行授權：%s", err, hint)
		}
		return fmt.Errorf("建表權限探測失敗: %v", err)
	}
	_, _ = db.ExecContext(ctx, drop)
	return nil
}

// writeSwitchFiles 落盤覆蓋配置 + 待安裝憑據。
func writeSwitchFiles(in dbSwitchInput, admin InstallInput) error {
	dsn, err := buildDSN(in)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return err
	}
	// 覆蓋配置（僅 data 段；密碼含特殊字符走 yaml 雙引號轉義）
	override := fmt.Sprintf(`# 在線安裝生成（數據庫切換）——重裝可刪除本文件
data:
  database:
    driver: %s
    source: %q
  redis:
    addr: %s
    password: %q
`, in.Dialect, dsn, in.RedisAddr, in.RedisPassword)
	if err := os.WriteFile(overrideConfigFile(), []byte(override), 0o644); err != nil {
		return fmt.Errorf("寫覆蓋配置失敗: %w", err)
	}
	// 待安裝憑據（新庫啓動後補裝；裝完即刪）
	if err := os.MkdirAll(filepath.Dir(pendingInstallFile()), 0o755); err != nil {
		return err
	}
	payload, _ := json.Marshal(admin)
	if err := os.WriteFile(pendingInstallFile(), payload, 0o600); err != nil {
		return fmt.Errorf("寫待安裝憑據失敗: %w", err)
	}
	return nil
}

// scheduleSelfRestart 延遲自重啓（syscall.Exec 原位換進程：保 pid/端口，
// systemd 與前台運行均適用；延遲讓安裝響應先送達客户端）。
func scheduleSelfRestart(delay time.Duration) {
	time.AfterFunc(delay, func() {
		exe, err := os.Executable()
		if err != nil {
			exe = os.Args[0]
		}
		_ = syscall.Exec(exe, os.Args, os.Environ())
	})
}

// CompletePendingInstall 啓動鏈接力（runServe 在遷移後調用）：存在待安裝憑據
// 且當前庫未安裝 → 在新庫上執行安裝並刪除憑據文件。
func CompletePendingInstall(ctx context.Context, d *data.Data) error {
	raw, err := os.ReadFile(pendingInstallFile())
	if err != nil {
		return nil // 無待裝（正常啓動）
	}
	var in InstallInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("待安裝憑據損壞（可刪除 %s 後重新安裝）: %w", pendingInstallFile(), err)
	}
	if Installed(ctx, d) {
		return os.Remove(pendingInstallFile()) // 新庫已裝過（冪等）：只清憑據
	}
	if err := Install(ctx, d, in); err != nil {
		return fmt.Errorf("補裝失敗: %w", err)
	}
	return os.Remove(pendingInstallFile())
}
