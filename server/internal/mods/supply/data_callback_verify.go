package supply

// E：上游回調接收驗籤（採購三通道之回調通道的服務端側）。
//
// 我們作為下游時，上游（zcard / dujiao-next）交付後會主動 POST 回調：
// zcard 四頭 X-Supply-*（雙口徑驗籤，與自家協議同源；supplier/signing.go）
// dujiao 三頭 Dujiao-Next-*（簽名 path 固定為協議常量 /api/v1/upstream/callback，
// 非實際接收路徑——dujiao-next downstreamcallback 客户端口徑）
// acg 協議無回調（同步交付），返回 ErrCallbackNotSupported
//
// 防重放：時間窗 + 「ts+簽名摘要」組合鍵進程內去重（短窗內同一簽名只接受一次；
// 跨窗重放與併發重放由採購狀態機 CAS 冪等兜底——confirmResult 只生效一次）。
// 驗籤/查單失敗統一 ErrCallbackVerifyFailed（HTTP 側 401 防枚舉，1.x 紀律）。

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
)

// 回調驗籤參數。
const (
	callbackZcardSkew  = 300 // 秒（對齊自家協議）
	callbackDujiaoSkew = 60  // 秒（對齊 dujiao-next）
	callbackReplayWin  = 15 * time.Minute
	callbackMaxKeys    = 4096 // 去重窗口容量（粗界防內存膨脹）
)

// callbackReplayGuard 「ts+簽名摘要」進程內去重（短窗）。
var callbackReplayGuard = struct {
	sync.Mutex
	seen map[string]time.Time
}{seen: map[string]time.Time{}}

func replayOnce(key string) bool {
	callbackReplayGuard.Lock()
	defer callbackReplayGuard.Unlock()
	now := time.Now()
	if t, ok := callbackReplayGuard.seen[key]; ok && now.Sub(t) < callbackReplayWin {
		return false // 窗口內重放
	}
	if len(callbackReplayGuard.seen) >= callbackMaxKeys {
		callbackReplayGuard.seen = map[string]time.Time{} // 粗界重置（狀態機冪等兜底）
	}
	callbackReplayGuard.seen[key] = now
	return true
}

// VerifyUpstreamCallback 校驗並解析上游回調（port.UpstreamGateway 擴展）。
func (g *Gateway) VerifyUpstreamCallback(ctx context.Context, connectionID uint64, auth *supplyport.UpstreamCallbackAuth) (*supplyport.UpstreamCallbackResult, error) {
	conn, err := g.repo.GetConnection(ctx, connectionID)
	if err != nil {
		return nil, supplyport.ErrCallbackVerifyFailed // 連接不可見（防枚舉）
	}
	credsJSON, err := g.repo.OpenCredentials(conn)
	if err != nil {
		return nil, supplyport.ErrCallbackVerifyFailed
	}
	var creds struct {
		APIKey    string `json:"api_key"`
		APISecret string `json:"api_secret"`
	}
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return nil, supplyport.ErrCallbackVerifyFailed
	}

	var result *supplyport.UpstreamCallbackResult
	switch conn.Driver {
	case "zcard":
		result, err = verifyZcardCallback(creds.APIKey, creds.APISecret, auth)
	case "dujiao_next":
		result, err = verifyDujiaoCallback(creds.APIKey, creds.APISecret, auth)
	default:
		return nil, supplyport.ErrCallbackNotSupported
	}
	if err != nil {
		return nil, supplyport.ErrCallbackVerifyFailed
	}
	if result.DownstreamOrderNo == "" {
		return nil, supplyport.ErrCallbackVerifyFailed
	}
	return result, nil
}

