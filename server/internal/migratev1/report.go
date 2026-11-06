package migratev1

// 遷移報告輸出：report.md（人讀）+ report.json（腳本比對）+ errors.jsonl（失敗行流水）。
// P0 承載 preflight 報告；P1 起各階段向同一目錄追加章節。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReportWriter 報告目錄寫入器。
type ReportWriter struct {
	dir      string
	errFile  *os.File
	errCount int
}

// NewReportWriter 創建報告目錄（默認名 migrate-report-<ts> 由調用方決定）。
func NewReportWriter(dir string) (*ReportWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("創建報告目錄失敗: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "errors.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &ReportWriter{dir: dir, errFile: f}, nil
}

// Dir 報告目錄。
func (w *ReportWriter) Dir() string { return w.dir }

// Close 關閉文件句柄。
func (w *ReportWriter) Close() error { return w.errFile.Close() }

// ErrorCount 累計失敗行數。
func (w *ReportWriter) ErrorCount() int { return w.errCount }

// AddError 記錄一條失敗行（表名 + 1.x 舊 ID + 原因；P1 起數據階段使用）。
func (w *ReportWriter) AddError(table string, oldID uint64, msg string) {
	w.errCount++
	rec := map[string]any{
		"time":    time.Now().Format(time.RFC3339),
		"table":   table,
		"old_id":  oldID,
		"message": msg,
	}
	b, _ := json.Marshal(rec)
	_, _ = w.errFile.Write(append(b, '\n'))
}

// WriteJSON 通用 JSON 附件輸出。
func (w *ReportWriter) WriteJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, name), append(b, '\n'), 0o644)
}

// WritePreflight 輸出 preflight 的 report.md + report.json。
func (w *ReportWriter) WritePreflight(p *PreflightReport, meta PreflightMeta) error {
	if err := w.WriteJSON("report.json", map[string]any{"meta": meta, "preflight": p}); err != nil {
		return err
	}
	var md strings.Builder
	ver := ""
	if p.ServerVersion != "" {
		ver = "（MySQL " + p.ServerVersion + "）"
	}
	fmt.Fprintf(&md, "# ZCard 1.x → 2.0 遷移預檢報告\n\n")
	fmt.Fprintf(&md, "- 生成時間：%s\n", p.GeneratedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&md, "- 源庫：%s%s\n", meta.Source, ver)
	fmt.Fprintf(&md, "- 目標庫：%s\n", meta.Target)
	fmt.Fprintf(&md, "- 模式：%s\n", meta.Mode)
	fmt.Fprintf(&md, "- 階段計劃：%s\n\n", meta.Phases)

	fmt.Fprintf(&md, "## 巡檢結論：%s\n\n", verdict(p))
	fmt.Fprintf(&md, "| 狀態 | 檢查項 | 説明 |\n|---|---|---|\n")
	for _, c := range p.Checks {
		fmt.Fprintf(&md, "| %s | %s | %s |\n", icon(c.Status), c.Name, c.Message)
	}

	fmt.Fprintf(&md, "\n## 卡密抽樣\n\n")
	fmt.Fprintf(&md, "抽樣 %d 條：密文 %d（解密失敗 %d），明文 %d。\n",
		p.CardSampled, p.CardEncrypted, p.CardDecryptFailed, p.CardPlaintext)

	fmt.Fprintf(&md, "\n## 密鑰\n\n")
	fmt.Fprintf(&md, "- APP_KEY：%s\n", keyState(p.AppKey != nil))
	fmt.Fprintf(&md, "- 卡密鑰匙：%s\n", keyState(p.CardKey != nil))

	fmt.Fprintf(&md, "\n## 規模概覽\n\n")
	fmt.Fprintf(&md, "| 表 | 行數 |\n|---|---|\n")
	for _, t := range []string{"users", "products", "cards", "orders", "payments", "bills"} {
		if n, ok := p.TableCounts[t]; ok {
			fmt.Fprintf(&md, "| %s | %d |\n", t, n)
		}
	}

	if p.Timezone != "" {
		fmt.Fprintf(&md, "\n> 時區口徑：APP_TIMEZONE=%s（時間列將按此轉 UTC）\n", p.Timezone)
	}
	return os.WriteFile(filepath.Join(w.dir, "report.md"), []byte(md.String()), 0o644)
}

// PreflightMeta 報告元信息。
type PreflightMeta struct {
	Source string `json:"source"` // 脱敏 DSN
	Target string `json:"target"` // 目標庫描述（方言 + 庫名）
	Mode   string `json:"mode"`   // dry-run / preflight
	Phases string `json:"phases"`
}

// WriteStats 輸出遷移統計（report.json 內嵌 + report.md 追加章節）。
func (w *ReportWriter) WriteStats(st *Stats, meta PreflightMeta) error {
	if err := w.WriteJSON("stats.json", map[string]any{"meta": meta, "tables": st.Tables}); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "\n## 遷移統計（%s）\n\n", meta.Mode)
	fmt.Fprintf(&md, "| 表 | 遷移 | 冪等跳過 | 失敗 | 源行數 |\n|---|---|---|---|---|\n")
	for _, name := range sortedTableNames(st) {
		t := st.Tables[name]
		planned := "-"
		if t.Planned > 0 {
			planned = fmt.Sprintf("%d", t.Planned)
		}
		fmt.Fprintf(&md, "| %s | %d | %d | %d | %s |\n", name, t.Migrated, t.SkippedExists, t.Failed, planned)
	}
	f, err := os.OpenFile(filepath.Join(w.dir, "report.md"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(md.String())
	return err
}

func openAppend(dir, name string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

func sortedTableNames(st *Stats) []string {
	names := make([]string, 0, len(st.Tables))
	for n := range st.Tables {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func verdict(p *PreflightReport) string {
	if p.HasFail() {
		return "❌ 未通過（存在致命項，拒絕遷移）"
	}
	return "✅ 通過（可進入遷移）"
}

func icon(s CheckStatus) string {
	switch s {
	case StatusOK:
		return "✅"
	case StatusWarn:
		return "⚠️"
	case StatusFail:
		return "❌"
	default:
		return "ℹ️"
	}
}

func keyState(ok bool) string {
	if ok {
		return "✅ 已解析"
	}
	return "⚠️ 未提供"
}
