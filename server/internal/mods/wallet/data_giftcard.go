package wallet

// 禮品卡（，表 建）：
// 批次創建（面額/數量/有效期）→ 批量生成 code（CardCipher 同款 AES-GCM 密文
// + keyed hash 唯一索引）→ 兑換 = 查 hash → 核銷 → CreditInTx(giftcard:<id>)。
// 防爆破：兑換失敗計數（同用户 30s 窗口 5 次鎖定）；庫內無明文 code（鐵律 11）。

import (
	"context"
	"fmt"
	"sync"
	"time"

	crand "crypto/rand"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/giftcard"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/giftcardbatch"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
)

// GiftcardRepo 禮品卡倉儲（獨立構造——code 加密走 CardCipher，兑換入賬走 WalletRepoImpl）。
type GiftcardRepo struct {
	data   *data.Data
	cipher *inventory.CardCipher
	wallet *WalletRepoImpl
	// 防爆破限流（內存態：同用户失敗計數，30s 窗口 5 次鎖定）
	mu        sync.Mutex
	failures  map[uint64]int
	lockUntil map[uint64]time.Time
}

// NewGiftcardRepo 構造。
func NewGiftcardRepo(d *data.Data, cipher *inventory.CardCipher, wallet *WalletRepoImpl) *GiftcardRepo {
	return &GiftcardRepo{data: d, cipher: cipher, wallet: wallet, failures: map[uint64]int{}, lockUntil: map[uint64]time.Time{}}
}

// BatchInput 批次創建入參。
type BatchInput struct {
	BatchNo  string
	Name     string
	Amount   int64 // 面額（分）
	Quantity int32
	Operator uint64
}

// CreateBatch 批次創建 + 批量生成卡（密文 + keyed hash 唯一索引；批次號唯一冪等）。
// 返回明文碼列表——**僅此一次**（庫內無明文鐵律 11；調用方隨響應一次性下發，
// 服務端不留存），與 一次性明文交付快照同紀律。
func (r *GiftcardRepo) CreateBatch(ctx context.Context, in BatchInput) (*ent.GiftcardBatch, []string, error) {
	// 批量生成（數量上限護欄：單批 ≤ 5000）
	if in.Quantity <= 0 || in.Quantity > 5000 {
		return nil, nil, fmt.Errorf("giftcard.QUANTITY_INVALID")
	}
	var batch *ent.GiftcardBatch
	codes := make([]string, 0, in.Quantity)
	err := data.CurrencyTx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		created, err := client.GiftcardBatch.Create().
			SetBatchNo(in.BatchNo).
			SetName(in.Name).
			SetAmount(in.Amount).
			SetQuantity(in.Quantity).
			SetOperatorID(in.Operator).
			Save(txCtx)
		if err != nil {
			return fmt.Errorf("giftcard.BATCH_EXISTS: %w", err)
		}
		batch = created
		creates := make([]*ent.GiftcardCreate, 0, in.Quantity)
		for i := int32(0); i < in.Quantity; i++ {
			plain := randomGiftcardCode(in.BatchNo, i)
			codes = append(codes, plain)
			sealed, err := r.cipher.Seal(plain, 0, 0)
			if err != nil {
				return err
			}
			creates = append(creates, client.Giftcard.Create().
				SetBatchID(batch.ID).
				SetCode(sealed).
				SetCodeHash(r.cipher.ContentHash(plain)).
				SetAmount(in.Amount).
				SetStatus(giftcard.StatusUnused))
		}
		if _, err := client.Giftcard.CreateBulk(creates...).Save(txCtx); err != nil {
			return fmt.Errorf("giftcard.BULK_FAILED: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return batch, codes, nil
}

// ListBatches 批次列表。
func (r *GiftcardRepo) ListBatches(ctx context.Context, page, size int) ([]*ent.GiftcardBatch, int64, error) {
	q := data.Client(ctx, r.data).GiftcardBatch.Query().
		Where(giftcardbatch.DeletedAtIsNil()).
		Order(ent.Desc(giftcardbatch.FieldID))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, int64(total), err
}

// DeleteBatch 在同一事務中隱藏批次並作廢未兑換卡。已兑換卡和賬本保留。
// 與 Redeem 的 unused CAS 競爭：先完成兑換的保留入賬，先作廢的不能兑換。
func (r *GiftcardRepo) DeleteBatch(ctx context.Context, id uint64) error {
	return data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		batch, err := client.GiftcardBatch.Get(txCtx, id)
		if err != nil {
			return err
		}
		if !batch.DeletedAt.IsZero() {
			return nil
		}
		if _, err := client.GiftcardBatch.UpdateOneID(id).SetDeletedAt(time.Now().UTC()).Save(txCtx); err != nil {
			return err
		}
		_, err = client.Giftcard.Update().Where(giftcard.BatchID(id), giftcard.StatusEQ(giftcard.StatusUnused)).SetStatus(giftcard.StatusDisabled).Save(txCtx)
		return err
	})
}

