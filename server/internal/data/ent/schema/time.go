package schema

import "time"

// nowUTC 統一 UTC 時間（規劃 §3.4：時間一律 time.Time（UTC 存儲））。
func nowUTC() time.Time { return time.Now().UTC() }
