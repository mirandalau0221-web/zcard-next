package supplier

// 簽名原語（對外供貨協議， 口徑釘死）：
// 簽名串 = METHOD\nPATH(不含query)\ntimestamp\nnonce\nmd5(body) —— 舊口徑
// 簽名串 = ...\nmd5(body)\nmd5(rawQuery) —— 新口徑（v1.12.90+）
// 服務端雙口徑驗籤（先舊後新）；客户端一律新口徑。
// hex_lower(HMAC_SHA256(api_secret, 簽名串))；常數時間比較（hash_equals 語義）。

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

func md5HexBytes(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func hmacSha256Hex(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// signStringOld 舊口徑簽名串。
func signStringOld(method, path, timestamp, nonce string, body []byte) string {
	return strings.Join([]string{method, path, timestamp, nonce, md5HexBytes(body)}, "\n")
}

// signStringNew 新口徑簽名串（含 query md5 段）。
func signStringNew(method, path, rawQuery, timestamp, nonce string, body []byte) string {
	return strings.Join([]string{method, path, timestamp, nonce, md5HexBytes(body), md5HexBytes([]byte(rawQuery))}, "\n")
}

// supplySign 新口徑簽名（服務端/回調轉發共用）。
func supplySign(secret, method, path, rawQuery, timestamp, nonce string, body []byte) string {
	return hmacSha256Hex(secret, signStringNew(method, path, rawQuery, timestamp, nonce, body))
}

// verifyDual 雙口徑驗籤（常數時間；先舊後新——1.x 兼容語義）。
func verifyDual(secret, method, path, rawQuery, timestamp, nonce string, body []byte, signature string) bool {
	old := hmacSha256Hex(secret, signStringOld(method, path, timestamp, nonce, body))
	new := hmacSha256Hex(secret, signStringNew(method, path, rawQuery, timestamp, nonce, body))
	return subtle.ConstantTimeCompare([]byte(old), []byte(signature)) == 1 ||
		subtle.ConstantTimeCompare([]byte(new), []byte(signature)) == 1
}

// ── B/C 兼容層簽名原語（golden 向量見 data_compat_test.go）──

// dujiaoSign dujiao-next 3 頭協議簽名：
// hex_lower(HMAC_SHA256(secret, "METHOD\nPATH(不含query)\nts\nmd5(body)"))。
func dujiaoSign(secret, method, path, timestamp string, body []byte) string {
	msg := strings.Join([]string{method, path, timestamp, md5HexBytes(body)}, "\n")
	return hmacSha256Hex(secret, msg)
}

// acgFakaSignVerify acg-faka body MD5 驗籤：
// md5(urldecode(http_build_query(ksort 升序、剔除空字符串值、除 sign 外全部參數)) + "&key=" + appKey)。
// 與 PHP ksort/http_build_query 語義對齊（值經 rawurldecode 後拼接）。
func acgFakaSignVerify(params map[string]string, appKey, sign string) bool {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "sign" || params[k] == "" {
			continue // sign 與空字符串值不參與（0/"0" 保留）
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+params[k])
	}
	str := strings.Join(pairs, "&") + "&key=" + appKey
	return subtle.ConstantTimeCompare([]byte(fmt.Sprintf("%x", md5.Sum([]byte(str)))), []byte(sign)) == 1
}
