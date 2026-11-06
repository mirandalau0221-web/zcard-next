//go:build ignore

// entc 代碼生成入口（由 ent/generate.go 的 go:generate 驅動，運行於 internal/data/ent）。
//
// 與 ent CLI 等價，僅集中聲明 feature flags：
//   - sql/versioned-migration：ent/migrate 的 Diff/NamedDiff（atlas 生成遷移用）
//   - sql/upsert：跨方言 upsert（settings 等冪等寫入）
//
// 注意：MySQL 時間列統一 datetime(3) 由 schema 包的 timeCol() 助手在字段聲明處落地
// （《數據庫架構設計.md》§0 類型映射釘死 MySQL `DATETIME(3)`；ent 對 MySQL 的默認
// time 類型是 `timestamp`——秒精度且有 2038 上限，與文檔偏差，故所有 time 字段必須
// 經 timeCol() 聲明，架構上以「新增 time 字段必須用 timeCol」為 code review 檢查項）。
package main

import (
	"log"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

func main() {
	cfg := &gen.Config{
		Target:  ".",
		Package: "github.com/NovaWorks/zcard-next/server/internal/data/ent",
		Features: []gen.Feature{
			gen.FeatureVersionedMigration,
			gen.FeatureUpsert,
			gen.FeatureLock, // 查詢級行鎖（outbox relay FOR UPDATE SKIP LOCKED 等）
		},
	}
	if err := entc.Generate("./schema", cfg); err != nil {
		log.Fatalf("ent 代碼生成失敗: %v", err)
	}
}
