package db

import "fmt"

// Capabilities 方言能力開關（運行時判定，非編譯期）。
// 業務代碼據以下降級——顯式分支，不是運行時碰運氣（降級鐵律，ADR-D20）。
type Capabilities struct {
	SupportsRLS          bool // 行級安全策略（僅 PG；MySQL/SQLite 靠 Ent 攔截器單層保障）
	SupportsSchema       bool // 獨立 schema 命名空間（PG 原生；MySQL database==schema；SQLite attach 模擬默認不用）
	SupportsJSONB        bool // JSONB 高級查詢/GIN 索引（PG；MySQL JSON 弱等價；SQLite JSON1 弱）
	SupportsReturning    bool // INSERT/UPDATE ... RETURNING（PG/SQLite；MySQL 無）
	SupportsILIKE        bool // ILIKE（PG；MySQL/SQLite 用 LOWER 降級）
	SupportsForUpdate    bool // SELECT ... FOR UPDATE（PG/MySQL；SQLite 無，走 BEGIN IMMEDIATE + CAS）
	SupportsSkipLocked   bool // FOR UPDATE SKIP LOCKED（PG/MySQL8；高併發鎖卡利器）
	SupportsArray        bool // 數組列（PG；MySQL/SQLite 用 JSON 模擬）
	SupportsPartialIndex bool // 部分索引（PG/SQLite；MySQL 無）
	SupportsWindowFunc   bool // 窗口函數（三方言均支持，SQLite 需 3.25+）
}

// Capabilities 返回該方言的能力開關。
func (d Dialect) Capabilities() Capabilities {
	switch d {
	case Postgres:
		return Capabilities{
			SupportsRLS: true, SupportsSchema: true, SupportsJSONB: true,
			SupportsReturning: true, SupportsILIKE: true, SupportsForUpdate: true,
			SupportsSkipLocked: true, SupportsArray: true, SupportsPartialIndex: true,
			SupportsWindowFunc: true,
		}
	case MySQL:
		return Capabilities{
			SupportsForUpdate:  true,
			SupportsSkipLocked: true,
			SupportsWindowFunc: true,
		}
	case SQLite:
		return Capabilities{
			// SQLite 無行鎖：單寫者 + BEGIN IMMEDIATE 串行化 + UPDATE...WHERE status=CAS（§5.20.3）
			SupportsReturning:    true,
			SupportsPartialIndex: true,
			SupportsWindowFunc:   true,
		}
	}
	return Capabilities{}
}

// ErrUnsupported 能力不支持時返回的顯式錯誤（禁止靜默生成非法 SQL）。
type ErrUnsupported struct {
	Dialect Dialect
	Cap     string
}

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("db: %s does not support %s（顯式降級分支，不是靜默失敗）", e.Dialect, e.Cap)
}

// Require 斷言能力可用，不可用返回 *ErrUnsupported（調用方走降級路徑或明確報錯）。
func (d Dialect) Require(cap string, ok bool) error {
	if !ok {
		return &ErrUnsupported{Dialect: d, Cap: cap}
	}
	return nil
}
