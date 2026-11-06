package migratev1

// 1.x 時間口徑轉換：MySQL DATETIME 無時區（PHP 端 APP_TIMEZONE，默認 UTC）→
// 2.0 全 UTC。源掃描一律以字符串讀出（parseTime=false），經本文件顯式轉換。

import (
	"fmt"
	"strings"
	"time"
)

// srcTZ 源時區（來自 .env APP_TIMEZONE；空 = UTC）。
func srcTimezone(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// parseNaiveTime 解析 '2006-01-02 15:04:05[.fff]'（源 DATETIME 字符串），
// 在源時區解釋後轉 UTC。空串/NULL/MySQL 零日期返回零值與 false
// （零日期多見於繞過 ORM 的直插行，按未記錄時間處理而非報錯）。
func parseNaiveTime(s string, tz *time.Location) (time.Time, bool) {
	if s == "" || s == "NULL" || strings.HasPrefix(s, "0000-00-00") {
		return time.Time{}, false
	}
	// go-sql-driver 字符串形態；sqlite 測試源同格式
	for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, tz); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// mustTime 同 parseNaiveTime，解析失敗時返回錯誤而非靜默（時間列損壞應暴露）。
func mustTime(s string, tz *time.Location) (time.Time, bool, error) {
	if s == "" || s == "NULL" || strings.HasPrefix(s, "0000-00-00") {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, tz); err == nil {
			return t.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("時間字符串無法解析: %q", s)
}
