// Package updater 在線更新引擎（， ——「重寫 1.x 最危險組件」；
// 2026-09 在線更新方案 定稿重構）：
//
//	ed25519 強制驗籤的 release manifest + 原子替換（rename 舞步）+ 狀態機
//	（pending→ok）+ 自動回滾。重啓策略三分支見方案 ——進程管理器
//	（systemd/supervisord）為主，裸跑由 serve 層 syscall.Exec 降級。
//
//	更新源三型（方案 ）：github 直連 / accel 加速鏡像（ghproxy 系前綴拼接）/
//	static 自建靜態源。manifest 統一走 github.com 官方 releases/latest/download
//	重定向端點（加速器不代理 REST API；直連亦免 60 次/h 匿名限流）；
//	beta 通道需列 prerelease，僅 github 直連支持。
//
// 密碼學原語與 platform/license 同源（ed25519 單文件內嵌簽名），但使用
// 獨立密鑰對（license 一把、更新一把，互不連坐）。
package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// 更新安全模型的 fail-closed 錯誤（任一命中：磁盤零變更）。
var (
	ErrBadManifest       = errors.New("updater: manifest 非法或驗籤失敗")
	ErrFileMismatch      = errors.New("updater: 產物哈希/大小與清單不符")
	ErrNoAsset           = errors.New("updater: release 缺少所需產物")
	ErrRefuseUpdate      = errors.New("updater: 拒絕更新（版本非單調或非 semver）")
	ErrNoPubkey          = errors.New("updater: 未配置更新公鑰（dev 構建）")
	ErrBetaSource        = errors.New("updater: beta 通道僅支持 github 直連/自建靜態源（加速鏡像不走 REST API）")
	ErrSourceUnreachable = errors.New("updater: 更新源均不可達")
)

// DefaultPublicKeyHex 首發更新公鑰（2026-09-05 genkey 固化；ldflags -X 可覆蓋——
// 密鑰輪換期過渡用）。私鑰離線保管（發行側 sign 子命令），泄露即換鑰發版。
var DefaultPublicKeyHex = "e7d28f99b52cda5e2596c4bea8b125c8c29cb4ca07af83aa5fe2a898c0e587cd"

// DefaultRepo 發行倉庫（github/accel 源；編譯期 -X 可覆蓋）。
var DefaultRepo = "NovaWorks/zcard-next"

// DefaultAccelerators 內置加速鏡像前綴（方案 實測 2026-09-05 篩出；
// 加速器死亡是常態——列表在 settings 可配，此處僅為默認值）。
var DefaultAccelerators = []string{
	"https://gh-proxy.com", // 實測 206 直出 + Range 斷點
	"https://ghfast.top",   // 302 規範化 latest→具體 tag 後代理
	"https://ghproxy.net",  // 同上
}

// 源類型。
const (
	SourceGitHub = "github" // github.com 直連（海外/網絡通暢）
	SourceAccel  = "accel"  // ghproxy 系加速鏡像（中國大陸）
	SourceStatic = "static" // 自建靜態源（商業發行；<base>/update.json + 裸產物）
)

// FileEntry 單個發佈產物（雙架構二進制）。
type FileEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"` // hex
	Size   int64  `json:"size"`
}

// ReleaseNote 單個歷史版本的變更記錄（歷史 changelog 面板數據源）。
type ReleaseNote struct {
	Version  string `json:"version"`           // vX.Y.Z
	Channel  string `json:"channel,omitempty"` // stable | beta
	Notes    string `json:"notes,omitempty"`   // changelog markdown
	IssuedAt string `json:"issued_at,omitempty"`
}

// Manifest 發佈清單（驗籤覆蓋 content 段全部字段——含 History）。
// History 歷史版本 changelog（最近 N 條，發行側經 sign --history-file 注入）；
// omitempty 保證**存量已簽發清單（無 history 字段）驗籤向後兼容**——nil 重編碼
// 時省略該字段，content 原文一致。
type Manifest struct {
	Version   string        `json:"version"` // vMAJOR.MINOR.PATCH
	Channel   string        `json:"channel"` // stable | beta
	Notes     string        `json:"notes"`   // changelog markdown
	IssuedAt  string        `json:"issued_at"`
	Files     []FileEntry   `json:"files"`
	History   []ReleaseNote `json:"history,omitempty"` // 歷史版本記錄（新墈權威源=manifest，三源統一免 API）
	Signature string        `json:"signature"`         // base64(ed25519.Sign(priv, content))
}

// GitHub Releases API 載荷（beta 通道列 prerelease 用；僅 github 直連）。
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// ParseSemver 解析 vMAJOR.MINOR.PATCH（dev/髒構建等非 semver 一律 nil）。
func ParseSemver(v string) [3]int {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{-1, -1, -1}
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		_, _ = fmt.Sscanf(m[i+1], "%d", &out[i])
	}
	return out
}

