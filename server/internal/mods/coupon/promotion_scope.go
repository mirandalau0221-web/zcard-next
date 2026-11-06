package coupon

import (
	"github.com/go-kratos/kratos/v3/errors"
	"math"
)

// Validate writes so malformed/unknown scopes cannot silently match the whole store.
func validatePromotionScope(scope map[string]any) error {
	if scope == nil {
		return errors.BadRequest("coupon.SCOPE_INVALID", "範圍必須是 JSON 對象；全場請填寫 {}")
	}
	for key, value := range scope {
		if key != "product_ids" && key != "category_ids" {
			return errors.BadRequest("coupon.SCOPE_INVALID", "範圍僅支持 product_ids（商品）和 category_ids（分類）")
		}
		ids, ok := value.([]any)
		if !ok || len(ids) == 0 {
			return errors.BadRequest("coupon.SCOPE_INVALID", "編號列表不能為空；全場請填寫 {}")
		}
		for _, value := range ids {
			id, ok := value.(float64)
			if !ok || id <= 0 || id > 9007199254740991 || math.Trunc(id) != id {
				return errors.BadRequest("coupon.SCOPE_INVALID", "商品和分類編號必須為有效的正整數")
			}
		}
	}
	return nil
}
