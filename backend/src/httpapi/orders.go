package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/pricing"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const (
	// DefaultPaymentTimeout 是下单后的支付期限，与上游一致。
	DefaultPaymentTimeout = 30 * time.Minute
	// expiredOrderBatch 是关闭超时订单时每批处理的订单数。
	expiredOrderBatch    = 100
	timeoutCancelReason  = "支付超时自动关闭"
	maxCancelReasonRunes = 200
	maxReviewRunes       = 500
	maxReviewTags        = 5
	maxReviewTagRunes    = 20
)

var (
	ErrOrderNotFound     = &APIError{Status: http.StatusNotFound, Code: "order_not_found", Message: "订单不存在"}
	ErrOrderItemNotFound = &APIError{Status: http.StatusNotFound, Code: "order_item_not_found", Message: "订单里没有这件商品"}
	ErrEmptyCheckout     = &APIError{Status: http.StatusBadRequest, Code: "empty_cart", Message: "请先在购物车选择要结算的商品"}
	ErrOrderExpired      = &APIError{Status: http.StatusConflict, Code: "order_expired", Message: "订单已超过支付时间，已自动关闭"}
	ErrOrderNotCompleted = &APIError{Status: http.StatusConflict, Code: "order_not_completed", Message: "确认收货后才能评价"}
	ErrReviewExists      = &APIError{Status: http.StatusConflict, Code: "review_exists", Message: "这件商品已经评价过了"}

	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	// 模拟支付只接受这几种方式；不传时用余额支付。
	paymentMethods = map[string]bool{"mock_balance": true, "mock_wechat": true, "mock_alipay": true}
)

var orderStatusText = map[domain.OrderStatus]string{
	domain.OrderPendingPayment: "待支付", domain.OrderPaid: "待发货", domain.OrderShipped: "已发货",
	domain.OrderCompleted: "已完成", domain.OrderCancelled: "已取消",
}

// statusConflict 说明订单当前状态不允许该操作。
func statusConflict(status domain.OrderStatus, action string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: "order_status_conflict",
		Message: fmt.Sprintf("订单%s，不能%s", orderStatusText[status], action)}
}

// ---------- DTO ----------

type orderItemView struct {
	OrderItemID string       `json:"order_item_id"`
	ProductID   string       `json:"product_id"`
	SkuID       string       `json:"sku_id"`
	Name        string       `json:"name"`
	SkuName     string       `json:"sku_name"`
	ImageURL    string       `json:"image_url"`
	Price       domain.Money `json:"price"`
	Quantity    int          `json:"quantity"`
	Amount      domain.Money `json:"amount"`
	// ReviewID 是该订单项的评价，未评价时为空；只有订单详情填写。
	ReviewID string `json:"review_id"`
}

type paymentView struct {
	PaymentID     string               `json:"payment_id"`
	Amount        domain.Money         `json:"amount"`
	Status        domain.PaymentStatus `json:"status"`
	Method        string               `json:"method"`
	TransactionNo string               `json:"transaction_no"`
	ExpiresAt     time.Time            `json:"expires_at"`
	PaidAt        *time.Time           `json:"paid_at"`
}

