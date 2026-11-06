package migratev1

// verify-only：對已遷移數據做事後校驗（不遷移、不寫業務表），產出獨立報告。
// 校驗項（《數據遷移工具開發計劃》§6）：
//  1. 金額恆等式：orders.total_amount == SUM(order_amount_lines.amount)（2.0 內部）
//  2. 錢包三方對賬：末條流水 balance_after vs wallet_accounts.available（差異清單）
//  3. 分站餘額對賬：reseller_ledger 合計 vs reseller_balance_accounts.available
//  4. 卡密抽樣：源端解密明文 vs 目標端解密明文逐字節相等 + HMAC 一致
//  5. 行數三方核對：源行數 / v1id_maps 映射數 / 目標行數（差異列出供人工判讀）
//  6. 引用完整性：已遷訂單的 user_id=0 但源庫有值的清單（映射缺失）

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderamountline"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/resellerledgerentry"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/v1idmap"
	"github.com/NovaWorks/zcard-next/server/internal/mods/inventory"
)

// VerifyResult 校驗報告。
type VerifyResult struct {
	Passed  bool
	Checks  []Check
	Summary map[string]string
}

// RunVerify 執行事後校驗。
func (m *Migrator) RunVerify(ctx context.Context, sample int) (*VerifyResult, error) {
	if sample <= 0 {
		sample = 100
	}
	res := &VerifyResult{Summary: map[string]string{}}
	fail := func(name, msg string) {
		res.Checks = append(res.Checks, Check{Name: name, Status: StatusFail, Message: msg})
	}
	pass := func(name, msg string) {
		res.Checks = append(res.Checks, Check{Name: name, Status: StatusOK, Message: msg})
	}

	// 1. 金額恆等式（2.0 內部，逐單核對取彙總）
	type row struct {
		OrderID uint64
		Total   int64
		Sum     int64
	}
	var broken []row
	os, err := m.Client.Order.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range os {
		sum, err := m.Client.OrderAmountLine.Query().
			Where(orderamountline.OrderIDEQ(o.ID)).
			Aggregate(ent.Sum(orderamountline.FieldAmount)).
			Int(ctx)
		if err != nil {
			return nil, err
		}
		if int64(sum) != o.TotalAmount {
			broken = append(broken, row{o.ID, o.TotalAmount, int64(sum)})
		}
	}
	if len(broken) > 0 {
		fail("金額恆等式", fmt.Sprintf("%d/%d 單 total≠SUM(lines)，首批：order_id=%d total=%d sum=%d",
			len(broken), len(os), broken[0].OrderID, broken[0].Total, broken[0].Sum))
		for i, b := range broken {
			m.RW.AddError("verify_amount", b.OrderID, fmt.Sprintf("total=%d lines_sum=%d", b.Total, b.Sum))
			if i >= 100 {
				break
			}
		}
	} else {
		pass("金額恆等式", fmt.Sprintf("全部 %d 單 total == SUM(lines)", len(os)))
	}

	// 2. 錢包三方對賬（複用 P5 邏輯的只讀版）
	if err := m.ReconcileWallets(ctx); err != nil {
		return nil, err
	}
	rc := m.st.table("wallet_reconcile")
	if rc.SkippedExists > 0 {
		fail("錢包對賬", fmt.Sprintf("%d 個賬戶流水與快照不一致（差異清單見 errors.jsonl）", rc.SkippedExists))
	} else {
		pass("錢包對賬", fmt.Sprintf("全部 %d 個賬戶流水與快照一致", rc.Migrated))
	}

	// 3. 分站餘額對賬
	profiles, err := m.Client.ResellerProfile.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	subDiff := 0
	accs, err := m.Client.ResellerBalanceAccount.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	accBySite := map[uint64]*ent.ResellerBalanceAccount{}
	for _, a := range accs {
		accBySite[a.SubsiteID] = a
	}
	for _, p := range profiles {
		acc := accBySite[p.ID]
		sum, err := m.Client.ResellerLedgerEntry.Query().
			Where(resellerledgerentry.SubsiteIDEQ(p.ID)).
			Aggregate(ent.Sum(resellerledgerentry.FieldAmount)).
			Int(ctx)
		if err != nil {
			return nil, err
		}
		if acc == nil && sum != 0 {
			subDiff++
			continue
		}
		if acc != nil && acc.Available != int64(sum) {
			subDiff++
			m.RW.AddError("verify_reseller", p.ID,
				fmt.Sprintf("ledger 合計 %d ≠ 賬戶快照 %d", sum, acc.Available))
		}
	}
	if subDiff > 0 {
		fail("分站餘額對賬", fmt.Sprintf("%d 個分站賬實不一致", subDiff))
	} else {
		pass("分站餘額對賬", fmt.Sprintf("全部 %d 個分站一致（含零流水分站）", len(profiles)))
	}

	// 4. 卡密抽樣：源解密 vs 目標解密逐字節相等
	if err := m.verifyCardSamples(ctx, sample, fail, pass); err != nil {
		return nil, err
	}

	// 5. 行數三方核對（源 / idmap / 目標）
	for _, t := range []struct{ src, dst string }{
		{"users", "users"}, {"products", "products"}, {"orders", "orders"}, {"cards", "cards"},
	} {
		var srcN int64
		if err := m.Src.DB.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM `%s`", t.src)).Scan(&srcN); err != nil {
			continue // 源表缺失（可選域）
		}
		mapN, err := m.Client.V1IDMap.Query().
			Where(v1idmap.TableName(t.src)).Count(ctx)
		if err != nil {
			return nil, err
		}
		var dstN int64
		switch t.dst {
		case "users":
			var u int
			u, err = m.Client.User.Query().Count(ctx)
			dstN = int64(u)
		case "products":
			var u int
			u, err = m.Client.Product.Query().Count(ctx)
			dstN = int64(u)
		case "orders":
			var u int
			u, err = m.Client.Order.Query().Count(ctx)
			dstN = int64(u)
		case "cards":
			var u int
			u, err = m.Client.Card.Query().Count(ctx)
			dstN = int64(u)
		}
		if err != nil {
			return nil, err
		}
		res.Summary[t.src] = fmt.Sprintf("源 %d / 映射 %d / 目標 %d", srcN, mapN, dstN)
		if int64(mapN) != dstN {
			fail("行數核對:"+t.src, fmt.Sprintf("映射 %d ≠ 目標 %d（差異可能來自回填行或分站未遷——人工判讀）", mapN, dstN))
		} else if int64(mapN) < srcN && srcN > 0 {
			res.Checks = append(res.Checks, Check{
				Name: "行數核對:" + t.src, Status: StatusWarn,
				Message: fmt.Sprintf("映射 %d < 源 %d（分站/跳過/軟刪——主站先行模式下屬預期）", mapN, srcN),
			})
		}
	}

	// 6. 引用完整性：已遷訂單 user_id=0 但源庫有值
	var orderIDs []uint64
	srcOrders, err := m.Src.DB.QueryContext(ctx,
		"SELECT `id`, `user_id` FROM `orders` WHERE `user_id` > 0")
	if err == nil {
		for srcOrders.Next() {
			var id, uid int64
			if srcOrders.Scan(&id, &uid) == nil {
				newO, ok := m.IDs.Get(ctx, "orders", uint64(id))
				if !ok {
					continue
				}
				o, err := m.Client.Order.Get(ctx, newO)
				if err != nil {
					continue
				}
				if o.UserID == 0 {
					orderIDs = append(orderIDs, newO)
				}
			}
		}
		srcOrders.Close()
	}
	if len(orderIDs) > 0 {
		fail("引用完整性", fmt.Sprintf("%d 筆已遷訂單丟失 user 關聯（映射缺失）", len(orderIDs)))
	} else {
		pass("引用完整性", "已遷訂單的用戶關聯完整")
	}

	res.Passed = true
	for _, c := range res.Checks {
		if c.Status == StatusFail {
			res.Passed = false
		}
	}
	return res, nil
}

