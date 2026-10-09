// Package shop 是用户侧交易的业务层：购物车与优惠试算、优惠券、结算、订单操作和评价。
// HTTP 接口（httpapi）和导购 Agent 的工具（agent）共用这里的逻辑，保证两条路径的校验、金额计算和状态流转完全一致：
// Agent 不会比用户在页面上多做任何事，也不会少做任何校验。
//
// 方法返回的错误要么是 *Error（可直接展示给用户，带错误码和 HTTP 状态），要么是底层存储错误（调用方按内部错误处理）。
package shop

import (
	"fmt"
	"net/http"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// Error 是业务层拒绝请求的原因。Status 是对应的 HTTP 状态码，Code 是机器可读的错误码，Message 可直接展示；
// Field 指出有问题的输入字段（校验错误）。
type Error struct {
	Status  int
	Code    string
	Field   string
	Message string
}

func (e *Error) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// FieldError 是 400 invalid_argument 的字段校验错误。
func FieldError(field, message string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_argument", Field: field, Message: message}
}

// InvalidArgument 是没有具体字段的 400 invalid_argument。
func InvalidArgument(message string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_argument", Message: message}
}

const (
	// MaxLineQuantity 是购物车单行的数量上限（与库存取较小值）。
	MaxLineQuantity = 99
	// MaxCouponChoice 是一次试算或结算最多指定的券数。
	MaxCouponChoice = 20
	// DefaultPaymentTimeout 是下单后的支付期限，与上游一致。
	DefaultPaymentTimeout = 30 * time.Minute
	// TimeoutCancelReason 是支付超时自动关闭订单时记录的取消原因。
	TimeoutCancelReason = "支付超时自动关闭"

	MaxCancelReasonRunes = 200
	MaxReviewRunes       = 500
	MaxReviewTags        = 5
	MaxReviewTagRunes    = 20

	// 计价时读取的活动和券数量上限，远大于实际数据量。
	pricingPromotionLimit = 1000
	pricingCouponLimit    = 200
)

var (
	ErrProductNotFound  = &Error{Status: http.StatusNotFound, Code: "product_not_found", Message: "商品不存在或已下架"}
	ErrCartItemNotFound = &Error{Status: http.StatusNotFound, Code: "cart_item_not_found", Message: "购物车项不存在"}
	ErrCartFull         = &Error{Status: http.StatusConflict, Code: "cart_full", Message: fmt.Sprintf("购物车最多放 %d 种商品", store.MaxCartLines)}
	ErrOutOfStock       = &Error{Status: http.StatusConflict, Code: "out_of_stock", Message: "该规格已售罄"}
	ErrItemUnavailable  = &Error{Status: http.StatusConflict, Code: "item_unavailable", Message: "商品已失效，只能删除"}

	ErrCouponNotFound     = &Error{Status: http.StatusNotFound, Code: "coupon_not_found", Message: "优惠券不存在"}
	ErrCouponUnavailable  = &Error{Status: http.StatusConflict, Code: "coupon_unavailable", Message: "优惠券不在领取时间内或已停用"}
	ErrCouponSoldOut      = &Error{Status: http.StatusConflict, Code: "coupon_sold_out", Message: "优惠券已领完"}
	ErrCouponLimitReached = &Error{Status: http.StatusConflict, Code: "coupon_limit_reached", Message: "已达到这张券的领取上限"}

	ErrOrderNotFound     = &Error{Status: http.StatusNotFound, Code: "order_not_found", Message: "订单不存在"}
	ErrOrderItemNotFound = &Error{Status: http.StatusNotFound, Code: "order_item_not_found", Message: "订单里没有这件商品"}
	ErrEmptyCheckout     = &Error{Status: http.StatusBadRequest, Code: "empty_cart", Message: "请先在购物车选择要结算的商品"}
	ErrOrderExpired      = &Error{Status: http.StatusConflict, Code: "order_expired", Message: "订单已超过支付时间，已自动关闭"}
	ErrOrderNotCompleted = &Error{Status: http.StatusConflict, Code: "order_not_completed", Message: "确认收货后才能评价"}
	ErrReviewExists      = &Error{Status: http.StatusConflict, Code: "review_exists", Message: "这件商品已经评价过了"}
)

// Service 持有业务层依赖。now 用于判断促销/券是否有效和记录时间；paymentTimeout 是下单后的支付期限。
type Service struct {
	store          store.Store
	now            func() time.Time
	paymentTimeout time.Duration
}

// New 创建业务层；now 为 nil 表示 time.Now，paymentTimeout 为 0 表示 DefaultPaymentTimeout。
func New(st store.Store, now func() time.Time, paymentTimeout time.Duration) *Service {
	if now == nil {
		now = time.Now
	}
	if paymentTimeout <= 0 {
		paymentTimeout = DefaultPaymentTimeout
	}
	return &Service{store: st, now: now, paymentTimeout: paymentTimeout}
}

// Now 返回业务层当前时间。
func (s *Service) Now() time.Time { return s.now() }

// PaymentTimeout 返回下单后的支付期限。
func (s *Service) PaymentTimeout() time.Duration { return s.paymentTimeout }

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
