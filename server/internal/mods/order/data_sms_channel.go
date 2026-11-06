package order

import (
	"context"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/smsretailquote"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplyconnection"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/supplymapping"
	"github.com/NovaWorks/zcard-next/server/internal/platform/db"
	"time"
)

func (uc *OrderUsecase) retailSMSQuote(ctx context.Context, p *ent.Product, in CreateOrderInput, consume bool, no string) (*ent.SMSRetailQuote, error) {
	if in.SMSQuoteID == "" {
		return nil, fmt.Errorf("order.FORM_INVALID: 請在接碼頁面選擇報價後購買")
	}
	c := data.Client(ctx, uc.Data)
	q, e := c.SMSRetailQuote.Query().Where(smsretailquote.ID(in.SMSQuoteID), smsretailquote.ProductID(p.ID), smsretailquote.UserID(in.UserID), smsretailquote.SubsiteID(in.SubsiteID)).Only(ctx)
	if e != nil {
		return nil, fmt.Errorf("order.FORM_INVALID: 接碼報價不存在或不屬於當前會員")
	}
	if q.ConsumedBy != "" || q.ExpiresAt <= time.Now().Unix() || q.ProductRevision != p.LockVersion || q.ConnectionID != p.UpstreamSourceID || q.UpstreamProductID != p.UpstreamProductCode || q.CostCents <= 0 {
		return nil, fmt.Errorf("order.FORM_INVALID: 接碼報價過期、已使用或金額變化，請重新選價")
	}
	var rev string
	if consume && uc.Data.Dialect != db.SQLite {
		conn, e := c.SupplyConnection.Query().Where(supplyconnection.ID(p.UpstreamSourceID)).ForUpdate().Only(ctx)
		if e != nil {
			return nil, e
		}
		m, e := c.SupplyMapping.Query().Where(supplymapping.ConnectionID(conn.ID), supplymapping.LocalProductID(p.ID), supplymapping.UpstreamProduct(p.UpstreamProductCode), supplymapping.UpstreamSku("")).ForUpdate().Only(ctx)
		if e != nil {
			return nil, e
		}
		rev = data.SMSRetailPricingRevision(conn, m)
	} else {
		rev, e = data.SMSRetailCurrentRevision(ctx, c, p)
	}
	if e != nil {
		return nil, e
	}
	if q.PricingRevision != rev {
		return nil, fmt.Errorf("order.FORM_INVALID: 商品加價或貨源賬號已變化，請重新選價")
	}
	if consume {
		n, e := c.SMSRetailQuote.Update().Where(smsretailquote.ID(q.ID), smsretailquote.ConsumedBy(""), smsretailquote.ExpiresAtGT(time.Now().Unix())).SetConsumedBy(no).Save(ctx)
		if e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, fmt.Errorf("order.FORM_INVALID: 報價已使用")
		}
	}
	return q, nil
}
