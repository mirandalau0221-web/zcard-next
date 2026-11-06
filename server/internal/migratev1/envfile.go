package migratev1

// 1.x .env 解析與源庫 DSN 構造（--old-env 指向 1.x 部署目錄時的自動裝配）。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

// ParseDotEnv 解析 Laravel .env（KEY=VALUE；支持單雙引號包裹與 # 註釋行）。
// 未存在的文件返回錯誤（--old-env 是顯式指定，指向錯誤應立刻暴露）。
func ParseDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("讀取 1.x .env 失敗（%s）: %w", path, err)
	}
	defer f.Close()

	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		env[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return env, nil
}

// OldEnvPath .env 路徑（目錄或直接指向 .env 文件均可）。
func OldEnvPath(dir string) string {
	if filepath.Base(dir) == ".env" {
		return dir
	}
	return filepath.Join(dir, ".env")
}

// MySQLDSN 由 .env 鍵集構造 go-sql-driver DSN（經 mysql.Config 轉義，密碼特殊字符安全）。
// 缺省端口 3306；附帶 utf8mb4 與保守超時。
func MySQLDSN(env map[string]string) string {
	host := env["DB_HOST"]
	if host == "" {
		host = "127.0.0.1"
	}
	if host == "localhost" {
		host = "127.0.0.1" // 避免走 socket 的歧義
	}
	port := env["DB_PORT"]
	if port == "" {
		port = "3306"
	}
	cfg := gomysql.NewConfig()
	cfg.User = env["DB_USERNAME"]
	cfg.Passwd = env["DB_PASSWORD"]
	cfg.Net = "tcp"
	cfg.Addr = host + ":" + port
	cfg.DBName = env["DB_DATABASE"]
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	// 時間列以原始字符串讀出，由映射層按 APP_TIMEZONE 顯式轉 UTC（避免 parseTime+loc 隱式偏移）
	cfg.ParseTime = false
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 60 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	return cfg.FormatDSN()
}