// verifyZcardCallback zcard 四頭雙口徑驗籤 + 載荷解析。
func verifyZcardCallback(apiKey, secret string, auth *supplyport.UpstreamCallbackAuth) (*supplyport.UpstreamCallbackResult, error) {
	key := auth.Headers["X-Supply-Key"]
	ts := auth.Headers["X-Supply-Timestamp"]
	nonce := auth.Headers["X-Supply-Nonce"]
	sig := auth.Headers["X-Supply-Signature"]
	if key == "" || ts == "" || nonce == "" || sig == "" || key != apiKey {
		return nil, fmt.Errorf("missing headers")
	}
	tsNum, err := strconvParseInt(ts)
	if err != nil || absInt64(time.Now().Unix()-tsNum) > callbackZcardSkew {
		return nil, fmt.Errorf("timestamp skew")
	}
	// 雙口徑驗籤（supplier 包同源算法的本地實現——避免跨模塊 import）
	if !verifySupplyDual(secret, auth.Method, auth.Path, auth.RawQuery, ts, nonce, auth.Body, sig) {
		return nil, fmt.Errorf("invalid signature")
	}
	if !replayOnce(ts + "|" + sig) {
		return nil, fmt.Errorf("replay")
	}
	var payload struct {
		DownstreamOrderNo string `json:"downstream_order_no"`
		Status            string `json:"status"`
		Amount            int64  `json:"amount"`
	}
	if err := json.Unmarshal(auth.Body, &payload); err != nil {
		return nil, err
	}
	// zcard 回調不攜帶卡密（確認後經查單獲取）
	return &supplyport.UpstreamCallbackResult{
		DownstreamOrderNo: payload.DownstreamOrderNo, Status: payload.Status, Amount: payload.Amount,
	}, nil
}

// verifyDujiaoCallback dujiao 三頭驗籤（path 固定協議常量）+ 事件載荷解析。
func verifyDujiaoCallback(apiKey, secret string, auth *supplyport.UpstreamCallbackAuth) (*supplyport.UpstreamCallbackResult, error) {
	key := auth.Headers["Dujiao-Next-Api-Key"]
	ts := auth.Headers["Dujiao-Next-Timestamp"]
	sig := auth.Headers["Dujiao-Next-Signature"]
	if key == "" || ts == "" || sig == "" || key != apiKey {
		return nil, fmt.Errorf("missing headers")
	}
	tsNum, err := strconvParseInt(ts)
	if err != nil || absInt64(time.Now().Unix()-tsNum) > callbackDujiaoSkew {
		return nil, fmt.Errorf("timestamp skew")
	}
	if !verifyHmacEq(secret, "POST", "/api/v1/upstream/callback", ts, auth.Body, sig) {
		return nil, fmt.Errorf("invalid signature")
	}
	if !replayOnce(ts + "|" + sig) {
		return nil, fmt.Errorf("replay")
	}
	var payload struct {
		Event             string `json:"event"`
		DownstreamOrderNo string `json:"downstream_order_no"`
		Status            string `json:"status"`
		Fulfillment       *struct {
			Status  string `json:"status"`
			Payload string `json:"payload"`
		} `json:"fulfillment"`
	}
	if err := json.Unmarshal(auth.Body, &payload); err != nil {
		return nil, err
	}
	out := &supplyport.UpstreamCallbackResult{
		DownstreamOrderNo: payload.DownstreamOrderNo, Status: payload.Status,
	}
	if payload.Fulfillment != nil && payload.Fulfillment.Status == "delivered" {
		out.Status = "delivered"
		out.Cards = splitCardLines(payload.Fulfillment.Payload)
	}
	return out, nil
}

// splitCardLines 卡密文本按行拆分（\r\n/\r/\n 兼容；trim 空行）。
func splitCardLines(s string) []string {
	var out []string
	start := 0
	flush := func(seg string) {
		if seg != "" {
			out = append(out, seg)
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			flush(trimStr(s[start:i]))
			start = i + 1
		} else if s[i] == '\r' {
			flush(trimStr(s[start:i]))
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	flush(trimStr(s[start:]))
	return out
}

func trimStr(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// ── 本地純函數（避免跨模塊 import supplier 包；算法口徑與之一致，golden 見測試）──

func strconvParseInt(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// verifySupplyDual zcard 四頭雙口徑驗籤（supplier/signing.go 同源算法）。
func verifySupplyDual(secret, method, path, rawQuery, timestamp, nonce string, body []byte, signature string) bool {
	md5b := md5hex(body)
	md5q := md5hex([]byte(rawQuery))
	old := hmacHex(secret, strings.Join([]string{method, path, timestamp, nonce, md5b}, "\n"))
	newS := hmacHex(secret, strings.Join([]string{method, path, timestamp, nonce, md5b, md5q}, "\n"))
	return ctEq(old, signature) || ctEq(newS, signature)
}

// verifyHmacEq dujiao 三頭驗籤（簽名串 METHOD/PATH/ts/md5(body) 換行連接）。
func verifyHmacEq(secret, method, path, timestamp string, body []byte, signature string) bool {
	return ctEq(hmacHex(secret, strings.Join([]string{method, path, timestamp, md5hex(body)}, "\n")), signature)
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func hmacHex(secret, msg string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

func ctEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
