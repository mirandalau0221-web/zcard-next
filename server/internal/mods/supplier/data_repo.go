package supplier

// 供貨倉儲（）：下游賬戶（secret AES-GCM）、供貨賬本（冪等鍵 append-only）、
// 供貨訂單、nonce 防重放、差異化定價、回調記錄。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/downstreamcallback"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/rechargeorder"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplieraccount"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplierledgerentry"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplierproductprice"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplynonce"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplyorder"
	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
)

// 哨兵錯誤。
var (
	ErrNotFound            = errors.New("supplier: 記錄不存在")
	ErrInsufficientBalance = errors.New("supplier: 供貨餘額不足")
	ErrDuplicateLedger     = errors.New("supplier: 賬本冪等鍵已存在")
)

// secretAAD api_secret 加密 AAD（按賬戶 api_key 綁定）。
func secretAAD(apiKey string) []byte { return []byte("supplier_account:" + apiKey) }

// SupplierRepoImpl 供貨倉儲。
type SupplierRepoImpl struct {
	data *data.Data
	box  *crypto.Box // ZCARD_DATA_KEY
}

// NewSupplierRepoImpl 構造。
func NewSupplierRepoImpl(d *data.Data, box *crypto.Box) *SupplierRepoImpl {
	return &SupplierRepoImpl{data: d, box: box}
}

// ── 賬戶 ──────────────────────────────────────────────────

// CreateAccount 下游申請（secret 加密入庫；只顯示一次的語義由 service 層處理）。
func (r *SupplierRepoImpl) createAccount(ctx context.Context, name, apiKey, apiSecret, contact, protocol, displayName string) (*ent.SupplierAccount, error) {
	enc, err := r.box.Seal([]byte(apiSecret), secretAAD(apiKey))
	if err != nil {
		return nil, fmt.Errorf("supplier: secret 加密失敗: %w", err)
	}
	create := data.Client(ctx, r.data).SupplierAccount.Create().
		SetName(name).
		SetAPIKey(apiKey).
		SetAPISecret(enc).
		SetContact(contact).
		SetStatus(supplieraccount.StatusApplying).
		SetProtocol(supplieraccount.Protocol(protocol)).
		SetOwnerUserID(0)
	if displayName != "" {
		create.SetDisplayName(displayName)
	}
	return create.Save(ctx)
}

// CreateApplication 前台對接申請（owner=用戶 id；憑據由 service 生成後傳入）。
func (r *SupplierRepoImpl) createApplication(ctx context.Context, ownerUserID uint64, protocol, displayName, contact, applyReason, notifyURL, apiKey, apiSecret string) (*ent.SupplierAccount, error) {
	enc, err := r.box.Seal([]byte(apiSecret), secretAAD(apiKey))
	if err != nil {
		return nil, fmt.Errorf("supplier: secret 加密失敗: %w", err)
	}
	create := data.Client(ctx, r.data).SupplierAccount.Create().
		SetName(displayName).
		SetAPIKey(apiKey).
		SetAPISecret(enc).
		SetContact(contact).
		SetStatus(supplieraccount.StatusApplying).
		SetProtocol(supplieraccount.Protocol(protocol)).
		SetOwnerUserID(ownerUserID).
		SetDisplayName(displayName)
	if applyReason != "" {
		create.SetApplyReason(applyReason)
	}
	if notifyURL != "" {
		create.SetNotifyURL(notifyURL)
	}
	return create.Save(ctx)
}

// ListMyAccounts 前台用戶的賬戶列表（按創建倒序）。
func (r *SupplierRepoImpl) ListMyAccounts(ctx context.Context, ownerUserID uint64) ([]*ent.SupplierAccount, error) {
	return data.Client(ctx, r.data).SupplierAccount.Query().
		Where(supplieraccount.OwnerUserID(ownerUserID)).
		Order(ent.Desc(supplieraccount.FieldID)).
		All(ctx)
}

