package migratev1

// P1 身份域遷移：users（+wallet/point 快照）→ admin 提取 → invite 鏈二次遍歷。
// 映射規格《數據遷移工具開發計劃》§5.2；密碼 bcrypt 直遷（$2y$ 前綴保留，
// Go x/crypto/bcrypt 可校驗——golden vector 釘死）。

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminrole"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/adminuser"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz"
)

// v1UserMorph spatie model_type（參數綁定傳遞，避免反斜槓字面量的方言差異）。
const v1UserMorph = "App\\Models\\User"

// MigrateIdentity P1 階段。
func (m *Migrator) MigrateIdentity(ctx context.Context) error {
	if err := m.migrateUsers(ctx); err != nil {
		return err
	}
	if err := m.migrateAdmins(ctx); err != nil {
		return err
	}
	return m.migrateInviteChain(ctx)
}

// migrateUsers 1.x users → users + wallet_accounts + point_accounts（快照三寫）。
// 丟棄列（2.0 無對應，報告統計）：name/qq/avatar/login_ip/total_recharge/total_consumption。
// 1.x 明確組的等級權益不落列：2.0 由閾值實時推導，交易域遷完後自動恢復。
func (m *Migrator) migrateUsers(ctx context.Context) error {
	var (
		id, balance, points                        int64
		username, password                         string
		email, phone                               sql.NullString
		status                                     int64
		pid, groupID                               sql.NullInt64
		deletedAt, lastLogin, createdAt, updatedAt sql.NullString
	)
	return m.scanTable(ctx, "users",
		[]string{"id", "username", "email", "phone", "password", "status", "deleted_at",
			"balance", "points", "pid", "group_id", "last_login_at", "created_at", "updated_at"},
		func() []any {
			return []any{&id, &username, &email, &phone, &password, &status, &deletedAt,
				&balance, &points, &pid, &groupID, &lastLogin, &createdAt, &updatedAt}
		},
		func(int64) error {
			if _, ok := m.IDs.Get(ctx, "users", uint64(id)); ok {
				m.st.Record("users", "skip")
				return nil
			}
			if gid := nullInt(groupID); gid > 0 {
				// 顯式組對照關係（供對賬/客服追溯；等級權益本身由閾值推導）
				if _, ok := m.IDs.Get(ctx, "user_groups", uint64(gid)); !ok {
					m.RW.AddError("users", uint64(id),
						fmt.Sprintf("顯式用戶組 %d 無對應 member_level（等級將按閾值推導）", gid))
				}
			}
			st := user.StatusActive
			switch {
			case nullStr(deletedAt) != "":
				st = user.StatusDeleted
			case status == 0:
				st = user.StatusBanned
			}
			ca, caOK, err := mustTime(nullStr(createdAt), m.TZ)
			if err != nil {
				return err
			}
			ua, uaOK, err := mustTime(nullStr(updatedAt), m.TZ)
			if err != nil {
				return err
			}
			exists, err := m.Client.User.Query().Where(user.Username(username)).Exist(ctx)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("用戶名 %q 已存在於目標庫（重複遷移或與 2.0 存量衝突）", username)
			}
			var lastLoginAt *time.Time
			if t, ok, err := mustTime(nullStr(lastLogin), m.TZ); err != nil {
				return err
			} else if ok {
				lastLoginAt = &t
			}
			ub := m.Client.User.Create().
				SetUsername(username).
				SetNillableEmail(nilIfEmpty(nullStr(email))).
				SetNillablePhone(nilIfEmpty(nullStr(phone))).
				SetNillablePasswordHash(nilIfEmpty(password)).
				SetStatus(st).
				SetNillableLastLoginAt(lastLoginAt)
			if caOK { // 直插行可能零日期——不 Set 走 ent 默認（與 reviews 同防禦）
				ub = ub.SetCreatedAt(ca)
			}
			if uaOK {
				ub = ub.SetUpdatedAt(ua)
			}
			u, err := ub.Save(ctx)
			if err != nil {
				return err
			}
			// 錢包/積分快照（賬務流水自 P5 重放，此處只建快照）
			if _, err := m.Client.WalletAccount.Create().
				SetUserID(u.ID).
				SetAvailable(balance).
				Save(ctx); err != nil {
				return fmt.Errorf("錢包快照失敗: %w", err)
			}
			if points != 0 {
				if _, err := m.Client.PointAccount.Create().
					SetUserID(u.ID).
					SetBalance(points).
					Save(ctx); err != nil {
					return fmt.Errorf("積分快照失敗: %w", err)
				}
			}
			if _, err := m.IDs.Put(ctx, m.Client, "users", uint64(id), u.ID); err != nil {
				return err
			}
			m.st.Record("users", "migrated")
			return nil
		},
	)
}

