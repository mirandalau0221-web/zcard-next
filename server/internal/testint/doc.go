// Package testint CI 集成測試骨架（， 方言三線矩陣的 MySQL/PG 線）。
//
// 全部用例掛 `-tags=integration`（make test 單元線零影響）；DSN 環境變量
// ZCARD_TEST_MYSQL_DSN / ZCARD_TEST_PG_DSN 未配置時自動 Skip。骨架細節見
// testint.go（同目錄，tag 內）。本文件無 tag——保證包在不帶 tag 時仍可編譯
// （go test ./... 不因「全文件被排除」報 setup failed）。
package testint
