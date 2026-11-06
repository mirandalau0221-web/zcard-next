package order

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/product"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productcontrol"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/productsku"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type serviceItem struct {
	mode, name, skuName string
	sourceID            uint64
	answers             []map[string]string
}

func itemKey(p, s uint64) [2]uint64 { return [2]uint64{p, s} }

func (uc *OrderUsecase) prepareServices(ctx context.Context, in CreateOrderInput, revisions map[uint64]int64) (map[[2]uint64]serviceItem, error) {
	c := data.Client(ctx, uc.Data)
	out := map[[2]uint64]serviceItem{}
	demand := map[uint64]int64{}
	allowed := map[string]bool{}
	for _, it := range in.Items {
		if it.Quantity <= 0 || it.Quantity > 100000 {
			return nil, fmt.Errorf("order.FORM_INVALID: 購買數量無效")
		}
		key := itemKey(it.ProductID, it.SkuID)
		if _, ok := out[key]; ok {
			return nil, fmt.Errorf("order.FORM_INVALID: 同一商品規格請合併數量；不同賬號請分別下單")
		}
		p, err := c.Product.Query().Where(product.ID(it.ProductID), product.SubsiteID(in.SubsiteID)).Only(ctx)
		if err != nil {
			return nil, err
		}
		if err := c.Product.UpdateOneID(p.ID).AddLockVersion(0).Exec(ctx); err != nil {
			return nil, err
		}
		pq := c.Product.Query().Where(product.ID(p.ID))
		if uc.Data.Dialect.Capabilities().SupportsSkipLocked {
			pq = pq.ForUpdate()
		}
		p, err = pq.Only(ctx)
		if err != nil {
			return nil, err
		}
		if p.LockVersion != revisions[p.ID] {
			return nil, fmt.Errorf("商品發貨設置已變化，請刷新後重新下單")
		}
		var sku *ent.ProductSku
		if it.SkuID > 0 {
			sku, err = c.ProductSku.Query().Where(productsku.ID(it.SkuID), productsku.ProductID(p.ID)).Only(ctx)
			if err != nil {
				return nil, fmt.Errorf("order.SKU_INVALID")
			}
		}
		v := serviceItem{mode: data.FulfillmentMode(p, sku), name: p.Name}
		if v.mode == "shipping" {
			if p.UpstreamSourceID != 0 {
				return nil, fmt.Errorf("實體商品不支持上游代發")
			}
			if sku == nil {
				n, e := c.ProductSku.Query().Where(productsku.ProductID(p.ID)).Count(ctx)
				if e != nil {
					return nil, e
				}
				if n > 0 {
					return nil, fmt.Errorf("order.SKU_INVALID: 請選擇實體規格")
				}
			}
		}
		if v.mode == "reuse" {
			if it.Quantity != 1 {
				return nil, fmt.Errorf("重複發貨規格每單限購一份")
			}
			v.sourceID, err = data.AdmitDeliverySource(ctx, uc.Data, p, it.SkuID)
			if err != nil {
				return nil, err
			}
		}
		if sku != nil {
			v.skuName = sku.Name
		}
		controls, err := c.ProductControl.Query().Where(productcontrol.ProductID(p.ID)).Order(ent.Asc(productcontrol.FieldSort), ent.Asc(productcontrol.FieldID)).All(ctx)
		if err != nil {
			return nil, err
		}
		if (v.mode == "upstream" || v.mode == "reuse") && len(controls) > 0 {
			return nil, fmt.Errorf("order.FORM_INVALID: 上游暫不支持傳遞填寫資料，請聯繫客服")
		}
		answers := it.ControlAnswers
		if answers == nil {
			answers = in.ControlAnswers
		}
		own := map[string]bool{}
		for _, f := range controls {
			k := strconv.FormatUint(f.ID, 10)
			allowed[k] = true
			own[k] = true
			val := strings.TrimSpace(answers[k])
			if err := validateAnswer(f, val); err != nil {
				return nil, fmt.Errorf("order.FORM_INVALID: %s：%s", f.Name, err)
			}
			if val != "" {
				v.answers = append(v.answers, map[string]string{"id": k, "name": f.Name, "value": val, "type": string(f.Type)})
			}
		}
		if it.ControlAnswers != nil {
			for k := range answers {
				if !own[k] {
					return nil, fmt.Errorf("order.FORM_INVALID: 存在不屬於該商品的填寫字段")
				}
			}
		}
		if v.mode == "manual" {
			// An UPDATE acquires a write lock on every dialect, serializing shared quota admissions.
			if _, err = c.Product.Update().Where(product.ID(p.ID), product.SubsiteID(in.SubsiteID)).AddManualStock(0).Save(ctx); err != nil {
				return nil, err
			}
			pq := c.Product.Query().Where(product.ID(p.ID))
			if uc.Data.Dialect.Capabilities().SupportsSkipLocked {
				pq = pq.ForUpdate()
			}
			p, err = pq.Only(ctx)
			if err != nil {
				return nil, err
			}
			available, err := data.ManualAvailable(ctx, c, p, uc.Data.Dialect.Capabilities().SupportsSkipLocked)
			if err != nil {
				return nil, err
			}
			demand[p.ID] += int64(it.Quantity)
			if available >= 0 && demand[p.ID] > available {
				return nil, fmt.Errorf("order.INSUFFICIENT_STOCK")
			}
		}
		out[key] = v
	}
	for k := range in.ControlAnswers {
		if !allowed[k] {
			return nil, fmt.Errorf("order.FORM_INVALID: 存在無效的填寫字段")
		}
	}
	return out, nil
}

func validateAnswer(f *ent.ProductControl, v string) error {
	if v == "" {
		if f.Required {
			return fmt.Errorf("請填寫此項")
		}
		return nil
	}
	limit := int(f.MaxLength)
	if limit <= 0 {
		limit = 500
	}
	if utf8.RuneCountInString(v) > limit {
		return fmt.Errorf("最多填寫 %d 個字符", limit)
	}
	switch string(f.Type) {
	case "number":
		n, e := strconv.ParseFloat(v, 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("請輸入有效數字")
		}
	case "select", "radio", "checkbox":
		vals := []string{v}
		if string(f.Type) == "checkbox" {
			vals = strings.Split(v, ",")
		}
		seen := map[string]bool{}
		for _, x := range vals {
			ok := false
			for _, o := range f.Options {
				if x == o {
					ok = true
				}
			}
			if !ok || seen[x] {
				return fmt.Errorf("請選擇有效選項")
			}
			seen[x] = true
		}
	}
	switch f.Validation {
	case "url":
		u, e := url.Parse(v)
		if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("請輸入完整的 http/https 鏈接")
		}
	case "username":
		if !regexp.MustCompile(`^@?[A-Za-z0-9_\.\-]{1,64}$`).MatchString(v) {
			return fmt.Errorf("請輸入有效用戶名")
		}
	case "tron":
		if !validTronAddress(v) {
			return fmt.Errorf("請輸入有效的 TRON 地址")
		}
	}
	return nil
}

// Base58Check prevents accepting an address with a mistyped checksum.
func validTronAddress(v string) bool {
	if len(v) != 34 || v[0] != 'T' {
		return false
	}
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	n := new(big.Int)
	for _, ch := range v {
		i := strings.IndexRune(alphabet, ch)
		if i < 0 {
			return false
		}
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(int64(i)))
	}
	b := n.Bytes()
	if len(b) != 25 || b[0] != 0x41 {
		return false
	}
	h := sha256.Sum256(b[:21])
	h = sha256.Sum256(h[:])
	return bytes.Equal(b[21:], h[:4])
}
