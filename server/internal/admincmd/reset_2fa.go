package admincmd

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminuser"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-sql-driver/mysql"
)

func runReset2FA(args []string) error {
	fs := flag.NewFlagSet("admin reset-2fa", flag.ContinueOnError)
	confDir := fs.String("conf", "configs", "配置目錄（建議絕對路徑）")
	username := fs.String("username", "", "管理員登錄名")
	yes := fs.Bool("yes", false, "跳過交互確認")
	dry := fs.Bool("dry-run", false, "只檢查目標，不修改")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*username) == "" {
		return fmt.Errorf("--username 必填")
	}
	path, err := filepath.Abs(*confDir)
	if err != nil {
		return err
	}
	bc := &conf.Bootstrap{}
	if err = scanRecoveryConf(path, bc); err != nil {
		return err
	}
	if bc.Data == nil || bc.Data.Database == nil {
		return fmt.Errorf("缺少數據庫配置")
	}
	cfg := bc.Data.Database
	target := "配置中的數據庫"
	if cfg.Driver == "mysql" {
		if parsed, e := mysql.ParseDSN(cfg.Source); e == nil {
			target = parsed.Addr + "/" + parsed.DBName
		}
	}
	if cfg.Driver == "postgres" || cfg.Driver == "pgx" {
		if parsed, e := url.Parse(cfg.Source); e == nil && parsed.Host != "" {
			target = parsed.Host + parsed.Path
		}
	}
	// Refuse SQLite implicit creation: a mistyped working directory must never
	// look like successful recovery against an empty installation.
	if strings.HasPrefix(cfg.Driver, "sqlite") {
		source := strings.SplitN(strings.TrimPrefix(cfg.Source, "file:"), "?", 2)[0]
		source, err = url.PathUnescape(source)
		if err != nil {
			return err
		}
		if source == "" || source == ":memory:" || strings.Contains(cfg.Source, "mode=memory") {
			return fmt.Errorf("恢復命令不支持內存數據庫")
		}
		target, _ = filepath.Abs(source)
		info, err := os.Stat(source)
		if err != nil || info.IsDir() {
			return fmt.Errorf("SQLite 數據庫文件不存在或不可讀，請確認工作目錄和配置")
		}
	}
	d, cleanup, err := data.NewData(bc.Data)
	if err != nil {
		return fmt.Errorf("無法打開目標數據庫，請檢查配置")
	}
	defer cleanup()
	ctx := context.Background()
	u, err := d.Client.AdminUser.Query().Where(adminuser.Username(*username)).Only(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("管理員 %q 不存在", *username)
	}
	if err != nil {
		return fmt.Errorf("讀取管理員失敗，請確認數據庫已經完成本版本遷移: %w", err)
	}
	fmt.Printf("配置：%s\n數據庫類型：%s\n目標：%s\n賬號：%s（ID %d，啓用 %v，已綁定 %v）\n", path, cfg.Driver, target, u.Username, u.ID, u.Enabled, len(u.TotpSecret) > 0)
	if *dry {
		fmt.Println("檢查完成，未修改任何數據")
		return nil
	}
	if !*yes {
		fmt.Printf("將解除兩步驗證並註銷該賬號全部後台會話。請輸入賬號 %s 確認：", u.Username)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("未收到確認；自動化執行請明確傳入 --yes")
		}
		if strings.TrimSpace(line) != u.Username {
			return fmt.Errorf("確認不匹配，未修改")
		}
	}
	if err := identity.ResetAdminTwoFactor(ctx, d, u.ID, 0, "server CLI recovery"); err != nil {
		return fmt.Errorf("重置失敗，未完成恢復: %w", err)
	}
	fmt.Printf("已重置 %s 的兩步驗證，待確認綁定、恢復碼及舊登錄憑證已失效。密碼、權限和禁用狀態保持不變。\n", u.Username)
	return nil
}

// The standard decoder logs raw values on errors. Recovery is often invoked
// with a mistyped path; never print database files or malformed secret configs.
func scanRecoveryConf(path string, out *conf.Bootstrap) error {
	c := config.New(config.WithSource(file.NewSource(path)), config.WithDecoder(func(src *config.KeyValue, target map[string]any) error {
		if codec := encoding.GetCodec(src.Format); codec != nil {
			if err := codec.Unmarshal(src.Value, &target); err == nil {
				return nil
			}
		}
		src.Value = []byte("[redacted]")
		return fmt.Errorf("配置文件格式無效，請檢查 --conf")
	}))
	defer c.Close()
	if err := c.Load(); err != nil {
		return fmt.Errorf("無法讀取配置，請確認 --conf 指向正確的配置目錄或文件")
	}
	if err := c.Scan(out); err != nil {
		return fmt.Errorf("配置字段無效，請檢查數據庫配置")
	}
	return nil
}