type orderView struct {
	OrderID           string             `json:"order_id"`
	OrderNo           string             `json:"order_no"`
	AccountID         string             `json:"account_id"`
	MerchantID        string             `json:"merchant_id"`
	MerchantName      string             `json:"merchant_name"`
	Status            domain.OrderStatus `json:"status"`
	TotalAmount       domain.Money       `json:"total_amount"`
	DiscountAmount    domain.Money       `json:"discount_amount"`
	PayAmount         domain.Money       `json:"pay_amount"`
	PaymentDeadlineAt *time.Time         `json:"payment_deadline_at"`
	PaidAt            *time.Time         `json:"paid_at"`
	ShippedAt         *time.Time         `json:"shipped_at"`
	CompletedAt       *time.Time         `json:"completed_at"`
	ClosedAt          *time.Time         `json:"closed_at"`
	CancelReason      string             `json:"cancel_reason"`
	Items             []orderItemView    `json:"items"`
	// Payment 只在订单详情和订单操作的响应中出现。
	Payment   *paymentView `json:"payment,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

func toOrderView(d store.OrderDetail) orderView {
	v := orderView{OrderID: d.OrderID, OrderNo: d.OrderNo, AccountID: d.AccountID, MerchantID: d.MerchantID, MerchantName: d.MerchantName,
		Status: d.Status, TotalAmount: d.TotalAmount, DiscountAmount: d.DiscountAmount, PayAmount: d.PayAmount, PaymentDeadlineAt: d.PaymentDeadlineAt,
		PaidAt: d.PaidAt, ShippedAt: d.ShippedAt, CompletedAt: d.CompletedAt, ClosedAt: d.ClosedAt, CancelReason: d.CancelReason,
		Items: []orderItemView{}, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	for _, it := range d.Items {
		v.Items = append(v.Items, orderItemView{OrderItemID: it.OrderItemID, ProductID: it.ProductID, SkuID: it.SkuID, Name: it.Name,
			SkuName: it.SkuName, ImageURL: it.ImageURL, Price: it.Price, Quantity: it.Quantity, Amount: it.Price.Mul(it.Quantity),
			ReviewID: d.ReviewIDs[it.OrderItemID]})
	}
	if p := d.Payment; p != nil {
		v.Payment = &paymentView{PaymentID: p.PaymentID, Amount: p.Amount, Status: p.Status, Method: p.Method, TransactionNo: p.TransactionNo,
			ExpiresAt: p.ExpiresAt, PaidAt: p.PaidAt}
	}
	return v
}

// readOrderStatus 读取 status 查询参数：为空表示全部，否则必须是已知的订单状态。
func readOrderStatus(r *http.Request) (domain.OrderStatus, error) {
	status := domain.OrderStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if _, ok := orderStatusText[status]; status != "" && !ok {
		return "", fieldError("status", "状态只能是 pending_payment、paid、shipped、completed 或 cancelled")
	}
	return status, nil
}

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request, q store.OrderQuery) {
	status, err := readOrderStatus(r)
	if err != nil {
		writeError(w, err)
		return
	}
	q.Status, q.OrderNo, q.Page = status, strings.TrimSpace(r.URL.Query().Get("order_no")), readPage(r)
	items, total, err := s.store.ListOrders(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list orders", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toOrderView))
}

// getOrder 读取订单详情；visible 为 false（不属于当前账户/商家）时按不存在处理，不暴露订单是否存在。
func (s *Server) getOrder(w http.ResponseWriter, r *http.Request, visible func(o domain.Order) bool) {
	d, err := s.store.GetOrder(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !visible(d.Order)) {
		writeError(w, ErrOrderNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "get order", err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderView(d))
}

// updateOrder 调用 Store.UpdateOrder 并统一处理错误：fn 返回的 APIError 原样输出，订单不存在 404。
func (s *Server) updateOrder(w http.ResponseWriter, r *http.Request, orderID string, fn func(o *domain.Order, p *domain.Payment) error) (store.OrderDetail, bool) {
	d, err := s.store.UpdateOrder(r.Context(), orderID, fn)
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrOrderNotFound)
	case err != nil:
		s.storeFailed(w, r, "update order", err)
	default:
		return d, true
	}
	return store.OrderDetail{}, false
}

// ---------- 用户：结算 ----------

type checkoutRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	// UserCouponIDs 不传时自动选最优券（与试算一致）；传了（可以为空数组）只用指定的券。
	UserCouponIDs *[]string `json:"user_coupon_ids"`
	// ExpectedPayAmount 是客户端确认页显示的实付金额；传了且与下单时重新计算的金额不同，返回 409 price_changed，不下单。
	ExpectedPayAmount *domain.Money `json:"expected_pay_amount"`
}

type checkoutResponse struct {
	CheckoutRequestID string      `json:"checkout_request_id"`
	Replayed          bool        `json:"replayed"`
	Items             []orderView `json:"items"`
}

// handleCheckout 把购物车中已选中的商品按店铺拆单下单。幂等键放在 Idempotency-Key 请求头或 idempotency_key 字段（必填）：
// 同一账户同一个键成功下单后，再次请求直接返回那次的订单（200，replayed=true），不会重复下单；失败的请求不占用这个键。
// 价格、库存、优惠都在事务内按最新数据重新计算，任何一项不满足都整体失败，不创建订单、不扣库存、不清购物车。
func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in checkoutRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, err)
			return
		}
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	bodyKey := strings.TrimSpace(in.IdempotencyKey)
	switch {
	case key != "" && bodyKey != "" && key != bodyKey:
		writeError(w, fieldError("idempotency_key", "请求头和请求体中的幂等键不一致"))
		return
	case key == "":
		key = bodyKey
	}
	if !idempotencyKeyPattern.MatchString(key) {
		writeError(w, fieldError("idempotency_key", "请提供幂等键（1–128 位字母、数字或 . _ : -）"))
		return
	}
	var choice []string
	if in.UserCouponIDs != nil {
		choice = []string{}
		for _, id := range *in.UserCouponIDs {
			if id = strings.TrimSpace(id); id != "" && !contains(choice, id) {
				choice = append(choice, id)
			}
		}
		if len(choice) > 20 {
			writeError(w, fieldError("user_coupon_ids", "一次最多指定 20 张券"))
			return
		}
	}
	res, err := s.store.Checkout(r.Context(), acc.AccountID, key, func(ctx context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
		// 已拿到账户、购物车、库存和券的锁之后，在同一事务里读取当前时间、分类和有效活动：排队等锁再久，
		// 计价也按此刻生效的规则，支付期限也从订单真正创建时开始算。
		pc, err := s.loadPricingContext(ctx)
		if err != nil {
			return store.CheckoutPlan{}, err
		}
		return checkoutPlan(pc, st, choice, in.ExpectedPayAmount, pc.now.Add(s.paymentTimeout))
	})
	var apiErr *APIError
	var ce *pricing.CouponError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr)
		return
	case errors.As(err, &ce):
		writeError(w, &APIError{Status: http.StatusBadRequest, Code: "coupon_not_applicable", Field: "user_coupon_ids", Message: "优惠券不能使用：" + ce.Reason})
		return
	case err != nil:
		s.storeFailed(w, r, "checkout", err)
		return
	}
	out := checkoutResponse{CheckoutRequestID: res.RequestID, Replayed: res.Replayed, Items: []orderView{}}
	ids := make([]string, 0, len(res.Orders))
	for _, o := range res.Orders {
		out.Items = append(out.Items, toOrderView(o))
		ids = append(ids, o.OrderID)
	}
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	} else {
		s.audit(r, "order.checkout", acc.AccountID, "checkout_request_id", res.RequestID, "order_ids", strings.Join(ids, ","))
	}
	writeJSON(w, status, out)
}

// checkoutPlan 根据事务内加锁读到的购物车和券、事务内读取的计价数据（pc）计价并拆单。与试算用同一个 pricingContext.price。
func checkoutPlan(pc pricingContext, st store.CheckoutState, choice []string, expected *domain.Money, deadline time.Time) (store.CheckoutPlan, error) {
	if len(st.Lines) == 0 {
		return store.CheckoutPlan{}, ErrEmptyCheckout
	}
	// 券的过期按事务内的当前时间判断。
	coupons := make([]store.OwnedCoupon, len(st.Coupons))
	for i, c := range st.Coupons {
		c.Status = store.EffectiveCouponStatus(c.Status, c.Coupon, pc.now)
		coupons[i] = c
	}
	// 已选中的项必须全部可以购买：不悄悄跳过失效商品（上游会跳过并照样清掉它们）。
	for _, l := range st.Lines {
		if reason := unavailableReason(l); reason != "" {
			name := l.ProductName
			if name == "" {
				name = "已删除的商品"
			}
			return store.CheckoutPlan{}, &APIError{Status: http.StatusConflict, Code: "item_unavailable",
				Message: fmt.Sprintf("「%s」%s，请先在购物车里处理", name, reason)}
		}
	}
	priced, err := pc.price(st.Lines, coupons, choice)
	if err != nil {
		return store.CheckoutPlan{}, err
	}
	res := priced.result
	if expected != nil && *expected != res.PayAmount {
		return store.CheckoutPlan{}, &APIError{Status: http.StatusConflict, Code: "price_changed",
			Message: fmt.Sprintf("价格或优惠有变化，实付金额变为 ¥%s，请确认后重新提交", res.PayAmount)}
	}
	itemDiscount := map[string]pricing.ItemResult{}
	for _, it := range res.Items {
		itemDiscount[it.CartItemID] = it
	}
	merchantOf := map[string]string{}
	byMerchant := map[string]*store.PlannedOrder{}
	var order []string
	for _, l := range st.Lines {
		merchantOf[l.CartItemID] = l.MerchantID
		po := byMerchant[l.MerchantID]
		if po == nil {
			po = &store.PlannedOrder{MerchantID: l.MerchantID}
			byMerchant[l.MerchantID] = po
			order = append(order, l.MerchantID)
		}
		po.Items = append(po.Items, store.PlannedItem{CartItemID: l.CartItemID, OrderItem: domain.OrderItem{
			ProductID: l.ProductID, SkuID: l.SkuID, Name: l.ProductName, SkuName: l.SkuName, ImageURL: l.ImageURL, Price: l.UnitPrice,
			Quantity: l.Quantity, MerchantID: l.MerchantID, MerchantName: l.MerchantName}})
		it := itemDiscount[l.CartItemID]
		po.TotalAmount += it.Amount
		po.DiscountAmount += it.Discount
		po.PayAmount += it.PayAmount
	}
	// 券记在它分摊到的每个订单上（平台券可能跨多个店铺）。
	for _, line := range res.Lines {
		if line.Type != "coupon" {
			continue
		}
		for _, cartItemID := range line.CartItemIDs {
			po := byMerchant[merchantOf[cartItemID]]
			if po != nil && !contains(po.UserCouponIDs, line.ID) {
				po.UserCouponIDs = append(po.UserCouponIDs, line.ID)
			}
		}
	}
	plan := store.CheckoutPlan{PaymentDeadline: deadline}
	for _, id := range order {
		plan.Orders = append(plan.Orders, *byMerchant[id])
	}
	return plan, nil
}

// ---------- 用户：订单 ----------

func (s *Server) handleListMyOrders(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	s.listOrders(w, r, store.OrderQuery{AccountID: acc.AccountID})
}

func (s *Server) handleGetMyOrder(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	s.getOrder(w, r, func(o domain.Order) bool { return o.AccountID == acc.AccountID })
}

type payRequest struct {
	Method string `json:"method"`
}

type payResponse struct {
	Order   orderView   `json:"order"`
	Payment paymentView `json:"payment"`
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

// handleOrderAction 处理 POST /orders/{id}:pay | :cancel | :confirm-receipt（路径段内带动作，与上游一致）。
func (s *Server) handleOrderAction(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id, action, ok := strings.Cut(r.PathValue("action"), ":")
	if !ok || id == "" {
		writeError(w, ErrNotFound)
		return
	}
	switch action {
	case "pay":
		s.payOrder(w, r, acc.AccountID, id)
	case "cancel":
		s.cancelOrder(w, r, acc.AccountID, id)
	case "confirm-receipt":
		s.confirmReceipt(w, r, acc.AccountID, id)
	default:
		writeError(w, ErrNotFound)
	}
}

// decodeOptionalJSON 读取可以省略的请求体。
func decodeOptionalJSON(r *http.Request, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return decodeJSON(r, dst)
}

// payOrder 模拟支付：只能支付自己待支付且未超时的订单。已超时的订单在这里顺带关闭（回补库存、退券）并返回 409 order_expired。
func (s *Server) payOrder(w http.ResponseWriter, r *http.Request, accountID, orderID string) {
	var in payRequest
	if err := decodeOptionalJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	method := strings.TrimSpace(in.Method)
	if method == "" {
		method = "mock_balance"
	}
	if !paymentMethods[method] {
		writeError(w, fieldError("method", "支付方式只能是 mock_balance、mock_wechat 或 mock_alipay"))
		return
	}
	now := s.now().UTC()
	expired := false
	d, ok := s.updateOrder(w, r, orderID, func(o *domain.Order, p *domain.Payment) error {
		switch {
		case o.AccountID != accountID:
			return ErrOrderNotFound
		case o.Status != domain.OrderPendingPayment:
			return statusConflict(o.Status, "支付")
		case p == nil || p.Status != domain.PaymentPending:
			return statusConflict(o.Status, "支付")
		case o.PaymentDeadlineAt != nil && !now.Before(*o.PaymentDeadlineAt):
			expired = true
			closeOrder(o, p, now, timeoutCancelReason)
			return nil
		}
		o.Status, o.PaidAt = domain.OrderPaid, &now
		p.Status, p.PaidAt, p.Method, p.TransactionNo = domain.PaymentPaid, &now, method, newTransactionNo()
		return nil
	})
	if !ok {
		return
	}
	if expired {
		s.audit(r, "order.closed", accountID, "order_id", orderID, "reason", "payment_timeout")
		writeError(w, ErrOrderExpired)
		return
	}
	s.audit(r, "order.paid", accountID, "order_id", orderID, "payment_id", d.Payment.PaymentID, "method", method, "amount", d.Payment.Amount.String())
	v := toOrderView(d)
	writeJSON(w, http.StatusOK, payResponse{Order: v, Payment: *v.Payment})
}

func newTransactionNo() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "MOCK" + strings.ToUpper(hex.EncodeToString(b[:]))
}

// closeOrder 把待支付订单改为已取消并关闭支付单；库存回补和退券由 Store 在同一事务内完成。
func closeOrder(o *domain.Order, p *domain.Payment, now time.Time, reason string) {
	o.Status, o.ClosedAt, o.CancelReason = domain.OrderCancelled, &now, reason
	if p != nil && p.Status == domain.PaymentPending {
		p.Status = domain.PaymentClosed
	}
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request, accountID, orderID string) {
	var in cancelRequest
	if err := decodeOptionalJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(reason) > maxCancelReasonRunes {
		writeError(w, fieldError("reason", fmt.Sprintf("取消原因最多 %d 个字", maxCancelReasonRunes)))
		return
	}
	if reason == "" {
		reason = "用户取消"
	}
	now := s.now().UTC()
	d, ok := s.updateOrder(w, r, orderID, func(o *domain.Order, p *domain.Payment) error {
		switch {
		case o.AccountID != accountID:
			return ErrOrderNotFound
		case o.Status != domain.OrderPendingPayment:
			return statusConflict(o.Status, "取消")
		}
		closeOrder(o, p, now, reason)
		return nil
	})
	if !ok {
		return
	}
	s.audit(r, "order.cancelled", accountID, "order_id", orderID, "by", "user")
	writeJSON(w, http.StatusOK, toOrderView(d))
}

func (s *Server) confirmReceipt(w http.ResponseWriter, r *http.Request, accountID, orderID string) {
	now := s.now().UTC()
	d, ok := s.updateOrder(w, r, orderID, func(o *domain.Order, _ *domain.Payment) error {
		switch {
		case o.AccountID != accountID:
			return ErrOrderNotFound
		case o.Status != domain.OrderShipped:
			return statusConflict(o.Status, "确认收货")
		}
		o.Status, o.CompletedAt = domain.OrderCompleted, &now
		return nil
	})
	if !ok {
		return
	}
	s.audit(r, "order.completed", accountID, "order_id", orderID)
	writeJSON(w, http.StatusOK, toOrderView(d))
}

// ---------- 用户：评价 ----------

type reviewRequest struct {
	Rating  *int     `json:"rating"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

type myReviewView struct {
	ReviewID    string    `json:"review_id"`
	OrderID     string    `json:"order_id"`
	OrderItemID string    `json:"order_item_id"`
	ProductID   string    `json:"product_id"`
	SkuID       string    `json:"sku_id"`
	Rating      int       `json:"rating"`
	Content     string    `json:"content"`
	Tags        []string  `json:"tags"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// handleOrderItemAction 处理 POST /orders/{id}/items/{item_id}:review：只能评价自己已完成订单里的商品，每件只能评价一次。
func (s *Server) handleOrderItemAction(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	orderID := r.PathValue("id")
	itemID, ok := strings.CutSuffix(r.PathValue("action"), ":review")
	if !ok || itemID == "" {
		writeError(w, ErrNotFound)
		return
	}
	var in reviewRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.Rating == nil || *in.Rating < 1 || *in.Rating > 5 {
		writeError(w, fieldError("rating", "评分必须是 1 到 5 的整数"))
		return
	}
	content := strings.TrimSpace(in.Content)
	if n := utf8.RuneCountInString(content); n == 0 || n > maxReviewRunes {
		writeError(w, fieldError("content", fmt.Sprintf("评价内容需要 1 到 %d 个字", maxReviewRunes)))
		return
	}
	tags := []string{}
	for _, t := range in.Tags {
		t = strings.TrimSpace(t)
		if t == "" || contains(tags, t) {
			continue
		}
		if utf8.RuneCountInString(t) > maxReviewTagRunes {
			writeError(w, fieldError("tags", fmt.Sprintf("每个标签最多 %d 个字", maxReviewTagRunes)))
			return
		}
		tags = append(tags, t)
	}
	if len(tags) > maxReviewTags {
		writeError(w, fieldError("tags", fmt.Sprintf("最多 %d 个标签", maxReviewTags)))
		return
	}
	review, err := s.store.CreateReview(r.Context(), acc.AccountID, orderID, itemID, func(o domain.Order, _ domain.OrderItem) (domain.ProductReview, error) {
		if o.Status != domain.OrderCompleted {
			return domain.ProductReview{}, ErrOrderNotCompleted
		}
		return domain.ProductReview{Rating: *in.Rating, Content: content, Tags: tags, Status: domain.ReviewVisible}, nil
	})
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrOrderNotFound)
	case errors.Is(err, store.ErrOrderItemNotFound):
		writeError(w, ErrOrderItemNotFound)
	case errors.Is(err, store.ErrConflict):
		writeError(w, ErrReviewExists)
	case err != nil:
		s.storeFailed(w, r, "create review", err)
	default:
		s.audit(r, "review.created", acc.AccountID, "review_id", review.ReviewID, "order_id", orderID, "order_item_id", itemID, "rating", review.Rating)
		writeJSON(w, http.StatusCreated, myReviewView{ReviewID: review.ReviewID, OrderID: review.OrderID, OrderItemID: review.OrderItemID,
			ProductID: review.ProductID, SkuID: review.SkuID, Rating: review.Rating, Content: review.Content, Tags: review.Tags,
			Status: review.Status, CreatedAt: review.CreatedAt})
	}
}

// ---------- 商家 ----------

type orderStatusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (s *Server) handleListMerchantOrders(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	s.listOrders(w, r, store.OrderQuery{MerchantID: acc.MerchantID})
}

func (s *Server) handleGetMerchantOrder(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	s.getOrder(w, r, func(o domain.Order) bool { return o.MerchantID == acc.MerchantID })
}

// handleUpdateMerchantOrder 商家只能把自己店铺“待发货”（已支付）的订单改为已发货；其他店铺的订单按不存在处理（404，与上游一致）。
func (s *Server) handleUpdateMerchantOrder(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in orderStatusRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if domain.OrderStatus(strings.TrimSpace(in.Status)) != domain.OrderShipped {
		writeError(w, fieldError("status", "商家只能把订单改为 shipped（已发货）"))
		return
	}
	id := r.PathValue("id")
	d, ok := s.updateOrder(w, r, id, func(o *domain.Order, _ *domain.Payment) error {
		if o.MerchantID != acc.MerchantID {
			return ErrOrderNotFound
		}
		return ship(o, s.now().UTC())
	})
	if !ok {
		return
	}
	s.audit(r, "order.shipped", acc.AccountID, "order_id", id, "merchant_id", acc.MerchantID)
	writeJSON(w, http.StatusOK, toOrderView(d))
}

func ship(o *domain.Order, now time.Time) error {
	if o.Status != domain.OrderPaid {
		return statusConflict(o.Status, "发货")
	}
	o.Status, o.ShippedAt = domain.OrderShipped, &now
	return nil
}

// ---------- 管理员 ----------

func (s *Server) handleListAdminOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.listOrders(w, r, store.OrderQuery{MerchantID: strings.TrimSpace(q.Get("merchant_id")), AccountID: strings.TrimSpace(q.Get("account_id"))})
}

func (s *Server) handleGetAdminOrder(w http.ResponseWriter, r *http.Request) {
	s.getOrder(w, r, func(domain.Order) bool { return true })
}

// handleUpdateAdminOrder 管理员可以代商家发货（paid → shipped），或取消待支付订单（回补库存、退券）。
// 与上游不同，管理员同样受订单状态机约束，不能把订单改成任意状态。
func (s *Server) handleUpdateAdminOrder(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in orderStatusRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(reason) > maxCancelReasonRunes {
		writeError(w, fieldError("reason", fmt.Sprintf("取消原因最多 %d 个字", maxCancelReasonRunes)))
		return
	}
	if reason == "" {
		reason = "平台取消"
	}
	target := domain.OrderStatus(strings.TrimSpace(in.Status))
	if target != domain.OrderShipped && target != domain.OrderCancelled {
		writeError(w, fieldError("status", "管理员只能把订单改为 shipped（已发货）或 cancelled（已取消）"))
		return
	}
	id := r.PathValue("id")
	now := s.now().UTC()
	d, ok := s.updateOrder(w, r, id, func(o *domain.Order, p *domain.Payment) error {
		if target == domain.OrderShipped {
			return ship(o, now)
		}
		if o.Status != domain.OrderPendingPayment {
			return statusConflict(o.Status, "取消")
		}
		closeOrder(o, p, now, reason)
		return nil
	})
	if !ok {
		return
	}
	action := "order.shipped"
	if target == domain.OrderCancelled {
		action = "order.cancelled"
	}
	s.audit(r, action, acc.AccountID, "order_id", id, "by", "admin")
	writeJSON(w, http.StatusOK, toOrderView(d))
}

// ---------- 超时关闭 ----------

// errSkipOrder 表示订单在加锁后已不需要关闭（已支付或已取消）。
var errSkipOrder = errors.New("order no longer expired")

// CloseExpiredOrders 关闭所有已过支付期限的待支付订单（回补库存、退券），返回关闭的数量。由后台定时调用；
// 支付接口也会在发现超时时顺带关闭对应订单，两者在订单行锁内判断，不会重复关闭。
func (s *Server) CloseExpiredOrders(ctx context.Context) (int, error) {
	closed := 0
	for {
		now := s.now().UTC()
		ids, err := s.store.ListExpiredOrderIDs(ctx, now, expiredOrderBatch)
		if err != nil {
			return closed, err
		}
		for _, id := range ids {
			_, err := s.store.UpdateOrder(ctx, id, func(o *domain.Order, p *domain.Payment) error {
				if o.Status != domain.OrderPendingPayment || o.PaymentDeadlineAt == nil || now.Before(*o.PaymentDeadlineAt) {
					return errSkipOrder
				}
				closeOrder(o, p, now, timeoutCancelReason)
				return nil
			})
			switch {
			case errors.Is(err, errSkipOrder):
			case err != nil:
				return closed, err
			default:
				closed++
				s.logger.InfoContext(ctx, "audit", "action", "order.closed", "order_id", id, "reason", "payment_timeout", "by", "system")
			}
		}
		if len(ids) < expiredOrderBatch {
			return closed, nil
		}
	}
}

// RunOrderCloser 每隔 interval 关闭一次超时订单，直到 ctx 结束；启动时先执行一次。出错只记日志，下一轮重试。
func (s *Server) RunOrderCloser(ctx context.Context, interval time.Duration) {
	for {
		if n, err := s.CloseExpiredOrders(ctx); err != nil {
			s.logger.ErrorContext(ctx, "close expired orders failed", "error", err)
		} else if n > 0 {
			s.logger.InfoContext(ctx, "expired orders closed", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
