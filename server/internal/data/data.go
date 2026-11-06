// Package data 數據層裝配：Ent client、三方言驅動選擇（platform/db 收口）、
// 事務工作單元（通道 B：biz 層不見 *ent.Tx，repo 經 data.Client(ctx) 自動加入事務）。
package data

import (
	"context"
	"database/sql"
	"fmt"
	_ "github.com/NovaWorks/zcard-next/server/internal/data/ent/runtime"
	"slices"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	"github.com/NovaWorks/zcard-next/server/internal/platform/events"

	// 三方言驅動註冊（同一二進制，運行時按 DSN 切換，ADR-D20）：
	// 純 Go 無 CGO（SQLite 用 modernc，保單二進制交叉編譯，ADR-D19）
	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/google/wire"
)

// atlas 的 sqlclient 按驅動名 "postgres" 打開連接（遷移執行器 PG 路徑）；
// pgx stdlib 註冊名是 "pgx"，此處註冊別名（冪等）。
func init() {
	if !slices.Contains(sql.Drivers(), "postgres") {
		sql.Register("postgres", stdlib.GetDefaultDriver())
	}
}

// ProviderSet data providers（wire）。
var ProviderSet = wire.NewSet(
	NewData,
	NewOutboxWriter,
	NewFailedTaskWriter,
	// 接口綁定：OutboxWriter → events.Writer（模塊發佈事件經接口，通道 A）
	wire.Bind(new(events.Writer), new(*OutboxWriter)),
)

// Data 數據句柄（業務模塊 repo 經構造函數持有；禁止繞過 data.Client 持有全局單例，
// 未來 per-tenant client 由 TenantStore 注入，鐵律 14）。
type Data struct {
	Client  *ent.Client
	DB      *sql.DB
	Dialect db.Dialect
}

// entDialect platform/db 方言 → ent 方言名。
func entDialect(d db.Dialect) string {
	switch d {
	case db.MySQL:
		return dialect.MySQL
	case db.Postgres:
		return dialect.Postgres
	case db.SQLite:
		return dialect.SQLite
	}
	return string(d)
}

// NewData 打開數據庫並裝配 ent client（wire provider）。
func NewData(c *conf.Data) (*Data, func(), error) {
	if c == nil || c.Database == nil || c.Database.Driver == "" {
		return nil, func() {}, fmt.Errorf("data: 缺少數據庫配置（data.database.driver/source）")
	}
	d, err := db.Detect(c.Database.Driver)
	if err != nil {
		return nil, func() {}, err
	}
	handle, err := d.Open(c.Database.Source)
	if err != nil {
		return nil, func() {}, err
	}
	if n := c.Database.MaxOpenConns; n > 0 {
		handle.SetMaxOpenConns(int(n))
	}
	if n := c.Database.MaxIdleConns; n > 0 {
		handle.SetMaxIdleConns(int(n))
	}
	drv := entsql.OpenDB(entDialect(d), handle)
	client := ent.NewClient(ent.Driver(drv))

	cleanup := func() {
		_ = client.Close()
		_ = handle.Close()
	}
	return &Data{Client: client, DB: handle, Dialect: d}, cleanup, nil
}

// Ping 連接健康檢查（/health 與啓動自檢）。
func (d *Data) Ping(ctx context.Context) error {
	return d.DB.PingContext(ctx)
}

type txKey struct{}

// Tx 事務工作單元：fn 內所有經 data.Client(ctx, d) 取句柄的倉儲操作加入同一事務。
// biz 層只見 TxFunc，不見 *ent.Tx（規劃 §4.7 通道 B）。
func Tx(ctx context.Context, d *Data, fn func(ctx context.Context) error) error {
	// 嵌套事務：複用外層（SAVEPOINT 分級 M1 按需引入）
	if _, ok := ctx.Value(txKey{}).(*ent.Tx); ok {
		return fn(ctx)
	}
	tx, err := d.Client.Tx(ctx)
	if err != nil {
		return err
	}
	txCtx := context.WithValue(ctx, txKey{}, tx)
	if err := fn(txCtx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Client 取當前上下文的 ent 句柄：事務內返回 *ent.Tx 包裝的 client，否則返回基礎 client。
// 所有 repo 的查詢入口（ Mods 數據層唯一取句柄方式）。
func Client(ctx context.Context, d *Data) *ent.Client {
	if tx, ok := ctx.Value(txKey{}).(*ent.Tx); ok {
		return tx.Client()
	}
	return d.Client
}
