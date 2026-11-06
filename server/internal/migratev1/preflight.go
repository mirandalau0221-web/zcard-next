package migratev1

// preflight 巡檢：源庫連通性 / 表完整性 / schema 版本探測 / 密鑰解析與抽樣自檢。
// 任何 FAIL 即拒絕遷移（不帶病遷移，《數據遷移工具開發計劃》§2.2）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/migratev1/laracrypt"
)

// CheckStatus 巡檢項狀態。
type CheckStatus string

const (
	StatusOK   CheckStatus = "ok"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
	StatusInfo CheckStatus = "info"
)

// Check 單項巡檢結果。
type Check struct {
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message"`
}

// PreflightOptions 巡檢輸入。
type PreflightOptions struct {
	// Env 1.x .env 鍵集（--old-env 解析產物；可為 nil）。
	Env map[string]string
	// AppKeyRaw / CardKeyRaw 顯式密鑰串（--old-app-key / --old-card-key）。
	AppKeyRaw, CardKeyRaw string
	// SampleLimit 卡密抽樣行數（默認 10）。
	SampleLimit int
	// SkipMAC 搶救場景跳過 MAC 校驗（正常路徑必須校驗）。
	SkipMAC bool
}

// PreflightReport 巡檢報告。
type PreflightReport struct {
	GeneratedAt   time.Time
	ServerVersion string
	Checks        []Check

	// 密鑰解析結果（後續階段直接複用，避免重複解析）。
	AppKey     []byte `json:"-"`
	AppKeyRaw  string // 脱敏展示用（來源説明）
	CardKey    []byte `json:"-"`
	CardKeyRaw string

	// 卡密抽樣統計。
	CardSampled       int
	CardEncrypted     int
	CardPlaintext     int
	CardDecryptFailed int

	// 規模概覽（信息級）。
	TableCounts map[string]int64

	// Timezone 1.x 時區口徑（APP_TIMEZONE；空 = 未提供，按 UTC 處理）。
	Timezone string
}

// HasFail 是否存在致命項。
func (r *PreflightReport) HasFail() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

func (r *PreflightReport) add(status CheckStatus, name, msg string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Message: msg})
}

// requiredTables 缺失即 FAIL 的核心表；optionalTables 缺失僅 WARN。
var (
	requiredTables = []string{
		"users", "user_groups", "categories", "products", "product_skus",
		"card_imports", "cards", "orders", "order_items", "order_deliveries",
		"payments", "payment_channels", "recharges", "coupons", "reviews",
		"bills", "withdrawals", "commissions", "merchants", "merchant_members",
		"currencies", "settings", "media", "media_categories",
		"supply_sources", "supplier_accounts", "supplier_product_prices",
		"supplier_ledger_entries", "supply_orders",
		"subsite_domains", "subsite_product_settings", "subsite_ledger_entries", "subsite_order_snapshots",
	}
	optionalTables = []string{"supply_sync_tasks", "security_audit_logs", "visit_logs"}
)

// integerTypes 「元→分」改造後應為整型族的列（探測舊 schema 用）。
var integerTypes = map[string]bool{
	"tinyint": true, "smallint": true, "mediumint": true, "int": true, "bigint": true,
}

