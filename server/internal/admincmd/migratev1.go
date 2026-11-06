package admincmd

// migrate-from-v1 子命令入口（《數據遷移工具開發計劃》P0：骨架 + preflight）。
//
//	zcard migrate-from-v1 --old-env <1.x 部署目錄> [--conf configs] [--dry-run]
//	zcard migrate-from-v1 --dsn-old 'user:pass@tcp(host:3306)/zcard' ...
//
// 密鑰優先級：--old-app-key/--old-card-key > 環境變量 ZCARD_V1_APP_KEY/ZCARD_V1_CARD_KEY
// > --old-env 的 .env > 源庫 settings（卡密鑰匙）。
// 實跑（非 dry-run/verify-only）需交互確認或 --yes——這是唯一批量寫庫的子命令。

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/migratev1"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
)

// RunMigrateV1 migrate-from-v1 子命令入口（main 分發調用）。
func RunMigrateV1(args []string) error {
	fs := flag.NewFlagSet("migrate-from-v1", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "2.0 配置目錄（決定目標庫與密鑰）")
	dsnOld := fs.String("dsn-old", "", "1.x 源庫 DSN（mysql；與 --old-env 二選一）")
	oldEnv := fs.String("old-env", "", "1.x 部署目錄（讀 .env 自動裝配 DSN/APP_KEY/CARD_ENCRYPTION_KEY/APP_TIMEZONE）")
	appKey := fs.String("old-app-key", "", "1.x APP_KEY（base64: 前綴形態；優先級最高）")
	cardKey := fs.String("old-card-key", "", "1.x 卡密鑰匙（覆蓋 .env 與 settings 解析）")
	phase := fs.String("phase", "all", "遷移階段：0-6 區間 / 逗號列表 / all")
	batch := fs.Int("batch", 1000, "每批行數")
	dryRun := fs.Bool("dry-run", false, "只巡檢與產出報告，不寫目標庫")
	verifyOnly := fs.Bool("verify-only", false, "僅對已遷移數據跑校驗（P6 交付）")
	reportDir := fs.String("report-dir", "", "報告目錄（默認 ./migrate-report-<時間戳>）")
	onError := fs.String("on-error", "continue", "行級錯誤策略：continue | abort")
	visitDays := fs.Int("visit-days", 90, "visit_logs 聚合保留天數（0=不遷）")
	auditMonths := fs.Int("audit-months", 12, "security_audit_logs 保留月數（0=全量，-1=不遷）")
	sample := fs.Int("sample", 100, "卡密抽樣比對數量")
	skipMAC := fs.Bool("skip-mac", false, "跳過載荷 MAC 校驗（僅限密鑰疑似損壞的搶救場景）")
	oldStorage := fs.String("old-storage", "", "1.x 部署根目錄（媒體文件源：<root>/storage/app/public）")
	yes := fs.Bool("yes", false, "跳過交互確認（腳本化運行）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsnOld == "" && *oldEnv == "" {
		return fmt.Errorf("缺少源庫：--dsn-old 或 --old-env 二選一")
	}
	if *onError != "continue" && *onError != "abort" {
		return fmt.Errorf("--on-error 僅支持 continue|abort")
	}
	phases, err := migratev1.ParsePhaseSpec(*phase)
	if err != nil {
		return err
	}

	// 目標配置（確認提示用；open() 內部再次裝載）
	bc := &conf.Bootstrap{}
	if err := scanConf(*confDir, bc); err != nil {
		return err
	}
	targetDesc := "zcard 2.0"
	if bc.Data != nil && bc.Data.Database != nil {
		source := bc.Data.Database.Source
		if i := strings.Index(source, "@"); i > 0 {
			source = source[i:] // 粗脱敏：截掉用户名密碼段
		}
		if len(source) > 60 {
			source = source[:60] + "…"
		}
		targetDesc = fmt.Sprintf("zcard 2.0（%s %s）", bc.Data.Database.Driver, source)
	}

	// 源 DSN 展示（脱敏）
	srcDesc := migratev1.MaskDSN(*dsnOld)
	if *oldEnv != "" && *dsnOld == "" {
		env, perr := migratev1.ParseDotEnv(migratev1.OldEnvPath(*oldEnv))
		if perr != nil {
			return perr
		}
		srcDesc = migratev1.MaskDSN(migratev1.MySQLDSN(env))
	}

	// 交互確認：實跑前展示雙側庫與階段計劃（唯一批量寫庫的子命令，對齊 reencrypt 的安全基線）
	if !*dryRun && !*verifyOnly && !*yes {
		fmt.Printf("即將執行 1.x → 2.0 數據遷移\n  源庫  ：%s\n  目標庫：%s\n  階段  ：%s\n", srcDesc, targetDesc, migratev1.PhaseSummary(phases))
		fmt.Printf("確認請輸入 yes：")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			return fmt.Errorf("已取消")
		}
	}

	client, cleanup, err := open(*confDir)
	if err != nil {
		return err
	}
	defer cleanup()

	// 2.0 業務數據密鑰（憑據重加密 DataBox）：env > conf.security.data_key（同 reencrypt 裝配模式）
	dataKeyRaw := os.Getenv("ZCARD_DATA_KEY")
	if dataKeyRaw == "" && bc.Security != nil {
		dataKeyRaw = bc.Security.DataKey
	}
	dataKey, kerr := crypto.ParseHexKey(dataKeyRaw)
	if kerr != nil {
		return fmt.Errorf("ZCARD_DATA_KEY 不可用（supply 憑據重加密必需）: %w", kerr)
	}
	// 2.0 卡密密鑰（直髮內容/卡密重加密）：env > conf.security.card_key
	cardKeyNewRaw := os.Getenv("ZCARD_CARD_KEY")
	if cardKeyNewRaw == "" && bc.Security != nil {
		cardKeyNewRaw = bc.Security.CardKey
	}
	cardKeyNew, ckerr := crypto.ParseHexKey(cardKeyNewRaw)
	if ckerr != nil {
		return fmt.Errorf("ZCARD_CARD_KEY 不可用（直髮內容重加密必需）: %w", ckerr)
	}

	return migratev1.Run(context.Background(), client, migratev1.Options{
		DSNOld:         *dsnOld,
		OldEnvDir:      *oldEnv,
		AppKeyRaw:      *appKey,
		CardKeyRaw:     *cardKey,
		Phases:         phases,
		Batch:          *batch,
		DryRun:         *dryRun,
		VerifyOnly:     *verifyOnly,
		ReportDir:      *reportDir,
		OnError:        *onError,
		VisitDays:      *visitDays,
		AuditMonths:    *auditMonths,
		Sample:         *sample,
		SkipMAC:        *skipMAC,
		Yes:            *yes,
		TargetDesc:     targetDesc,
		DataKey:        dataKey,
		NewCardKey:     cardKeyNew,
		OldStorageRoot: *oldStorage,
	})
}
