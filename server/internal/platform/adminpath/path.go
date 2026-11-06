// Package adminpath validates the private admin SPA entry independently of API authentication.
package adminpath

import (
	"fmt"
	"regexp"
	"strings"
)

var segment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Normalize accepts a relative or absolute URL path; empty means the startup default.
func Normalize(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	p := strings.Trim(value, "/")
	if p == "" || len(p) > 128 {
		return "", fmt.Errorf("後台路徑必須為 1–128 位字母、數字、短橫線或下劃線，可用 / 分隔")
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if !segment.MatchString(part) {
			return "", fmt.Errorf("後台路徑只能包含字母、數字、短橫線、下劃線及路徑分隔符")
		}
	}
	switch strings.ToLower(parts[0]) {
	case "payment", "tickets", "withdraw", "points", "coupons", "api", "uploads", "health", "payments", "install", "templates", "assets", "product", "products", "category", "categories", "login", "register", "member", "cart", "checkout", "order", "orders", "post", "posts", "fetch", "supplier", "supplier-apply", "recharge", "affiliate", "reset-password", "forgot-password":
		return "", fmt.Errorf("此路徑已被系統或前台使用，請換一個後台路徑")
	}
	return "/" + p, nil
}
