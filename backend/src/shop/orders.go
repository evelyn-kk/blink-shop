package shop

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

var (
	// IdempotencyKeyPattern 是结算幂等键的格式。
	IdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	// PaymentMethods 是模拟支付接受的方式；不传时用余额支付。
	PaymentMethods = map[string]bool{"mock_balance": true, "mock_wechat": true, "mock_alipay": true}
	// DefaultPaymentMethod 是不指定时的支付方式。
	DefaultPaymentMethod = "mock_balance"
)

// OrderStatusText 是订单状态的中文说明。
var OrderStatusText = map[domain.OrderStatus]string{
	domain.OrderPendingPayment: "待支付", domain.OrderPaid: "待发货", domain.OrderShipped: "已发货",
	domain.OrderCompleted: "已完成", domain.OrderCancelled: "已取消",
}

// StatusConflict 说明订单当前状态不允许该操作。
func StatusConflict(status domain.OrderStatus, action string) *Error {
	return &Error{Status: http.StatusConflict, Code: "order_status_conflict",
		Message: fmt.Sprintf("订单%s，不能%s", OrderStatusText[status], action)}
}

// ValidateOrderStatus 校验状态筛选：空表示全部，否则必须是已知的订单状态。
func ValidateOrderStatus(status string) (domain.OrderStatus, error) {
	st := domain.OrderStatus(strings.TrimSpace(status))
	if _, ok := OrderStatusText[st]; st != "" && !ok {
		return "", FieldError("status", "状态只能是 pending_payment、paid、shipped、completed 或 cancelled")
	}
	return st, nil
}

// ---------- 视图 ----------

