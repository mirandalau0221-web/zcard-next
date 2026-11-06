package payment

// 支付服務（；admin 渠道/支付單/退款 + storefront 創建支付 + 回調入口）。

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/NovaWorks/zcard-next/server/api/admin/v1"
	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"
	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/order"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/payment"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/paymentchannel"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/refundorder"
	"github.com/NovaWorks/zcard-next/server/internal/mods/identity"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment/adapter"
	"github.com/NovaWorks/zcard-next/server/internal/mods/payment/port"
	walletport "github.com/NovaWorks/zcard-next/server/internal/mods/wallet/port"
	"github.com/NovaWorks/zcard-next/server/internal/platform/money"
	"github.com/NovaWorks/zcard-next/server/internal/platform/tenancy"

	"github.com/go-kratos/kratos/v3/errors"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ── Admin ──

// AdminPaymentService 管理面支付服務。
type AdminPaymentService struct {
	adminv1.UnimplementedAdminPaymentServiceServer
	repo *PaymentRepoImpl
	data *data.Data
}

// NewAdminPaymentService 構造。
func NewAdminPaymentService(repo *PaymentRepoImpl, d *data.Data) *AdminPaymentService {
	return &AdminPaymentService{repo: repo, data: d}
}

// ListChannels 渠道列表（憑據脱敏）。
func (s *AdminPaymentService) ListChannels(ctx context.Context, _ *emptypb.Empty) (*adminv1.ChannelList, error) {
	rows, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, errors.InternalServer("payment.LIST_FAILED", "讀取渠道失敗")
	}
	reply := &adminv1.ChannelList{}
	for _, ch := range rows {
		reply.Channels = append(reply.Channels, s.channelPB(ctx, ch))
	}
	return reply, nil
}

func (s *AdminPaymentService) CreateChannel(ctx context.Context, req *adminv1.CreateChannelRequest) (*adminv1.Channel, error) {
	var out *adminv1.Channel
	err := data.Tx(ctx, s.data, func(tx context.Context) error { var err error; out, err = s.createChannel(tx, req); return err })
	return out, err
}

// CreateChannel 創建渠道（：驅動存在性 + 憑據即時校驗——創建即反饋）。
func (s *AdminPaymentService) createChannel(ctx context.Context, req *adminv1.CreateChannelRequest) (*adminv1.Channel, error) {
	if req.GetName() == "" || req.GetCode() == "" || req.GetDriver() == "" {
		return nil, errors.BadRequest("payment.INVALID_INPUT", "名稱/編碼/驅動必填")
	}
	if req.GetDriver() != "wallet" { // wallet 為內置驅動（service 層直接 markPaid）
		provider, err := s.repo.reg.Provider(req.GetDriver())
		if err != nil {
			return nil, errors.BadRequest("payment.DRIVER_UNSUPPORTED", "渠道驅動未實現: "+req.GetDriver())
		}
		// 創建允許空憑據（待配置狀態——勾選式添加後引導補憑據）；
		// 僅提交了實際配置時校驗，填寫支付信息保存那一步即時反饋
		if strings.TrimSpace(req.GetConfigJson()) != "" && req.GetConfigJson() != "{}" {
			if err := provider.ValidateConfig(json.RawMessage(req.GetConfigJson())); err != nil {
				return nil, errors.BadRequest("payment.CHANNEL_CONFIG_INVALID", err.Error())
			}
		}
	}
	feeType := req.GetFeeType()
	if feeType == "" {
		feeType = string(paymentchannel.FeeTypeFixed) // ent enum 拒絕空值——默認固定費率
	}
	methods, err := methodsJSON(req.GetMethodsJson())
	if err != nil {
		return nil, errors.BadRequest("payment.METHODS_INVALID", "支付方式列表格式錯誤")
	}
	ch, err := s.repo.CreateChannel(ctx, req.GetName(), req.GetCode(), req.GetDriver(),
		req.GetConfigJson(), req.GetFee(), feeType, req.GetEnabled(), req.GetSort(),
		req.GetIcon(), methods, ChannelUsage{req.AllowPurchase, req.AllowMemberRecharge, req.AllowSupplyRecharge})
	if err != nil {
		return nil, errors.InternalServer("payment.CREATE_FAILED", "創建失敗（code 可能重複）")
	}
	ch, err = s.saveCheckout(ctx, ch, req.GetFeeBearer(), req.GetRecommended(), req.GetRecommendLabel(), req.GetRecommendDescription())
	if err != nil {
		return nil, err
	}
	return s.channelPB(ctx, ch), nil
}

// ListDrivers 驅動元數據（：admin 配置面動態表單渲染的數據源）。
func (s *AdminPaymentService) ListDrivers(ctx context.Context, _ *emptypb.Empty) (*adminv1.DriverList, error) {
	drivers := []*adminv1.Driver{{
		Code: "wallet", Name: "餘額支付", Icon: "wallet",
		Description: "站內餘額支付（無需外部憑據）",
	}}
	for _, p := range s.repo.reg.All() {
		d := &adminv1.Driver{Code: p.Type()}
		if m, ok := p.(port.MetaProvider); ok {
			meta := m.Meta()
			d.Name, d.Icon, d.Description = meta.Name, meta.Icon, meta.Description
		} else {
			d.Name = p.Type() // 未聲明元數據的驅動回落驅動碼
		}
		if f, ok := p.(port.FieldProvider); ok {
			for _, cf := range f.ConfigFields() {
				fd := &adminv1.ConfigField{
					Key: cf.Key, Label: cf.Label, Type: cf.Type, Required: cf.Required,
					Placeholder: cf.Placeholder, Help: cf.Help, Sensitive: cf.Sensitive,
					Dynamic: cf.Dynamic, Multiple: cf.Multiple, Default: cf.Default,
				}
				for _, o := range cf.Options {
					fd.Options = append(fd.Options, &adminv1.Option{Label: o.Label, Value: o.Value})
				}
				d.Fields = append(d.Fields, fd)
			}
		} else {
			// 未聲明 schema 的驅動（自定義）——默認 JSON 文本框
			d.Fields = append(d.Fields, &adminv1.ConfigField{
				Key: "config", Label: "憑據 JSON", Type: "textarea", Required: true,
				Help: "該驅動未聲明字段模板，直接填寫 JSON",
			})
		}
		drivers = append(drivers, d)
	}
	return &adminv1.DriverList{Drivers: drivers}, nil
}

