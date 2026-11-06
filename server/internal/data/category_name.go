package data

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const CategoryNameMaxChars = 100

func ValidateCategoryName(name string) error {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > CategoryNameMaxChars {
		return fmt.Errorf("分類名稱須為 1–100 個字符（中文和表情各按一個字符計算）")
	}
	return nil
}
