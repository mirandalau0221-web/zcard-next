package wallet

// wire providers。

import (
	"context"

	"github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"

	"github.com/google/wire"
)

// ProviderSet wallet providers。
var ProviderSet = wire.NewSet(
	NewWalletRepoImpl,
	NewGiftcardRepo,
	NewStoreWalletService,
	NewAdminWalletService,
	// ：錢包端口適配器（Entry 字段口徑轉換：port.Entry → 本地 Entry）
	ProvidePortWallet,
	// ：積分賬本端口（payment 充值贈送消費）
	ProvidePortPoints,
	// ：積分扣減/讀取 + 累計充值口徑端口（order 兑換下單、memberlevel 等級進度消費）
	ProvidePortPointsDebiter,
	ProvidePortPointsReader,
	ProvidePortRechargeReader,
)

// portWalletAdapter port.Wallet → WalletRepoImpl（Entry 轉換）。
type portWalletAdapter struct{ repo *WalletRepoImpl }

func (a portWalletAdapter) CreditInTx(ctx context.Context, e port.Entry) error {
	return a.repo.CreditInTx(ctx, toLocalEntry(e))
}

func (a portWalletAdapter) DebitInTx(ctx context.Context, e port.Entry) error {
	return a.repo.DebitInTx(ctx, toLocalEntry(e))
}

func (a portWalletAdapter) Lock(ctx context.Context, userID uint64, amount money.Cents, availableAt int64) error {
	return a.repo.Lock(ctx, userID, int64(amount), availableAt)
}

func (a portWalletAdapter) Unlock(ctx context.Context, userID uint64, amount money.Cents) error {
	return a.repo.Unlock(ctx, userID, int64(amount))
}

// portPointsAdapter port.Points → WalletRepoImpl（積分入賬）。
type portPointsAdapter struct{ repo *WalletRepoImpl }

func (a portPointsAdapter) PointCreditInTx(ctx context.Context, e port.PointEntry) error {
	return a.repo.PointCreditInTx(ctx, PointEntry{
		UserID: e.UserID, Direction: e.Direction, Type: e.Type,
		Amount: e.Amount, Reference: e.Reference,
		OrderID: e.OrderID, Remark: e.Remark,
	})
}

func (a portPointsAdapter) PointDebitInTx(ctx context.Context, e port.PointEntry) error {
	return a.repo.PointDebitInTx(ctx, PointEntry{
		UserID: e.UserID, Direction: e.Direction, Type: e.Type,
		Amount: e.Amount, Reference: e.Reference,
		OrderID: e.OrderID, Remark: e.Remark,
	})
}

func (a portPointsAdapter) GetPoints(ctx context.Context, userID uint64) (int64, error) {
	return a.repo.GetPoints(ctx, userID)
}

func (a portPointsAdapter) CumulativeRecharge(ctx context.Context, userID uint64) (int64, error) {
	return a.repo.CumulativeRecharge(ctx, userID)
}

// ProvidePortPoints 積分端口適配 provider。
func ProvidePortPoints(repo *WalletRepoImpl) port.Points {
	return portPointsAdapter{repo: repo}
}

// ProvidePortPointsDebiter 積分扣減端口（ order 積分兑換下單消費）。
func ProvidePortPointsDebiter(repo *WalletRepoImpl) port.PointsDebiter {
	return portPointsAdapter{repo: repo}
}

// ProvidePortPointsReader 積分餘額端口（ memberlevel 等級進度消費）。
func ProvidePortPointsReader(repo *WalletRepoImpl) port.PointsReader {
	return portPointsAdapter{repo: repo}
}

// ProvidePortRechargeReader 累計充值口徑端口（ memberlevel 升級閾值消費）。
func ProvidePortRechargeReader(repo *WalletRepoImpl) port.RechargeReader {
	return portPointsAdapter{repo: repo}
}

func toLocalEntry(e port.Entry) Entry {
	return Entry{
		UserID: e.UserID, Direction: e.Direction, Type: e.Type,
		Amount: int64(e.Amount), Reference: e.Reference,
		OrderID: e.OrderID, OperatorID: e.Operator, Remark: e.Remark,
	}
}

// ProvidePortWallet 端口適配 provider。
func ProvidePortWallet(repo *WalletRepoImpl) port.Wallet {
	return portWalletAdapter{repo: repo}
}

func (a portPointsAdapter) PointRevokeInTx(ctx context.Context, e port.PointEntry) error {
	return a.repo.PointRevokeInTx(ctx, PointEntry{UserID: e.UserID, Direction: e.Direction, Type: e.Type, Amount: e.Amount, Reference: e.Reference, OrderID: e.OrderID, Remark: e.Remark})
}
