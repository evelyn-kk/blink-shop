package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// 结算、支付、取消、收货和评价的业务规则在 shop 包（与导购 Agent 的工具共用）；这里只做 HTTP 输入输出和审计。

const (
	// DefaultPaymentTimeout 是下单后的支付期限，与上游一致。
	DefaultPaymentTimeout = shop.DefaultPaymentTimeout
	// expiredOrderBatch 是关闭超时订单时每批处理的订单数。
	expiredOrderBatch   = 100
	timeoutCancelReason = shop.TimeoutCancelReason
)

var ErrOrderNotFound = &APIError{Status: http.StatusNotFound, Code: "order_not_found", Message: "订单不存在"}

// 测试和其他处理器沿用的视图名。
type (
	orderView    = shop.Order
	paymentView  = shop.Payment
	myReviewView = shop.Review
)

// listOrders 输出订单列表；q 已限定账户或店铺。
func (s *Server) listOrders(w http.ResponseWriter, r *http.Request, q store.OrderQuery) {
	status, err := shop.ValidateOrderStatus(r.URL.Query().Get("status"))
	if err != nil {
		s.shopFailed(w, r, "list orders", err)
		return
	}
	q.Status, q.OrderNo, q.Page = status, strings.TrimSpace(r.URL.Query().Get("order_no")), readPage(r)
	items, total, err := s.shop().ListOrders(r.Context(), q)
	if err != nil {
		s.shopFailed(w, r, "list orders", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, func(o shop.Order) shop.Order { return o }))
}

// getOrder 读取订单详情；visible 为 false（不属于当前账户/商家）时按不存在处理，不暴露订单是否存在。
func (s *Server) getOrder(w http.ResponseWriter, r *http.Request, visible func(o domain.Order) bool) {
	d, err := s.shop().GetOrder(r.Context(), r.PathValue("id"), visible)
	if err != nil {
		s.shopFailed(w, r, "get order", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// updateOrder 调用业务层并统一输出错误。
func (s *Server) updateOrder(w http.ResponseWriter, r *http.Request, orderID string, fn func(o *domain.Order, p *domain.Payment) error) (shop.Order, bool) {
	d, err := s.shop().UpdateOrder(r.Context(), orderID, fn)
	if err != nil {
		s.shopFailed(w, r, "update order", err)
		return shop.Order{}, false
	}
	return d, true
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
	CheckoutRequestID string       `json:"checkout_request_id"`
	Replayed          bool         `json:"replayed"`
	Items             []shop.Order `json:"items"`
}

// handleCheckout 把购物车中已选中的商品按店铺拆单下单。幂等键放在 Idempotency-Key 请求头或 idempotency_key 字段（必填）：
// 同一账户同一个键成功下单后，再次请求直接返回那次的订单（200，replayed=true），不会重复下单；失败的请求不占用这个键。
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
	var choice []string
	if in.UserCouponIDs != nil {
		choice = *in.UserCouponIDs
		if choice == nil {
			choice = []string{}
		}
	}
	res, err := s.shop().Checkout(r.Context(), acc.AccountID, shop.CheckoutInput{IdempotencyKey: key, UserCouponIDs: choice, ExpectedPayAmount: in.ExpectedPayAmount})
	if err != nil {
		s.shopFailed(w, r, "checkout", err)
		return
	}
	out := checkoutResponse{CheckoutRequestID: res.RequestID, Replayed: res.Replayed, Items: res.Orders}
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	} else {
		ids := make([]string, 0, len(res.Orders))
		for _, o := range res.Orders {
			ids = append(ids, o.OrderID)
		}
		s.audit(r, "order.checkout", acc.AccountID, "checkout_request_id", res.RequestID, "order_ids", strings.Join(ids, ","))
	}
	writeJSON(w, status, out)
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
	Order   shop.Order   `json:"order"`
	Payment shop.Payment `json:"payment"`
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

// payOrder 模拟支付（规则见 shop.Service.PayOrder）。
func (s *Server) payOrder(w http.ResponseWriter, r *http.Request, accountID, orderID string) {
	var in payRequest
	if err := decodeOptionalJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	d, err := s.shop().PayOrder(r.Context(), accountID, orderID, in.Method)
	if errors.Is(err, shop.ErrOrderExpired) {
		s.audit(r, "order.closed", accountID, "order_id", orderID, "reason", "payment_timeout")
		writeError(w, shopError(err))
		return
	}
	if err != nil {
		s.shopFailed(w, r, "pay order", err)
		return
	}
	s.audit(r, "order.paid", accountID, "order_id", orderID, "payment_id", d.Payment.PaymentID, "method", d.Payment.Method, "amount", d.Payment.Amount.String())
	writeJSON(w, http.StatusOK, payResponse{Order: d, Payment: *d.Payment})
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request, accountID, orderID string) {
	var in cancelRequest
	if err := decodeOptionalJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	d, err := s.shop().CancelOrder(r.Context(), accountID, orderID, in.Reason)
	if err != nil {
		s.shopFailed(w, r, "cancel order", err)
		return
	}
	s.audit(r, "order.cancelled", accountID, "order_id", orderID, "by", "user")
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) confirmReceipt(w http.ResponseWriter, r *http.Request, accountID, orderID string) {
	d, err := s.shop().ConfirmReceipt(r.Context(), accountID, orderID)
	if err != nil {
		s.shopFailed(w, r, "confirm receipt", err)
		return
	}
	s.audit(r, "order.completed", accountID, "order_id", orderID)
	writeJSON(w, http.StatusOK, d)
}

// ---------- 用户：评价 ----------

type reviewRequest struct {
	Rating  *int     `json:"rating"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
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
	review, err := s.shop().CreateReview(r.Context(), acc.AccountID, orderID, itemID, shop.ReviewInput{Rating: in.Rating, Content: in.Content, Tags: in.Tags})
	if err != nil {
		s.shopFailed(w, r, "create review", err)
		return
	}
	s.audit(r, "review.created", acc.AccountID, "review_id", review.ReviewID, "order_id", orderID, "order_item_id", itemID, "rating", review.Rating)
	writeJSON(w, http.StatusCreated, review)
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
			return shop.ErrOrderNotFound
		}
		return shop.Ship(o, s.now().UTC())
	})
	if !ok {
		return
	}
	s.audit(r, "order.shipped", acc.AccountID, "order_id", id, "merchant_id", acc.MerchantID)
	writeJSON(w, http.StatusOK, d)
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
	reason, err := shop.ValidateCancelReason(in.Reason, "平台取消")
	if err != nil {
		s.shopFailed(w, r, "update order", err)
		return
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
			return shop.Ship(o, now)
		}
		if o.Status != domain.OrderPendingPayment {
			return shop.StatusConflict(o.Status, "取消")
		}
		shop.CloseOrder(o, p, now, reason)
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
	writeJSON(w, http.StatusOK, d)
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
				shop.CloseOrder(o, p, now, timeoutCancelReason)
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