// CountMyApplying 用戶申請中數量（防刷護欄）。
func (r *SupplierRepoImpl) CountMyApplying(ctx context.Context, ownerUserID uint64) (int, error) {
	return data.Client(ctx, r.data).SupplierAccount.Query().
		Where(
			supplieraccount.OwnerUserID(ownerUserID),
			supplieraccount.StatusEQ(supplieraccount.StatusApplying),
		).
		Count(ctx)
}

// CountMyAccounts 用戶賬戶總數（防刷護欄）。
func (r *SupplierRepoImpl) CountMyAccounts(ctx context.Context, ownerUserID uint64) (int, error) {
	return data.Client(ctx, r.data).SupplierAccount.Query().
		Where(supplieraccount.OwnerUserID(ownerUserID)).
		Count(ctx)
}

// CancelApplication 撤銷申請（僅 applying；已審核的不可撤銷）。
func (r *SupplierRepoImpl) CancelApplication(ctx context.Context, id uint64) error {
	n, err := data.Client(ctx, r.data).SupplierAccount.Delete().
		Where(
			supplieraccount.ID(id),
			supplieraccount.StatusEQ(supplieraccount.StatusApplying),
		).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("supplier: 僅申請中的賬戶可撤銷")
	}
	return nil
}

// SetIPWhitelist 設置 IP 白名單（空 = 所有 IP 放行；條目合法性由 service 校驗）。
func (r *SupplierRepoImpl) SetIPWhitelist(ctx context.Context, id uint64, ips []string) error {
	if ips == nil {
		ips = []string{}
	}
	_, err := data.Client(ctx, r.data).SupplierAccount.UpdateOneID(id).
		SetIPWhitelist(ips).
		Save(ctx)
	return err
}

// CreateSupplyRechargeOrder 落供貨充值單（pending；target=supply）。
// 入賬只發生在支付回調成功後（鐵律 16）；供貨預存無贈送。
func (r *SupplierRepoImpl) createSupplyRechargeOrder(ctx context.Context, userID, accountID uint64, amount, giftAmount int64) (*ent.RechargeOrder, error) {
	return data.Client(ctx, r.data).RechargeOrder.Create().
		SetUserID(userID).
		SetAmount(amount).
		SetGiftAmount(giftAmount).
		SetGiftPoints(0).
		SetTarget(rechargeorder.TargetSupply).
		SetSupplierAccountID(accountID).
		SetStatus(rechargeorder.StatusPending).
		Save(ctx)
}

// AccountByKey 按 key 查賬戶（AuthStore 實現；返回解密 secret）。
func (r *SupplierRepoImpl) AccountByKey(ctx context.Context, key string) (*ent.SupplierAccount, string, error) {
	acc, err := data.Client(ctx, r.data).SupplierAccount.Query().
		Where(supplieraccount.APIKey(key)).
		Only(ctx)
	if err != nil {
		return nil, "", err
	}
	plain, err := r.box.Open(acc.APISecret, secretAAD(acc.APIKey))
	if err != nil {
		return nil, "", fmt.Errorf("supplier: secret 解密失敗（需重置密鑰）: %w", err)
	}
	return acc, string(plain), nil
}

// GetAccount 賬戶詳情（不含 secret）。
func (r *SupplierRepoImpl) GetAccount(ctx context.Context, id uint64) (*ent.SupplierAccount, error) {
	acc, err := data.Client(ctx, r.data).SupplierAccount.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return acc, nil
}

// ListAccounts 賬戶列表。
func (r *SupplierRepoImpl) ListAccounts(ctx context.Context, page, pageSize int) ([]*ent.SupplierAccount, int, error) {
	q := data.Client(ctx, r.data).SupplierAccount.Query().Order(ent.Desc(supplieraccount.FieldID))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	return rows, total, err
}