// IsSemver 是否合法 semver。
func IsSemver(v string) bool { return ParseSemver(v)[0] >= 0 }

// CompareSemver 比較 a、b：-1/0/1。任一非 semver 視為最小。
func CompareSemver(a, b string) int {
	sa, sb := ParseSemver(a), ParseSemver(b)
	for i := 0; i < 3; i++ {
		if sa[i] != sb[i] {
			if sa[i] < sb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// VerifyManifest 驗籤並解析 manifest（與 license.Verify 同構：重編碼 content 段
// 作為簽名原文，防字段注入）。
func VerifyManifest(raw []byte, pub ed25519.PublicKey) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, ErrBadManifest
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return nil, ErrBadManifest
	}
	content, err := json.Marshal(Manifest{
		Version: m.Version, Channel: m.Channel, Notes: m.Notes,
		IssuedAt: m.IssuedAt, Files: m.Files, History: m.History,
	})
	if err != nil {
		return nil, ErrBadManifest
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, content, sig) {
		return nil, ErrBadManifest
	}
	if !IsSemver(m.Version) || len(m.Files) == 0 {
		return nil, ErrBadManifest
	}
	return &m, nil
}

// SignManifest 發行側簽發（私鑰離線保管，見 self-update sign 子命令；
// history 為可選的歷史版本 changelog，nil/空 = 不攜帶）。
func SignManifest(priv ed25519.PrivateKey, version, channel, notes string, files []FileEntry, history []ReleaseNote) ([]byte, error) {
	m := Manifest{
		Version: version, Channel: channel, Notes: notes,
		IssuedAt: time.Now().UTC().Format(time.RFC3339), Files: files, History: history,
	}
	content, err := json.Marshal(Manifest{
		Version: m.Version, Channel: m.Channel, Notes: m.Notes,
		IssuedAt: m.IssuedAt, Files: m.Files, History: m.History,
	})
	if err != nil {
		return nil, err
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, content))
	return json.MarshalIndent(m, "", "  ")
}

