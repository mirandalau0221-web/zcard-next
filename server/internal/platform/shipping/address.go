// Package shipping contains local address metadata and shipping validation.
package shipping

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ModeFree    = "free"
	ModeFixed   = "fixed"
	ModeCollect = "collect"
)

//go:embed regions.json
var RegionsJSON []byte
var Regions map[string]map[string]string

func init() {
	if err := json.Unmarshal(RegionsJSON, &Regions); err != nil {
		panic(err)
	}
}
func Normalize(a map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range []string{"country", "region", "city", "district", "address", "address_line2", "name", "phone", "postal_code"} {
		v := strings.TrimSpace(a[k])
		if utf8.RuneCountInString(v) > 300 || strings.ContainsAny(v, "\x00\r\n") {
			return nil, fmt.Errorf("收貨信息格式無效")
		}
		// An absent/empty optional line must preserve the exact legacy map shape:
		// normalized addresses participate in persisted idempotency fingerprints.
		if k != "address_line2" || v != "" {
			out[k] = v
		}
	}
	out["country"] = strings.ToUpper(out["country"])
	return out, nil
}
func Validate(a map[string]string) (map[string]string, error) {
	out, e := Normalize(a)
	if e != nil {
		return nil, e
	}
	r, ok := Regions[out["country"]]
	if !ok {
		return nil, fmt.Errorf("請選擇有效國家或地區")
	}
	required := r["require"]
	if required == "" {
		required = "AC"
	}
	for _, k := range []string{"name", "phone", "address"} {
		if out[k] == "" {
			return nil, fmt.Errorf("請填寫收貨人、電話號碼和詳細地址")
		}
	}
	for code, key := range map[string]string{"S": "region", "C": "city", "Z": "postal_code", "D": "district"} {
		if strings.Contains(required, code) && out[key] == "" {
			return nil, fmt.Errorf("請完整填寫國家要求的地區、城市和郵編")
		}
	}
	if keys := r["sub_keys"]; keys != "" && out["region"] != "" {
		valid := false
		for _, k := range strings.Split(keys, "~") {
			if k == out["region"] {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("請選擇屬於該國家的地區")
		}
	}
	digits := 0
	for _, c := range out["phone"] {
		if c >= '0' && c <= '9' {
			digits++
		}
	}
	if digits < 6 || digits > 15 || !regexp.MustCompile(`^\+?[0-9 ()\-]{6,30}$`).MatchString(out["phone"]) {
		return nil, fmt.Errorf("電話號碼格式無效，請包含國際區號")
	}
	if pattern := r["zip"]; pattern != "" && out["postal_code"] != "" {
		if re, e := regexp.Compile("(?i)^(?:" + pattern + ")$"); e == nil && !re.MatchString(out["postal_code"]) {
			return nil, fmt.Errorf("郵編與國家格式不符")
		}
	}
	return out, nil
}