// migrateAdmins 1.x spatie 角色提取 → admin_users（username/password bcrypt 直遷）。
// 1.x admin 與普通用戶分屬兩表，無命名衝突；2.0 需先種內置角色再掛超管。
func (m *Migrator) migrateAdmins(ctx context.Context) error {
	if m.dry {
		return m.dryCount(ctx,
			"SELECT COUNT(DISTINCT u.id) FROM users u "+
				"JOIN model_has_roles mhr ON mhr.model_id = u.id AND mhr.model_type = ? "+
				"JOIN roles r ON r.id = mhr.role_id WHERE r.name IN ('super_admin','admin')",
			"admin_users", v1UserMorph)
	}
	if err := authz.EnsureBuiltinRoles(ctx, m.Client); err != nil {
		return fmt.Errorf("內置角色種子失敗: %w", err)
	}
	role, err := m.Client.AdminRole.Query().Where(adminrole.Code("super_admin")).Only(ctx)
	if err != nil {
		return fmt.Errorf("查詢內置超管角色失敗: %w", err)
	}
	rows, err := m.Src.DB.QueryContext(ctx,
		"SELECT DISTINCT u.id, u.username, u.password, u.name, u.last_login_at FROM users u "+
			"JOIN model_has_roles mhr ON mhr.model_id = u.id AND mhr.model_type = ? "+
			"JOIN roles r ON r.id = mhr.role_id WHERE r.name IN ('super_admin','admin') ORDER BY u.id",
		v1UserMorph)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                 int64
			username, password string
			name               sql.NullString
			lastLogin          sql.NullString
		)
		if err := rows.Scan(&id, &username, &password, &name, &lastLogin); err != nil {
			return err
		}
		exists, err := m.Client.AdminUser.Query().Where(adminuser.Username(username)).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			m.st.Record("admin_users", "skip")
			continue
		}
		var lastLoginAt *time.Time
		if t, ok, err := mustTime(nullStr(lastLogin), m.TZ); err != nil {
			return err
		} else if ok {
			lastLoginAt = &t
		}
		if _, err := m.Client.AdminUser.Create().
			SetUsername(username).
			SetPasswordHash(password).
			SetNillableNickname(nilIfEmpty(nullStr(name))).
			SetRoleID(role.ID).
			SetEnabled(true).
			SetNillableLastLoginAt(lastLoginAt).
			Save(ctx); err != nil {
			if m.Opts.OnError == "abort" {
				return err
			}
			m.st.Record("admin_users", "fail")
			m.RW.AddError("admin_users", uint64(id), err.Error())
			continue
		}
		m.st.Record("admin_users", "migrated")
	}
	return rows.Err()
}

// migrateInviteChain pid 鏈 → invite_l1/l2/l3（二次遍歷：全部用戶落庫後再解析依賴）。
// 環防護：上溯鏈遇已見節點即止（自身天然在 seen 中）；冪等：已填 invite_l1 的行跳過。
func (m *Migrator) migrateInviteChain(ctx context.Context) error {
	if m.dry {
		return nil
	}
	pids := map[int64]int64{} // 1.x uid -> 1.x pid
	rows, err := m.Src.DB.QueryContext(ctx, "SELECT id, pid FROM users WHERE pid > 0")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, pid int64
		if err := rows.Scan(&id, &pid); err != nil {
			return err
		}
		pids[id] = pid
	}
	if err := rows.Err(); err != nil {
		return err
	}
	updated := int64(0)
	for id, pid := range pids {
		newID, ok := m.IDs.Get(ctx, "users", uint64(id))
		if !ok {
			continue
		}
		var chain []uint64 // l1,l2,l3（新 ID）
		seen := map[int64]bool{id: true}
		cur := pid
		for len(chain) < 3 && cur > 0 && !seen[cur] {
			seen[cur] = true
			if up, ok := m.IDs.Get(ctx, "users", uint64(cur)); ok {
				chain = append(chain, up)
			}
			cur = pids[cur]
		}
		if len(chain) == 0 {
			continue
		}
		// 冪等：重跑時鏈計算結果確定，直接覆蓋等價；跳過已填行省一次寫
		existing, err := m.Client.User.Get(ctx, newID)
		if err != nil {
			return err
		}
		if existing.InviteL1 != 0 {
			continue
		}
		upd := m.Client.User.UpdateOneID(newID)
		switch len(chain) {
		case 1:
			upd = upd.SetInviteL1(chain[0])
		case 2:
			upd = upd.SetInviteL1(chain[0]).SetInviteL2(chain[1])
		default:
			upd = upd.SetInviteL1(chain[0]).SetInviteL2(chain[1]).SetInviteL3(chain[2])
		}
		if _, err := upd.Save(ctx); err != nil {
			return err
		}
		updated++
	}
	m.st.table("invite_chain").Migrated = updated
	return nil
}

// dryCount 通用計數（dry-run 時 admin 提取等手寫 SQL 的預估）。
func (m *Migrator) dryCount(ctx context.Context, query, table string, args ...any) error {
	var n int64
	if err := m.Src.DB.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return err
	}
	m.st.table(table).Planned = n
	return nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
