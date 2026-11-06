package schema

// mysqlTime 時間字段的 MySQL 方言列類型（全 schema 統一引用）。
// 《數據庫架構設計.md》§0 類型映射釘死 MySQL `DATETIME(3)`（毫秒精度，無 2038 上限）；
// ent 對 MySQL 的默認 time 類型是 `timestamp`（秒精度 + 2038 上限），與文檔偏差。
// PG（timestamptz）與 SQLite（ISO8601 文本）由驅動默認產出正確類型。
//
// 規則（由 schema_test.go 強制）：新增 time 字段一律寫成
//	field.Time("xxx").SchemaType(mysqlTime)
import "entgo.io/ent/dialect"

var mysqlTime = map[string]string{
	dialect.MySQL: "datetime(3)",
}