// FieldOptions 驅動字段動態選項（ 修復：epusdt network/token 以網關
// supported_assets 為準——轉發適配器 OptionProvider，失敗回落靜態矩陣）。
func (s *AdminPaymentService) FieldOptions(ctx context.Context, req *adminv1.FieldOptionsRequest) (*adminv1.FieldOptionsReply, error) {
	if req.GetCode() == "" || req.GetField() == "" {
		return nil, errors.BadRequest("payment.INVALID_INPUT", "code/field 必填")
	}
	provider, err := s.repo.reg.Provider(req.GetCode())
	if err != nil {
		return nil, errors.BadRequest("payment.DRIVER_UNSUPPORTED", "渠道驅動未實現: "+req.GetCode())
	}
	op, ok := provider.(port.OptionProvider)
	if !ok {
		return nil, errors.BadRequest("payment.OPTIONS_UNSUPPORTED", "驅動不支持動態選項")
	}
	res, err := op.FieldOptions(ctx, req.GetField(), json.RawMessage(req.GetConfigJson()))
	if err != nil {
		return nil, errors.InternalServer("payment.OPTIONS_FAILED", "加載選項失敗: "+err.Error())
	}
	reply := &adminv1.FieldOptionsReply{Fallback: res.Fallback}
	for _, o := range res.Options {
		reply.Options = append(reply.Options, &adminv1.Option{Label: o.Label, Value: o.Value})
	}
	return reply, nil
}

// channelPB 渠道協議對象（）：
// 補充已配置字段名 + 回調地址；憑據脱敏回顯——敏感字段掩碼 ****
// （編輯體驗：非敏感字段可回顯，敏感字段留空不覆蓋）。
func (s *AdminPaymentService) channelPB(ctx context.Context, ch *ent.PaymentChannel) *adminv1.Channel {
	pb := ToChannelPB(ch)
	pb.ConfiguredFields = s.repo.ConfiguredFields(ch)
	pb.CallbackUrl = s.repo.callbackURLFor(ctx, ch)
	if ch.Driver != "wallet" {
		if p, err := s.repo.reg.Provider(ch.Driver); err == nil {
			if fp, ok := p.(port.FieldProvider); ok {
				sensitive := map[string]bool{}
				for _, f := range fp.ConfigFields() {
					if f.Sensitive {
						sensitive[f.Key] = true
					}
				}
				var m map[string]json.RawMessage
				if json.Unmarshal(s.repo.DecryptConfig(ch), &m) == nil {
					for k := range m {
						if sensitive[k] {
							m[k] = json.RawMessage(`"****"`)
						}
					}
					if b, err := json.Marshal(m); err == nil {
						pb.ConfigJson = string(b)
					}
				}
			}
		}
	}
	return pb
}

// UpdateChannel 更新渠道（：fee_type 更新 + 憑據變更即時校驗；
// config_json=**** 跳過憑據修改——敏感字段留空不覆蓋）。
func (s *AdminPaymentService) updateChannel(ctx context.Context, req *adminv1.UpdateChannelRequest) (*adminv1.Channel, error) {
	if ft := req.GetFeeType(); ft != "" && ft != string(paymentchannel.FeeTypePercent) && ft != string(paymentchannel.FeeTypeFixed) {
		return nil, errors.BadRequest("payment.INVALID_INPUT", "fee_type 須為 percent/fixed")
	}
	configJSON := req.GetConfigJson()
	if req.GetConfigJson() != "" && req.GetConfigJson() != `"****"` {
		old, err := data.Client(ctx, s.data).PaymentChannel.Get(ctx, req.GetId())
		if ent.IsNotFound(err) {
			return nil, errors.NotFound("payment.CHANNEL_NOT_FOUND", "渠道不存在")
		}
		if err != nil {
			return nil, errors.InternalServer("payment.UPDATE_FAILED", "讀取渠道失敗")
		}
		if old.Driver != "wallet" {
			provider, err := s.repo.reg.Provider(old.Driver)
			if err != nil {
				return nil, errors.BadRequest("payment.DRIVER_UNSUPPORTED", "渠道驅動未實現: "+old.Driver)
			}
			// 編輯表單只提交變更字段；先合併舊憑據，再校驗完整配置。
			merged := map[string]json.RawMessage{}
			_ = json.Unmarshal(s.repo.DecryptConfig(old), &merged)
			if merged == nil {
				merged = map[string]json.RawMessage{}
			}
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(configJSON), &patch); err != nil || patch == nil {
				return nil, errors.BadRequest("payment.CHANNEL_CONFIG_INVALID", "憑據必須為 JSON 對象")
			}
			sensitive := map[string]bool{}
			if fp, ok := provider.(port.FieldProvider); ok {
				for _, f := range fp.ConfigFields() {
					sensitive[f.Key] = f.Sensitive
				}
			}
			for key, value := range patch {
				var text string
				if sensitive[key] && json.Unmarshal(value, &text) == nil && (text == "****" || strings.TrimSpace(text) == "") {
					continue
				}
				merged[key] = value
			}
			encoded, err := json.Marshal(merged)
			if err != nil {
				return nil, errors.BadRequest("payment.CHANNEL_CONFIG_INVALID", "憑據格式錯誤")
			}
			configJSON = string(encoded)
			if err := provider.ValidateConfig(encoded); err != nil {
				return nil, errors.BadRequest("payment.CHANNEL_CONFIG_INVALID", err.Error())
			}
		}
	}
	var methods []map[string]any
	if req.MethodsJson != nil { // optional：缺省=不修改
		var mErr error
		methods, mErr = methodsJSON(req.GetMethodsJson())
		if mErr != nil {
			return nil, errors.BadRequest("payment.METHODS_INVALID", "支付方式列表格式錯誤")
		}
	}
	ch, err := s.repo.UpdateChannel(ctx, req.GetId(), req.GetName(), configJSON,
		req.GetFee(), req.GetFeeType(), req.GetEnabled(), req.GetSort(),
		req.Icon != nil, req.GetIcon(), req.MethodsJson != nil, methods, ChannelUsage{req.AllowPurchase, req.AllowMemberRecharge, req.AllowSupplyRecharge})
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("payment.CHANNEL_NOT_FOUND", "渠道不存在")
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "payment.CHANNEL_BUSY") || strings.HasPrefix(err.Error(), "payment.CHANNEL_DELETED") || strings.HasPrefix(err.Error(), "payment.METHODS_INVALID") {
			return nil, errors.BadRequest("payment.UPDATE_FAILED", err.Error())
		}
		return nil, errors.InternalServer("payment.UPDATE_FAILED", "更新失敗")
	}
	ch, err = s.saveCheckout(ctx, ch, req.GetFeeBearer(), req.GetRecommended(), req.GetRecommendLabel(), req.GetRecommendDescription())
	if err != nil {
		return nil, err
	}
	return s.channelPB(ctx, ch), nil
}

