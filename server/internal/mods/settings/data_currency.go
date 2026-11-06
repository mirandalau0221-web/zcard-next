package settings

// admin 貨幣管理實現（）：rate 走 decimal 解析（拒絕非法字符串），無浮點入口。

import (
	"context"
	"strconv"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/currency"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/emptypb"
)

// baseCurrencyDefault 基礎貨幣默認 code（與 directory.go i18n.base_currency 默認一致）。
const baseCurrencyDefault = "CNY"

// EnsureDefaultCurrencies 基礎貨幣種子（冪等；）：
// 新裝（Install 事務內）與老庫升級（serve 啓動補種）都調用。
// 僅缺 CNY 時寫入（rate=1 恆等基礎貨幣）；已存在不覆蓋——管理員可能
// 改過 symbol/精度等，尊重存量。
func EnsureDefaultCurrencies(ctx context.Context, d *data.Data) error {
	client := data.Client(ctx, d)
	exists, err := client.Currency.Query().Where(currency.Code(baseCurrencyDefault)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if err := client.Currency.Create().
		SetCode(baseCurrencyDefault).
		SetSymbol("¥").
		SetPosition(currency.PositionPrefix).
		SetPrecision(2).
		SetRate(1).
		SetEnabled(true).
		SetSort(0).
		OnConflictColumns(currency.FieldCode).
		Ignore().
		Exec(ctx); err != nil {
		return err
	}
	return nil
}

// ensureBaseRateOne 基礎貨幣匯率恆為 1（rate 語義：1 基礎貨幣 = rate 目標幣）。
func ensureBaseRateOne(ctx context.Context, d *data.Data, code string, rate float64) error {
	base, err := data.BaseCurrency(ctx, d)
	if err != nil {
		return err
	}
	if code == base && rate != 1 {
		return errors.BadRequest("settings.CURRENCY_BASE_RATE", "基礎貨幣匯率必須為 1（記賬恆為基礎貨幣）")
	}
	return nil
}

// AdminCurrencyService 貨幣管理（實現 adminv1）。
type AdminCurrencyService struct {
	adminv1.UnimplementedAdminCurrencyServiceServer
	data *data.Data
}

// NewAdminCurrencyService 構造。
func NewAdminCurrencyService(d *data.Data) *AdminCurrencyService {
	return &AdminCurrencyService{data: d}
}

// ListCurrencies 全部貨幣（含停用）。
func (s *AdminCurrencyService) ListCurrencies(ctx context.Context, _ *emptypb.Empty) (*adminv1.CurrencyList, error) {
	rows, err := data.Client(ctx, s.data).Currency.Query().Order(ent.Asc(currency.FieldSort)).All(ctx)
	if err != nil {
		return nil, errors.InternalServer("settings.CURRENCY_LIST_FAILED", "讀取貨幣失敗")
	}
	out := &adminv1.CurrencyList{}
	for _, r := range rows {
		out.Currencies = append(out.Currencies, toPBCurrency(r))
	}
	return out, nil
}

// CreateCurrency 新增（code 唯一）。
func (s *AdminCurrencyService) createCurrency(ctx context.Context, req *adminv1.CreateCurrencyRequest) (*adminv1.Currency, error) {
	if !currencyCodePattern.MatchString(req.GetCode()) {
		return nil, errors.BadRequest("settings.CURRENCY_BAD_CODE", "貨幣代碼須為三位大寫字母，如 AUD")
	}
	rate, err := parseRate(req.GetRateJson())
	if err != nil {
		return nil, err
	}
	if err := ensureBaseRateOne(ctx, s.data, req.GetCode(), rate); err != nil {
		return nil, err
	}
	pos := req.GetPosition()
	if pos == "" {
		pos = "prefix"
	}
	prec := req.GetPrecision()
	if prec < 0 || prec > 8 {
		return nil, errors.BadRequest("settings.CURRENCY_BAD_PRECISION", "小數位 0-8")
	}
	row, err := data.Client(ctx, s.data).Currency.Create().
		SetCode(req.GetCode()).
		SetSymbol(req.GetSymbol()).
		SetPosition(currency.Position(pos)).
		SetPrecision(prec).
		SetRate(rate).
		Save(ctx)
	if err != nil {
		return nil, errors.InternalServer("settings.CURRENCY_CREATE_FAILED", "創建失敗（code 可能重複）")
	}
	return toPBCurrency(row), nil
}

// UpdateCurrency 修改。
func (s *AdminCurrencyService) updateCurrency(ctx context.Context, req *adminv1.UpdateCurrencyRequest) (*adminv1.Currency, error) {
	var rate float64
	if req.GetRateJson() != "" {
		var err error
		rate, err = parseRate(req.GetRateJson())
		if err != nil {
			return nil, err
		}
		if err := ensureBaseRateOne(ctx, s.data, req.GetCode(), rate); err != nil {
			return nil, err
		}
	}
	if req.Enabled != nil && !req.GetEnabled() {
		referenced, err := currencyIsReferenced(ctx, s.data, req.GetCode())
		if err != nil {
			return nil, err
		}
		if referenced {
			return nil, errors.Conflict("settings.CURRENCY_IN_USE", "基礎貨幣或默認顯示貨幣不能停用，請先修改相應設置")
		}
	}
	q := data.Client(ctx, s.data).Currency.Update().Where(currency.Code(req.GetCode()))
	if req.GetSymbol() != "" {
		q.SetSymbol(req.GetSymbol())
	}
	if req.GetPosition() != "" {
		q.SetPosition(currency.Position(req.GetPosition()))
	}
	if req.Precision != nil {
		if req.GetPrecision() < 0 || req.GetPrecision() > 8 {
			return nil, errors.BadRequest("settings.CURRENCY_BAD_PRECISION", "小數位必須為 0-8 的整數")
		}
		q.SetPrecision(req.GetPrecision())
	}
	if req.GetRateJson() != "" {
		q.SetRate(rate)
	}
	if req.Enabled != nil {
		q.SetEnabled(req.GetEnabled())
	}
	if req.Sort != nil && req.GetSort() >= 0 {
		q.SetSort(req.GetSort())
	}
	rows, err := q.Save(ctx)
	if err != nil || rows == 0 {
		return nil, errors.NotFound("settings.CURRENCY_NOT_FOUND", "貨幣不存在")
	}
	row, _ := data.Client(ctx, s.data).Currency.Query().Where(currency.Code(req.GetCode())).Only(ctx)
	return toPBCurrency(row), nil
}

// DeleteCurrency 刪除。
func (s *AdminCurrencyService) deleteCurrency(ctx context.Context, req *adminv1.DeleteCurrencyRequest) (*emptypb.Empty, error) {
	referenced, err := currencyIsReferenced(ctx, s.data, req.GetCode())
	if err != nil {
		return nil, err
	}
	if referenced {
		return nil, errors.Conflict("settings.CURRENCY_IN_USE", "基礎貨幣或默認顯示貨幣不能刪除，請先修改相應設置")
	}
	n, err := data.Client(ctx, s.data).Currency.Delete().Where(currency.Code(req.GetCode())).Exec(ctx)
	if err != nil {
		return nil, errors.InternalServer("settings.CURRENCY_DELETE_FAILED", "刪除失敗")
	}
	if n == 0 {
		return nil, errors.NotFound("settings.CURRENCY_NOT_FOUND", "貨幣不存在")
	}
	return &emptypb.Empty{}, nil
}

func parseRate(s string) (float64, error) {
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() || !d.Round(8).IsPositive() || d.GreaterThan(decimal.RequireFromString("999999999999.99999999")) {
		return 0, errors.BadRequest("settings.CURRENCY_BAD_RATE", "匯率須為大於零的 decimal 字符串，最多 8 位小數且不超出支持範圍")
	}
	d = d.Round(8)
	f, _ := d.Float64()
	return f, nil
}

func toPBCurrency(r *ent.Currency) *adminv1.Currency {
	return &adminv1.Currency{
		Code: r.Code, Symbol: r.Symbol, Position: string(r.Position),
		Precision: r.Precision, RateJson: strconv.FormatFloat(r.Rate, 'f', -1, 64),
		Enabled: r.Enabled, Sort: r.Sort,
	}
}

// Currency writes and base-currency switching share the same database lock.
func (s *AdminCurrencyService) CreateCurrency(ctx context.Context, req *adminv1.CreateCurrencyRequest) (out *adminv1.Currency, err error) {
	err = data.Tx(ctx, s.data, func(ctx context.Context) error {
		if _, e := data.LockCurrencyConfiguration(ctx, s.data); e != nil {
			return e
		}
		var e error
		out, e = s.createCurrency(ctx, req)
		return e
	})
	return
}

func (s *AdminCurrencyService) UpdateCurrency(ctx context.Context, req *adminv1.UpdateCurrencyRequest) (out *adminv1.Currency, err error) {
	err = data.Tx(ctx, s.data, func(ctx context.Context) error {
		if _, e := data.LockCurrencyConfiguration(ctx, s.data); e != nil {
			return e
		}
		var e error
		out, e = s.updateCurrency(ctx, req)
		return e
	})
	return
}

func (s *AdminCurrencyService) DeleteCurrency(ctx context.Context, req *adminv1.DeleteCurrencyRequest) (out *emptypb.Empty, err error) {
	err = data.Tx(ctx, s.data, func(ctx context.Context) error {
		if _, e := data.LockCurrencyConfiguration(ctx, s.data); e != nil {
			return e
		}
		var e error
		out, e = s.deleteCurrency(ctx, req)
		return e
	})
	return
}