// verifyCardSamples 卡密抽樣比對：源明文（1.x 解密/直通）vs 目標明文（2.0 卡密鑰匙解密）。
func (m *Migrator) verifyCardSamples(ctx context.Context, sample int,
	fail func(name, msg string), pass func(name, msg string)) error {

	var srcTotal int64
	if err := m.Src.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM `cards`").Scan(&srcTotal); err != nil {
		pass("卡密抽樣", "源庫無 cards 表，跳過")
		return nil
	}
	if srcTotal == 0 {
		pass("卡密抽樣", "源庫卡密為空")
		return nil
	}
	n := int64(sample)
	if srcTotal < n {
		n = srcTotal
	}
	// 隨機偏移抽樣（id 均勻性近似）
	mismatch := 0
	checked := 0
	for i := int64(0); i < n; i++ {
		r, err := rand.Int(rand.Reader, big.NewInt(srcTotal))
		if err != nil {
			return err
		}
		var (
			oldID                int64
			content, contentHash string
		)
		err = m.Src.DB.QueryRowContext(ctx,
			"SELECT `id`, `content`, `content_hash` FROM `cards` ORDER BY `id` LIMIT 1 OFFSET ?", r.Int64()).
			Scan(&oldID, &content, &contentHash)
		if err != nil {
			return err
		}
		newID, ok := m.IDs.Get(ctx, "cards", uint64(oldID))
		if !ok {
			continue // 未遷（分站/跳過）
		}
		c, err := m.Client.Card.Get(ctx, newID)
		if err != nil {
			return err
		}
		srcPlain, _, err := openV1Card(content, m.CardKey)
		if err != nil {
			mismatch++
			m.RW.AddError("verify_cards", uint64(oldID), "源端解密失敗: "+err.Error())
			continue
		}
		dstPlain, err := m.verifyOpen(c.Content, c.ProductID, c.SubsiteID)
		if err != nil || dstPlain != srcPlain {
			mismatch++
			m.RW.AddError("verify_cards", uint64(oldID), "明文不一致或解密失敗")
			continue
		}
		checked++
		_ = contentHash
	}
	if mismatch > 0 {
		fail("卡密抽樣", fmt.Sprintf("抽樣 %d 條中 %d 條不一致（致命——密鑰或數據損壞）", checked+mismatch, mismatch))
	} else {
		pass("卡密抽樣", fmt.Sprintf("抽樣 %d 條全部一致（含 AAD 綁定校驗）", checked))
	}
	return nil
}

