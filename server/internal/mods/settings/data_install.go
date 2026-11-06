package settings

// 安裝嚮導（）：CLI 交互式 + 事務原子（初始 settings + 內置角色種子 + 管理員）。
// 冪等：ops.installed_at 已存在即拒絕。Web /install 表單入口 前端補齊（後端邏輯同源）。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminrole"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/setting"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
)

// ErrAlreadyInstalled 已安裝（冪等拒絕）。
var ErrAlreadyInstalled = errors.New("settings.ALREADY_INSTALLED")

// installedAt 安裝時間（未安裝返回空串）。
func installedAt(ctx context.Context, d *data.Data) (string, error) {
	s, err := data.Client(ctx, d).Setting.Query().
		Where(setting.Group("ops"), setting.Key("installed_at")).Only(ctx)
	if err != nil {
		return "", err
	}
	var ts string
	if err := json.Unmarshal(s.Value, &ts); err != nil {
		return "", err
	}
	return ts, nil
}

// Installed 是否已安裝（ops.installed_at 存在判定）。
func Installed(ctx context.Context, d *data.Data) bool {
	_, err := data.Client(ctx, d).Setting.Query().
		Where(setting.Group("ops"), setting.Key("installed_at")).Only(ctx)
	return err == nil
}

// InstallInput 安裝參數（CLI 交互收集或 Web 表單）。
type InstallInput struct {
	AdminUsername string
	AdminPassword string
	SiteName      string
	SiteURL       string
}

// Install 執行安裝（單事務：寫關鍵默認 settings + installed_at + 角色/權限種子 + 管理員）。
func Install(ctx context.Context, d *data.Data, in InstallInput) error {
	if Installed(ctx, d) {
		return ErrAlreadyInstalled
	}
	if len(in.AdminPassword) < 8 {
		return errors.New("settings.WEAK_PASSWORD：管理員密碼至少 8 位")
	}
	return data.Tx(ctx, d, func(ctx context.Context) error {
		// 事務句柄（通道 B）——舊代碼誤用閉包外 d.Client：每條語句獨立連接自動提交，
		// 中途失敗半提交（settings/roles 已落、管理員缺失）且多連接競態偶發 SQLITE_BUSY
		client := data.Client(ctx, d)
		// 1) 關鍵業務默認值（站點名/網址 + 安裝時間戳）
		writes := []struct {
			group, key string
			value      any
		}{
			{"site", "name", in.SiteName},
			{"site", "url", in.SiteURL},
			{"ops", "installed_at", time.Now().UTC().Format(time.RFC3339)},
		}
		for _, w := range writes {
			v, _ := json.Marshal(w.value)
			// 衝突目標顯式化：PG 要求 ON CONFLICT (group, key) 帶推斷列
			//（SQLite 容忍省略——跨方言紀律 ADR-D18）
			if err := client.Setting.Create().
				SetGroup(w.group).SetKey(w.key).SetValue(v).
				OnConflict(sql.ConflictColumns(setting.FieldGroup, setting.FieldKey)).
				UpdateValue().Exec(ctx); err != nil {
				return err
			}
		}
		// 2) 內置角色與權限種子（冪等）
		if err := authz.EnsureBuiltinRoles(ctx, client); err != nil {
			return err
		}
		// 2.1) 基礎貨幣種子（冪等；CNY 恆等 rate=1）
		if err := EnsureDefaultCurrencies(ctx, d); err != nil {
			return err
		}
		// 3) 管理員（super_admin）
		role, err := client.AdminRole.Query().Where(adminrole.Code("super_admin")).Only(ctx)
		if err != nil {
			return err
		}
		hash, err := crypto.HashPassword(in.AdminPassword)
		if err != nil {
			return err
		}
		if _, err := client.AdminUser.Create().
			SetUsername(in.AdminUsername).
			SetPasswordHash(hash).
			SetNickname(in.AdminUsername).
			SetRoleID(role.ID).
			SetEnabled(true).
			Save(ctx); err != nil {
			return err
		}
		return nil
	})
}

// RunInstallCLI CLI 交互式入口（zcard install；stdin 三問）。
func RunInstallCLI(ctx context.Context, d *data.Data) error {
	if Installed(ctx, d) {
		return ErrAlreadyInstalled
	}
	r := bufio.NewReader(os.Stdin)
	in := InstallInput{}
	in.AdminUsername = prompt(r, "管理員用戶名")
	for {
		in.AdminPassword = prompt(r, "管理員密碼（≥8 位）")
		if len(in.AdminPassword) >= 8 {
			break
		}
		fmt.Println("  ✗ 密碼至少 8 位，請重輸")
	}
	in.SiteName = promptDefault(r, "站點名稱", "ZCard 演示站")
	in.SiteURL = promptDefault(r, "站點網址", "")
	return Install(ctx, d, in)
}

func prompt(r *bufio.Reader, label string) string {
	for {
		fmt.Printf("%s: ", label)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
}

func promptDefault(r *bufio.Reader, label, def string) string {
	fmt.Printf("%s [%s]: ", label, def)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}