// Redeem 兑換（登錄用户）：查 hash → 核銷 → 餘額入賬（冪等鍵 giftcard:<id>）。
// 失敗（卡不存在/已用/過期）統一返回「卡密無效」（防枚舉）；連續失敗 5 次鎖 30s。
func (r *GiftcardRepo) Redeem(ctx context.Context, code string, userID uint64) (int64, error) {
	// 防爆破：鎖定窗口內直接拒絕
	r.mu.Lock()
	if until, ok := r.lockUntil[userID]; ok && time.Now().Before(until) {
		r.mu.Unlock()
		return 0, fmt.Errorf("giftcard.LOCKED: 嘗試次數過多，請稍後再試")
	}
	r.mu.Unlock()

	client := data.Client(ctx, r.data)
	hash := r.cipher.ContentHash(code)
	g, err := client.Giftcard.Query().
		Where(giftcard.CodeHash(hash)).Only(ctx)
	if ent.IsNotFound(err) {
		r.recordFailure(userID)
		return 0, fmt.Errorf("giftcard.INVALID: 卡密無效或已使用")
	}
	if err != nil {
		return 0, err
	}
	if g.Status != giftcard.StatusUnused || (!g.ExpiresAt.IsZero() && time.Now().After(g.ExpiresAt)) {
		r.recordFailure(userID)
		return 0, fmt.Errorf("giftcard.INVALID: 卡密無效或已使用")
	}

	var credited int64
	err = data.Tx(ctx, r.data, func(txCtx context.Context) error {
		client := data.Client(txCtx, r.data)
		// 核銷（CAS：unused → used；防併發重複兑換）
		affected, err := client.Giftcard.Update().
			Where(
				giftcard.ID(g.ID),
				giftcard.StatusEQ(giftcard.StatusUnused),
			).
			SetStatus(giftcard.StatusUsed).
			SetUsedBy(userID).
			SetUsedAt(time.Now().UTC()).
			Save(txCtx)
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("giftcard.INVALID: 卡密無效或已使用")
		}
		// 餘額入賬（冪等鍵 giftcard:<id>）
		if err := r.wallet.CreditInTx(txCtx, Entry{
			UserID: userID, Direction: "in", Type: "giftcard",
			Amount: g.Amount, Reference: fmt.Sprintf("giftcard:%d", g.ID),
			Remark: "禮品卡兑換",
		}); err != nil {
			return err
		}
		credited = g.Amount
		return nil
	})
	if err != nil {
		r.recordFailure(userID)
		return 0, err
	}
	r.clearFailures(userID)
	return credited, nil
}

// recordFailure 失敗計數（30s 窗口 5 次 → 鎖 30s）。
func (r *GiftcardRepo) recordFailure(userID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures[userID]++
	if r.failures[userID] >= 5 {
		r.lockUntil[userID] = time.Now().Add(30 * time.Second)
		r.failures[userID] = 0
	}
}

func (r *GiftcardRepo) clearFailures(userID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.failures, userID)
	delete(r.lockUntil, userID)
}

// randomGiftcardCode 高熵禮品卡碼（批次號 + 序號 + 隨機段 → 大寫字母數字）。
func randomGiftcardCode(batchNo string, seq int32) string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 去易混淆字符
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return fmt.Sprintf("%s-%08d", batchNo, seq)
	}
	out := make([]byte, len(b)) // 與隨機源等長（尾部 NUL 會進明文碼/JSON）
	for i, c := range b {
		out[i] = charset[int(c)%len(charset)]
	}
	return fmt.Sprintf("%s-%s", batchNo, out)
}