// ReviewAccount 審核（applying → approved/rejected；駁回可帶意見）。
func (r *SupplierRepoImpl) ReviewAccount(ctx context.Context, id uint64, approve bool, reviewNote string) (*ent.SupplierAccount, error) {
	status := supplieraccount.StatusRejected
	if approve {
		status = supplieraccount.StatusApproved
	}
	upd := data.Client(ctx, r.data).SupplierAccount.UpdateOneID(id).
		SetStatus(status).
		SetReviewedAt(time.Now().UTC())
	if reviewNote != "" {
		upd.SetReviewNote(reviewNote)
	}
	acc, err := upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	return acc, nil
}

// ToggleAccount 啓停（approved ↔ disabled）。
func (r *SupplierRepoImpl) ToggleAccount(ctx context.Context, id uint64, enabled bool) (*ent.SupplierAccount, error) {
	status := supplieraccount.StatusDisabled
	if enabled {
		status = supplieraccount.StatusApproved
	}
	return data.Client(ctx, r.data).SupplierAccount.UpdateOneID(id).SetStatus(status).Save(ctx)
}

// ResetSecret 重置密鑰（返回明文一次）。
func (r *SupplierRepoImpl) ResetSecret(ctx context.Context, id uint64, newSecret string) error {
	acc, err := r.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	enc, err := r.box.Seal([]byte(newSecret), secretAAD(acc.APIKey))
	if err != nil {
		return err
	}
	_, err = data.Client(ctx, r.data).SupplierAccount.UpdateOneID(id).SetAPISecret(enc).Save(ctx)
	return err
}

// SetNotifyURL 配置回調地址（HTTPS 校驗在 service 層）。
func (r *SupplierRepoImpl) SetNotifyURL(ctx context.Context, id uint64, notifyURL string) error {
	_, err := data.Client(ctx, r.data).SupplierAccount.UpdateOneID(id).SetNotifyURL(notifyURL).Save(ctx)
	return err
}

// ── 賬本（：append-only + 冪等鍵 + balance_cache）───────

// LedgerEntry 賬本流水。
func (r *SupplierRepoImpl) LedgerEntry(ctx context.Context, accountID, supplyOrderID uint64, typ string, amount int64, reference, remark string) error {
	if amount == 0 {
		return fmt.Errorf("supplier: 流水金額不能為 0")
	}
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		acc, err := r.lockAccount(ctx, accountID)
		if err != nil {
			return err
		}
		client := data.Client(ctx, r.data)
		exists, err := client.SupplierLedgerEntry.Query().Where(supplierledgerentry.Reference(reference)).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			return ErrDuplicateLedger
		}
		if amount < 0 && (amount == math.MinInt64 || acc.BalanceCache < -amount) {
			return ErrInsufficientBalance
		}
		if amount > 0 && acc.BalanceCache > math.MaxInt64-amount {
			return errors.New("supplier.BALANCE_OVERFLOW")
		}
		_, err = client.SupplierLedgerEntry.Create().
			SetAccountID(accountID).SetSupplyOrderID(supplyOrderID).SetType(typ).
			SetAmount(amount).SetReference(reference).SetRemark(remark).Save(ctx)
		if err != nil {
			return err
		}
		return client.SupplierAccount.UpdateOneID(accountID).SetBalanceCache(acc.BalanceCache + amount).Exec(ctx)
	})
}

// lockAccount 必須在 data.Tx 內調用；所有餘額及供貨訂單寫入按賬戶串行。
func (r *SupplierRepoImpl) lockAccount(ctx context.Context, accountID uint64) (*ent.SupplierAccount, error) {
	if accountID == 0 {
		return nil, ErrNotFound
	}
	client := data.Client(ctx, r.data)
	q := client.SupplierAccount.Query().Where(supplieraccount.ID(accountID))
	if r.data.Dialect == db.SQLite {
		// SQLite 沒有 FOR UPDATE，先取得寫鎖，避免讀後升級事務的競爭。
		if _, err := client.SupplierAccount.Update().Where(supplieraccount.ID(accountID)).AddBalanceCache(0).Save(ctx); err != nil {
			return nil, err
		}
	} else {
		q = q.ForUpdate()
	}
	return q.Only(ctx)
}

