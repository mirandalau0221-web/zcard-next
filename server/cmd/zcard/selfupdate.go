// self-update 子命令（；2026-09 方案 / 重構）：在線更新 CLI 面。
//
//	zcard self-update [--check] [--rollback] [-conf <dir>]
//
// [-source auto|github|accel|static] [-repo <owner/repo>]
// [-accel <prefix[,prefix...]>] [-base <url>] [-channel stable|beta]
// [-pubkey <hex>] [-y]
//
//	zcard self-update genkey # 發行側密鑰對
//	zcard self-update sign --key <file> --dir <dist> --version vX.Y.Z [--notes-file <md>]
//
// 安全模型見 platform/updater 包註釋與 ；
// 非 TTY 且無 -y 拒絕執行（防腳本誤觸）。
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/mods/update"
	"github.com/NovaWorks/zcard-next/server/internal/platform/updater"
)

func runSelfUpdate(args []string) error {
	// 發行側工具子形態（與更新執行互斥）
	if len(args) > 0 {
		switch args[0] {
		case "genkey":
			return runGenKey()
		case "sign":
			return runSign(args[1:])
		}
	}

	fs := flag.NewFlagSet("self-update", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	check := fs.Bool("check", false, "只檢查更新不執行（輸出 JSON）")
	rollback := fs.Bool("rollback", false, "回滾到上一版本（zcard.prev）")
	source := fs.String("source", "auto", "更新源 auto|github|accel|static（auto=直連探測，不通走加速）")
	repo := fs.String("repo", updater.DefaultRepo, "發行倉庫 owner/repo")
	accel := fs.String("accel", "", "加速前綴（逗號分隔多個；source=accel/auto 時用，缺省內置列表）")
	base := fs.String("base", "", "靜態源基址（source=static 必填）")
	channel := fs.String("channel", "stable", "更新通道 stable|beta（beta 僅直連/靜態源）")
	pubkeyHex := fs.String("pubkey", "", "更新公鑰 hex（空=編譯默認；輪換期過渡用）")
	yes := fs.Bool("y", false, "跳過確認（非 TTY 必須顯式指定）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if updater.IsContainer() && (!*check || *rollback) {
		return updater.ErrContainerUpdate
	}

	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("self-update: 定位當前二進制失敗: %w", err)
	}
	binPath, _ = filepath.EvalSymlinks(binPath)

	if *rollback {
		if err := updater.Rollback(binPath); err != nil {
			return fmt.Errorf("self-update: 回滾失敗: %w", err)
		}
		fmt.Println("已回滾到上一版本（systemd 將於下次重啓生效；手動環境請重啓服務）")
		return nil
	}

	pubHex := strings.TrimSpace(*pubkeyHex)
	if pubHex == "" {
		pubHex = updater.DefaultPublicKeyHex
	}
	pub, err := updater.PublicKey(pubHex)
	if err != nil {
		return fmt.Errorf("self-update: %v（dev 構建未注入公鑰，或用 -pubkey 指定）", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cfg := updater.SourceConfig{
		Mode: *source, Repo: *repo, StaticBase: *base, Channel: *channel,
	}
	if v := strings.TrimSpace(*accel); v != "" {
		cfg.Accels = strings.Split(v, ",")
	}
	cli, outcome, err := updater.ResolveSource(ctx, cfg, 5*time.Second)
	if err != nil {
		return fmt.Errorf("self-update: %w", err)
	}
	m, manifestURL, err := cli.FetchManifest(ctx, *channel, pub)
	if err != nil {
		return fmt.Errorf("self-update: %w（源 %s）", err, outcome.SourceDesc())
	}

	if *check {
		out, err := json.MarshalIndent(map[string]any{
			"current": orDev(Version), "latest": m.Version,
			"channel": m.Channel, "notes": m.Notes, "manifest": manifestURL,
			"source":           outcome.SourceDesc(),
			"update_available": updater.HasUpdate(m.Version, Version),
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}

	if m.Version == Version {
		fmt.Printf("已是最新版本 %s\n", Version)
		return nil
	}
	// 版本單調：拒絕降級與非 semver 目標（防降級攻擊/誤更到 dev）
	if !updater.IsSemver(m.Version) || updater.CompareSemver(m.Version, Version) <= 0 {
		return fmt.Errorf("self-update: %w（當前 %s ↔ 目標 %s）", updater.ErrRefuseUpdate, orDev(Version), m.Version)
	}

	assetName := "zcard-" + runtime.GOOS + "-" + runtime.GOARCH
	var assetSize int64
	for _, f := range m.Files {
		if f.Name == assetName {
			assetSize = f.Size
		}
	}
	fmt.Printf("發現新版本 %s → %s（源 %s）\n\n變更記錄:\n%s\n\n", orDev(Version), m.Version, outcome.SourceDesc(), m.Notes)
	if !*yes {
		if !isTTY() {
			return fmt.Errorf("self-update: 非 TTY 環境需 -y 顯式確認")
		}
		fmt.Print("確認更新? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			fmt.Println("已取消")
			return nil
		}
	}

	// 磁盤預檢（方案 ）：產物 + 64MB 餘量（DB 備份另計由 dump 工具自行失敗）
	if err := updater.CheckDiskSpace(filepath.Dir(binPath), assetSize+64<<20); err != nil {
		return fmt.Errorf("self-update: %w", err)
	}

	// 備份（方言感知：SQLite VACUUM INTO / mysqldump / pg_dump；失敗即中止）
	bc, err := loadBootstrap(*confDir)
	if err != nil {
		return err
	}
	backupDir := updater.BackupPath(binPath, orDev(Version))
	if err := update.BackupDatabase(ctx, bc.Data, backupDir); err != nil {
		return fmt.Errorf("self-update: 備份失敗，中止更新: %w", err)
	}
	// 二進制一併入備份目錄（DB 恢復 + 二進制回退雙保險）
	_ = copyFile(binPath, filepath.Join(backupDir, "zcard.old"))

	// 下載 → 驗籤落位（落盤流式，124MB 大產物零內存，方案 ）
	newPath := filepath.Join(filepath.Dir(binPath), "zcard.new")
	if err := downloadToFile(ctx, cli, m, assetName, newPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("self-update: %w", err)
	}
	if err := updater.ApplyFile(binPath, orDev(Version), m.Version, newPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("self-update: %w", err)
	}
	fmt.Printf("已更新到 %s（備份: %s）\n", m.Version, backupDir)

	// 重啓：systemd 安裝態自動；失敗給手動指引（新版啓動後健康門自動確認/回滾）
	if err := updater.RestartService("zcard"); err != nil {
		fmt.Println("!! " + err.Error())
	}
	return nil
}

// runGenKey 生成更新密鑰對（發行側離線保管私鑰；公鑰 -X 注入或 -pubkey 過渡）。
func runGenKey() error {
	pub, priv, err := updater.GenerateKeyPair()
	if err != nil {
		return err
	}
	fmt.Printf("公鑰（編譯註入 -X updater.DefaultPublicKeyHex）:\n  %s\n", hex.EncodeToString(pub))
	fmt.Printf("私鑰（離線保管，泄露即換鑰發版）:\n  %s\n", hex.EncodeToString(priv))
	return nil
}

// runSign 掃描 dist 目錄產物並生成簽名清單 update.json（CI 發版步驟）。
func runSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyFile := fs.String("key", "", "私鑰文件（hex；或 ZCARD_SIGN_KEY 環境變量）")
	dir := fs.String("dir", "dist", "產物目錄")
	version := fs.String("version", "", "版本 tag（vX.Y.Z，必填）")
	channel := fs.String("channel", "stable", "通道 stable|beta")
	notesFile := fs.String("notes-file", "", "changelog markdown 文件")
	historyFile := fs.String("history-file", "", "歷史版本 changelog JSON 文件（[{version,notes,date}]；後台更新頁歷史面板數據源）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || !updater.IsSemver(*version) {
		return fmt.Errorf("sign: --version 必須為 vX.Y.Z semver")
	}
	keyHex := *keyFile
	if keyHex == "" {
		keyHex = os.Getenv("ZCARD_SIGN_KEY")
	} else {
		raw, err := os.ReadFile(keyHex)
		if err != nil {
			return err
		}
		keyHex = strings.TrimSpace(string(raw))
	}
	if keyHex == "" {
		return fmt.Errorf("sign: 缺少私鑰（--key 或 ZCARD_SIGN_KEY）")
	}
	rawKey, err := hex.DecodeString(keyHex)
	if err != nil || len(rawKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("sign: 私鑰格式非法")
	}

	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	var files []updater.FileEntry
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "update.json" || !strings.HasPrefix(name, "zcard-") {
			continue
		}
		p := filepath.Join(*dir, name)
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		sum, size, err := updater.HashFile(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		files = append(files, updater.FileEntry{Name: name, SHA256: sum, Size: size})
	}
	if len(files) == 0 {
		return fmt.Errorf("sign: 目錄 %s 無 zcard-* 產物", *dir)
	}
	notes := ""
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			return err
		}
		notes = string(b)
	}
	// 歷史版本 changelog（更新頁歷史面板）：[{version, notes, date}] 寬鬆解析
	var history []updater.ReleaseNote
	if *historyFile != "" {
		b, err := os.ReadFile(*historyFile)
		if err != nil {
			return err
		}
		var raw []struct {
			Version string `json:"version"`
			Notes   string `json:"notes"`
			Date    string `json:"date"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return fmt.Errorf("sign: history 文件須為 [{version,notes,date}] JSON: %w", err)
		}
		for _, h := range raw {
			if !updater.IsSemver(h.Version) {
				return fmt.Errorf("sign: history 含非 semver 版本 %q", h.Version)
			}
			history = append(history, updater.ReleaseNote{Version: h.Version, Notes: h.Notes, IssuedAt: h.Date})
		}
	}
	out, err := updater.SignManifest(ed25519.PrivateKey(rawKey), *version, *channel, notes, files, history)
	if err != nil {
		return err
	}
	dst := filepath.Join(*dir, "update.json")
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("已簽名 %d 個產物 → %s（歷史記錄 %d 條）\n", len(files), dst, len(history))
	return nil
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// downloadToFile 流式落盤下載（DownloadAsset 邊寫邊哈希校驗；失敗文件由調用方刪）。
func downloadToFile(ctx context.Context, cli *updater.Client, m *updater.Manifest, name, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := cli.DownloadAsset(ctx, m, name, f, func(received, total int64) {
		if total > 0 {
			fmt.Printf("\r下載中 %d/%d MB", received>>20, total>>20)
		}
	}); err != nil {
		_ = f.Close()
		return err
	}
	fmt.Println()
	return f.Close()
}
