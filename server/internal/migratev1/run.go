// Package migratev1 1.x→2.0 數據遷移內核（《數據遷移工具開發計劃》P0 交付）。
//
// P0 範圍：子命令骨架 + preflight 巡檢（連通性/表完整性/schema 版本探測/密鑰解析
// 與抽樣自檢）+ 報告框架 + v1id_maps 讀寫 + 1.x 加密載荷解密原語（laracrypt 子包，
// 由真實 Laravel 加密器生成的 golden vectors 釘死）。
// 數據搬遷執行器自 P1（身份域）起逐階段交付；本包只 import data/ent 與 platform/*
// （架構測試規則 3b 白名單內），卡密重加密經注入的 Sealer 接口複用 inventory 實現。
package migratev1

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
)

// Options 子命令選項（admincmd 解析後傳入）。
type Options struct {
	DSNOld     string // 1.x 源庫 DSN（與 OldEnvDir 二選一）
	OldEnvDir  string // 1.x 部署目錄（讀 .env 自動裝配 DSN 與密鑰）
	AppKeyRaw  string // --old-app-key（優先級最高）
	CardKeyRaw string // --old-card-key

	Phases      []int
	Batch       int
	DryRun      bool
	VerifyOnly  bool
	ReportDir   string
	OnError     string // continue | abort
	VisitDays   int
	AuditMonths int
	Sample      int
	SkipMAC     bool
	Yes         bool
	// TargetDesc 目標庫描述（admincmd 從配置裝配傳入：方言 + 庫地址），用於確認提示與報告。
	TargetDesc string
	// DataKey 2.0 業務數據密鑰（ZCARD_DATA_KEY 原始 32 字節；憑據重加密必需）。
	DataKey []byte
	// NewCardKey 2.0 卡密密鑰（ZCARD_CARD_KEY 原始 32 字節；直發內容/卡密重加密用）。
	NewCardKey []byte
	// OldStorageRoot 1.x 部署根（媒體文件源：<root>/storage/app/public）。
	OldStorageRoot string
}