// DeleteChannel 刪除渠道。
func (s *AdminPaymentService) DeleteChannel(ctx context.Context, req *adminv1.DeleteChannelRequest) (*emptypb.Empty, error) {
	if err := s.repo.DeleteChannel(ctx, req.GetId()); err != nil {
		if strings.HasPrefix(err.Error(), "payment.CHANNEL_ENABLED") {
			return nil, errors.BadRequest("payment.CHANNEL_ENABLED", err.Error())
		}
		return nil, errors.NotFound("payment.CHANNEL_NOT_FOUND", "渠道不存在")
	}
	return &emptypb.Empty{}, nil
}

// ListPayments 支付單列表。
func (s *AdminPaymentService) ListPayments(ctx context.Context, req *adminv1.ListPaymentsRequest) (*adminv1.ListPaymentsReply, error) {
	limit := req.GetLimit()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.repo.ListPayments(ctx, req.GetStatus(), req.GetOrderNo(), req.GetCursor(), limit, req.GetReviewOnly())
	if err != nil {
		return nil, errors.InternalServer("payment.LIST_FAILED", "讀取支付單失敗")
	}
	reply := &adminv1.ListPaymentsReply{}
	for _, p := range rows {
		orderNo := s.getOrderNo(ctx, p.OrderID)
		reply.Payments = append(reply.Payments, ToPaymentPB(p, orderNo))
	}
	if len(rows) == int(limit) {
		reply.NextCursor = rows[len(rows)-1].ID
	}
	return reply, nil
}

// GetPayment 支付單詳情。
func (s *AdminPaymentService) GetPayment(ctx context.Context, req *adminv1.GetPaymentRequest) (*adminv1.Payment, error) {
	p, err := s.repo.GetPayment(ctx, req.GetId())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("payment.NOT_FOUND", "支付單不存在")
	}
	if err != nil {
		return nil, errors.InternalServer("payment.GET_FAILED", "查詢失敗")
	}
	return ToPaymentPB(p, s.getOrderNo(ctx, p.OrderID)), nil
}

