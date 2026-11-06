package authz

// wire providers（RbacUsecase 同時實現 port.Authorizer，跨模塊消費經 port 綁定）。

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/authz/port"

	"github.com/google/wire"
)

// ProviderSet authz providers。
var ProviderSet = wire.NewSet(
	NewRbacUsecase,
	NewRoleRepoImpl,
	NewDirectory,
	NewRoleService,
	NewAdminUserService,
	wire.Bind(new(RoleRepo), new(*RoleRepoImpl)),
	wire.Bind(new(port.Authorizer), new(*RbacUsecase)),
)

// NewDirectory 權限目錄（聲明表構建；單例語義——聲明在 init 期完成）。
func NewDirectory() *Directory { return BuildDirectory() }
