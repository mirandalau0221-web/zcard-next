package db

import (
	"fmt"
	"strings"
)

// SQL 跨方言 SQL 原語構造器——internal/data/report 層唯一允許的裸 SQL 構造入口。
// 業務代碼一律走 Ent builder；在業務代碼拼接本包產出的方言 SQL 同樣被架構測試攔截。
type SQL struct{ d Dialect }

// New 按方言構造。
func New(d Dialect) *SQL { return &SQL{d: d} }

// Dialect 返回當前方言。
func (s *SQL) Dialect() Dialect { return s.d }

// Placeholder 第 i 個佔位符（從 1 起）：MySQL/SQLite `?`，PG `$i`。
func (s *SQL) Placeholder(i int) string {
	if s.d == Postgres {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// Placeholders 生成 n 個佔位符（逗號分隔），用於 IN (...)。
func (s *SQL) Placeholders(n, from int) string {
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = s.Placeholder(from + i)
	}
	return strings.Join(parts, ", ")
}

// QuoteIdent 引用標識符：MySQL “ `id` “，PG/SQLite `"id"`。
func (s *SQL) QuoteIdent(id string) string {
	if s.d == MySQL {
		return "`" + id + "`"
	}
	return `"` + id + `"`
}

// ILike 大小寫不敏感匹配表達式（片段內含佔位符）。
func (s *SQL) ILike(col string) string {
	if s.d == Postgres {
		return col + " ILIKE ?"
	}
	return "LOWER(" + col + ") LIKE LOWER(?)"
}

// Concat 字符串拼接表達式。
func (s *SQL) Concat(exprs ...string) string {
	if s.d == MySQL {
		return "CONCAT(" + strings.Join(exprs, ", ") + ")"
	}
	return "(" + strings.Join(exprs, " || ") + ")"
}

// DateAdd 日期加減表達式：unit 取 second/minute/hour/day。
func (s *SQL) DateAdd(col, unit string, n int) string {
	switch s.d {
	case MySQL:
		return fmt.Sprintf("DATE_ADD(%s, INTERVAL %d %s)", col, n, strings.ToUpper(unit))
	case Postgres:
		return fmt.Sprintf("(%s + (%d * interval '1 %s'))", col, n, unit)
	default: // SQLite
		sign := "+"
		if n < 0 {
			sign, n = "-", -n
		}
		return fmt.Sprintf("datetime(%s, '%s%d %s')", col, sign, n, unit)
	}
}

// Bool 布爾字面量：MySQL 1/0，PG/SQLite TRUE/FALSE。
func (s *SQL) Bool(v bool) string {
	if s.d == MySQL {
		if v {
			return "1"
		}
		return "0"
	}
	if v {
		return "TRUE"
	}
	return "FALSE"
}

// Paginate 分頁子句：MySQL `LIMIT offset, limit`，PG/SQLite `LIMIT limit OFFSET offset`。
func (s *SQL) Paginate(limit, offset int) string {
	if s.d == MySQL {
		return fmt.Sprintf("LIMIT %d, %d", offset, limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

// ForUpdate 行鎖子句（下單鎖卡/支付回調熱路徑）。
// SQLite 返回空串：無行鎖，走 BEGIN IMMEDIATE 單寫者 + affected rows CAS 語義（§5.20.3）。
func (s *SQL) ForUpdate(skipLocked bool) string {
	switch s.d {
	case MySQL, Postgres:
		if skipLocked {
			return "FOR UPDATE SKIP LOCKED"
		}
		return "FOR UPDATE"
	}
	return ""
}

// NowSQL 當前時間表達式。
func (s *SQL) NowSQL() string {
	if s.d == SQLite {
		return "CURRENT_TIMESTAMP"
	}
	return "NOW()"
}