// HashFile 計算流式 sha256（下載即校驗，不落盤兩遍）。
func HashFile(r io.Reader) (hexSum string, size int64, err error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Client 更新客户端（三源統一；源字段見 Source* 常量，測試經 GHBase/APIBase 注入）。
type Client struct {
	Source     string // github | accel | static
	Repo       string // "owner/repo"（github/accel）
	Accel      string // 加速前綴如 https://gh-proxy.com（accel）
	StaticBase string // 靜態源基址（static；update.json 與產物平鋪其下）
	GHBase     string // github 頁面基址（默認 https://github.com；測試注入）
	APIBase    string // github REST 基址（默認 https://api.github.com；僅 beta 通道）
	HTTP       *http.Client
	Token      string // 可選 GitHub Token（beta 通道限流緩解）
}

// NewClient 構造指定源的客户端（生產默認基址；測試可再覆寫 GHBase/APIBase）。
func NewClient(source, repo, accel, staticBase string) *Client {
	return &Client{
		Source: source, Repo: repo, Accel: accel, StaticBase: staticBase,
		GHBase: "https://github.com", APIBase: "https://api.github.com",
	}
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// dlHTTP 產物下載客户端：124MB 級大文件經免費加速鏡像（限速 1-10MB/s 常見）
// 30s 整體超時必斷（context deadline exceeded while reading body）——默認 10 分鐘，
// 與更新鏈 ctx（15min）匹配；顯式注入的 HTTP（測試/定製）尊重調用方。
func (c *Client) dlHTTP() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// githubPath github 頁面路徑（release 下載端點共用拼接）。
func (c *Client) githubPath(p string) string {
	return strings.TrimRight(c.GHBase, "/") + "/" + strings.Trim(c.Repo, "/") + "/" + p
}

// accelPrefix 加速前綴（ghproxy 系：<prefix>/https://github.com/...）。
func (c *Client) accelPrefix() string {
	return strings.TrimRight(c.Accel, "/") + "/"
}

// Desc 源展示串（status/UI/下載換源重試日誌用）。
func (c *Client) Desc() string {
	switch c.Source {
	case SourceStatic:
		return "static:" + c.StaticBase
	case SourceAccel:
		return c.Accel
	default:
		return "github"
	}
}

// manifestURL 按 source/channel 拼manifest 地址。
func (c *Client) manifestURL(channel string) (string, error) {
	const name = "update.json"
	switch c.Source {
	case SourceStatic:
		base := strings.TrimRight(c.StaticBase, "/")
		if base == "" {
			return "", fmt.Errorf("updater: static 源缺 StaticBase")
		}
		if channel == "beta" {
			return base + "/beta/" + name, nil
		}
		return base + "/" + name, nil
	case SourceAccel:
		if channel == "beta" {
			return "", ErrBetaSource
		}
		if c.Accel == "" {
			return "", fmt.Errorf("updater: accel 源缺 Accel 前綴")
		}
		return c.accelPrefix() + c.githubPath("releases/latest/download/"+name), nil
	default: // github 直連
		return c.githubPath("releases/latest/download/" + name), nil
	}
}

// assetURL 產物地址（version 來自已驗籤 manifest——tag 即版本，無注入面）。
func (c *Client) assetURL(version, name string) string {
	switch c.Source {
	case SourceStatic:
		return strings.TrimRight(c.StaticBase, "/") + "/" + name
	case SourceAccel:
		return c.accelPrefix() + c.githubPath("releases/download/"+version+"/"+name)
	default:
		return c.githubPath("releases/download/" + version + "/" + name)
	}
}

// FetchManifest 拉取並驗籤最新 release 的 update.json。
// stable：github 官方 releases/latest/download 重定向端點（三源同構；
// 加速鏡像實測正確代理該語義，自身完成 latest→具體 tag 的規範化）。
// beta：需列 prerelease——僅 github 直連走 REST API（靜態源按 <base>/beta/ 約定）。
func (c *Client) FetchManifest(ctx context.Context, channel string, pub ed25519.PublicKey) (*Manifest, string, error) {
	if channel == "beta" && c.Source == SourceGitHub {
		return c.fetchBetaViaAPI(ctx, pub)
	}
	url, err := c.manifestURL(channel)
	if err != nil {
		return nil, "", err
	}
	raw, err := c.download(ctx, url)
	if err != nil {
		return nil, "", err
	}
	m, err := VerifyManifest(raw, pub)
	if err != nil {
		return nil, "", err
	}
	return m, url, nil
}

// fetchBetaViaAPI beta 通道（github 直連）：/releases 列表取首個 prerelease
// 的 update.json 資產直鏈（加速鏡像不代理 REST API，已在 manifestURL 拒絕）。
func (c *Client) fetchBetaViaAPI(ctx context.Context, pub ed25519.PublicKey) (*Manifest, string, error) {
	url := strings.TrimRight(c.APIBase, "/") + "/repos/" + strings.Trim(c.Repo, "/") + "/releases?per_page=20"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("updater: 檢查更新失敗（%s HTTP %d）", url, resp.StatusCode)
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, "", err
	}
	for _, rel := range rels {
		if !rel.Prerelease {
			continue
		}
		for _, a := range rel.Assets {
			if a.Name != "update.json" {
				continue
			}
			raw, err := c.download(ctx, a.BrowserDownloadURL)
			if err != nil {
				return nil, "", err
			}
			m, err := VerifyManifest(raw, pub)
			if err != nil {
				return nil, "", err
			}
			return m, a.BrowserDownloadURL, nil
		}
	}
	return nil, "", ErrNoAsset
}

// ProgressFunc 下載進度回調（received 已收字節；total 為 manifest 聲明大小，未知為 0）。
type ProgressFunc func(received, total int64)

// DownloadAsset 下載指定產物到 w（流式哈希校驗，不符即 ErrFileMismatch；
// 大產物經 onProgress 報進度——124MB 二進制必須落盤不能進內存，方案 ）。
func (c *Client) DownloadAsset(ctx context.Context, m *Manifest, name string, w io.Writer, onProgress ProgressFunc) error {
	entry := -1
	for i, f := range m.Files {
		if f.Name == name {
			entry = i
			break
		}
	}
	if entry < 0 {
		return ErrNoAsset
	}
	url := c.assetURL(m.Version, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.dlHTTP().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("updater: 下載失敗（%s HTTP %d）", url, resp.StatusCode)
	}
	f := m.Files[entry]
	var src io.Reader = resp.Body
	if onProgress != nil {
		src = &progressReader{r: resp.Body, total: f.Size, fn: onProgress}
	}
	sum, size, err := HashFile(io.TeeReader(src, w))
	if err != nil {
		return err
	}
	if sum != strings.ToLower(f.SHA256) || size != f.Size {
		return ErrFileMismatch
	}
	return nil
}

// progressReader 讀取計數包裝（週期性回調，避免高頻鎖競爭）。
type progressReader struct {
	r     io.Reader
	total int64
	n     int64
	last  time.Time
	fn    ProgressFunc
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if now := time.Now(); p.last.IsZero() || now.Sub(p.last) >= 300*time.Millisecond {
		p.last = now
		p.fn(p.n, p.total)
	}
	return n, err
}

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updater: 下載 update.json 失敗（HTTP %d）", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// PublicKey 解析 hex 公鑰（CLI -pubkey / 編譯默認值共用）。
func PublicKey(hexKey string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrNoPubkey
	}
	return ed25519.PublicKey(b), nil
}

// GenerateKeyPair 生成更新密鑰對（genkey 子命令；私鑰離線保管）。
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(nil)
}