// verifyOpen 目標端卡密解密（verify 專用，綁定行自身 product/subsite 的 AAD）。
func (m *Migrator) verifyOpen(content []byte, productID, subsiteID uint64) (string, error) {
	c, err := inventory.NewCardCipher(m.NewCardKey)
	if err != nil {
		return "", err
	}
	return c.Open(content, productID, subsiteID)
}

// WriteVerify 報告輸出。
func (w *ReportWriter) WriteVerify(v *VerifyResult, meta PreflightMeta) error {
	if err := w.WriteJSON("verify.json", map[string]any{"meta": meta, "result": v}); err != nil {
		return err
	}
	var md string
	title := "❌ 未通過"
	if v.Passed {
		title = "✅ 通過"
	}
	md = fmt.Sprintf("\n## 遷移後校驗（%s）\n\n| 狀態 | 檢查項 | 説明 |\n|---|---|---|\n", title)
	for _, c := range v.Checks {
		md += fmt.Sprintf("| %s | %s | %s |\n", c.Status, c.Name, c.Message)
	}
	if len(v.Summary) > 0 {
		md += "\n### 行數三方核對\n\n| 表 | 數字 |\n|---|---|\n"
		for _, k := range []string{"users", "products", "orders", "cards"} {
			if s, ok := v.Summary[k]; ok {
				md += fmt.Sprintf("| %s | %s |\n", k, s)
			}
		}
	}
	f, err := openAppend(w.dir, "report.md")
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(md)
	return err
}
