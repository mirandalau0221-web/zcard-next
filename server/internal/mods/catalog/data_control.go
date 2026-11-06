package catalog

// 自定義控件 admin CRUD 數據層（；ent import 收口：data 前綴文件）。

import (
	"context"
	"fmt"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productcontrol"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productsku"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"
)

// ListProductControls 商品控件列表（按 sort 升序）。
func (r *ProductRepoImpl) ListProductControls(ctx context.Context, productID uint64) ([]*ent.ProductControl, error) {
	return data.Client(ctx, r.data).ProductControl.Query().
		Where(productcontrol.ProductID(productID)).
		Order(ent.Asc(productcontrol.FieldSort)).
		All(ctx)
}

// CreateProductControl 創建控件。
func (r *ProductRepoImpl) createProductControl(ctx context.Context, productID, subsiteID uint64, name, typ string, required bool, options []string, sort int32, settings ...ControlSettings) (*ent.ProductControl, error) {
	c := data.Client(ctx, r.data)
	p, e := c.Product.Get(ctx, productID)
	if e != nil {
		return nil, e
	}
	if p.ProductKind == "sms_channel" || p.DeliveryKind == "social_service" {
		return nil, fmt.Errorf("渠道接碼不能添加普通下單控件")
	}
	reuse, e := c.ProductSku.Query().Where(productsku.ProductID(productID), productsku.FulfillmentMode("reuse")).Exist(ctx)
	if e != nil {
		return nil, e
	}
	if p.FulfillmentMode == "reuse" || reuse {
		return nil, fmt.Errorf("商品正在重複發貨，不能添加客戶專屬填寫字段；請先更改發貨來源")
	}

	if subsiteID == 0 {
		subsiteID = tenancy.FromContext(ctx).SubsiteID
	}
	v := ControlSettings{}
	if len(settings) > 0 {
		v = settings[0]
	}
	if err := v.validate(); err != nil {
		return nil, err
	}
	create := data.Client(ctx, r.data).ProductControl.Create().
		SetSubsiteID(subsiteID).
		SetProductID(productID).
		SetName(name).
		SetType(productcontrol.Type(typ)).
		SetRequired(required).
		SetOptions(options).
		SetSort(sort)
	create.SetPlaceholder(v.Placeholder)
	if v.Validation != "" {
		create.SetValidation(v.Validation)
	}
	if v.MaxLength != nil {
		create.SetMaxLength(*v.MaxLength)
	}
	return create.Save(ctx)
}

// UpdateProductControl 更新控件。
func (r *ProductRepoImpl) updateProductControl(ctx context.Context, id uint64, name, typ string, required bool, options []string, sort int32, settings ...ControlSettings) (*ent.ProductControl, error) {
	q := data.Client(ctx, r.data).ProductControl.UpdateOneID(id)
	if len(settings) > 0 {
		v := settings[0]
		if err := v.validate(); err != nil {
			return nil, err
		}
		if v.Validation != "" {
			q.SetValidation(v.Validation).SetPlaceholder(v.Placeholder)
		}
		if v.MaxLength != nil {
			q.SetMaxLength(*v.MaxLength)
		}
	}
	if name != "" {
		q.SetName(name)
	}
	if typ != "" {
		q.SetType(productcontrol.Type(typ))
	}
	q.SetRequired(required)
	if options != nil {
		q.SetOptions(options)
	}
	q.SetSort(sort)
	return q.Save(ctx)
}

// DeleteProductControl 刪除控件。
func (r *ProductRepoImpl) deleteProductControl(ctx context.Context, id uint64) error {
	return data.Client(ctx, r.data).ProductControl.DeleteOneID(id).Exec(ctx)
}

// ControlSettings is optional for old admin clients and internal callers.
type ControlSettings struct {
	Placeholder, Validation string
	MaxLength               *int32
}

func (v ControlSettings) validate() error {
	switch v.Validation {
	case "", "text", "url", "username", "tron":
	default:
		return fmt.Errorf("無效的校驗類型")
	}
	if v.MaxLength != nil && (*v.MaxLength < 1 || *v.MaxLength > 4000) {
		return fmt.Errorf("長度限制須為1至4000")
	}
	return nil
}

func (r *ProductRepoImpl) CreateProductControl(ctx context.Context, productID, subsiteID uint64, name, typ string, required bool, options []string, sort int32, settings ...ControlSettings) (out *ent.ProductControl, err error) {
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {

		if _, e := data.GuardProductWrite(ctx, r.data, productID); e != nil {
			return e
		}

		var inner error
		out, inner = r.createProductControl(ctx, productID, subsiteID, name, typ, required, options, sort, settings...)
		return inner
	})
	return
}

func (r *ProductRepoImpl) UpdateProductControl(ctx context.Context, id uint64, name, typ string, required bool, options []string, sort int32, settings ...ControlSettings) (out *ent.ProductControl, err error) {
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {
		row, e := data.Client(ctx, r.data).ProductControl.Get(ctx, id)
		if e != nil {
			return e
		}
		if _, e := data.GuardProductWrite(ctx, r.data, row.ProductID); e != nil {
			return e
		}

		var inner error
		out, inner = r.updateProductControl(ctx, id, name, typ, required, options, sort, settings...)
		return inner
	})
	return
}

func (r *ProductRepoImpl) DeleteProductControl(ctx context.Context, id uint64) error {
	return data.Tx(ctx, r.data, func(ctx context.Context) error {
		row, e := data.Client(ctx, r.data).ProductControl.Get(ctx, id)
		if e != nil {
			return e
		}
		if _, e := data.GuardProductWrite(ctx, r.data, row.ProductID); e != nil {
			return e
		}

		return r.deleteProductControl(ctx, id)
	})
}