// CapturePayment 補單（M1a 框架——真實渠道拉取 接入）。
func (s *AdminPaymentService) CapturePayment(ctx context.Context, req *adminv1.CapturePaymentRequest) (*adminv1.Payment, error) {
	p, err := s.repo.GetPayment(ctx, req.GetId())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("payment.NOT_FOUND", "支付單不存在")
	}
	if err != nil {
		return nil, err
	}
	ch, err := s.repo.channelForPayment(ctx, p)
	if err != nil {
		return nil, errors.BadRequest("payment.CHANNEL_INVALID", "無法確定原支付渠道，請核對流水")
	}
	provider, err := s.repo.reg.Provider(ch.Driver)
	if err != nil {
		return nil, errors.BadRequest("payment.CHANNEL_UNSUPPORTED", "原支付驅動不可用")
	}
	capr, ok := provider.(port.Capturer)
	if !ok {
		return nil, errors.BadRequest("payment.CAPTURE_UNSUPPORTED", "渠道不支持主動查單，請到網關核對到賬")
	}
	var f *port.CallbackFact
	if orderCapturer, ok := provider.(port.OrderCapturer); ok {
		ref := p.GatewayOrderRef
		if ref == "" && p.ChannelOrderNo == "" {
			if p.OrderID != 0 {
				ref = s.getOrderNo(ctx, p.OrderID)
			} else if p.RechargeOrderID != 0 {
				ref = fmt.Sprintf("RCH%d", p.RechargeOrderID)
			}
		}
		if ref != "" {
			f, err = orderCapturer.QueryPaymentByOrderNo(ctx, ref, s.repo.DecryptConfig(ch))
		} else if p.ChannelOrderNo != "" {
			f, err = capr.QueryPayment(ctx, p.ChannelOrderNo, s.repo.DecryptConfig(ch))
		} else {
			return nil, errors.BadRequest("payment.GATEWAY_ORDER_MISSING", "缺少可查詢訂單號，請到網關核對到賬")
		}
	} else {
		if p.ChannelOrderNo == "" {
			return nil, errors.BadRequest("payment.GATEWAY_ORDER_MISSING", "缺少網關單號，請到網關核對到賬")
		}
		f, err = capr.QueryPayment(ctx, p.ChannelOrderNo, s.repo.DecryptConfig(ch))
	}
	if err != nil {
		return nil, errors.InternalServer("payment.CAPTURE_FAILED", "查單失敗: "+err.Error())
	}
	if f == nil || !f.Success {
		return nil, errors.BadRequest("payment.NOT_PAID", "上游尚未確認支付")
	}
	fact := CallbackFact{Channel: p.Channel, OrderNo: f.OrderNo, Amount: int64(f.Amount), Currency: f.Currency, Success: true, ChannelOrderNo: f.ChannelOrderNo, Raw: f.Raw}
	if err := s.repo.HandleCallback(ctx, p.ID, fact); err != nil {
		return nil, errors.InternalServer("payment.CAPTURE_FAILED", "補單失敗: "+err.Error())
	}
	updated, _ := s.repo.GetPayment(ctx, req.GetId())
	return ToPaymentPB(updated, s.getOrderNo(ctx, p.OrderID)), nil
}

