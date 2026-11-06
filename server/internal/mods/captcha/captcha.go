// Package captcha 圖形驗證碼（位數/字符類型可配置；base64Captcha 生成 + 內存 store 一次性消費）。
//
// 場景開關收斂（dujiao 同款紀律）：VerifyScene 讀 settings.security.captcha_*，
// 未啓用場景直接放行——業務 handler 無條件調用，零侵入。
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	"github.com/mojocn/base64Captcha"
)

// 場景枚舉（對應 settings.security.captcha_* 鍵）。
const (
	SceneLogin      = "login"       // 登錄（captcha_login）
	SceneRegister   = "register"    // 註冊（captcha_register）
	SceneOrder      = "order"       // 遊客下單（captcha_order）
	SceneReset      = "reset"       // 找回密碼發碼（captcha_reset）
	SceneAdminLogin = "admin_login" // 後台登錄（captcha_admin_login）
)

// 場景 → settings 鍵映射。
var sceneKeys = map[string]string{
	SceneLogin:      "captcha_login",
	SceneRegister:   "captcha_register",
	SceneOrder:      "captcha_order",
	SceneReset:      "captcha_reset",
	SceneAdminLogin: "captcha_admin_login",
}

// 場景驗證錯誤（業務層映射友好提示用；storefront 原樣透傳，admin 經 mapLoginErr 映射）。
var (
	ErrCaptchaRequired = errors.New("captcha.REQUIRED: 請輸入圖形驗證碼")
	ErrCaptchaInvalid  = errors.New("captcha.INVALID: 圖形驗證碼錯誤或已過期")
)

// Service 驗證碼服務（生成 + 場景校驗）。
type Service struct {
	store base64Captcha.Store
	read  notifyport.SettingsReader // settings.security 讀取（nil=全關）
}

// Image 圖片及其輸入格式；格式與本次生成的答案一致。
type Image struct {
	ID          string
	ImageBase64 string
	Length      int
	Charset     string
}

// New 構造（默認 4 位純數字；內存 store 300s 過期——單副本口徑）。
func New(read notifyport.SettingsReader) *Service {
	return &Service{
		store: newMemoryStore(),
		read:  read,
	}
}

// Get 每次生成時讀取設置，無需重啓；獨立 driver 避免併發請求修改共享配置。
func (s *Service) Get(ctx context.Context) (*Image, error) {
	length, charset := s.format(ctx)
	width := length * 25
	var driver base64Captcha.Driver
	if charset == "digit" {
		driver = base64Captcha.NewDriverDigit(40, width, length, 0.7, 4)
	} else {
		source := "abcdefghijklmnopqrstuvwxyz"
		if charset == "mixed" {
			source += "0123456789"
		}
		driver = base64Captcha.NewDriverString(40, width, 2, base64Captcha.OptionShowSineLine, length, source, nil, nil, []string{"wqy-microhei.ttc"})
	}
	id, content, answer := driver.GenerateIdQuestionAnswer()
	// 混合模式保證每張圖片同時含數字和字母。
	for charset == "mixed" && (!strings.ContainsAny(answer, "0123456789") || !strings.ContainsAny(answer, "abcdefghijklmnopqrstuvwxyz")) {
		id, content, answer = driver.GenerateIdQuestionAnswer()
	}
	item, err := driver.DrawCaptcha(content)
	if err != nil {
		return nil, err
	}
	if err := s.store.Set(id, answer); err != nil {
		return nil, err
	}
	return &Image{ID: id, ImageBase64: item.EncodeB64string(), Length: length, Charset: charset}, nil
}

// format 未配置/讀取失敗/歷史非法值均回退默認值，與設置目錄一致。
func (s *Service) format(ctx context.Context) (length int, charset string) {
	length, charset = 4, "digit"
	if s.read == nil {
		return
	}
	if raw, err := s.read.GetJSON(ctx, "security", "captcha_length"); err == nil {
		var n int
		if json.Unmarshal(raw, &n) == nil && n >= 4 && n <= 8 {
			length = n
		}
	}
	if raw, err := s.read.GetJSON(ctx, "security", "captcha_charset"); err == nil {
		var v string
		if json.Unmarshal(raw, &v) == nil && (v == "digit" || v == "letter" || v == "mixed") {
			charset = v
		}
	}
	return
}

// Verify 原始校驗（一次性消費：無論對錯即刪——防重試爆破）。
func (s *Service) Verify(id, code string) bool {
	if id == "" || code == "" {
		return false
	}
	return s.store.Verify(id, strings.TrimSpace(code), true)
}

// VerifyScene 場景校驗（開關收斂）：場景未啓用直接放行；
// 啓用時 payload 缺失 → captcha.REQUIRED；校驗失敗 → captcha.INVALID。
func (s *Service) VerifyScene(ctx context.Context, scene, captchaID, captchaCode string) error {
	key, ok := sceneKeys[scene]
	if !ok {
		return errors.New("captcha.SCENE_INVALID")
	}
	if !s.sceneEnabled(ctx, key) {
		return nil // 場景未啓用：放行（零侵入）
	}
	if captchaID == "" || captchaCode == "" {
		return ErrCaptchaRequired
	}
	if !s.Verify(captchaID, captchaCode) {
		return ErrCaptchaInvalid
	}
	return nil
}

// sceneEnabled 場景開關（settings.security.captcha_*）。
// 未入庫時回退目錄默認值（captcha_register/reset=true、login/order=false）——
// 與後台表單展示一致（ListSettings 的 withDefaults 同源）。
func (s *Service) sceneEnabled(ctx context.Context, key string) bool {
	if s.read == nil {
		return false
	}
	raw, err := s.read.GetJSON(ctx, "security", key)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return sceneDefault[key]
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return sceneDefault[key]
	}
	return v
}

// sceneDefault 目錄默認值（settings.directory security 組 Defaults 同源快照）。
var sceneDefault = map[string]bool{
	"captcha_register":    true,
	"captcha_reset":       true,
	"captcha_login":       false,
	"captcha_order":       false,
	"captcha_admin_login": false,
}

// SceneEnabledFor 場景開關查詢（前端 config 已下發；此方法供內部/測試用）。
func (s *Service) SceneEnabledFor(ctx context.Context, scene string) bool {
	key, ok := sceneKeys[scene]
	if !ok {
		return false
	}
	return s.sceneEnabled(ctx, key)
}
