package lottery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/card"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/category"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/lotteryaccount"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/lotteryactivity"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/lotterychancelog"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/lotteryprize"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productcontrol"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productsku"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	"github.com/go-kratos/kratos/v3/errors"
)

type Repo struct {
	Data   *data.Data
	Cipher *inventory.CardCipher
	now    func() time.Time
	random func() (int, error)
}

func NewRepo(d *data.Data, c *inventory.CardCipher) *Repo {
	return &Repo{Data: d, Cipher: c, now: time.Now, random: func() (int, error) {
		n, e := rand.Int(rand.Reader, big.NewInt(10000))
		if e != nil {
			return 0, e
		}
		return int(n.Int64()), nil
	}}
}
func bad(message string) error        { return errors.BadRequest("lottery.INVALID", message) }
func site(ctx context.Context) uint64 { return tenancy.FromContext(ctx).SubsiteID }
func actor(ctx context.Context) uint64 {
	if c := identity.ClaimsFromContext(ctx); c != nil {
		return c.Subject
	}
	return 0
}
func (r *Repo) member(ctx context.Context) (uint64, error) {
	id := actor(ctx)
	if id == 0 {
		return 0, errors.Unauthorized("lottery.LOGIN_REQUIRED", "請先登錄")
	}
	ok, e := data.Client(ctx, r.Data).User.Query().Where(user.ID(id), user.StatusEQ(user.StatusActive)).Exist(ctx)
	if e != nil {
		return 0, e
	}
	if !ok {
		return 0, errors.Forbidden("lottery.USER_UNAVAILABLE", "賬號不可參與抽獎")
	}
	return id, nil
}
func (r *Repo) activity(ctx context.Context, id uint64, lock bool) (*ent.LotteryActivity, error) {
	c := data.Client(ctx, r.Data)
	if lock {
		// 條件 UPDATE 持有事務行鎖；MySQL 未變化行可能返回 0，存在性由下面查詢判斷。
		_, e := c.LotteryActivity.Update().Where(lotteryactivity.ID(id), lotteryactivity.SubsiteID(site(ctx))).AddRevision(0).Save(ctx)
		if e != nil {
			return nil, e
		}
	}
	a, e := c.LotteryActivity.Query().Where(lotteryactivity.ID(id), lotteryactivity.SubsiteID(site(ctx))).Only(ctx)
	if ent.IsNotFound(e) {
		return nil, bad("活動不存在")
	}
	return a, e
}
func (r *Repo) prizes(ctx context.Context, id uint64) ([]*ent.LotteryPrize, error) {
	return data.Client(ctx, r.Data).LotteryPrize.Query().Where(lotteryprize.ActivityID(id), lotteryprize.SubsiteID(site(ctx)), lotteryprize.Enabled(true)).Order(ent.Asc(lotteryprize.FieldSort), ent.Asc(lotteryprize.FieldID)).All(ctx)
}
func state(a *ent.LotteryActivity, now time.Time) string {
	if a.Published && a.Status != "archived" && !now.Before(a.EndAt) {
		return "ended"
	}
	if a.Status != "live" {
		return a.Status
	}
	if !now.Before(a.EndAt) {
		return "ended"
	}
	if now.Before(a.StartAt) {
		return "scheduled"
	}
	return "live"
}
func active(a *ent.LotteryActivity, now time.Time) error {
	if state(a, now) != "live" {
		return bad("活動未開始、已暫停或已結束")
	}
	return nil
}
func period(a *ent.LotteryActivity, now time.Time) string {
	if a.ChanceMode == "daily" {
		loc, e := time.LoadLocation(a.Timezone)
		if e == nil {
			return now.In(loc).Format("2006-01-02")
		}
	}
	return "once"
}

var keyRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func requestKey(key string) error {
	if !keyRE.MatchString(key) {
		return bad("請求標識無效，請刷新頁面後重試")
	}
	return nil
}
func newNo() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return "L" + hex.EncodeToString(b), nil
}
func validImage(s string) bool {
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.ContainsAny(s, "\\\r\n") {
		return true
	}
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// eligibleCard 同時用於發佈與每次抽獎；不繞過商品來源、分類隱藏、規格或控件要求。
func (r *Repo) eligibleCard(ctx context.Context, pid, sku uint64, currentRead ...bool) (*ent.Product, error) {
	c := data.Client(ctx, r.Data)
	// Saving prize references holds the parent product lock. Re-read the product
	// and SKU as current rows after any wait, rather than using an older MySQL RR
	// snapshot that may still contain a concurrently deleted SKU.
	locked := len(currentRead) > 0 && currentRead[0] && r.Data.Dialect.Capabilities().SupportsForUpdate
	pq := c.Product.Query().Where(product.ID(pid), product.SubsiteID(site(ctx)), product.Status(1))
	if locked {
		pq = pq.ForUpdate()
	}
	p, e := pq.Only(ctx)
	if e != nil {
		if ent.IsNotFound(e) {
			return nil, bad("自動發獎商品不存在或已下架")
		}
		return nil, e
	}
	if e := data.RejectServiceProduct(ctx, c, p); e != nil {
		return nil, bad(e.Error())
	}
	if p.UpstreamSourceID != 0 || string(p.StockType) != "card" || string(p.DeliveryMode) != "status" {
		return nil, bad("自動髮卡僅支持普通自營卡密和保留卡密模式")
	}
	seen := map[uint64]bool{}
	cid := p.CategoryID
	for cid != 0 {
		if seen[cid] {
			return nil, bad("商品分類層級異常")
		}
		seen[cid] = true
		cat, e := c.Category.Query().Where(category.ID(cid), category.SubsiteID(site(ctx))).Only(ctx)
		if e != nil {
			return nil, bad("商品分類不可用")
		}
		if cat.Hide {
			return nil, bad("商品分類已隱藏，請改用人工獎品")
		}
		cid = cat.ParentID
	}
	required, e := c.ProductControl.Query().Where(productcontrol.ProductID(pid), productcontrol.Required(true)).Exist(ctx)
	if e != nil {
		return nil, e
	}
	if required {
		return nil, bad("該商品有必填下單控件，請改用人工獎品")
	}
	sq := c.ProductSku.Query().Where(productsku.ProductID(pid), productsku.SubsiteID(site(ctx))).Limit(1)
	if sku != 0 {
		sq = sq.Where(productsku.ID(sku))
	}
	if locked {
		sq = sq.ForUpdate()
	}
	skus, e := sq.All(ctx)
	if e != nil {
		return nil, e
	}
	if sku != 0 && len(skus) == 0 {
		return nil, bad("獎品規格不存在或不屬於該商品")
	}
	if sku == 0 && len(skus) > 0 {
		return nil, bad("該商品有多個規格，請明確選擇獎品規格")
	}
	return p, nil
}
func (r *Repo) cardQuery(ctx context.Context, pid, sku uint64) *ent.CardQuery {
	q := data.Client(ctx, r.Data).Card.Query().Where(card.ProductID(pid), card.SubsiteID(site(ctx)), card.StatusEQ(card.StatusAvailable), card.NumberHashIsNil())
	if sku > 0 {
		return q.Where(card.SkuID(sku))
	}
	return q.Where(card.Or(card.SkuIDIsNil(), card.SkuID(0)))
}
func (r *Repo) ready(ctx context.Context, ps []*ent.LotteryPrize) error {
	total := 0
	for _, p := range ps {
		total += int(p.Probability)
		if p.Probability == 0 {
			continue
		}
		if p.Issued >= p.Quantity {
			return bad("獎品已發完，活動暫不可抽獎，請等待平台補充獎品")
		}
		if p.Mode == "card" {
			if _, e := r.eligibleCard(ctx, p.ProductID, p.SkuID); e != nil {
				return e
			}
			n, e := r.cardQuery(ctx, p.ProductID, p.SkuID).Count(ctx)
			if e != nil {
				return e
			}
			if n == 0 {
				return bad("獎品庫存不足，活動暫不可抽獎，請等待平台補貨")
			}
		}
	}
	if total <= 0 || total > 10000 {
		return bad("獎品概率配置無效")
	}
	return nil
}
func snapshot(a *ent.LotteryActivity, ps []*ent.LotteryPrize) map[string]any {
	prizes := []map[string]any{}
	for _, p := range ps {
		prizes = append(prizes, map[string]any{"id": p.ID, "name": p.Name, "mode": p.Mode, "probability": p.Probability, "quantity": p.Quantity, "product_id": p.ProductID, "sku_id": p.SkuID})
	}
	return map[string]any{"name": a.Name, "status": a.Status, "start_at": a.StartAt.Unix(), "end_at": a.EndAt.Unix(), "timezone": a.Timezone, "chance_mode": a.ChanceMode, "chance_count": a.ChanceCount, "prizes": prizes}
}
func (r *Repo) account(ctx context.Context, a *ent.LotteryActivity, uid uint64, now time.Time) (*ent.LotteryAccount, error) {
	c := data.Client(ctx, r.Data)
	per := period(a, now)
	// Activity row is held by caller: no competing insert for this activity/user/period.
	acc, e := c.LotteryAccount.Query().Where(lotteryaccount.SubsiteID(site(ctx)), lotteryaccount.ActivityID(a.ID), lotteryaccount.UserID(uid), lotteryaccount.Period(per)).Only(ctx)
	if ent.IsNotFound(e) {
		acc, e = c.LotteryAccount.Create().SetSubsiteID(site(ctx)).SetActivityID(a.ID).SetUserID(uid).SetPeriod(per).Save(ctx)
	}
	return acc, e
}
func (r *Repo) autoGrant(ctx context.Context, a *ent.LotteryActivity, acc *ent.LotteryAccount) error {
	if acc.AutoGranted || a.ChanceMode == "manual" {
		return nil
	}
	c := data.Client(ctx, r.Data)
	if _, e := c.LotteryChanceLog.Create().SetSubsiteID(site(ctx)).SetActivityID(a.ID).SetUserID(acc.UserID).SetPeriod(acc.Period).SetAmount(a.ChanceCount).SetKind("auto").SetRequestKey("auto:" + acc.Period).Save(ctx); e != nil {
		return e
	}
	if e := c.LotteryAccount.UpdateOneID(acc.ID).AddBalance(a.ChanceCount).SetAutoGranted(true).Exec(ctx); e != nil {
		return e
	}
	acc.Balance += a.ChanceCount
	acc.AutoGranted = true
	return nil
}
func (r *Repo) Claim(ctx context.Context, id, uid uint64) (int32, string, error) {
	var acc *ent.LotteryAccount
	e := data.Tx(ctx, r.Data, func(ctx context.Context) error {
		a, e := r.activity(ctx, id, true)
		if e != nil {
			return e
		}
		now := r.now()
		if e = active(a, now); e != nil {
			return e
		}
		acc, e = r.account(ctx, a, uid, now)
		if e != nil {
			return e
		}
		return r.autoGrant(ctx, a, acc)
	})
	if e != nil {
		return 0, "", e
	}
	return acc.Balance, acc.Period, nil
}
func (r *Repo) Grant(ctx context.Context, id, uid uint64, count int32, remark, key string) error {
	if count < 1 || count > 1000 || strings.TrimSpace(remark) == "" || len([]rune(remark)) > 500 {
		return bad("補發數量應為 1～1000 次，並填寫原因")
	}
	if e := requestKey(key); e != nil {
		return e
	}
	return data.Tx(ctx, r.Data, func(ctx context.Context) error {
		a, e := r.activity(ctx, id, true)
		if e != nil {
			return e
		}
		c := data.Client(ctx, r.Data)
		old, e := c.LotteryChanceLog.Query().Where(lotterychancelog.SubsiteID(site(ctx)), lotterychancelog.ActivityID(id), lotterychancelog.UserID(uid), lotterychancelog.RequestKey("grant:"+key)).Only(ctx)
		if e == nil {
			if old.Amount != count || old.Remark != remark {
				return bad("請求標識已用於其他補發操作")
			}
			return nil
		}
		if !ent.IsNotFound(e) {
			return e
		}
		if !a.Published || a.Status == "archived" || !r.now().Before(a.EndAt) {
			return bad("僅可給尚未結束的已發佈活動補發次數")
		}
		ok, e := c.User.Query().Where(user.ID(uid), user.StatusEQ(user.StatusActive)).Exist(ctx)
		if e != nil {
			return e
		}
		if !ok {
			return bad("用户不存在或已禁用")
		}
		acc, e := r.account(ctx, a, uid, r.now())
		if e != nil {
			return e
		}
		if acc.Balance > 100000-count {
			return bad("該賬號剩餘次數過多")
		}
		if _, e = c.LotteryChanceLog.Create().SetSubsiteID(site(ctx)).SetActivityID(id).SetUserID(uid).SetPeriod(acc.Period).SetAmount(count).SetKind("manual").SetRequestKey("grant:" + key).SetRemark(remark).SetAdminID(actor(ctx)).Save(ctx); e != nil {
			return e
		}
		return c.LotteryAccount.UpdateOneID(acc.ID).AddBalance(count).Exec(ctx)
	})
}
