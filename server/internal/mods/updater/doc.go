// Package updater 在線更新模塊（M2）：ed25519 簽名校驗（公鑰編譯進二進制）、
// 備份 + 原子替換 + 健康檢查失敗自動回滾；DB 遷移與二進制更新解耦
// （啓動時檢測待執行 Atlas 遷移並加鎖串行執行，失敗拒絕啓動）。
//
// 重寫 1.x 最危險組件（以 PHP 進程身份執行 git reset --hard 的高危模式）。
package updater