// CreateRefund 創建退款單。
func (s *AdminPaymentService) CreateRefund(ctx context.Context, req *adminv1.CreateRefundRequest) (*adminv1.RefundOrder, error) {
	o, err := data.Client(ctx, s.data).Order.Query().Where(order.OrderNo(req.GetOrderNo()), order.SubsiteID(tenancy.FromContext(ctx).SubsiteID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("order.NOT_FOUND", "訂單不存在")
	}
	if err != nil {
		return nil, err
	}
	if o.CommerceVersion == 1 {
		var actor uint64
		if cl := identity.ClaimsFromContext(ctx); cl != nil {
			actor = cl.Subject
		}
		rf, e := s.repo.RefundPhysical(ctx, o.ID, actor, req)
		if e != nil {
			return nil, e
		}
		return ToRefundPB(rf, o.OrderNo), nil
	}
	if req.GetChannel() != "wallet" {
		return nil, errors.BadRequest("payment.REFUND_CHANNEL_UNSUPPORTED", "當前僅支持自動退至會員餘額；原路或上游退款請先向渠道核實")
	}
	var operatorID uint64
	if claims := identity.ClaimsFromContext(ctx); claims != nil {
		operatorID = claims.Subject
	}
	rf, err := s.repo.RefundToWallet(ctx, o.ID, req.GetAmountCents(), req.ExpectedRefundedCents, req.GetReason(), operatorID, RefundFeeInput{Amount: req.GetFeeCents(), Expected: req.ExpectedRefundedFeeCents})
	if err != nil {
		return nil, err
	}
	return ToRefundPB(rf, o.OrderNo), nil
}

// ListRefunds 退款列表。
func (s *AdminPaymentService) ListRefunds(ctx context.Context, req *adminv1.ListRefundsRequest) (*adminv1.ListRefundsReply, error) {
	rows, err := s.repo.ListRefunds(ctx, req.GetStatus(), req)
	if err != nil {
		return nil, errors.InternalServer("payment.LIST_FAILED", "讀取退款失敗")
	}
	reply := &adminv1.ListRefundsReply{}
	for _, rf := range rows {
		reply.Refunds = append(reply.Refunds, ToRefundPB(rf, s.getOrderNo(ctx, rf.OrderID)))
	}
	return reply, nil
}

func (s *AdminPaymentService) getOrderNo(ctx context.Context, orderID uint64) string {
	o, err := data.Client(ctx, s.data).Order.Get(ctx, orderID)
	if err != nil {
		return ""
	}
	return o.OrderNo
}

// requestBaseURL 從 kratos transport 取 scheme://host（支付回跳/回調絕對化：
// 外部網關只認絕對地址）。scheme 優先 X-Forwarded-Proto（反代 https），host 優先
// Host 回落 X-Forwarded-Host。用 RequestFromServerContext 而非斷言 khttp.Context
// 巨型接口（同 affiliate requestHost 教訓）。
func requestBaseURL(ctx context.Context) string {
	r, ok := khttp.RequestFromServerContext(ctx)
	if !ok {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if host == "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// absolutePayURL 相對支付 URL 絕對化（已絕對原樣返回；無請求上下文時保持相對
// ——此時適配器回落值/渠道顯式配置兜底）。
func absolutePayURL(ctx context.Context, path string) string {
	if path == "" || strings.HasPrefix(path, "http") {
		return path
	}
	if base := requestBaseURL(ctx); base != "" {
		return base + path
	}
	return path
}

// ── Storefront ──

// StorePaymentService 顧客支付服務。
type StorePaymentService struct {
	storefrontv1.UnimplementedStorePaymentServiceServer
	repo *PaymentRepoImpl
	data *data.Data
}

// NewStorePaymentService 構造。
func NewStorePaymentService(repo *PaymentRepoImpl, d *data.Data) *StorePaymentService {
	return &StorePaymentService{repo: repo, data: d}
}

// ListChannels 啓用渠道列表（：渠道下拉數據源——替代前端硬編碼枚舉）。
// 過濾：啓用 + 已配置（空憑據的「待配置」渠道不對顧客展示；wallet 內置無需配置）。
// 遊客不下發 wallet（餘額支付需登錄態；遊客僅可用真實支付渠道）。
func (s *StorePaymentService) ListChannels(ctx context.Context, req *storefrontv1.ListPaymentChannelsRequest) (*storefrontv1.ChannelListReply, error) {
	scene := req.GetScene()
	if scene == "" {
		scene = scenePurchase
	}
	if scene != scenePurchase && scene != sceneMemberRecharge && scene != sceneSupplyRecharge {
		return nil, errors.BadRequest("payment.SCENE_INVALID", "不支持的支付用途")
	}
	rows, err := data.Client(ctx, s.data).PaymentChannel.Query().
		Where(paymentchannel.Enabled(true), paymentchannel.DeletedAtIsNil()).
		Order(ent.Asc(paymentchannel.FieldSort), ent.Asc(paymentchannel.FieldID)).
		All(ctx)
	if err != nil {
		return nil, errors.InternalServer("payment.LIST_FAILED", "讀取渠道失敗")
	}
	baseCurrency, err := data.BaseCurrency(ctx, s.data)
	if err != nil {
		return nil, err
	}
	// 遊客判定：無登錄 claims（餘額支付依賴錢包賬户）
	guest := identity.ClaimsFromContext(ctx) == nil
	reply := &storefrontv1.ChannelListReply{}
	for _, ch := range rows {
		if _, err := chargeCurrency(ch.Driver, s.repo.DecryptConfig(ch), baseCurrency); err != nil {
			continue
		}
		if !channelAllows(ch, scene) {
			continue
		}
		if guest && ch.Driver == "wallet" {
			continue // 遊客不可用餘額支付
		}
		if ch.Driver != "wallet" && len(s.repo.ConfiguredFields(ch)) == 0 {
			continue // 待配置渠道不下發
		}
		item := &storefrontv1.ChannelItem{Code: ch.Code, Name: paymentChannelName(ch), Driver: ch.Driver, Icon: ch.Icon, Fee: ch.Fee, FeeType: string(ch.FeeType), FeeBearer: string(ch.FeeBearer), Recommended: ch.Recommended, RecommendLabel: ch.RecommendLabel, RecommendDescription: ch.RecommendDescription}
		methods := parseMethods(ch)
		if ch.Driver == "upay" {
			cfg, e := adapter.ParseUpayConfig(s.repo.DecryptConfig(ch))
			if e != nil {
				continue
			}
			for _, option := range cfg.Options() {
				methods = append(methods, ChannelMethod{Code: option.Value, Name: option.Label, Enabled: true})
			}
		}
		for _, m := range methods {
			if !m.Enabled || !m.ChannelUsage.allows(scene) {
				continue
			}
			item.Methods = append(item.Methods, &storefrontv1.MethodItem{Code: m.Code, Name: m.Name, Icon: m.Icon, Recommended: m.Recommended, RecommendLabel: m.RecommendLabel, RecommendDescription: m.RecommendDescription})
		}
		if len(ch.Methods) > 0 && len(item.Methods) == 0 {
			continue
		}
		reply.Channels = append(reply.Channels, item)
	}
	return reply, nil
}

// CreatePayment 創建支付。
func (s *StorePaymentService) CreatePayment(ctx context.Context, req *storefrontv1.CreatePaymentRequest) (*storefrontv1.CreatePaymentReply, error) {
	ctx = port.WithQuoteKey(ctx, req.GetQuoteKey())
	if req.GetOrderNo() == "" || req.GetChannel() == "" {
		return nil, errors.BadRequest("payment.INVALID_INPUT", "訂單號與渠道必填")
	}
	client := data.Client(ctx, s.data)

	// 查訂單
	o, err := s.paymentOrder(ctx, req.GetOrderNo(), req.GetQueryPassword(), req.GetOrderAccessToken())
	if err != nil {
		return nil, err
	}
	if o.Status != order.StatusPendingPayment {
		return nil, errors.BadRequest("payment.ORDER_NOT_PENDING", "訂單不在待支付狀態")
	}

	if o.ExpiredAt.IsZero() || !time.Now().UTC().Before(o.ExpiredAt) || o.ExpiryReview {
		return nil, errors.BadRequest("payment.ORDER_EXPIRED", "訂單已過期或待核對，請重新下單")
	}

	// 檢查渠道啓用
	ch, err := resolveChannel(ctx, s.data, o.SubsiteID, req.GetChannel())
	if ent.IsNotFound(err) {
		return nil, errors.NotFound("payment.CHANNEL_NOT_FOUND", "支付渠道不存在或未啓用")
	}
	if err != nil {
		return nil, err
	}

	if !ch.Enabled {
		return nil, errors.BadRequest("payment.CHANNEL_DISABLED", "支付渠道已停用")
	}
	if err := s.repo.checkPaymentUsage(ch, req.GetMethod(), scenePurchase); err != nil {
		return nil, errors.BadRequest("payment.SCENE_DISABLED", err.Error())
	}
	// wallet 渠道：餘額支付（直接 markPaid—— 接 wallet.DebitInTx）
	if ch.Driver == "wallet" {
		if err := authorizeWalletPayment(ctx, o); err != nil {
			return nil, err
		}
		p, err := s.repo.CreatePayment(ctx, o.ID, ch.Code, o.TotalAmount, "", req.GetMethod())
		if err != nil {
			return nil, paymentCreationError(err)
		}
		// 直接標記成功
		fact := CallbackFact{
			Channel: ch.Code, Amount: o.TotalAmount, Currency: orderBaseCurrency(o),
			Success: true, ChannelOrderNo: fmt.Sprintf("wallet-%d", p.ID),
		}
		if err := s.repo.HandleCallback(ctx, p.ID, fact); err != nil {
			if stderrors.Is(err, walletport.ErrInsufficientBalance) {
				return nil, errors.BadRequest("payment.BALANCE_INSUFFICIENT", "可用餘額不足，請充值或更換支付方式")
			}
			return nil, errors.InternalServer("payment.WALLET_FAILED", "餘額支付暫時失敗，請刷新訂單狀態後重試")
		}
		// 餘額支付同步完成：payload 指向支付頁（該頁會呈現成功態/卡密）——
		// 曾返回 /order/success?no=（前端無此路由，彈窗即 404）
		return &storefrontv1.CreatePaymentReply{
			PaymentId: p.ID, Type: "redirect", Payload: "/payment/" + o.OrderNo,
		}, nil
	}

	// 外部渠道：走 adapter 創建支付（返回收銀台/二維碼/參數包）
	provider, err := s.repo.reg.Provider(ch.Driver)
	if err != nil {
		return nil, errors.BadRequest("payment.CHANNEL_UNSUPPORTED", "渠道驅動未實現: "+ch.Driver)
	}
	cfg := s.repo.DecryptConfig(ch)
	if err := provider.ValidateConfig(cfg); err != nil {
		return nil, errors.InternalServer("payment.CHANNEL_CONFIG_INVALID", "渠道配置無效: "+err.Error())
	}
	// 方式級參數（收銀台顧客所選支付方式：易支付支付寶/微信、USDT 選鏈）：
	// 渠道配置了 methods 時 method 必填且須在啓用列表內；params 透傳適配器路由網關。
	var methodCode string
	var methodParams map[string]string
	if ms := parseMethods(ch); len(ms) > 0 {
		found := false
		for _, m := range ms {
			if m.Enabled && m.Code == req.GetMethod() {
				found = true
				methodCode = m.Code
				methodParams = m.Params
				break
			}
		}
		if !found {
			return nil, errors.BadRequest("payment.METHOD_INVALID", "請選擇該渠道支持的支付方式")
		}
	}
	if isNativeCryptoDriver(ch.Driver) {
		info, err := s.repo.createBepusdtPayment(ctx, ch.ID, o.ID, 0, req.GetMethod())
		if err != nil {
			return nil, errors.BadRequest("payment.CREATE_FAILED", err.Error())
		}
		return &storefrontv1.CreatePaymentReply{PaymentId: info.PaymentID, Type: info.Type, Payload: info.Payload, Quote: &storefrontv1.PaymentQuote{BaseCents: info.BaseCents, FeeCents: info.FeeCents, TotalCents: info.TotalCents}}, nil
	}
	p, err := s.repo.CreatePayment(ctx, o.ID, ch.Code, o.TotalAmount, "", req.GetMethod())
	if err != nil {
		return nil, paymentCreationError(err)
	}
	snap := pricingOf(p).Charge
	// 回跳/回調絕對化（易支付/Stripe/PayPal 等外部網關只認絕對地址）：
	// notify 用 site/url 前綴（CallbackURL；未配置時請求 Host 兜底）；
	// return 回支付頁（輪詢出結果並展示卡密）——曾傳空值，客户付完被
	// 網關回跳到空地址彈 404
	info, err := s.repo.dispatchPayment(ctx, p, provider, port.CreatePaymentRequest{
		OrderNo: p.GatewayOrderRef, GatewayOrderRef: p.GatewayOrderRef,
		Channel:       ch.Code,
		Amount:        money.Cents(p.Amount),
		Subject:       "訂單 " + o.OrderNo,
		ReturnURL:     absolutePayURL(ctx, "/payment/"+o.OrderNo),
		NotifyBaseURL: absolutePayURL(ctx, s.repo.callbackURLFor(ctx, ch)),
		Config:        cfg,
		ChargedUnits:  snap.Units, ChargedCurrency: snap.Currency,
		MethodCode: methodCode, MethodParams: methodParams,
	})
	if err != nil {
		return nil, errors.InternalServer("payment.CREATE_FAILED", "發起支付失敗: "+err.Error())
	}
	current, err := client.Order.Get(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	if current.Status != order.StatusPendingPayment || !time.Now().UTC().Before(current.ExpiredAt) {
		return nil, errors.BadRequest("payment.ORDER_EXPIRED", "訂單已過期，請重新下單；已付款請聯繫售後核對")
	}
	return &storefrontv1.CreatePaymentReply{
		PaymentId: p.ID, Type: info.Type, Payload: string(info.Payload), Quote: pricingQuote(pricingOf(p), p.Channel, p.ChannelID, o.OrderNo, p.ID),
	}, nil
}

// ── 回調路由（免鑑權——架構測試規則 9）────────────────────

// RegisterPaymentCallback 支付回調入口（POST /payments/callback/{channel_code}）
// + PayPal return 同步捕獲（GET /payments/return/{channel}?token=<order_id>）。
// 均不掛 JWT；驗籤由各渠道適配器完成。ACK 語義：驗籤失敗 401，狀態衝突 200，
// 系統錯誤 500 觸發重試。
func RegisterPaymentCallback(srv *khttp.Server, repo *PaymentRepoImpl, d *data.Data) {
	payments := srv.Route("/payments")
	// GET+POST 雙註冊：易支付族網關異步通知多為 GET（query 參數），ParseForm
	// 本就合併 query/body，同一 handler 即可
	handler := func(ctx khttp.Context) error {
		channelCode := ctx.Vars().Get("channel")
		r := ctx.Request()

		// 1) 讀 body（上限 1MB）
		body := make([]byte, 0)
		if r.Body != nil {
			b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "body read failed"})
			}
			body = b
		}

		// 2) 查渠道（按 code；停用渠道拒絕）
		ch, err := callbackChannel(ctx, d, channelCode, r.URL.Query().Get("channel_id"))
		if ent.IsNotFound(err) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": "channel not found"})
		}
		if err != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		// 3) 取 adapter + 解密憑據
		provider, err := repo.reg.Provider(ch.Driver)
		if err != nil {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported driver"})
		}
		cfg := repo.DecryptConfig(ch)

		// 4/5) 解析 + 驗籤 → CallbackFact：
		// Webhooker 分支（stripe/paypal——JSON body + 簽名頭，SDK 驗籤需憑據）
		// 優先於表單分支（alipay/wechat/epay/epusdt）
		var f *port.CallbackFact
		if hooker, ok := provider.(port.Webhooker); ok && isWebhookRequest(r, ch.Driver) {
			headers := map[string]string{}
			for k, vv := range r.Header {
				if len(vv) > 0 {
					headers[k] = vv[0]
				}
			}
			fact, err := hooker.ParseWebhook(headers, body, cfg)
			if err != nil {
				return ctx.JSON(http.StatusUnauthorized, map[string]string{"error": "verify failed"})
			}
			f = fact
		} else {
			verifier, ok := provider.(port.CallbackVerifier)
			if !ok {
				return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "driver has no callback verifier"})
			}
			form, err := parseCallbackForm(r, body)
			if err != nil {
				return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "parse failed"})
			}
			delete(form, "channel_id") // routing metadata is not part of gateway-signed fields
			f, err = verifier.VerifyCallback(form, cfg)
			if err != nil {
				return ctx.JSON(http.StatusUnauthorized, map[string]string{"error": "verify failed"})
			}
		}
		if !f.Success && !isNativeCryptoDriver(ch.Driver) {
			if ch.Driver == "xunhupay" || ch.Driver == "alipay" {
				return ctx.String(http.StatusOK, "success")
			}
			return ctx.JSON(http.StatusOK, map[string]string{"status": "ignored"})
		}

		// 6) 定位支付單（訂單號定位；充值單 RCH<id> 前綴走 recharge 關聯）
		paymentID := uint64(0)
		if isNativeCryptoDriver(ch.Driver) {
			matched, lookupErr := data.Client(ctx, d).Payment.Query().Where(payment.ChannelID(ch.ID), payment.DriverSnapshot(ch.Driver), payment.GatewayOrderRef(f.GatewayOrderRef)).Only(ctx)
			if lookupErr == nil {
				paymentID = matched.ID
			}
		} else {
			paymentID = locatePaymentByFact(ctx, d, channelCode, f)
		}
		if paymentID == 0 {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "payment not found"})
		}

		// 7) 走回調管線（四重校驗 + 冪等 + markPaid）
		fact := CallbackFact{
			Channel:         channelCode,
			GatewayOrderRef: f.GatewayOrderRef,
			ChannelOrderNo:  f.ChannelOrderNo,
			OrderNo:         f.OrderNo,
			Amount:          int64(f.Amount),
			Currency:        f.Currency,
			Success:         f.Success,
			Raw:             f.Raw,
		}
		p, checkErr := repo.GetPayment(ctx, paymentID)
		if checkErr != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": "payment lookup failed"})
		}
		resolved, checkErr := repo.channelForPayment(ctx, p)
		if checkErr != nil || resolved.ID != ch.ID {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "payment channel mismatch"})
		}
		if !f.Success {
			if p.GatewayOrderRef != f.GatewayOrderRef || (p.ChannelOrderNo != "" && p.ChannelOrderNo != f.ChannelOrderNo) || p.Amount != int64(f.Amount) {
				return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "payment mismatch"})
			}
			return ctx.String(http.StatusOK, "success")
		}
		if err := repo.HandleCallback(ctx, paymentID, fact); err != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		// 應答體渠道感知（Acker 能力位）：epusdt 類網關要求純文本 "ok"；
		// 易支付返回 success；未實現 Acker 的渠道維持 JSON。
		if acker, ok := provider.(port.Acker); ok {
			return ctx.String(http.StatusOK, acker.SuccessAck())
		}
		return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
	payments.POST("/callback/{channel}", handler)
	payments.GET("/callback/{channel}", handler)
	// PayPal return 同步捕獲（）：買家在 PayPal 授權後跳回
	// return_url（PayPal 追加 token=<order_id>，1.x 生產依賴）——
	// 先查後捕（Capturer），成功後走回調管線 markPaid，302 回店鋪頁。
	payments.GET("/return/{channel}", func(ctx khttp.Context) error {
		channelCode := ctx.Vars().Get("channel")
		token := strings.TrimSpace(ctx.Request().URL.Query().Get("token"))
		// 單號白名單（匿名端點出站放大器——1.x M-11）
		if !paypalOrderIDTokenRe.MatchString(token) {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid token"})
		}
		ch, err := callbackChannel(ctx, d, channelCode, ctx.Request().URL.Query().Get("channel_id"))
		if ent.IsNotFound(err) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": "channel not found"})
		}
		if err != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		provider, err := repo.reg.Provider(ch.Driver)
		if err != nil {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported driver"})
		}
		capturer, ok := provider.(port.Capturer)
		if !ok {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "driver has no capturer"})
		}
		f, err := capturer.QueryPayment(ctx, token, repo.DecryptConfig(ch))
		if err != nil {
			return ctx.JSON(http.StatusBadGateway, map[string]string{"error": "capture failed"})
		}
		paymentID := locatePaymentByFact(ctx, d, channelCode, f)
		if paymentID == 0 {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "payment not found"})
		}
		p, checkErr := repo.GetPayment(ctx, paymentID)
		if checkErr != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": "payment lookup failed"})
		}
		original, checkErr := repo.channelForPayment(ctx, p)
		if checkErr != nil || original.ID != ch.ID {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "payment channel mismatch"})
		}
		fallback := "/member?tab=recharge"
		if p.OrderID > 0 {
			o, e := data.Client(ctx, d).Order.Get(ctx, p.OrderID)
			if e != nil {
				return e
			}
			fallback = "/payment/" + o.OrderNo
		}
		if !f.Success {
			return ctx.JSON(http.StatusFound, khttp.NewRedirect(fallback, http.StatusFound))
		}
		if err := repo.HandleCallback(ctx, paymentID, CallbackFact{
			Channel:        channelCode,
			ChannelOrderNo: f.ChannelOrderNo,
			OrderNo:        f.OrderNo,
			Amount:         int64(f.Amount),
			Currency:       f.Currency,
			Success:        f.Success,
			Raw:            f.Raw,
		}); err != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusFound, khttp.NewRedirect(fallback, http.StatusFound))
	})
}