type OrderItem struct {
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

type Payment struct {
	PaymentID     string               `json:"payment_id"`
	Amount        domain.Money         `json:"amount"`
	Status        domain.PaymentStatus `json:"status"`
	Method        string               `json:"method"`
	TransactionNo string               `json:"transaction_no"`
	ExpiresAt     time.Time            `json:"expires_at"`
	PaidAt        *time.Time           `json:"paid_at"`
}

type Order struct {
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
	Items             []OrderItem        `json:"items"`
	// Payment 只在订单详情和订单操作的响应中出现。
	Payment   *Payment  `json:"payment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ToOrder 把存储层的订单详情转成对外视图。
func ToOrder(d store.OrderDetail) Order {
	v := Order{OrderID: d.OrderID, OrderNo: d.OrderNo, AccountID: d.AccountID, MerchantID: d.MerchantID, MerchantName: d.MerchantName,
		Status: d.Status, TotalAmount: d.TotalAmount, DiscountAmount: d.DiscountAmount, PayAmount: d.PayAmount, PaymentDeadlineAt: d.PaymentDeadlineAt,
		PaidAt: d.PaidAt, ShippedAt: d.ShippedAt, CompletedAt: d.CompletedAt, ClosedAt: d.ClosedAt, CancelReason: d.CancelReason,
		Items: []OrderItem{}, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	for _, it := range d.Items {
		v.Items = append(v.Items, OrderItem{OrderItemID: it.OrderItemID, ProductID: it.ProductID, SkuID: it.SkuID, Name: it.Name,
			SkuName: it.SkuName, ImageURL: it.ImageURL, Price: it.Price, Quantity: it.Quantity, Amount: it.Price.Mul(it.Quantity),
			ReviewID: d.ReviewIDs[it.OrderItemID]})
	}
	if p := d.Payment; p != nil {
		v.Payment = &Payment{PaymentID: p.PaymentID, Amount: p.Amount, Status: p.Status, Method: p.Method, TransactionNo: p.TransactionNo,
			ExpiresAt: p.ExpiresAt, PaidAt: p.PaidAt}
	}
	return v
}

// ---------- 结算 ----------

// CheckoutInput 是结算输入。UserCouponIDs 为 nil 时自动选最优券（与试算一致）；非 nil（可以为空）只用指定的券。
// ExpectedPayAmount 是客户端确认页显示的实付金额；传了且与下单时重新计算的金额不同，返回 409 price_changed，不下单。
type CheckoutInput struct {
	IdempotencyKey    string
	UserCouponIDs     []string
	ExpectedPayAmount *domain.Money
}

// CheckoutResult 是结算结果。Replayed 为 true 表示同一幂等键已经成功结算过，Orders 是那次的订单（当前状态）。
type CheckoutResult struct {
	RequestID string
	Replayed  bool
	Orders    []Order
}

// Checkout 把购物车中已选中的商品按店铺拆单下单。同一账户同一个幂等键成功下单后，再次请求直接返回那次的订单，不会重复下单；
// 失败的请求不占用这个键。价格、库存、优惠都在事务内按最新数据重新计算，任何一项不满足都整体失败，不创建订单、不扣库存、不清购物车。
func (s *Service) Checkout(ctx context.Context, accountID string, in CheckoutInput) (CheckoutResult, error) {
	if !IdempotencyKeyPattern.MatchString(in.IdempotencyKey) {
		return CheckoutResult{}, FieldError("idempotency_key", "请提供幂等键（1–128 位字母、数字或 . _ : -）")
	}
	choice, err := ParseCouponChoice(in.UserCouponIDs)
	if err != nil {
		return CheckoutResult{}, err
	}
	res, err := s.store.Checkout(ctx, accountID, in.IdempotencyKey, func(ctx context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
		// 已拿到账户、购物车、库存和券的锁之后，在同一事务里读取当前时间、分类和有效活动：排队等锁再久，
		// 计价也按此刻生效的规则，支付期限也从订单真正创建时开始算。
		pc, err := s.LoadPricingContext(ctx)
		if err != nil {
			return store.CheckoutPlan{}, err
		}
		return CheckoutPlan(pc, st, choice, in.ExpectedPayAmount, pc.Now.Add(s.paymentTimeout))
	})
	var ce *pricing.CouponError
	if errors.As(err, &ce) {
		return CheckoutResult{}, CouponNotApplicable(ce)
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	out := CheckoutResult{RequestID: res.RequestID, Replayed: res.Replayed, Orders: []Order{}}
	for _, o := range res.Orders {
		out.Orders = append(out.Orders, ToOrder(o))
	}
	return out, nil
}

// CheckoutPlan 根据事务内加锁读到的购物车和券、事务内读取的计价数据（pc）计价并拆单。与试算用同一个 PricingContext.Price。
func CheckoutPlan(pc PricingContext, st store.CheckoutState, choice []string, expected *domain.Money, deadline time.Time) (store.CheckoutPlan, error) {
	if len(st.Lines) == 0 {
		return store.CheckoutPlan{}, ErrEmptyCheckout
	}
	// 券的过期按事务内的当前时间判断。
	coupons := make([]store.OwnedCoupon, len(st.Coupons))
	for i, c := range st.Coupons {
		c.Status = store.EffectiveCouponStatus(c.Status, c.Coupon, pc.Now)
		coupons[i] = c
	}
	// 已选中的项必须全部可以购买：不悄悄跳过失效商品（上游会跳过并照样清掉它们）。
	for _, l := range st.Lines {
		if reason := UnavailableReason(l); reason != "" {
			name := l.ProductName
			if name == "" {
				name = "已删除的商品"
			}
			return store.CheckoutPlan{}, &Error{Status: http.StatusConflict, Code: "item_unavailable",
				Message: fmt.Sprintf("「%s」%s，请先在购物车里处理", name, reason)}
		}
	}
	priced, err := pc.Price(st.Lines, coupons, choice)
	if err != nil {
		return store.CheckoutPlan{}, err
	}
	res := priced.Result
	if expected != nil && *expected != res.PayAmount {
		return store.CheckoutPlan{}, &Error{Status: http.StatusConflict, Code: "price_changed",
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

// ---------- 订单查询 ----------

// ListOrders 按条件列出订单；调用方负责在 q 中限定账户或店铺。
func (s *Service) ListOrders(ctx context.Context, q store.OrderQuery) ([]Order, int, error) {
	items, total, err := s.store.ListOrders(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Order, 0, len(items))
	for _, d := range items {
		out = append(out, ToOrder(d))
	}
	return out, total, nil
}

// GetOrder 读取订单详情；visible 为 false（不属于当前账户/商家）时按不存在处理，不暴露订单是否存在。
func (s *Service) GetOrder(ctx context.Context, orderID string, visible func(o domain.Order) bool) (Order, error) {
	d, err := s.store.GetOrder(ctx, orderID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !visible(d.Order)) {
		return Order{}, ErrOrderNotFound
	}
	if err != nil {
		return Order{}, err
	}
	return ToOrder(d), nil
}

// GetMyOrder 读取本人的订单详情。
func (s *Service) GetMyOrder(ctx context.Context, accountID, orderID string) (Order, error) {
	return s.GetOrder(ctx, orderID, func(o domain.Order) bool { return o.AccountID == accountID })
}

// UpdateOrder 调用 Store.UpdateOrder 并统一处理错误：fn 返回的 *Error 原样返回，订单不存在返回 ErrOrderNotFound。
func (s *Service) UpdateOrder(ctx context.Context, orderID string, fn func(o *domain.Order, p *domain.Payment) error) (Order, error) {
	d, err := s.store.UpdateOrder(ctx, orderID, fn)
	if errors.Is(err, store.ErrNotFound) {
		return Order{}, ErrOrderNotFound
	}
	if err != nil {
		return Order{}, err
	}
	return ToOrder(d), nil
}

// ---------- 订单操作 ----------

// PayOrder 模拟支付：只能支付自己待支付且未超时的订单。已超时的订单在这里顺带关闭（回补库存、退券）并返回 ErrOrderExpired。
// method 为空表示 DefaultPaymentMethod。
func (s *Service) PayOrder(ctx context.Context, accountID, orderID, method string) (Order, error) {
	method = strings.TrimSpace(method)
	if method == "" {
		method = DefaultPaymentMethod
	}
	if !PaymentMethods[method] {
		return Order{}, FieldError("method", "支付方式只能是 mock_balance、mock_wechat 或 mock_alipay")
	}
	now := s.now().UTC()
	expired := false
	d, err := s.UpdateOrder(ctx, orderID, func(o *domain.Order, p *domain.Payment) error {
		switch {
		case o.AccountID != accountID:
			return ErrOrderNotFound
		case o.Status != domain.OrderPendingPayment:
			return StatusConflict(o.Status, "支付")
		case p == nil || p.Status != domain.PaymentPending:
			return StatusConflict(o.Status, "支付")
		case o.PaymentDeadlineAt != nil && !now.Before(*o.PaymentDeadlineAt):
			expired = true
			CloseOrder(o, p, now, TimeoutCancelReason)
			return nil
		}
		o.Status, o.PaidAt = domain.OrderPaid, &now
		p.Status, p.PaidAt, p.Method, p.TransactionNo = domain.PaymentPaid, &now, method, NewTransactionNo()
		return nil
	})
	if err != nil {
		return Order{}, err
	}
	if expired {
		return d, ErrOrderExpired
	}
	return d, nil
}

// NewTransactionNo 生成模拟支付的交易号。
func NewTransactionNo() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "MOCK" + strings.ToUpper(hex.EncodeToString(b[:]))
}

// CloseOrder 把待支付订单改为已取消并关闭支付单；库存回补和退券由 Store 在同一事务内完成。
func CloseOrder(o *domain.Order, p *domain.Payment, now time.Time, reason string) {
	o.Status, o.ClosedAt, o.CancelReason = domain.OrderCancelled, &now, reason
	if p != nil && p.Status == domain.PaymentPending {
		p.Status = domain.PaymentClosed
	}
}

// Ship 把已支付订单改为已发货。
func Ship(o *domain.Order, now time.Time) error {
	if o.Status != domain.OrderPaid {
		return StatusConflict(o.Status, "发货")
	}
	o.Status, o.ShippedAt = domain.OrderShipped, &now
	return nil
}

// ValidateCancelReason 校验取消原因长度；为空时返回 fallback。
func ValidateCancelReason(reason, fallback string) (string, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxCancelReasonRunes {
		return "", FieldError("reason", fmt.Sprintf("取消原因最多 %d 个字", MaxCancelReasonRunes))
	}
	if reason == "" {
		reason = fallback
	}
	return reason, nil
}

// CancelOrder 用户取消自己的待支付订单（回补库存、退券）。reason 为空时记为“用户取消”。
func (s *Service) CancelOrder(ctx context.Context, accountID, orderID, reason string) (Order, error) {
	reason, err := ValidateCancelReason(reason, "用户取消")
	if err != nil {
		return Order{}, err
	}
	now := s.now().UTC()
	return s.UpdateOrder(ctx, orderID, func(o *domain.Order, p *domain.Payment) error {
		switch {
		case o.AccountID != accountID:
			return ErrOrderNotFound
		case o.Status != domain.OrderPendingPayment:
			return StatusConflict(o.Status, "取消")
		}
		CloseOrder(o, p, now, reason)
		return nil
	})
}

// ConfirmReceipt 用户确认收货：只能对自己已发货的订单操作。
func (s *Service) ConfirmReceipt(ctx context.Context, accountID, orderID string) (Order, error) {
	now := s.now().UTC()
	return s.UpdateOrder(ctx, orderID, func(o *domain.Order, _ *domain.Payment) error {
		switch {
		case o.AccountID != accountID:
			return ErrOrderNotFound
		case o.Status != domain.OrderShipped:
			return StatusConflict(o.Status, "确认收货")
		}
		o.Status, o.CompletedAt = domain.OrderCompleted, &now
		return nil
	})
}

// ---------- 评价 ----------

// ReviewInput 是发布评价的输入。
type ReviewInput struct {
	Rating  *int
	Content string
	Tags    []string
}

// Review 是用户看到的自己的评价。
type Review struct {
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

// CreateReview 评价自己已完成订单里的商品，每件只能评价一次。
func (s *Service) CreateReview(ctx context.Context, accountID, orderID, itemID string, in ReviewInput) (Review, error) {
	if in.Rating == nil || *in.Rating < 1 || *in.Rating > 5 {
		return Review{}, FieldError("rating", "评分必须是 1 到 5 的整数")
	}
	content := strings.TrimSpace(in.Content)
	if n := utf8.RuneCountInString(content); n == 0 || n > MaxReviewRunes {
		return Review{}, FieldError("content", fmt.Sprintf("评价内容需要 1 到 %d 个字", MaxReviewRunes))
	}
	tags := []string{}
	for _, t := range in.Tags {
		t = strings.TrimSpace(t)
		if t == "" || contains(tags, t) {
			continue
		}
		if utf8.RuneCountInString(t) > MaxReviewTagRunes {
			return Review{}, FieldError("tags", fmt.Sprintf("每个标签最多 %d 个字", MaxReviewTagRunes))
		}
		tags = append(tags, t)
	}
	if len(tags) > MaxReviewTags {
		return Review{}, FieldError("tags", fmt.Sprintf("最多 %d 个标签", MaxReviewTags))
	}
	review, err := s.store.CreateReview(ctx, accountID, orderID, itemID, func(o domain.Order, _ domain.OrderItem) (domain.ProductReview, error) {
		if o.Status != domain.OrderCompleted {
			return domain.ProductReview{}, ErrOrderNotCompleted
		}
		return domain.ProductReview{Rating: *in.Rating, Content: content, Tags: tags, Status: domain.ReviewVisible}, nil
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return Review{}, ErrOrderNotFound
	case errors.Is(err, store.ErrOrderItemNotFound):
		return Review{}, ErrOrderItemNotFound
	case errors.Is(err, store.ErrConflict):
		return Review{}, ErrReviewExists
	case err != nil:
		return Review{}, err
	}
	return Review{ReviewID: review.ReviewID, OrderID: review.OrderID, OrderItemID: review.OrderItemID, ProductID: review.ProductID,
		SkuID: review.SkuID, Rating: review.Rating, Content: review.Content, Tags: review.Tags, Status: review.Status, CreatedAt: review.CreatedAt}, nil
}
