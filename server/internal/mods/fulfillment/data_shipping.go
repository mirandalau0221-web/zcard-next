package fulfillment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/orderitem"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/physicalreturnreceipt"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/physicalstockmovement"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/shipment"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/platform/shipping"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/emptypb"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func trackingValid(carrier, no string) bool {
	return strings.TrimSpace(carrier) != "" && strings.TrimSpace(no) != "" && utf8.RuneCountInString(carrier) <= 100 && utf8.RuneCountInString(no) <= 100 && !strings.ContainsAny(carrier+no, "\r\n\x00")
}
func (s *AdminFulfillmentService) shippingOrder(ctx context.Context, no string) (*ent.Order, uint64, error) {
	cl := identity.ClaimsFromContext(ctx)
	if cl == nil {
		return nil, 0, fmt.Errorf("請登錄")
	}
	o, e := data.Client(ctx, s.data).Order.Query().Where(order.OrderNo(no), order.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Only(ctx)
	return o, cl.Subject, e
}
func (s *AdminFulfillmentService) ShipOrder(ctx context.Context, req *adminv1.ShipOrderRequest) (*emptypb.Empty, error) {
	if !trackingValid(req.Carrier, req.TrackingNo) || len(req.ItemIds) == 0 || len(req.ItemIds) > 100 || len(req.RequestKey) < 8 || len(req.RequestKey) > 100 {
		return nil, kerrors.BadRequest("shipping.INVALID", "請選擇商品並填寫快遞公司、單號")
	}
	e := data.Tx(ctx, s.data, func(ctx context.Context) error {
		o, aid, e := s.shippingOrder(ctx, req.OrderNo)
		if e != nil {
			return e
		}
		c := data.Client(ctx, s.data)
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", o.ID, req.RequestKey))))
		old, e := c.Shipment.Query().Where(shipment.RequestKey(key)).Only(ctx)
		if e == nil {
			if old.Carrier != req.Carrier || old.TrackingNo != req.TrackingNo || len(old.Items) != len(req.ItemIds) {
				return fmt.Errorf("發貨請求內容已變化")
			}
			for _, id := range req.ItemIds {
				if old.Items[strconv.FormatUint(id, 10)] == 0 {
					return fmt.Errorf("發貨商品已變化")
				}
			}
			return nil
		}
		if !ent.IsNotFound(e) {
			return e
		}
		if e = data.LockPhysicalOrder(ctx, s.data, o); e != nil {
			return e
		}
		items := map[string]int32{}
		for _, id := range req.ItemIds {
			k := strconv.FormatUint(id, 10)
			if _, ok := items[k]; ok {
				return fmt.Errorf("商品不能重複選擇")
			}
			it, e := c.OrderItem.Query().Where(orderitem.ID(id), orderitem.OrderID(o.ID), orderitem.GoodsType("physical")).Only(ctx)
			if e != nil {
				return e
			}
			qty := it.Quantity - it.CanceledQuantity - it.ShippedQuantity
			if qty <= 0 {
				return fmt.Errorf("商品已發貨或已取消")
			}
			if e = c.OrderItem.UpdateOneID(it.ID).AddShippedQuantity(qty).SetFulfillmentStatus("shipped").Exec(ctx); e != nil {
				return e
			}
			items[k] = qty
		}
		pkg, e := c.Shipment.Create().SetOrderID(o.ID).SetSubsiteID(o.SubsiteID).SetCarrier(req.Carrier).SetTrackingNo(req.TrackingNo).SetItems(items).SetAddress(o.ShippingAddress).SetAdminID(aid).SetRequestKey(key).Save(ctx)
		if e != nil {
			return e
		}
		if e = data.PhysicalOrderEvent(ctx, s.data, o, "shipment_created", "admin", aid, fmt.Sprintf("寄出包裹 %d", pkg.ID)); e != nil {
			return e
		}
		if e = data.PhysicalNotification(ctx, s.data, o, "order.shipped", fmt.Sprintf("shipment:%d", pkg.ID), map[string]any{"carrier": pkg.Carrier, "tracking_no": pkg.TrackingNo, "shipment_id": pkg.ID}); e != nil {
			return e
		}
		return data.RefreshPhysicalProgress(ctx, s.data, o)
	})
	if e != nil {
		return nil, kerrors.BadRequest("shipping.SHIP", e.Error())
	}
	return &emptypb.Empty{}, nil
}
func (s *AdminFulfillmentService) UpdateShipping(ctx context.Context, req *adminv1.UpdateShippingRequest) (*emptypb.Empty, error) {
	e := data.Tx(ctx, s.data, func(ctx context.Context) error {
		o, aid, e := s.shippingOrder(ctx, req.OrderNo)
		if e != nil {
			return e
		}
		if strings.TrimSpace(req.Reason) == "" || len([]rune(req.Reason)) > 120 {
			return fmt.Errorf("請填寫120字以內的更正原因")
		}
		if req.Received {
			return data.ReceivePhysicalShipment(ctx, s.data, req.OrderNo, req.ShipmentId, "admin", aid)
		}
		// A refund does not invalidate an existing parcel. Only corrections may
		// operate on refunded orders; new shipments and address changes remain blocked.
		if e = data.LockPhysicalOrder(ctx, s.data, o, req.ShipmentId > 0); e != nil {
			return e
		}
		c := data.Client(ctx, s.data)
		if req.ShipmentId > 0 {
			if !trackingValid(req.Carrier, req.TrackingNo) {
				return fmt.Errorf("快遞信息無效")
			}
			p, e := c.Shipment.Query().Where(shipment.ID(req.ShipmentId), shipment.OrderID(o.ID), shipment.Status("shipped")).Only(ctx)
			if e != nil {
				return e
			}
			if e = c.Shipment.UpdateOneID(p.ID).SetCarrier(req.Carrier).SetTrackingNo(req.TrackingNo).Exec(ctx); e != nil {
				return e
			}
			return data.PhysicalOrderEvent(ctx, s.data, o, "shipment_corrected", "admin", aid, fmt.Sprintf("包裹%d 原單號:%s；%s", p.ID, p.TrackingNo, req.Reason))
		}
		sent, e := c.Shipment.Query().Where(shipment.OrderID(o.ID)).Exist(ctx)
		if e != nil {
			return e
		}
		if sent {
			return fmt.Errorf("已有包裹寄出，不能修改訂單地址")
		}
		a, e := shipping.Validate(req.Address)
		if e != nil {
			return e
		}
		if a["country"] != o.ShippingAddress["country"] {
			return fmt.Errorf("付款後不能更改配送國家")
		}
		if o.Extra == nil {
			o.Extra = map[string]any{}
		}
		var history []map[string]any
		b, _ := json.Marshal(o.Extra["shipping_address_history"])
		_ = json.Unmarshal(b, &history)
		if len(history) >= 100 {
			return fmt.Errorf("地址更正次數超過上限")
		}
		history = append(history, map[string]any{"before": o.ShippingAddress, "after": a, "admin_id": aid, "at": time.Now().Unix(), "reason": req.Reason})
		o.Extra["shipping_address_history"] = history
		if e = c.Order.UpdateOneID(o.ID).SetShippingAddress(a).SetExtra(o.Extra).Exec(ctx); e != nil {
			return e
		}
		return data.PhysicalOrderEvent(ctx, s.data, o, "shipping_address_changed", "admin", aid, req.Reason)
	})
	if e != nil {
		return nil, kerrors.BadRequest("shipping.UPDATE", e.Error())
	}
	return &emptypb.Empty{}, nil
}
func (s *AdminFulfillmentService) RestockReturn(ctx context.Context, req *adminv1.RestockReturnRequest) (*emptypb.Empty, error) {
	e := data.Tx(ctx, s.data, func(ctx context.Context) error {
		o, aid, e := s.shippingOrder(ctx, req.OrderNo)
		if e != nil {
			return e
		}
		if o.CommerceVersion != 1 || req.Quantity <= 0 || len(req.RequestKey) < 8 || len(req.RequestKey) > 100 || len([]rune(req.Reason)) > 120 || strings.TrimSpace(req.Reason) == "" {
			return fmt.Errorf("請填寫退貨數量和驗收原因")
		}
		c := data.Client(ctx, s.data)
		key := fmt.Sprintf("return:%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", o.ID, req.RequestKey))))
		requestBytes, _ := json.Marshal([]any{req.ItemId, req.Quantity, strings.TrimSpace(req.Reason)})
		requestHash := fmt.Sprintf("%x", sha256.Sum256(requestBytes))
		receipt, e := c.PhysicalReturnReceipt.Query().Where(physicalreturnreceipt.RequestKey(key), physicalreturnreceipt.OrderID(o.ID), physicalreturnreceipt.SubsiteID(o.SubsiteID)).Only(ctx)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		if receipt != nil {
			if receipt.RequestHash != requestHash {
				return fmt.Errorf("同一退貨請求不能改變商品、數量或原因")
			}
			return nil
		}
		// Older versions used the inventory movement itself as the receipt.
		// Preserve their retries while new returns always have their own record.
		oldMovement, e := c.PhysicalStockMovement.Query().Where(physicalstockmovement.Reference(key)).Only(ctx)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		if oldMovement != nil {
			it, e := c.OrderItem.Get(ctx, req.ItemId)
			if e != nil {
				return e
			}
			if it.OrderID != o.ID || it.ProductID != oldMovement.ProductID || it.SkuID != oldMovement.SkuID || int64(req.Quantity) != oldMovement.Delta {
				return fmt.Errorf("同一入庫請求不能改變商品或數量")
			}
			return nil
		}
		n, e := c.Order.Update().Where(order.ID(o.ID), order.Version(o.Version)).AddVersion(1).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return fmt.Errorf("訂單已變化")
		}
		it, e := c.OrderItem.Query().Where(orderitem.ID(req.ItemId), orderitem.OrderID(o.ID), orderitem.GoodsType("physical")).Only(ctx)
		if e != nil {
			return e
		}
		if req.Quantity > it.ShippedQuantity-it.ReturnedQuantity {
			return fmt.Errorf("退貨入庫不能超過已發貨且未入庫數量")
		}
		if e = c.PhysicalReturnReceipt.Create().SetSubsiteID(o.SubsiteID).SetOrderID(o.ID).SetItemID(it.ID).SetQuantity(req.Quantity).SetRequestKey(key).SetRequestHash(requestHash).SetReason(strings.TrimSpace(req.Reason)).SetInventoryTracked(it.InventoryTracked).Exec(ctx); e != nil {
			return e
		}
		if it.InventoryTracked {
			if e = data.MovePhysicalStock(ctx, s.data, o.SubsiteID, it.ProductID, it.SkuID, o.ID, int64(req.Quantity), key, "退貨驗收入庫"); e != nil {
				return e
			}
		}
		if e = c.OrderItem.UpdateOneID(it.ID).AddReturnedQuantity(req.Quantity).Exec(ctx); e != nil {
			return e
		}
		name := it.ProductName
		if name == "" {
			name = fmt.Sprintf("商品 #%d", it.ProductID)
		}
		if it.SkuName != "" {
			name += "（" + it.SkuName + "）"
		}
		event, action := "return_registered", "登記退貨"
		if it.InventoryTracked {
			event, action = "return_restocked", "入庫"
		}
		return data.PhysicalOrderEvent(ctx, s.data, o, event, "admin", aid, fmt.Sprintf("商品項 #%d · %s %d 件：%s · %s", it.ID, action, req.Quantity, req.Reason, name))
	})
	if e != nil {
		return nil, kerrors.BadRequest("shipping.RESTOCK", e.Error())
	}
	return &emptypb.Empty{}, nil
}