// locatePaymentByFact 按回調事實定位支付單（訂單號定位；充值單 RCH<id> 前綴走
// recharge 關聯）。回調路由與 PayPal return 捕獲端點共用。
func locatePaymentByFact(ctx context.Context, d *data.Data, channelCode string, f *port.CallbackFact) uint64 {
	if f == nil || f.OrderNo == "" {
		return 0
	}
	client := data.Client(ctx, d)
	if strings.HasPrefix(f.OrderNo, "ZP") {
		p, err := client.Payment.Query().Where(payment.Channel(channelCode), payment.GatewayOrderRef(f.OrderNo)).Only(ctx)
		if err != nil {
			return 0
		}
		return p.ID
	}
	query := client.Payment.Query().Where(payment.Channel(channelCode))
	if rid, ok := strings.CutPrefix(f.OrderNo, "RCH"); ok {
		id, err := strconv.ParseUint(rid, 10, 64)
		if err != nil {
			return 0
		}
		query.Where(payment.RechargeOrderID(id))
	} else {
		o, err := client.Order.Query().Where(order.OrderNo(f.OrderNo)).Only(ctx)
		if err != nil {
			return 0
		}
		query.Where(payment.OrderID(o.ID))
	}
	// 重複回調先找已登記的網關流水，保持冪等；發起重試會產生多筆
	// pending 流水，不能使用 Only（多行即報錯，導致已付款回調找不到單）。
	if f.ChannelOrderNo != "" {
		p, err := query.Clone().Where(payment.ChannelOrderNo(f.ChannelOrderNo)).First(ctx)
		if err == nil {
			return p.ID
		}
		if !ent.IsNotFound(err) {
			return 0
		}
	}
	p, err := query.Where(payment.GatewayOrderRefIsNil(), payment.StatusNEQ(payment.StatusSuccess)).Order(ent.Desc(payment.FieldID)).First(ctx)
	if err != nil {
		return 0
	}
	return p.ID
}

