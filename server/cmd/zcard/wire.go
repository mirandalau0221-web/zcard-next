//go:build wireinject
// +build wireinject

// wire 注入樁（build tag 保證不進最終構建；wire 命令生成 wire_gen.go）。

package main

import (
	"log/slog"

	"github.com/NovaWorks/zcard-next/server/internal/bootstrap"
	"github.com/NovaWorks/zcard-next/server/internal/conf"
	"github.com/NovaWorks/zcard-next/server/internal/mods/update"
	"github.com/NovaWorks/zcard-next/server/internal/server"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"
)

// appDeps wire 裝配產物打包（wire 多具體返回值受限）。
type appDeps struct {
	App    *kratos.App
	Update *update.Service // serve 層掛更新重啓 hook 用
}

// wireApp init kratos application（update.Service 旁路返回——serve 層掛重啓 hook）。
func wireApp(
	serverConf *conf.Server,
	dataConf *conf.Data,
	securityConf *conf.Security,
	logger *slog.Logger,
) (*appDeps, func(), error) {
	panic(wire.Build(bootstrap.ProviderSet, server.ProviderSet, provideRunMode, newApp, wire.Struct(new(appDeps), "*")))
}
