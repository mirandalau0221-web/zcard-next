package order

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/smsintent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/smsretailquote"
	supplyport "github.com/NovaWorks/zcard-next/server/internal/mods/supply/port"
	"github.com/google/uuid"
	"time"
)

func (uc *OrderUsecase) freezeSMS(ctx context.Context, it *ent.OrderItem, q supplyport.SMSQuote) error {
	return uc.freezeSMSSelection(ctx, it, q, CreateOrderInput{}, "")
}
func (uc *OrderUsecase) freezeSMSSelection(ctx context.Context, it *ent.OrderItem, q supplyport.SMSQuote, in CreateOrderInput, orderNo string) error {
	c := data.Client(ctx, uc.Data)
	p, err := c.Product.Get(ctx, it.ProductID)
	if err != nil {
		return err
	}
	if err := c.SupplyConnection.UpdateOneID(q.ConnectionID).AddRetryMax(0).Exec(ctx); err != nil {
		return err
	}
	conn, err := c.SupplyConnection.Get(ctx, q.ConnectionID)
	if err != nil {
		return err
	}
	if p.UpstreamProductCode != q.ProductID || p.DeliveryKind != supplyport.SMSDelivery || p.UpstreamSourceID != q.ConnectionID || data.SMSConnectionIdentity(conn) != q.Identity {
		return fmt.Errorf("order.FORM_INVALID: 接碼商品或貨源已變化")
	}
	request := supplyport.SMSPurchase{ProductID: p.UpstreamProductCode, Quantity: 1, DownstreamOrderNo: "sms_" + uuid.NewString(), RequiredCapability: supplyport.SMSCapability, MaxSupplyAmountCents: supplyport.Integer(q.Amount), Currency: "CNY"}
	metadata := p.SmsProduct
	if p.ProductKind == "sms_channel" {
		retail, e := uc.retailSMSQuote(ctx, p, in, true, orderNo)
		if e != nil {
			return e
		}
		request.RequiredCapability = supplyport.SMSProductPurchase
		request.SMSQuoteID = retail.UpstreamQuoteID
		metadata = retail.Selection
	}
	raw, _ := json.Marshal(request)
	snap, _ := json.Marshal(data.FrozenSMS{Description: p.Description, ConnectionID: q.ConnectionID, Identity: q.Identity, Request: string(raw), Hash: fmt.Sprintf("%x", sha256.Sum256(raw)), RequestNo: request.DownstreamOrderNo})
	return c.OrderItem.UpdateOneID(it.ID).SetDeliveryKind(supplyport.SMSDelivery).SetSmsProduct(metadata).SetSmsPurchaseSnapshot(string(snap)).SetCost(q.Amount).Exec(ctx)
}
func (uc *OrderUsecase) createSMSIntents(ctx context.Context, o *ent.Order) error {
	c := data.Client(ctx, uc.Data)
	items, err := c.OrderItem.Query().Where(orderitem.OrderID(o.ID), orderitem.DeliveryKind(supplyport.SMSDelivery)).All(ctx)
	if err != nil {
		return err
	}
	for _, it := range items {
		if o.UserID == 0 || o.TotalAmount <= 0 || o.BaseCurrency != "CNY" || it.Quantity != 1 {
			return fmt.Errorf("order.SMS_INVALID")
		}
		exists, err := c.SMSIntent.Query().Where(smsintent.OrderItemID(it.ID)).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		var f data.FrozenSMS
		if json.Unmarshal([]byte(it.SmsPurchaseSnapshot), &f) != nil || f.Hash != fmt.Sprintf("%x", sha256.Sum256([]byte(f.Request))) {
			return fmt.Errorf("order.SMS_INVALID_INTENT")
		}
		if err := c.SupplyConnection.UpdateOneID(f.ConnectionID).AddRetryMax(0).Exec(ctx); err != nil {
			return err
		}
		conn, e := c.SupplyConnection.Get(ctx, f.ConnectionID)
		if e != nil {
			return e
		}
		if data.SMSConnectionIdentity(conn) != f.Identity {
			return fmt.Errorf("order.SMS_CONNECTION_CHANGED")
		}
		_, err = c.SMSIntent.Create().SetSubsiteID(o.SubsiteID).SetOrderID(o.ID).SetOrderItemID(it.ID).SetUserID(o.UserID).SetConnectionID(f.ConnectionID).SetConnectionIdentity(f.Identity).SetRequestNo(f.RequestNo).SetRequestJSON(f.Request).SetRequestHash(f.Hash).Save(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

func (uc *OrderUsecase) validateSMSPayment(ctx context.Context, orderID uint64) error {
	c := data.Client(ctx, uc.Data)
	items, err := c.OrderItem.Query().Where(orderitem.OrderID(orderID), orderitem.DeliveryKind(supplyport.SMSDelivery)).All(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	if !data.SMSSalesEnabled() {
		return fmt.Errorf("接碼新購已關閉")
	}
	g, ok := uc.StockGate.(supplyport.SMSGateway)
	if !ok {
		return fmt.Errorf("接碼服務未就緒")
	}
	for _, it := range items {
		var f data.FrozenSMS
		var req supplyport.SMSPurchase
		if json.Unmarshal([]byte(it.SmsPurchaseSnapshot), &f) != nil || json.Unmarshal([]byte(f.Request), &req) != nil {
			return fmt.Errorf("接碼購買意圖異常")
		}
		if req.RequiredCapability == supplyport.SMSProductPurchase {
			o, e := c.Order.Get(ctx, it.OrderID)
			if e != nil {
				return e
			}
			quote, e := c.SMSRetailQuote.Query().Where(smsretailquote.ConsumedBy(o.OrderNo), smsretailquote.ProductID(it.ProductID), smsretailquote.UserID(o.UserID), smsretailquote.SubsiteID(o.SubsiteID)).Only(ctx)
			if e != nil || quote.ExpiresAt <= time.Now().Unix() {
				return fmt.Errorf("接碼報價已過期，請取消未付款訂單後重新選價")
			}
			continue
		}
		q, e := g.PrepareSMS(ctx, f.ConnectionID, req.ProductID)
		if e != nil {
			return e
		}
		if q.Identity != f.Identity || q.Amount > int64(req.MaxSupplyAmountCents) {
			return fmt.Errorf("接碼報價或賬號已變化，請重新下單")
		}
	}
	return nil
}