// BalanceOf 賬戶餘額（balance_cache 快照；對賬由流水重算）。
func (r *SupplierRepoImpl) BalanceOf(ctx context.Context, accountID uint64) (int64, error) {
	acc, err := r.GetAccount(ctx, accountID)
	if err != nil {
		return 0, err
	}
	return acc.BalanceCache, nil
}

// ListLedger 賬本流水（分頁）。
func (r *SupplierRepoImpl) ListLedger(ctx context.Context, accountID uint64, page, pageSize int) ([]*ent.SupplierLedgerEntry, int, error) {
	q := data.Client(ctx, r.data).SupplierLedgerEntry.Query().
		Order(ent.Desc(supplierledgerentry.FieldCreatedAt))
	if accountID > 0 {
		q = q.Where(supplierledgerentry.AccountID(accountID))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	return rows, total, err
}

// Recharge 充值（走 payment 管線的落點；本方法為賬本原子入賬）。
func (r *SupplierRepoImpl) Recharge(ctx context.Context, accountID uint64, amount int64, reference, remark string) error {
	return r.LedgerEntry(ctx, accountID, 0, "recharge", amount, reference, remark)
}

// ── nonce（AuthStore 實現）────────────────────────────────

// ConsumeNonce 消費 nonce（UNIQUE(key,nonce) 衝突 = 重放）。
func (r *SupplierRepoImpl) ConsumeNonce(ctx context.Context, key, nonce string, expiresAt time.Time) error {
	_, err := data.Client(ctx, r.data).SupplyNonce.Create().
		SetKey(key).
		SetNonce(nonce).
		SetExpiresAt(expiresAt).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return fmt.Errorf("supplier: nonce 重放")
		}
		return err
	}
	return nil
}

// CleanupExpiredNonces 過期 nonce 清理（cron）。
func (r *SupplierRepoImpl) CleanupExpiredNonces(ctx context.Context) error {
	_, err := data.Client(ctx, r.data).SupplyNonce.Delete().
		Where(supplynonce.ExpiresAtLT(time.Now().UTC())).
		Exec(ctx)
	return err
}

// ── 供貨訂單 ──────────────────────────────────────────────

// CreateSupplyOrder 創建供貨訂單（downstream_order_no 冪等）。
func (r *SupplierRepoImpl) CreateSupplyOrder(ctx context.Context, accountID uint64, downstreamOrderNo string, items []map[string]any, amount int64) (*ent.SupplyOrder, error) {
	o, err := data.Client(ctx, r.data).SupplyOrder.Create().
		SetAccountID(accountID).
		SetDownstreamOrderNo(downstreamOrderNo).
		SetItems(items).
		SetAmount(amount).
		SetStatus(supplyorder.StatusPending).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, fmt.Errorf("supplier: 重複 downstream_order_no")
		}
		return nil, err
	}
	return o, nil
}

// ListSupplyOrders 時間窗內供貨單（對賬數據源；按創建時間）。
func (r *SupplierRepoImpl) ListSupplyOrders(ctx context.Context, start, end time.Time) ([]*ent.SupplyOrder, error) {
	return data.Client(ctx, r.data).SupplyOrder.Query().
		Where(
			supplyorder.CreatedAtGTE(start),
			supplyorder.CreatedAtLT(end),
		).
		Order(ent.Asc(supplyorder.FieldID)).
		All(ctx)
}

