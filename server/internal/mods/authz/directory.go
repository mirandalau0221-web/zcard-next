package authz

// 權限點目錄（ ：權限目錄自動生成，替代 server/middleware 手工映射）。
//
// 設計（三源合一，杜絕雙源漂移）：
// - 各模塊經 Perm 聲明權限點：Op（proto 方法全名）+ Method/Path（HTTP 註解）同條給出
// - 啓動時 Reconcile 用 http.Server.WalkRoute 提取的真實路由表逐條對賬——
// admin 前綴路由未聲明且非 Public → 啓動失敗（fail-fast， 「新增路由
// 未掛角色 = 僅超管可見」的強化版：未聲明權限點直接拒絕啓動）
// - middleware 運行時按 Op 查目錄（聲明同源，path↔op 不漂移）；miss = 403 + 告警
//
// 敏感權限點（）用 AdminOnly 標記：不進非超管內置角色種子，僅 super_admin 的 * 覆蓋。

import (
	"fmt"
	"sort"
	"sync"
)

// Perm 權限點聲明（每條 admin RPC 一條；RPC 與 HTTP 一一對應）。
type Perm struct {
	Code   string // 權限點編碼（域:動作，如 settings:read）
	Desc   string // 中文描述（權限樹展示）
	Domain string // 所屬域（樹分組；缺省取 Code 前綴）
	Op     string // proto 方法全名（middleware 查詢鍵）
	Method string // HTTP 方法（路由對賬）
	Path   string // HTTP 路徑模板（路由對賬）
	Public bool   // 免鑑權（僅登錄等極少數）
	// AdminOnly 敏感權限點：不進入非超管內置角色種子（card:view_content 等）
	AdminOnly bool
}

var (
	declMu   sync.RWMutex
	declared = []Perm{}            // 全部聲明（同 Code 可掛多條路由，如讀/寫分開的 REST 路由）
	byOpSeen = map[string]string{} // op → code（衝突檢測）
)

// Declare 註冊權限點聲明（模塊 init/providers 裝配期調用；同 Code 可多次聲明
// 以覆蓋多條路由，同 Op 衝突 panic；僅登記目錄無路由的敏感權限點可省略 Op）。
func Declare(ps ...Perm) {
	declMu.Lock()
	defer declMu.Unlock()
	for _, p := range ps {
		if p.Code == "" {
			panic("authz: 權限點聲明缺少 Code")
		}
		if p.Op != "" {
			if prev, dup := byOpSeen[p.Op]; dup && prev != p.Code {
				panic(fmt.Sprintf("authz: 權限點 Op 衝突：%s 已屬於 %s，又聲明為 %s", p.Op, prev, p.Code))
			}
			byOpSeen[p.Op] = p.Code
		}
		if p.Domain == "" {
			p.Domain = domainOf(p.Code)
		}
		declared = append(declared, p)
	}
}

func domainOf(code string) string {
	for i := 0; i < len(code); i++ {
		if code[i] == ':' {
			return code[:i]
		}
	}
	return code
}

// RouteInfo 路由對賬輸入（與 kratos http.RouteInfo 同構，避免 authz 反向依賴 transport）。
type RouteInfo struct {
	Method string
	Path   string
}

// Directory 權限目錄（構建後只讀，併發安全）。
type Directory struct {
	perms   map[string]Perm   // code → Perm
	byOp    map[string]string // op → code
	byRoute map[string]string // "METHOD /path" → code
}

// BuildDirectory 以當前全局聲明構建目錄。
func BuildDirectory() *Directory {
	declMu.RLock()
	defer declMu.RUnlock()
	d := &Directory{
		perms:   make(map[string]Perm, len(declared)),
		byOp:    make(map[string]string, len(declared)),
		byRoute: make(map[string]string, len(declared)),
	}
	for _, p := range declared {
		if _, seen := d.perms[p.Code]; !seen {
			d.perms[p.Code] = p // 元信息取首條定義
		}
		if p.Op != "" {
			d.byOp[p.Op] = p.Code
		}
		if p.Method != "" && p.Path != "" {
			d.byRoute[p.Method+" "+p.Path] = p.Code
		}
	}
	return d
}

