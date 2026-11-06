package license

// ：專業套餐在線購買（發行側部署）。
//
// 模型：本部署同時充當發行側時（settings.license.purchase_privkey 配置了 ed25519
// 簽發私鑰），站點用戶可錢包餘額購買專業套餐；簽發的許可證綁定目標實例 ID
// （購買人填自己的實例 ID——在其部署後台可見；空=綁定本實例自裝）。
// 原子性：購買單落庫 + 錢包扣款 + 許可證簽發自驗 + （自購時）安裝，全部同一事務；
// 任一步失敗整單回滾（鐵律 15/16：金額服務端裁決，扣款前零落賬）。
// 經濟閉環：充值管線（真實支付渠道）→ 錢包餘額 → 購買扣款——「3U/月、30U/年」。

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/licenseorder"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	platformlicense "github.com/NovaWorks/zcard-next/server/internal/platform/license"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
)

// 專業套餐特性清單（ 首批；* 為永久通配——訂閲版逐項授權）。
var professionalFeatures = []string{"analytics", "whitelabel_ads", "auto_pricing"}

// PurchaseOffer 報價視圖。
type PurchaseOffer struct {
	MonthlyCents int64
	YearlyCents  int64
	Purchasable  bool   // 簽發私鑰已配置
	InstanceID   string // 本部署實例 ID
}

// PurchaseRepo 購買倉儲。
type PurchaseRepo struct {
	data   *data.Data
	lic    *LicenseRepo
	wallet walletport.Wallet
}

// NewPurchaseRepo 構造。
func NewPurchaseRepo(d *data.Data, lic *LicenseRepo, wallet walletport.Wallet) *PurchaseRepo {
	return &PurchaseRepo{data: d, lic: lic, wallet: wallet}
}

// settingInt 讀 license 組數值鍵（缺省回落默認值）。
func (r *PurchaseRepo) settingInt(ctx context.Context, key string, fallback int64) int64 {
	raw, err := r.lic.settings.Get(ctx, "license", key)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v float64 // settings JSON 數值統一 float64
	if json.Unmarshal(raw, &v) != nil {
		return fallback
	}
	return int64(v)
}

// Offer 報價（價格服務端裁決；鐵律 16）。
func (r *PurchaseRepo) Offer(ctx context.Context) (*PurchaseOffer, error) {
	self, err := r.lic.InstanceID(ctx)
	if err != nil {
		return nil, err
	}
	return &PurchaseOffer{
		MonthlyCents: r.settingInt(ctx, "purchase_monthly_cents", 300),
		YearlyCents:  r.settingInt(ctx, "purchase_yearly_cents", 3000),
		Purchasable:  r.issuerKey(ctx) != nil,
		InstanceID:   self,
	}, nil
}

// issuerKey 簽發私鑰（未配置返回 nil）。
func (r *PurchaseRepo) issuerKey(ctx context.Context) ed25519.PrivateKey {
	raw, err := r.lic.settings.Get(ctx, "license", "purchase_privkey")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var b64 string
	if json.Unmarshal(raw, &b64) != nil || b64 == "" {
		return nil
	}
	priv, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.PrivateKey(priv)
}

// Purchase 購買（單事務：建單 → 扣款 → 簽發自驗 → 自購安裝 → success）。
func (r *PurchaseRepo) Purchase(ctx context.Context, payer uint64, plan, targetInstance, targetDomain string) (*ent.LicenseOrder, error) {
	// 事務前 fail-fast：檔位/價格/密鑰/公鑰
	var price int64
	var months int
	switch plan {
	case "monthly":
		price, months = r.settingInt(ctx, "purchase_monthly_cents", 300), 1
	case "yearly":
		price, months = r.settingInt(ctx, "purchase_yearly_cents", 3000), 12
	default:
		return nil, fmt.Errorf("license.PLAN_INVALID")
	}
	if !money.ValidCents(price) || price <= 0 {
		return nil, fmt.Errorf("license.PRICE_INVALID")
	}
	priv := r.issuerKey(ctx)
	if priv == nil {
		return nil, fmt.Errorf("license.ISSUER_UNCONFIGURED")
	}
	pub, err := r.lic.PubKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("license.PUBKEY_UNCONFIGURED")
	}
	self, err := r.lic.InstanceID(ctx)
	if err != nil {
		return nil, err
	}
	if targetInstance == "" {
		targetInstance = self // 自購：綁定本實例
	}
	now := time.Now().UTC()
	expires := now.AddDate(0, months, 0)

	var row *ent.LicenseOrder
	err = data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		// 1) 購買單（pending）
		var derr error
		row, derr = client.LicenseOrder.Create().
			SetPlan(licenseorder.Plan(plan)).
			SetAmount(price).
			SetStatus(licenseorder.StatusPending).
			SetPayerUserID(payer).
			SetInstanceID(targetInstance).
			SetDomain(targetDomain).
			SetFeatures(professionalFeatures).
			SetExpiresAt(expires).
			Save(txCtx)
		if derr != nil {
			return derr
		}
		// 2) 錢包扣款（冪等鍵 license_order:<id>；餘額不足整單回滾）
		if err := r.wallet.DebitInTx(txCtx, walletport.Entry{
			UserID: payer, Direction: walletport.DirectionOut,
			Type: "license_purchase", Amount: money.Cents(price),
			Reference: fmt.Sprintf("license_order:%d", row.ID),
			Remark:    "專業套餐 " + plan,
		}); err != nil {
			return fmt.Errorf("license.BALANCE_INSUFFICIENT: %w", err)
		}
		// 3) 簽發 + 自驗（防簽發配置錯誤直接落庫）
		file, err := platformlicense.Sign(priv, platformlicense.License{
			InstanceID: targetInstance, Domain: targetDomain,
			Features: professionalFeatures,
			IssuedAt: now.Format(time.RFC3339), ExpiresAt: expires.Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		if _, err := platformlicense.Verify(file, pub, targetInstance, targetDomain, now); err != nil {
			return fmt.Errorf("license.SIGN_VERIFY_FAILED: %w", err)
		}
		// 4) 自購（目標=本實例）：同事務安裝直接激活
		if targetInstance == self {
			if err := r.lic.settings.Put(txCtx, "license", "file", json.RawMessage(`"`+jsonEscape(string(file))+`"`)); err != nil {
				return err
			}
		}
		// 5) 收尾 success
		row, err = client.LicenseOrder.UpdateOneID(row.ID).
			SetStatus(licenseorder.StatusSuccess).
			SetLicenseFile(string(file)).
			SetPaidAt(now).
			Save(txCtx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// ListPurchases 購買記錄（購買人視角）。
func (r *PurchaseRepo) ListPurchases(ctx context.Context, payer uint64, page, size int) ([]*ent.LicenseOrder, int64, error) {
	q := data.Client(ctx, r.data).LicenseOrder.Query().
		Where(licenseorder.PayerUserID(payer)).
		Order(ent.Desc(licenseorder.FieldID))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Clone().Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, int64(total), err
}
