// Package migrations 內嵌 Atlas 版本化遷移（go:embed 編譯期嵌入，單二進制交付）。
//
// 每方言獨立遷移線（ADR-D18）：migrations/{sqlite,mysql,postgres}/。
// 生成：make migrate-diff DIALECT=<dialect> NAME=<變更名>（經 docker dev-db + ent NamedDiff）。
// 啓動時由 internal/data.ApplyMigrations 應用（加鎖串行，失敗拒絕啓動，§10.4）。
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed sqlite mysql postgres
var files embed.FS

// FS 返回指定方言的遷移文件系統（目錄不存在報錯——遷移線缺失即構建期問題）。
func FS(dialect string) (fs.FS, error) {
	dir := fmt.Sprintf("%s", dialect)
	if _, err := fs.ReadDir(files, dir); err != nil {
		return nil, fmt.Errorf("migrations: 方言 %q 遷移目錄缺失: %w", dialect, err)
	}
	return fs.Sub(files, dir)
}
