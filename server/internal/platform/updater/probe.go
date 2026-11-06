// updater 源解析（方案 §4.3）：auto 模式 = 網絡環境自動判定。
//
// 語義「中國大陸主機自動走加速鏡像，其餘直連」，實現不做 geo IP 查詢
// （geo 服務牆內不可靠且系額外第三方依賴），而是直接探測 github.com 直連
// 可達性——判定目標本質即「GitHub 通不通」，地理只是代理變量：
//
//	通（≤timeout）→ github 直連（海外主機即此路徑）
//	超時/失敗      → 併發競速內置加速器，取最快響應者（大陸主機即此路徑）
//	全部不可達     → ErrSourceUnreachable（UI 列出各源狀態）
package updater

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SourceConfig 更新源配置（settings system/update 下發；缺省歸一）。
type SourceConfig struct {
	Mode       string   `json:"mode"`         // auto | github | accel | static（默認 auto）
	Repo       string   `json:"repo"`         // owner/repo（github/accel；空=DefaultRepo）
	Accels     []string `json:"accelerators"` // 加速前綴列表（空=DefaultAccelerators）
	StaticBase string   `json:"static_base"`  // static 基址（static 模式必填）
	Channel    string   `json:"channel"`      // stable | beta（默認 stable）
	// Supervisor 進程管理器顯式覆蓋（auto=探測[默認] | systemd | supervisord | pm2 | none）。
	// 探測盡力而為的盲區出口——重啓三分支的正確分流比探測更重要（方案 §5）。
	Supervisor string `json:"supervisor,omitempty"`
}

// Normalize 缺省填充（repo/accels/channel；mode 歸一非法值到 auto）。
func (c SourceConfig) Normalize() SourceConfig {
	out := c
	switch out.Mode {
	case SourceGitHub, SourceAccel, SourceStatic:
	default:
		out.Mode = "auto"
	}
	if strings.TrimSpace(out.Repo) == "" {
		out.Repo = DefaultRepo
	}
	if len(out.Accels) == 0 {
		out.Accels = DefaultAccelerators
	}
	if out.Channel != "beta" {
		out.Channel = "stable"
	}
	return out
}

// ProbeOutcome 源探測結果（status 接口下發——UI 展示當前源與延遲）。
type ProbeOutcome struct {
	Mode      string        `json:"mode"`       // 生效模式：github | accel | static
	Accel     string        `json:"accel"`      // accel 模式的前綴
	Source    string        `json:"source"`     // 展示串：github | <accel> | static:<base>
	Latency   time.Duration `json:"-"`          // 勝出源探測延遲
	DirectOK  bool          `json:"direct_ok"`  // 直連探測結果（auto 判據）
	DirectMS  int64         `json:"direct_ms"`  // 直連延遲（未通為 0）
	CheckedAt time.Time     `json:"checked_at"` // 探測時刻
}

// SourceDesc 源展示串（status/UI 用）。
func (o *ProbeOutcome) SourceDesc() string {
	if o == nil {
		return ""
	}
	return o.Source
}

// ResolveSource 解析生效源：釘死模式（github/accel/static）直接構造；
// auto 先探測直連，不通則競速加速器。探測目標即 manifest 端點（HEAD，
// 5s 量級超時由調用方注入——service 層緩存 10min）。
func ResolveSource(ctx context.Context, cfg SourceConfig, timeout time.Duration) (*Client, *ProbeOutcome, error) {
	cfg = cfg.Normalize()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	mk := func(source, accel string) *Client {
		c := NewClient(source, cfg.Repo, accel, cfg.StaticBase)
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
		return c
	}
	switch cfg.Mode {
	case SourceGitHub:
		return mk(SourceGitHub, ""), &ProbeOutcome{Mode: SourceGitHub, Source: "github", CheckedAt: time.Now()}, nil
	case SourceStatic:
		if strings.TrimSpace(cfg.StaticBase) == "" {
			return nil, nil, fmt.Errorf("updater: static 模式未配置 static_base")
		}
		return mk(SourceStatic, ""), &ProbeOutcome{Mode: SourceStatic, Source: "static:" + cfg.StaticBase, CheckedAt: time.Now()}, nil
	case SourceAccel:
		accel := pickFirstAccel(ctx, cfg, timeout)
		if accel == "" {
			return nil, nil, fmt.Errorf("%w（加速器列表 %v）", ErrSourceUnreachable, cfg.Accels)
		}
		return mk(SourceAccel, accel), &ProbeOutcome{Mode: SourceAccel, Accel: accel, Source: accel, CheckedAt: time.Now()}, nil
	}

	// auto：直連探測（ghbase 固定 github.com——探測真實網絡環境，非測試注入面）
	directStart := time.Now()
	directOK := probeURL(ctx, "https://github.com/"+cfg.Repo+"/releases/latest/download/update.json", timeout)
	out := &ProbeOutcome{CheckedAt: time.Now(), DirectOK: directOK}
	if directOK {
		out.DirectMS = time.Since(directStart).Milliseconds()
		out.Mode, out.Source, out.Latency = SourceGitHub, "github", time.Since(directStart)
		return mk(SourceGitHub, ""), out, nil
	}
	accel := pickFirstAccel(ctx, cfg, timeout)
	if accel == "" {
		return nil, nil, fmt.Errorf("%w（github 直連與加速器 %v 均不可達）", ErrSourceUnreachable, cfg.Accels)
	}
	out.Mode, out.Accel, out.Source, out.Latency = SourceAccel, accel, accel, 0
	return mk(SourceAccel, accel), out, nil
}

// pickFirstAccel 併發競速加速器列表，取最快成功者（ghproxy 系死亡是常態）。
func pickFirstAccel(ctx context.Context, cfg SourceConfig, timeout time.Duration) string {
	type hit struct {
		accel   string
		latency time.Duration
	}
	done := make(chan hit, len(cfg.Accels))
	var wg sync.WaitGroup
	for _, accel := range cfg.Accels {
		accel := accel
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			if probeURL(ctx, strings.TrimRight(accel, "/")+"/https://github.com/"+cfg.Repo+"/releases/latest/download/update.json", timeout) {
				done <- hit{accel, time.Since(start)}
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	var best *hit
	for h := range done {
		if best == nil || h.latency < best.latency {
			hh := h
			best = &hh
		}
	}
	if best == nil {
		return ""
	}
	return best.accel
}

// probeURL HEAD 探測：**收到任何 HTTP 響應即鏈路可達**（含 404/405/403——恰恰
// 證明服務端活着；404 是 repo 未發 release 的正常回包，若按狀態碼判可達會把
// 「首版未發佈」誤判成「網絡不通」）。僅網絡層失敗（超時/DNS/拒連）為不可達；
// 選錯路無實害——後續 manifest 驗籤 fail-closed 兜底。
func probeURL(ctx context.Context, url string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	// 不跟隨重定向：github.com 對該端點回 302 → 跳 objects.githubusercontent。
	// 跟隨則探測耗時=整條重定向鏈（目標偶發慢即誤判「直連不可達」錯走加速——
	// 港澳台等直連可用地區實測踩坑）；不跟隨時收到 github.com 的任何響應
	// （302/404 皆可）即證明直連可達，探測語義精確對齊「能到 github.com」。
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}