// Run 遷移入口內核（client = 2.0 目標庫 ent 客戶端）。
func Run(ctx context.Context, client *ent.Client, opts Options) error {
	if opts.Batch <= 0 {
		opts.Batch = 1000
	}
	if opts.Sample <= 0 {
		opts.Sample = 100
	}
	if len(opts.Phases) == 0 {
		opts.Phases = ParsePhaseSpecDefault()
	}

	// 源裝配：--old-env 或 --dsn-old
	var env map[string]string
	dsn := opts.DSNOld
	if opts.OldEnvDir != "" {
		parsed, err := ParseDotEnv(OldEnvPath(opts.OldEnvDir))
		if err != nil {
			return err
		}
		env = parsed
		if dsn == "" {
			dsn = MySQLDSN(parsed)
		}
	}
	if dsn == "" {
		return fmt.Errorf("缺少源庫：--dsn-old <mysql-dsn> 或 --old-env <1.x 部署目錄> 二選一")
	}
	if env == nil {
		env = map[string]string{}
	}

	src, err := OpenSource(dsn)
	if err != nil {
		return err
	}
	defer src.Close()

	// preflight（dry-run 與實跑共用；verify-only 同樣先巡檢再校驗）
	report, err := RunPreflight(ctx, src, PreflightOptions{
		Env:         env,
		AppKeyRaw:   opts.AppKeyRaw,
		CardKeyRaw:  opts.CardKeyRaw,
		SampleLimit: 10,
		SkipMAC:     opts.SkipMAC,
	})
	if err != nil {
		return err
	}

	reportDir := opts.ReportDir
	if reportDir == "" {
		reportDir = fmt.Sprintf("migrate-report-%s", time.Now().Format("20060102-150405"))
	}
	rw, err := NewReportWriter(reportDir)
	if err != nil {
		return err
	}
	defer rw.Close()

	mode := "preflight"
	if opts.DryRun {
		mode = "dry-run"
	}
	meta := PreflightMeta{
		Source: MaskDSN(dsn),
		Target: opts.TargetDesc,
		Mode:   mode,
		Phases: PhaseSummary(opts.Phases),
	}
	if err := rw.WritePreflight(report, meta); err != nil {
		return err
	}

	printPreflight(report, meta, reportDir)
	if report.HasFail() {
		return fmt.Errorf("%w：詳見 %s", ErrPreflightFailed, filepath.Join(reportDir, "report.md"))
	}

	// 數據搬遷：按 opts.Phases 編排（已交付：P0 系統域 / P1 身份域；P2+ 逐階段交付）
	m := NewMigrator(src, client, NewIDMapper(client), rw, opts, report.Timezone)
	m.AppKey = report.AppKey
	m.CardKey = report.CardKey
	m.DataKey = opts.DataKey
	m.NewCardKey = opts.NewCardKey

	if opts.VerifyOnly {
		res, err := m.RunVerify(ctx, opts.Sample)
		if err != nil {
			return err
		}
		if err := rw.WriteVerify(res, meta); err != nil {
			return err
		}
		for _, c := range res.Checks {
			fmt.Printf("  [%s] %s：%s\n", c.Status, c.Name, c.Message)
		}
		for k, v := range res.Summary {
			fmt.Printf("  行數核對 %s：%s\n", k, v)
		}
		if !res.Passed {
			return fmt.Errorf("校驗未通過：詳見 %s", filepath.Join(reportDir, "report.md"))
		}
		fmt.Printf("\n校驗通過。報告目錄：%s\n", reportDir)
		return nil
	}

	notDelivered := []string{}
	for _, id := range opts.Phases {
		var err error
		switch id {
		case 0:
			err = m.MigrateSystem(ctx)
		case 1:
			err = m.MigrateIdentity(ctx)
		case 2:
			err = m.MigrateCatalog(ctx)
		case 3:
			err = m.MigrateInventory(ctx)
		case 4:
			err = m.MigrateTrade(ctx)
		case 5:
			err = m.MigrateMoney(ctx)
		case 6:
			if err := m.MigrateReseller(ctx); err != nil {
				return err
			}
			if err := m.MigrateSuppliers(ctx); err != nil {
				return err
			}
			err = m.MigrateContent(ctx)
		default:
			if p, ok := PhaseByID(id); ok {
				notDelivered = append(notDelivered, fmt.Sprintf("P%d %s", p.ID, p.Name))
			}
		}
		if err != nil {
			_ = rw.WriteStats(m.Stats(), meta)
			return fmt.Errorf("P%d 執行失敗: %w（已遷移進度可重跑續接，報告：%s）", id, err, reportDir)
		}
	}

	if err := rw.WriteStats(m.Stats(), meta); err != nil {
		return err
	}
	fmt.Printf("\n── 遷移統計 ──\n")
	for name, t := range m.Stats().Tables {
		fmt.Printf("  %-18s 遷移 %d / 跳過 %d / 失敗 %d%s\n", name, t.Migrated, t.SkippedExists, t.Failed, plannedSuffix(t.Planned))
	}
	if len(notDelivered) > 0 {
		fmt.Printf("\n注意：%s 尚未交付（按計劃後續階段實現），本次未執行。\n", strings.Join(notDelivered, "、"))
	}
	fmt.Printf("\n%s 完成。報告目錄：%s\n", map[bool]string{true: "dry-run", false: "遷移"}[opts.DryRun], reportDir)
	return nil
}

func plannedSuffix(p int64) string {
	if p > 0 {
		return fmt.Sprintf("（源共 %d 行）", p)
	}
	return ""
}

func printPreflight(r *PreflightReport, meta PreflightMeta, dir string) {
	fmt.Printf("── ZCard 1.x → 2.0 遷移預檢 ──\n")
	src := meta.Source
	if r.ServerVersion != "" {
		src += "（MySQL " + r.ServerVersion + "）"
	}
	fmt.Printf("源庫：%s\n", src)
	for _, c := range r.Checks {
		fmt.Printf("  [%s] %s：%s\n", c.Status, c.Name, c.Message)
	}
	fmt.Printf("報告：%s\n", dir)
}

// ParsePhaseSpecDefault 空 --phase 時的全階段。
func ParsePhaseSpecDefault() []int {
	ids, _ := ParsePhaseSpec("all")
	return ids
}

// MaskDSN 脱敏 DSN（隱去密碼，用於日誌/確認提示/報告）。
func MaskDSN(dsn string) string {
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return "mysql:<無法解析的 DSN>"
	}
	if cfg.Passwd != "" {
		cfg.Passwd = "****"
	}
	return cfg.FormatDSN()
}
