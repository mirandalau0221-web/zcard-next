package main

// 零配置首啓（單文件部署）：configs/config.yaml 缺失時自動生成 SQLite 引導配置。
//
// 生成的配置只是安裝嚮導（/install）的運行載體——Web 裏選 PG/MySQL 後向導會
// 改寫本文件並自重啓切換到目標庫；選 SQLite 則直接在引導庫上完成安裝。
// 四把安全密鑰落盤真隨機值（env ZCARD_* 仍可覆蓋；卡密密鑰絕不能留空——
// 空值每次啓動隨機，重啓後已存卡密不可解）。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
)

const bootstrapConfigTemplate = `# ZCard 零配置首啓自動生成（%s）
# 引導載體：瀏覽器打開 http://<本機IP>:8000/install 在嚮導中選擇數據庫完成安裝。
# 選 PG/MySQL 時嚮導會改寫本文件並自重啓；手工調整參考 config.example.yaml。

server:
  http:
    addr: 0.0.0.0:8000
    timeout: 30s
  # gRPC 留空 = 不啓用（單機 HTTP 部署；容器環境迴環綁定受限時尤其適用）
  grpc:
    addr: ""
    timeout: 30s
  migrate_on_start: true
  admin_base_path: ""

data:
  database:
    driver: sqlite
    source: file:data/zcard.db
    max_open_conns: 20
    max_idle_conns: 5
  # Redis 留空：隊列同步降級（零依賴起步）；嚮導選 PG/MySQL 時填寫並落盤
  redis:
    addr: ""
    read_timeout: 0.2s
    write_timeout: 0.2s
  worker_concurrency: 5

security:
  jwt_admin_key: "%s"
  jwt_user_key: "%s"
  card_key: "%s"
  data_key: "%s"

tenancy:
  mode: row
  main_domain: ""

log:
  level: info
  format: text
`

// randomHex n 字節加密隨機數的 hex（卡密/數據密鑰 32 字節 = 64 字符）。
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand 失敗: %v", err))
	}
	return hex.EncodeToString(buf)
}

// ensureBootstrapConfig 配置缺失時生成引導配置（已存在則不動）。
// 返回是否新生成；目錄不可寫等失敗給出可行動的錯誤信息。
func ensureBootstrapConfig(confDir string) (bool, error) {
	path := filepath.Join(confDir, "config.yaml")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return false, fmt.Errorf("創建配置目錄 %s 失敗（當前目錄不可寫？可改用 -conf 指定）: %w", confDir, err)
	}
	content := fmt.Sprintf(bootstrapConfigTemplate,
		time.Now().Format("2006-01-02 15:04:05"),
		randomHex(32), randomHex(32), randomHex(32), randomHex(32),
	)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return false, fmt.Errorf("寫入引導配置 %s 失敗: %w", path, err)
	}
	return true, nil
}

// ensureSQLiteDir SQLite 文件庫的父目錄自動創建（驅動不建目錄；對自定義
// file: 路徑的既有部署同樣生效，目錄已存在時冪等）。
func ensureSQLiteDir(bc *conf.Bootstrap) {
	if bc == nil || bc.Data == nil || bc.Data.Database == nil {
		return
	}
	db := bc.Data.Database
	if !strings.EqualFold(db.Driver, "sqlite") || !strings.HasPrefix(db.Source, "file:") {
		return
	}
	p := strings.TrimPrefix(db.Source, "file:")
	if i := strings.IndexByte(p, '?'); i >= 0 { // 去 ?params 後綴
		p = p[:i]
	}
	if dir := filepath.Dir(p); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
}
