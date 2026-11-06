// Package admincmd 運維子命令（規劃 §10.3：打進同一二進制，容器免工具）。
//
//	zcard admin create --username admin --password <pwd> [--nickname <n>] [--role super_admin]
//	zcard admin list
//	zcard admin reset-password --username admin --password <newpwd>
//	zcard admin reset-2fa --username admin
package admincmd

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminrole"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminuser"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
)

// Run admin 子命令入口（main 分發調用）。
func Run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "reset-2fa":
		return runReset2FA(rest)
	case "create":
		return runCreate(rest)
	case "list":
		return runList(rest)
	case "reset-password":
		return runResetPassword(rest)
	default:
		return fmt.Errorf("未知 admin 子命令 %q（create | list | reset-password | reset-2fa）", sub)
	}
}

func usage() error {
	fmt.Print(`zcard admin —— 管理員運維子命令

用法:
  zcard admin reset-2fa --username <u> [--conf configs] [--yes] [--dry-run]
  zcard admin create --username <u> --password <p> [--nickname <n>] [--role super_admin] [--conf configs]
  zcard admin list [--conf configs]
  zcard admin reset-password --username <u> --password <p> [--conf configs]
`)
	return nil
}

// open 打開數據層（含內置角色種子；不執行遷移——遷移由 serve/migrate 負責）。
func open(confDir string) (*ent.Client, func(), error) {
	if confDir == "" {
		confDir = "configs"
	}
	bc := &conf.Bootstrap{}
	// 複用 file source 解析
	if err := scanConf(confDir, bc); err != nil {
		return nil, nil, err
	}
	d, cleanup, err := data.NewData(bc.Data)
	if err != nil {
		return nil, nil, err
	}
	return d.Client, cleanup, nil
}

func runCreate(args []string) error {
	fs := flag.NewFlagSet("admin create", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	username := fs.String("username", "", "登錄名")
	password := fs.String("password", "", "密碼")
	nickname := fs.String("nickname", "", "暱稱（默認同登錄名）")
	roleCode := fs.String("role", "super_admin", "角色編碼（默認 super_admin）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" {
		return fmt.Errorf("--username 與 --password 必填")
	}
	if len(*password) < 8 {
		return fmt.Errorf("密碼至少 8 位（安全基線）")
	}

	client, cleanup, err := open(*confDir)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx := context.Background()

	if err := authz.EnsureBuiltinRoles(ctx, client); err != nil {
		return fmt.Errorf("內置角色種子失敗: %w", err)
	}
	role, err := client.AdminRole.Query().Where(adminrole.Code(*roleCode)).Only(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("角色 %q 不存在（內置：super_admin/operator/support）", *roleCode)
	}
	if err != nil {
		return err
	}
	exists, err := client.AdminUser.Query().Where(adminuser.Username(*username)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("管理員 %q 已存在", *username)
	}
	hash, err := crypto.HashPassword(*password)
	if err != nil {
		return err
	}
	if *nickname == "" {
		*nickname = *username
	}
	u, err := client.AdminUser.Create().
		SetUsername(*username).
		SetPasswordHash(hash).
		SetNickname(*nickname).
		SetRoleID(role.ID).
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("管理員創建成功：%s（角色 %s，ID %d）\n", u.Username, role.Name, u.ID)
	return nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("admin list", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, cleanup, err := open(*confDir)
	if err != nil {
		return err
	}
	defer cleanup()
	rows, err := client.AdminUser.Query().Order(ent.Asc(adminuser.FieldID)).All(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("%-4s %-20s %-12s %-8s %-10s\n", "ID", "用户名", "暱稱", "啓用", "最近登錄")
	for _, r := range rows {
		last := "-"
		if !r.LastLoginAt.IsZero() {
			last = r.LastLoginAt.Local().Format("2006-01-02")
		}
		fmt.Printf("%-4d %-20s %-12s %-8v %-10s\n", r.ID, r.Username, orDash(r.Nickname), r.Enabled, last)
	}
	return nil
}

func runResetPassword(args []string) error {
	fs := flag.NewFlagSet("admin reset-password", flag.ExitOnError)
	confDir := fs.String("conf", "configs", "配置目錄")
	username := fs.String("username", "", "登錄名")
	password := fs.String("password", "", "新密碼")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" {
		return fmt.Errorf("--username 與 --password 必填")
	}
	if len(*password) < 8 {
		return fmt.Errorf("密碼至少 8 位（安全基線）")
	}
	client, cleanup, err := open(*confDir)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx := context.Background()
	u, err := client.AdminUser.Query().Where(adminuser.Username(*username)).Only(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("管理員 %q 不存在", *username)
	}
	if err != nil {
		return err
	}
	if err := identity.NewAdminUserRepoImpl(&data.Data{Client: client}).ResetPassword(ctx, u.ID, *password); err != nil {
		return err
	}
	fmt.Printf("密碼已重置：%s\n", u.Username)
	return nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
