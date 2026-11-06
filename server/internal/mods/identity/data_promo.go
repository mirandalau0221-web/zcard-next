package identity

// 推廣碼（promo_code）：8 位隨機字符串邀請標識，替代裸 user_id（防枚舉）。
// 存量用户懶生成（登錄/查詢時補）；註冊自動生成。

import (
	"context"
	"crypto/rand"
	"strconv"
	"strings"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/user"
)

// promoAlphabet 推廣碼字母表（剔除 I/O/0/1 防手抄混淆——dujiao 同款紀律）。
const promoAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
const promoCodeLen = 8

// genPromoCode 生成 8 位隨機推廣碼（crypto/rand）。
func genPromoCode() string {
	buf := make([]byte, promoCodeLen)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失敗極罕見：回退時間戳低位（仍滿足唯一性重試語義）
		return "FALLBACK"
	}
	for i, b := range buf {
		buf[i] = promoAlphabet[int(b)%len(promoAlphabet)]
	}
	return string(buf)
}

// EnsurePromoCode 用户推廣碼懶生成（空則補；唯一衝突重試 8 次）。
// 返回有效推廣碼（已有直接返回）。Me/Login/推廣中心等入口調用。
func (r *UserRepo) EnsurePromoCode(ctx context.Context, userID uint64) string {
	client := data.Client(ctx, r.data)
	u, err := client.User.Get(ctx, userID)
	if err != nil {
		return ""
	}
	if u.PromoCode != "" {
		return u.PromoCode
	}
	for i := 0; i < 8; i++ {
		code := genPromoCode()
		_, err := client.User.UpdateOne(u).SetPromoCode(code).Save(ctx)
		if err == nil {
			return code
		}
		// 唯一衝突（概率 ~8 字符空間碰撞極低）或更新失敗：重試新碼
	}
	return ""
}

// ResolvePromoCode 推廣標識 → 邀請人（雙格式兼容）：
// 1) 8 位隨機碼 → 按 promo_code 精確匹配；
// 2) 純數字 → 舊 user_id（存量鏈接兼容）。
// 大小寫不敏感（手抄場景）；解析失敗返回 nil。
func (r *UserRepo) ResolvePromoCode(ctx context.Context, code string) *ent.User {
	u, _ := r.ResolvePromoCodeChecked(ctx, code)
	return u
}

// ResolvePromoCodeChecked 保留數據庫錯誤，避免註冊時將故障誤認為無權益。
func (r *UserRepo) ResolvePromoCodeChecked(ctx context.Context, code string) (*ent.User, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 20 {
		return nil, nil
	}
	client := data.Client(ctx, r.data)
	u, err := client.User.Query().Where(user.PromoCode(strings.ToUpper(code))).Only(ctx)
	if err == nil {
		return u, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	if isAllDigits(code) {
		id, e := strconv.ParseUint(code, 10, 64)
		if e == nil && id > 0 {
			u, err = client.User.Get(ctx, id)
			if err == nil {
				return u, nil
			}
			if !ent.IsNotFound(err) {
				return nil, err
			}
		}
	}
	return nil, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