// Reconcile 路由對賬：admin 前綴路由必須被聲明覆蓋（Public 或權限點），否則返回錯誤
// （啓動 fail-fast）。非 admin 路由（storefront/supply/回調）直接放行。
func (d *Directory) Reconcile(routes []RouteInfo, adminPrefix string) error {
	for _, r := range routes {
		if !hasPrefix(r.Path, adminPrefix) {
			continue
		}
		_, ok := d.byRoute[r.Method+" "+r.Path]
		if !ok {
			return fmt.Errorf("authz: 管理路由未聲明權限點（拒絕啓動）：%s %s——請在模塊內 authz.Declare 登記", r.Method, r.Path)
		}
	}
	return nil
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// PermissionForOp middleware 查詢：返回 (code, public, ok)。
// operation 歸一化：HTTP transport 的 operation 帶前導斜槓（/pkg.Svc/Method），
// 聲明側統一不帶——兩種形態均兼容。
func (d *Directory) PermissionForOp(op string) (code string, public bool, ok bool) {
	if len(op) > 0 && op[0] == '/' {
		op = op[1:]
	}
	c, exists := d.byOp[op]
	if !exists {
		return "", false, false
	}
	return c, d.perms[c].Public, true
}

// Perm 取權限點定義。
func (d *Directory) Perm(code string) (Perm, bool) {
	p, ok := d.perms[code]
	return p, ok
}

// Tree 權限樹節點（GetPermissionTree API 輸出）。
type TreeNode struct {
	Domain string
	Label  string
	Perms  []Perm
}

// Tree 按域分組導出（域與權限點均按字典序，展示穩定）。
func (d *Directory) Tree() []TreeNode {
	domains := map[string][]Perm{}
	for _, p := range d.perms {
		domains[p.Domain] = append(domains[p.Domain], p)
	}
	names := make([]string, 0, len(domains))
	for k := range domains {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]TreeNode, 0, len(names))
	for _, name := range names {
		perms := domains[name]
		sort.Slice(perms, func(i, j int) bool { return perms[i].Code < perms[j].Code })
		out = append(out, TreeNode{Domain: name, Label: domainLabel(name), Perms: perms})
	}
	return out
}

// ValidateCodes 校驗權限點集全部存在於目錄（防髒數據寫入角色）。
func (d *Directory) ValidateCodes(codes []string) error {
	for _, c := range codes {
		if _, ok := d.perms[c]; !ok {
			return fmt.Errorf("authz: 權限點 %q 不在目錄內（拒絕寫入角色）", c)
		}
	}
	return nil
}

// Codes 全部權限點編碼（種子與覆蓋測試用）。
func (d *Directory) Codes() []string {
	out := make([]string, 0, len(d.perms))
	for c := range d.perms {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// NonAdminOnlyCodes 非敏感權限點（內置角色種子候選）。
func (d *Directory) NonAdminOnlyCodes() []string {
	out := make([]string, 0, len(d.perms))
	for c, p := range d.perms {
		if !p.AdminOnly && !p.Public {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// AdminOnlyCodes 敏感權限點清單。
func (d *Directory) AdminOnlyCodes() []string {
	out := make([]string, 0)
	for c, p := range d.perms {
		if p.AdminOnly {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func domainLabel(domain string) string {
	if l, ok := domainLabels[domain]; ok {
		return l
	}
	return domain
}

// domainLabels 域中文名（權限樹分組標題；新域在此補充）。
var domainLabels = map[string]string{
	"auth":        "認證",
	"settings":    "設置中心",
	"authz":       "權限管理",
	"identity":    "員工管理",
	"catalog":     "商品目錄",
	"inventory":   "卡密庫存",
	"order":       "訂單",
	"payment":     "支付",
	"wallet":      "錢包",
	"supply":      "上游貨源",
	"procurement": "採購",
	"supplier":    "對外供貨",
	"reseller":    "分站",
	"affiliate":   "分銷",
	"coupon":      "營銷",
	"ticket":      "工單",
	"media":       "素材",
	"dashboard":   "工作台",
	"notify":      "通知",
	"audit":       "審計",
	"system":      "系統",
}

// All 返回全部權限點聲明（審計路徑映射等消費方使用）。
func (d *Directory) All() []Perm {
	out := make([]Perm, 0, len(d.perms))
	for _, p := range d.perms {
		out = append(out, p)
	}
	return out
}
