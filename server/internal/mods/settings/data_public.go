package settings

// 前台公開配置服務 + 多幣種讀取（）。
// 白名單輸出（SECRET 鍵物理隔離）；admin 貨幣 CRUD 在 service.go（AdminCurrencyService）。

import (
	"context"
	"encoding/json"
	"github.com/NovaWorks/zcard-next/server/internal/platform/theme"
	"sort"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/currency"
	"github.com/NovaWorks/zcard-next/server/internal/mods/settings/port"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/emptypb"
)

// StorefrontConfigService 前台配置服務（實現 storefrontv1）。
type StorefrontConfigService struct {
	storefrontv1.UnimplementedStorefrontConfigServiceServer
	repo Repo
}

// NewStorefrontConfigService 構造。
func NewStorefrontConfigService(repo Repo) *StorefrontConfigService {
	return &StorefrontConfigService{repo: repo}
}

// GetPublicConfig 白名單輸出：DB 值優先，無值回落目錄默認值；僅 PUBLIC_KEYS 鍵；
// SECRET 鍵雙保險（即使誤入 PublicKeys 也不下發）。
func (s *StorefrontConfigService) GetPublicConfig(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.PublicConfig, error) {
	// One snapshot avoids issuing a query for each public key on every page load.
	rows, err := s.repo.List(ctx, "")
	if err != nil {
		return nil, errors.InternalServer("settings.CONFIG_UNAVAILABLE", "店鋪配置暫時無法讀取，請稍後重試")
	}
	stored := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		stored[row.Group+"."+row.Key] = row.Value
	}
	type entry struct{ key, val string }
	var out []entry
	for _, gname := range GroupsSorted() {
		g, _ := Group(gname)
		keys := PublicKeysOf(gname)
		sort.Strings(keys)
		for _, k := range keys {
			if IsSecret(gname, k) {
				continue
			}
			val, _ := g.DefaultJSON(k)
			if raw := stored[gname+"."+k]; raw != nil {
				val = string(raw)
			}
			if isThemeKey(gname, k) {
				b, _ := json.Marshal(selectedTheme(ctx, s.repo))
				val = string(b)
			}
			out = append(out, entry{gname + "." + k, val})
			if gname == "ops" && k == "announcement" {
				var source string
				if json.Unmarshal([]byte(val), &source) == nil {
					rendered, summary := renderAnnouncement(source)
					for key, value := range map[string]string{"ops.announcement_html": rendered, "ops.announcement_summary": summary} {
						raw, _ := json.Marshal(value)
						out = append(out, entry{key, string(raw)})
					}
				}
			}
		}
	}
	runtime := theme.RuntimeFromContext(ctx)
	if runtime == nil {
		var err error
		runtime, err = NewAdminSettingsService(NewSettingsUsecase(s.repo)).runtime(ctx)
		if err != nil {
			return nil, err
		}
	}
	if runtime != nil {
		values := map[string]string{}
		for _, e := range out {
			values[e.key] = e.val
		}
		for k, v := range runtime.Values {
			raw, _ := json.Marshal(v)
			values[k] = string(raw)
		}
		if !runtime.Capabilities["cart"] {
			values["theme.nav_cart"] = "false"
		}
		out = nil
		for k, v := range values {
			out = append(out, entry{k, v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	reply := &storefrontv1.PublicConfig{}
	for _, e := range out {
		reply.Entries = append(reply.Entries, &storefrontv1.PublicConfigEntry{Key: e.key, ValueJson: e.val})
	}
	return reply, nil
}

// ListCurrencies 啓用貨幣（rate 為 decimal 字符串，前端 decimal 換算杜絕浮點）。
func (s *StorefrontConfigService) ListCurrencies(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.Currencies, error) {
	rows, err := s.repo.Currencies(ctx)
	if err != nil {
		return nil, errors.InternalServer("settings.CURRENCY_LIST_FAILED", "讀取貨幣失敗")
	}
	out := &storefrontv1.Currencies{}
	for _, c := range rows {
		out.Currencies = append(out.Currencies, &storefrontv1.Currency{
			Code:      c.Code,
			Symbol:    c.Symbol,
			Position:  c.Position,
			Precision: c.Precision,
			RateJson:  c.Rate.String(),
		})
	}
	return out, nil
}

// CurrencyView 貨幣視圖。
type CurrencyView struct {
	Code      string
	Symbol    string
	Position  string
	Precision int32
	Rate      decimal.Decimal
}

// Currencies 啓用貨幣視圖列表。
func (r *RepoImpl) Currencies(ctx context.Context) ([]CurrencyView, error) {
	rows, err := data.Client(ctx, r.data).Currency.Query().
		Where(currency.Enabled(true)).
		Order(ent.Asc(currency.FieldSort)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CurrencyView, 0, len(rows))
	for _, row := range rows {
		out = append(out, CurrencyView{
			Code: row.Code, Symbol: row.Symbol, Position: string(row.Position),
			Precision: row.Precision, Rate: decimal.NewFromFloat(row.Rate),
		})
	}
	return out, nil
}

// CurrencyByCode 按 code 取（ exchange 的取數端；port.CurrencyReader）。
func (r *RepoImpl) CurrencyByCode(ctx context.Context, code string) (string, int32, error) {
	row, err := data.Client(ctx, r.data).Currency.Query().Where(currency.Code(code), currency.Enabled(true)).Only(ctx)
	if ent.IsNotFound(err) {
		return "", 2, errors.NotFound("settings.CURRENCY_NOT_FOUND", "貨幣不存在")
	}
	if err != nil {
		return "", 2, err
	}
	return decimal.NewFromFloat(row.Rate).String(), row.Precision, nil
}

var _ port.CurrencyReader = (*RepoImpl)(nil)