// RunPreflight 執行巡檢。返回報告；報告內含 FAIL 項時由調用方決定退出。
func RunPreflight(ctx context.Context, src *Source, opt PreflightOptions) (*PreflightReport, error) {
	if opt.SampleLimit <= 0 {
		opt.SampleLimit = 10
	}
	r := &PreflightReport{
		GeneratedAt: time.Now(),
		TableCounts: map[string]int64{},
	}

	// 1. 連通性與版本
	ver, err := src.ServerVersion(ctx)
	if err != nil {
		r.add(StatusFail, "源庫連通性", "無法連接/查詢失敗: "+err.Error())
		return r, nil
	}
	r.ServerVersion = ver
	r.add(StatusOK, "源庫連通性", "MySQL "+ver)

	// 2. 表完整性
	for _, t := range requiredTables {
		ok, err := src.TableExists(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("巡檢表 %s 失敗: %w", t, err)
		}
		if !ok {
			r.add(StatusFail, "表完整性", fmt.Sprintf("核心表 %s 不存在——疑似非 ZCard 1.x 庫或庫不完整", t))
		}
	}
	for _, t := range optionalTables {
		ok, err := src.TableExists(ctx, t)
		if err != nil {
			return nil, err
		}
		if !ok {
			r.add(StatusWarn, "表完整性", fmt.Sprintf("可選表 %s 不存在（將跳過對應數據）", t))
		}
	}
	if !r.HasFail() {
		r.add(StatusOK, "表完整性", fmt.Sprintf("核心表 %d 張齊全（可選表 %d 張）", len(requiredTables), len(optionalTables)))
	}

	// 3. schema 版本探測：「元→分」改造（1.x 2026_08_02 遷移）後的列必須是整型。
	//    停在改造前的老庫金額差 100 倍，必須先升級 1.x 而不是在遷移工具裏換算。
	for _, col := range [][2]string{
		{"user_groups", "min_recharge"}, {"user_groups", "min_consumption"},
		{"cards", "draft_premium"}, {"cards", "draft_cost"},
	} {
		dt, err := src.ColumnDataType(ctx, col[0], col[1])
		if err != nil {
			r.add(StatusWarn, "schema 版本探測", err.Error()+"（列可能不存在於更老版本，需先升級 1.x）")
			continue
		}
		if !integerTypes[dt] {
			r.add(StatusFail, "schema 版本探測",
				fmt.Sprintf("%s.%s 仍為 %s 類型——源庫停在「元→分」改造（1.x 2026_08_02）之前，請先把 1.x 升級到 ≥ v1.9.x 再遷移",
					col[0], col[1], dt))
		}
	}
	if !r.HasFail() {
		r.add(StatusOK, "schema 版本探測", "金額列均為整型（分），schema 版本滿足要求")
	}

	// 4. 密鑰解析（顯式 flag > 環境變量 > .env；卡密鑰匙兜底走源庫 settings）
	r.AppKeyRaw = firstNonEmpty(opt.AppKeyRaw, os.Getenv("ZCARD_V1_APP_KEY"), opt.Env["APP_KEY"])
	if r.AppKeyRaw != "" {
		key, err := laracrypt.ParseKey(r.AppKeyRaw)
		if err != nil {
			r.add(StatusFail, "APP_KEY 解析", err.Error())
		} else {
			r.AppKey = key
			r.add(StatusOK, "APP_KEY 解析", "已解析（32 字節）")
		}
	} else {
		r.add(StatusWarn, "APP_KEY 解析", "未提供 APP_KEY（--old-app-key / ZCARD_V1_APP_KEY / .env APP_KEY）；若源庫存在加密憑據將無法遷移")
	}

	r.CardKeyRaw = firstNonEmpty(opt.CardKeyRaw, os.Getenv("ZCARD_V1_CARD_KEY"), opt.Env["CARD_ENCRYPTION_KEY"])
	cardKeyFrom := "flag/env/.env"
	if r.CardKeyRaw != "" {
		key, err := laracrypt.ParseKey(r.CardKeyRaw)
		if err != nil {
			r.add(StatusFail, "卡密鑰匙解析", err.Error())
			r.CardKeyRaw = ""
		} else {
			r.CardKey = key
		}
	}
	// 兜底：源庫 settings.card_encryption_key（值本身可能是 Crypt 密文或歷史明文）
	if r.CardKey == nil {
		raw, found, err := src.SettingValue(ctx, "card_encryption_key")
		if err != nil {
			r.add(StatusWarn, "卡密鑰匙解析", "讀取 settings.card_encryption_key 失敗: "+err.Error())
		} else if found {
			if r.AppKey == nil {
				r.add(StatusWarn, "卡密鑰匙解析", "settings 中存有卡密鑰匙但無 APP_KEY 可解密——請提供 --old-app-key 或改用 --old-card-key")
			} else {
				key, fromCipher, kerr := laracrypt.CardKeyFromSetting(raw, r.AppKey, !opt.SkipMAC)
				if kerr != nil {
					r.add(StatusFail, "卡密鑰匙解析", "settings.card_encryption_key 無法解析: "+kerr.Error())
				} else {
					r.CardKey = key
					cardKeyFrom = "settings.card_encryption_key"
					if fromCipher {
						r.add(StatusOK, "卡密鑰匙解析", "已從 settings 解出（Crypt 密文形態）")
					} else {
						r.add(StatusWarn, "卡密鑰匙解析", "已從 settings 解出（歷史明文形態，建議在 1.x 後台重存為密文）")
					}
				}
			}
		}
	}
	if r.CardKey != nil && cardKeyFrom == "flag/env/.env" {
		r.add(StatusOK, "卡密鑰匙解析", "已解析（來源：flag/env/.env）")
	}

	// 5. 卡密抽樣自檢：形態統計 + （有密文時）解密驗證
	contents, err := src.SampleStrings(ctx, "cards", "content", opt.SampleLimit)
	if err != nil {
		r.add(StatusWarn, "卡密抽樣", "抽樣失敗: "+err.Error())
	} else {
		r.CardSampled = len(contents)
		for _, c := range contents {
			if !laracrypt.LooksEncrypted(c) {
				r.CardPlaintext++
				continue
			}
			r.CardEncrypted++
			if r.CardKey == nil {
				r.CardDecryptFailed++
				continue
			}
			if _, _, derr := laracrypt.OpenCard(c, r.CardKey, !opt.SkipMAC); derr != nil {
				r.CardDecryptFailed++
			}
		}
		switch {
		case r.CardSampled == 0:
			r.add(StatusInfo, "卡密抽樣", "cards 表為空（或無樣本）")
		case r.CardEncrypted == 0:
			r.add(StatusInfo, "卡密抽樣", fmt.Sprintf("抽樣 %d 條全部為明文形態（加密開關關閉期的存量）", r.CardSampled))
		case r.CardKey == nil:
			r.add(StatusFail, "卡密抽樣", fmt.Sprintf("抽樣 %d 條中 %d 條為密文，但卡密鑰匙不可用——請提供 --old-card-key / .env CARD_ENCRYPTION_KEY 或可解的 APP_KEY",
				r.CardSampled, r.CardEncrypted))
		case r.CardDecryptFailed > 0:
			r.add(StatusFail, "卡密抽樣", fmt.Sprintf("抽樣 %d 條中 %d 條解密失敗——疑似鑰匙錯配（MAC 拒收）或數據損壞",
				r.CardEncrypted, r.CardDecryptFailed))
		default:
			r.add(StatusOK, "卡密抽樣", fmt.Sprintf("抽樣 %d 條：密文 %d 全部解密成功，明文 %d", r.CardSampled, r.CardEncrypted, r.CardPlaintext))
		}
	}

	// 6. 憑據抽樣自檢（APP_KEY 系：supply credentials / payment config / settings SECRET）
	credChecks := []struct{ table, column string }{
		{"supply_sources", "credentials"},
		{"payment_channels", "config"},
	}
	credEncrypted := 0
	for _, cc := range credChecks {
		exists, err := src.TableExists(ctx, cc.table)
		if err != nil || !exists {
			continue
		}
		samples, err := src.SampleStrings(ctx, cc.table, cc.column, 2)
		if err != nil {
			continue // 列不存在等：表結構巡檢已覆蓋
		}
		for _, s := range samples {
			var inner string
			if cc.table == "payment_channels" {
				// 雙層：列值是 JSON 字符串，內層才是密文
				if e := json.Unmarshal([]byte(s), &inner); e != nil {
					continue // 歷史明文 JSON
				}
				s = inner
			}
			if !laracrypt.LooksEncrypted(s) {
				continue
			}
			credEncrypted++
			if r.AppKey == nil {
				r.add(StatusFail, "憑據抽樣", fmt.Sprintf("%s.%s 存在密文但 APP_KEY 不可用", cc.table, cc.column))
				continue
			}
			c, _ := laracrypt.New(r.AppKey)
			if _, derr := c.OpenString(s); derr != nil {
				r.add(StatusFail, "憑據抽樣", fmt.Sprintf("%s.%s 解密失敗（鑰匙錯配?）: %v", cc.table, cc.column, derr))
			}
		}
	}
	// settings SECRET（mail_password 為代表）
	if raw, found, _ := src.SettingValue(ctx, "mail_password"); found {
		var inner string
		if e := json.Unmarshal([]byte(raw), &inner); e == nil && laracrypt.LooksEncrypted(inner) {
			credEncrypted++
			if r.AppKey == nil {
				r.add(StatusFail, "憑據抽樣", "settings.mail_password 存在密文但 APP_KEY 不可用")
			} else if c, _ := laracrypt.New(r.AppKey); c != nil {
				if _, derr := c.OpenString(inner); derr != nil {
					r.add(StatusFail, "憑據抽樣", "settings.mail_password 解密失敗: "+derr.Error())
				}
			}
		}
	}
	if credEncrypted > 0 && !r.HasFail() {
		r.add(StatusOK, "憑據抽樣", fmt.Sprintf("發現 %d 處密文憑據，抽樣解密全部通過", credEncrypted))
	}
	if credEncrypted == 0 {
		r.add(StatusInfo, "憑據抽樣", "未發現密文憑據（全為明文配置或表為空）")
	}

	// 7. 時區口徑
	r.Timezone = opt.Env["APP_TIMEZONE"]
	if r.Timezone == "" {
		r.add(StatusInfo, "時區口徑", "未提供 APP_TIMEZONE——時間列將按 UTC 處理；若 1.x 實際為 Asia/Shanghai 請在 .env 標註後重跑")
	} else {
		r.add(StatusOK, "時區口徑", "APP_TIMEZONE="+r.Timezone)
	}

	// 8. 規模概覽（信息級）
	for _, t := range []string{"users", "products", "cards", "orders", "payments", "bills"} {
		if n, err := src.CountRows(ctx, t); err == nil {
			r.TableCounts[t] = n
		}
	}

	// 9. 只讀建議（常駐提示）
	r.add(StatusWarn, "安全建議", "遷移全程只讀源庫；生產切換請使用只讀賬號 + 提前歸檔兩把密鑰（鑰匙丟失=卡密/憑據永久不可遷）")
	return r, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ErrPreflightFailed 巡檢存在致命項（main 轉為非零退出碼）。
var ErrPreflightFailed = errors.New("preflight 未通過")