// parseCallbackForm 解析回調為扁平 map：XML（wechat）→ 元素名→文本；否則表單/查詢串。
func parseCallbackForm(r *http.Request, body []byte) (map[string]string, error) {
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "xml") {
		return parseXMLMap(body)
	}
	// 入口已讀取 body 供 webhook 驗籤使用，表單解析必須從保存的原文重讀。
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	m := map[string]string{}
	for k := range r.Form {
		m[k] = r.Form.Get(k)
	}
	delete(m, "channel_id")
	if strings.Contains(ct, "json") {
		m = map[string]string{}
	}
	// JSON 回調體（epusdt 類）：UseNumber 保留原始字面——10.00 不得塌成 10
	//（驗籤按原文重算，簽名不變式 ）
	if len(m) == 0 && len(body) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var j map[string]any
		if dec.Decode(&j) == nil {
			for k, v := range j {
				switch tv := v.(type) {
				case json.Number:
					m[k] = tv.String()
				case string:
					m[k] = tv
				case bool:
					m[k] = fmt.Sprintf("%v", tv)
				default:
					_ = tv
					if b, err := json.Marshal(v); err == nil {
						m[k] = string(b)
					}
				}
			}
		}
	}
	return m, nil
}

// isWebhookRequest webhook 分支判定：JSON 內容或渠道專屬簽名頭存在。
// （stripe：Stripe-Signature；paypal：Paypal-Transmission-*——均為表單分支不可達的頭）
func isWebhookRequest(r *http.Request, driver string) bool {
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		return true
	}
	return r.Header.Get("Stripe-Signature") != "" ||
		r.Header.Get("Paypal-Transmission-Id") != ""
}

// paypalOrderIDTokenRe PayPal return 捕獲 token 白名單（匿名端點出站放大器——1.x M-11）。
var paypalOrderIDTokenRe = regexp.MustCompile(`^[A-Z0-9]{5,30}$`)

// parseXMLMap 扁平 XML（wechat 回調 <xml><k>v</k>...</xml>）→ map。
func parseXMLMap(body []byte) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	m := map[string]string{}
	var cur string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = t.Name.Local
		case xml.CharData:
			if cur != "" {
				m[cur] = string(t)
			}
		case xml.EndElement:
			cur = ""
		}
	}
	return m, nil
}

var _ = refundorder.ChannelWallet // 保持引用