// GetSupplyOrderByNo 按下游單號查（冪等返回首單）。
func (r *SupplierRepoImpl) GetSupplyOrderByNo(ctx context.Context, accountID uint64, downstreamOrderNo string) (*ent.SupplyOrder, error) {
	if accountID == 0 {
		return nil, ErrNotFound
	}
	o, err := data.Client(ctx, r.data).SupplyOrder.Query().
		Where(supplyorder.AccountID(accountID), supplyorder.DownstreamOrderNo(downstreamOrderNo)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return o, nil
}

// ListAccountSupplyOrders 對外查詢必須顯式限制賬戶；內部全量查詢另用 ListSupplyOrders。
func (r *SupplierRepoImpl) ListAccountSupplyOrders(ctx context.Context, accountID uint64, start, end time.Time) ([]*ent.SupplyOrder, error) {
	if accountID == 0 {
		return nil, ErrNotFound
	}
	return data.Client(ctx, r.data).SupplyOrder.Query().Where(
		supplyorder.AccountID(accountID), supplyorder.CreatedAtGTE(start), supplyorder.CreatedAtLT(end),
	).Order(ent.Asc(supplyorder.FieldID)).All(ctx)
}

func (r *SupplierRepoImpl) GetAccountSupplyOrder(ctx context.Context, accountID, orderID uint64) (*ent.SupplyOrder, error) {
	if accountID == 0 {
		return nil, ErrNotFound
	}
	o, err := data.Client(ctx, r.data).SupplyOrder.Query().Where(supplyorder.AccountID(accountID), supplyorder.ID(orderID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return o, err
}

// RefundUndelivered 原子取消/退款；只退本賬戶、本訂單實際扣款的未退餘額。
// 歷史 cancel/refund 兩種流水均計入，不能靠換接口或換冪等鍵重複入賬。
func (r *SupplierRepoImpl) RefundUndelivered(ctx context.Context, accountID, orderID uint64) (bool, error) {
	ok := false
	err := data.Tx(ctx, r.data, func(ctx context.Context) error {
		if _, err := r.lockAccount(ctx, accountID); err != nil {
			return err
		}
		o, err := r.GetAccountSupplyOrder(ctx, accountID, orderID)
		if err != nil {
			return err
		}
		switch o.Status {
		case supplyorder.StatusPending, supplyorder.StatusPaid, supplyorder.StatusRejected, supplyorder.StatusRefunded:
		default:
			return nil // 已交付或交付中須人工處理，卡密不能撤回。
		}
		// 舊版曾忽略交付狀態寫入失敗；已有卡密快照的訂單即使仍是 paid，
		// 也不能自動退款，否則會讓已拿到卡密的客戶再次取回貨款。
		for _, item := range o.Items {
			if _, delivered := item["card_ids"]; delivered {
				return nil
			}
		}
		paid, refunded, err := r.orderPayments(ctx, accountID, orderID)
		if err != nil {
			return err
		}

		if refunded > paid || paid > o.Amount {
			return errors.New("supplier.INVALID_ORDER_LEDGER")
		}
		if remaining := paid - refunded; remaining > 0 {
			if err := r.LedgerEntry(ctx, accountID, orderID, "supply_refund", remaining,
				fmt.Sprintf("supply_order_id:%d:refund", orderID), "未交付訂單退回"); err != nil {
				return err
			}
		}
		status := supplyorder.StatusRejected
		if paid > 0 {
			status = supplyorder.StatusRefunded
		}
		if err := data.Client(ctx, r.data).SupplyOrder.UpdateOneID(orderID).SetStatus(status).Exec(ctx); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok && err == nil, err
}

// orderPayments 只彙總指定賬戶與訂單的實付/實退流水（包括舊版冪等鍵）。
func (r *SupplierRepoImpl) orderPayments(ctx context.Context, accountID, orderID uint64) (int64, int64, error) {
	if accountID == 0 {
		return 0, 0, ErrNotFound
	}
	rows, err := data.Client(ctx, r.data).SupplierLedgerEntry.Query().Where(
		supplierledgerentry.AccountID(accountID), supplierledgerentry.SupplyOrderID(orderID),
		supplierledgerentry.TypeIn("supply_pay", "supply_refund"),
	).All(ctx)
	if err != nil {
		return 0, 0, err
	}
	var paid, refunded int64
	for _, row := range rows {
		if row.Type == "supply_pay" {
			if row.Amount >= 0 || row.Amount == math.MinInt64 || paid > math.MaxInt64+row.Amount {
				return 0, 0, errors.New("supplier.INVALID_ORDER_LEDGER")
			}
			paid -= row.Amount
		} else {
			if row.Amount <= 0 || refunded > math.MaxInt64-row.Amount {
				return 0, 0, errors.New("supplier.INVALID_ORDER_LEDGER")
			}
			refunded += row.Amount
		}
	}
	return paid, refunded, nil
}

// GetSupplyOrder 內部任務/管理用途；對外接口必須用 GetAccountSupplyOrder。
func (r *SupplierRepoImpl) GetSupplyOrder(ctx context.Context, id uint64) (*ent.SupplyOrder, error) {
	o, err := data.Client(ctx, r.data).SupplyOrder.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return o, nil
}

// UpdateSupplyOrderItems 更新 items 快照（交付後補記 card_ids；兼容層查單重建 payload 用）。
func (r *SupplierRepoImpl) UpdateSupplyOrderItems(ctx context.Context, id uint64, items []map[string]any) error {
	_, err := data.Client(ctx, r.data).SupplyOrder.UpdateOneID(id).SetItems(items).Save(ctx)
	return err
}

// MarkSupplyOrderFulfilled 交付完成（fulfilling → fulfilled + fulfilled_at）。
func (r *SupplierRepoImpl) MarkSupplyOrderFulfilled(ctx context.Context, id uint64) error {
	_, err := data.Client(ctx, r.data).SupplyOrder.UpdateOneID(id).
		SetStatus(supplyorder.StatusFulfilled).
		SetFulfilledAt(time.Now().UTC()).
		Save(ctx)
	return err
}

// MarkSupplyOrderPaid 扣款成功（pending → paid + paid_at）。
func (r *SupplierRepoImpl) MarkSupplyOrderPaid(ctx context.Context, id uint64) error {
	_, err := data.Client(ctx, r.data).SupplyOrder.UpdateOneID(id).
		SetStatus(supplyorder.StatusPaid).
		SetPaidAt(time.Now().UTC()).
		Save(ctx)
	return err
}

// MarkSupplyOrderRejected 下單拒絕（庫存不足等）。
func (r *SupplierRepoImpl) MarkSupplyOrderRejected(ctx context.Context, id uint64) error {
	_, err := data.Client(ctx, r.data).SupplyOrder.UpdateOneID(id).
		SetStatus(supplyorder.StatusRejected).
		Save(ctx)
	return err
}

// ── 差異化定價 ────────────────────────────────────────────

// UpsertPrice 設置/更新供貨價（UNIQUE(account, product, sku)）。
func (r *SupplierRepoImpl) UpsertPrice(ctx context.Context, accountID, productID, skuID uint64, price int64) (*ent.SupplierProductPrice, error) {
	client := data.Client(ctx, r.data)
	existing, err := client.SupplierProductPrice.Query().
		Where(
			supplierproductprice.SupplierAccountID(accountID),
			supplierproductprice.ProductID(productID),
			supplierproductprice.SkuID(skuID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return client.SupplierProductPrice.Create().
			SetSupplierAccountID(accountID).
			SetProductID(productID).
			SetSkuID(skuID).
			SetPrice(price).
			Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	return client.SupplierProductPrice.UpdateOneID(existing.ID).SetPrice(price).Save(ctx)
}

// PriceOf 取供貨價（覆蓋價；無覆蓋返回 0）。
func (r *SupplierRepoImpl) PriceOf(ctx context.Context, accountID, productID, skuID uint64) (int64, error) {
	row, err := data.Client(ctx, r.data).SupplierProductPrice.Query().
		Where(
			supplierproductprice.SupplierAccountID(accountID),
			supplierproductprice.ProductID(productID),
			supplierproductprice.SkuID(skuID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	return row.Price, nil
}

// ── 回調記錄（）─────────────────────────────────────────

// CreateCallback 創建回調任務（supply_order_id UNIQUE）。
func (r *SupplierRepoImpl) CreateCallback(ctx context.Context, supplyOrderID, accountID uint64, downstreamOrderNo, callbackURL, traceID string) (*ent.DownstreamCallback, error) {
	cb, err := data.Client(ctx, r.data).DownstreamCallback.Create().
		SetSupplyOrderID(supplyOrderID).
		SetAccountID(accountID).
		SetDownstreamOrderNo(downstreamOrderNo).
		SetCallbackURL(callbackURL).
		SetTraceID(traceID).
		SetCallbackStatus(downstreamcallback.CallbackStatusPending).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 已存在（重複交付）→ 冪等返回
			return r.GetCallbackByOrder(ctx, supplyOrderID)
		}
		return nil, err
	}
	return cb, nil
}

// GetCallbackByOrder 按訂單查回調。
func (r *SupplierRepoImpl) GetCallbackByOrder(ctx context.Context, supplyOrderID uint64) (*ent.DownstreamCallback, error) {
	cb, err := data.Client(ctx, r.data).DownstreamCallback.Query().
		Where(downstreamcallback.SupplyOrderID(supplyOrderID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return cb, nil
}

// ListCallbacks 回調記錄列表。
func (r *SupplierRepoImpl) ListCallbacks(ctx context.Context, status string, page, pageSize int) ([]*ent.DownstreamCallback, int, error) {
	q := data.Client(ctx, r.data).DownstreamCallback.Query().Order(ent.Desc(downstreamcallback.FieldID))
	if status != "" {
		q = q.Where(downstreamcallback.CallbackStatusEQ(downstreamcallback.CallbackStatus(status)))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	return rows, total, err
}

// ListPendingCallbacks 待發回調（重試輪詢）。
func (r *SupplierRepoImpl) ListPendingCallbacks(ctx context.Context, limit int) ([]*ent.DownstreamCallback, error) {
	return data.Client(ctx, r.data).DownstreamCallback.Query().
		Where(
			downstreamcallback.CallbackStatusIn(
				downstreamcallback.CallbackStatusPending,
				downstreamcallback.CallbackStatusFailed,
			),
		).
		Order(ent.Asc(downstreamcallback.FieldID)).
		Limit(limit).
		All(ctx)
}

// MarkCallbackResult 回調結果（success/failed + 退避計數）。
func (r *SupplierRepoImpl) MarkCallbackResult(ctx context.Context, id uint64, success bool, errMsg string) error {
	cb, err := data.Client(ctx, r.data).DownstreamCallback.Get(ctx, id)
	if err != nil {
		return err
	}
	upd := data.Client(ctx, r.data).DownstreamCallback.UpdateOneID(id).
		SetLastCallbackAt(time.Now().UTC()).
		SetRetryCount(cb.RetryCount + 1)
	if success {
		upd.SetCallbackStatus(downstreamcallback.CallbackStatusSuccess)
	} else {
		upd.SetCallbackStatus(downstreamcallback.CallbackStatusFailed).SetLastError(errMsg)
	}
	_, err = upd.Save(ctx)
	return err
}

// ResetCallback 手動重發（failed → pending + 清計數）。
func (r *SupplierRepoImpl) ResetCallback(ctx context.Context, id uint64) (*ent.DownstreamCallback, error) {
	return data.Client(ctx, r.data).DownstreamCallback.UpdateOneID(id).
		SetCallbackStatus(downstreamcallback.CallbackStatusPending).
		SetRetryCount(0).
		ClearLastError().
		Save(ctx)
}

// CredentialsOf 按賬戶取憑據（api_key 明文 + 解密 secret；回調轉發簽名用）。
func (r *SupplierRepoImpl) CredentialsOf(ctx context.Context, accountID uint64) (apiKey, apiSecret string, err error) {
	key, secret, _, err := r.CredentialsWithProtocolOf(ctx, accountID)
	return key, secret, err
}

// CredentialsWithProtocolOf 憑據 + 賬號協議（兼容層回調簽名器分支用； B）。
func (r *SupplierRepoImpl) CredentialsWithProtocolOf(ctx context.Context, accountID uint64) (apiKey, apiSecret, protocol string, err error) {
	acc, err := r.GetAccount(ctx, accountID)
	if err != nil {
		return "", "", "", err
	}
	plain, err := r.box.Open(acc.APISecret, secretAAD(acc.APIKey))
	if err != nil {
		return "", "", "", fmt.Errorf("supplier: secret 解密失敗（需重置密鑰）: %w", err)
	}
	return acc.APIKey, string(plain), string(acc.Protocol), nil
}

// ListPrices 賬號的專屬價列表（：瀏覽/管理覆蓋價）。
func (r *SupplierRepoImpl) ListPrices(ctx context.Context, accountID uint64) ([]*ent.SupplierProductPrice, error) {
	return data.Client(ctx, r.data).SupplierProductPrice.Query().
		Where(supplierproductprice.SupplierAccountID(accountID)).
		Order(ent.Desc(supplierproductprice.FieldUpdatedAt)).
		All(ctx)
}

// DeletePrice 刪除專屬價（恢復基礎供貨價）。
func (r *SupplierRepoImpl) DeletePrice(ctx context.Context, id uint64) error {
	n, err := data.Client(ctx, r.data).SupplierProductPrice.Delete().Where(supplierproductprice.ID(id)).Exec(ctx)
	if err == nil && n == 0 {
		return fmt.Errorf("supplier: 專屬價不存在")
	}
	return err
}

func (r *SupplierRepoImpl) CreateAccount(ctx context.Context, name, apiKey, apiSecret, contact, protocol, displayName string) (out *ent.SupplierAccount, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		currency, e := data.BaseCurrency(ctx, r.data)
		if e != nil {
			return e
		}
		if currency != "CNY" && protocol != "zcard" {
			return fmt.Errorf("supplier.CURRENCY_UNSUPPORTED: 此對接協議僅支持人民幣計價，請使用 ZCard 原生協議")
		}
		var err error
		out, err = r.createAccount(ctx, name, apiKey, apiSecret, contact, protocol, displayName)
		return err
	})
	return out, err
}

func (r *SupplierRepoImpl) CreateApplication(ctx context.Context, ownerUserID uint64, protocol, displayName, contact, applyReason, notifyURL, apiKey, apiSecret string) (out *ent.SupplierAccount, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		currency, e := data.BaseCurrency(ctx, r.data)
		if e != nil {
			return e
		}
		if currency != "CNY" && protocol != "zcard" {
			return fmt.Errorf("supplier.CURRENCY_UNSUPPORTED: 此對接協議僅支持人民幣計價，請使用 ZCard 原生協議")
		}
		var err error
		out, err = r.createApplication(ctx, ownerUserID, protocol, displayName, contact, applyReason, notifyURL, apiKey, apiSecret)
		return err
	})
	return out, err
}

func (r *SupplierRepoImpl) CreateSupplyRechargeOrder(ctx context.Context, userID, accountID uint64, amount, giftAmount int64) (out *ent.RechargeOrder, err error) {
	err = data.CurrencyTx(ctx, r.data, func(ctx context.Context) error {
		var e error
		out, e = r.createSupplyRechargeOrder(ctx, userID, accountID, amount, giftAmount)
		return e
	})
	return out, err
}
