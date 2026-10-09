package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/shop"
)

// 购物车、试算和优惠券的业务规则在 shop 包（与导购 Agent 的工具共用）；这里只做 HTTP 输入输出和审计。

// 测试和其他处理器沿用的视图名。
type (
	cartItemView        = shop.CartItem
	cartResponse        = shop.Cart
	discountPreview     = shop.DiscountPreview
	userCouponView      = shop.UserCoupon
	availableCouponView = shop.AvailableCoupon
)

// shopError 把业务层错误转成接口错误；不是业务错误时返回 nil（调用方按内部错误处理）。
func shopError(err error) *APIError {
	var se *shop.Error
	if errors.As(err, &se) {
		return &APIError{Status: se.Status, Code: se.Code, Field: se.Field, Message: se.Message}
	}
	return nil
}

// shopFailed 输出业务层错误：业务错误原样返回，其他按内部错误处理。
func (s *Server) shopFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	if apiErr := shopError(err); apiErr != nil {
		writeError(w, apiErr)
		return
	}
	s.storeFailed(w, r, what, err)
}

func (s *Server) writeCart(w http.ResponseWriter, r *http.Request, accountID string) {
	cart, err := s.shop().Cart(r.Context(), accountID)
	if err != nil {
		s.shopFailed(w, r, "load cart", err)
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

func (s *Server) handleGetCart(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	s.writeCart(w, r, acc.AccountID)
}

// handleDiscountPreview 试算：不传 user_coupon_ids 时自动选最优券；传了（可以为空值）只使用指定的券。
func (s *Server) handleDiscountPreview(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var choice []string
	if r.URL.Query().Has("user_coupon_ids") {
		var err error
		if choice, err = shop.ParseCouponChoice(strings.Split(r.URL.Query().Get("user_coupon_ids"), ",")); err != nil {
			s.shopFailed(w, r, "preview discount", err)
			return
		}
	}
	preview, err := s.shop().PreviewDiscount(r.Context(), acc.AccountID, choice)
	if err != nil {
		s.shopFailed(w, r, "preview discount", err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

type addCartItemRequest struct {
	ProductID string `json:"product_id"`
	SkuID     string `json:"sku_id"`
	Quantity  *int   `json:"quantity"`
}

// handleAddCartItem 加购（规则见 shop.Service.AddCartItem），返回整个购物车（与上游一致）。
func (s *Server) handleAddCartItem(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in addCartItemRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	item, added, err := s.shop().AddCartItem(r.Context(), acc.AccountID, shop.AddCartItemInput{ProductID: in.ProductID, SkuID: in.SkuID, Quantity: in.Quantity})
	if err != nil {
		s.shopFailed(w, r, "add cart item", err)
		return
	}
	s.audit(r, "cart.item_added", acc.AccountID, "cart_item_id", item.CartItemID, "product_id", item.ProductID, "sku_id", item.SkuID,
		"added", added, "quantity", item.Quantity)
	s.writeCart(w, r, acc.AccountID)
}

type updateCartItemRequest struct {
	Quantity *int  `json:"quantity"`
	Selected *bool `json:"selected"`
}

// handleUpdateCartItem 修改数量或选中状态（规则见 shop.Service.UpdateCartItem）。
func (s *Server) handleUpdateCartItem(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in updateCartItemRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	updated, err := s.shop().UpdateCartItem(r.Context(), acc.AccountID, id, shop.UpdateCartItemInput{Quantity: in.Quantity, Selected: in.Selected})
	if err != nil {
		s.shopFailed(w, r, "update cart item", err)
		return
	}
	s.audit(r, "cart.item_updated", acc.AccountID, "cart_item_id", id, "quantity", updated.Quantity, "selected", updated.Selected)
	s.writeCart(w, r, acc.AccountID)
}

func (s *Server) handleDeleteCartItem(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id := r.PathValue("id")
	if err := s.shop().DeleteCartItem(r.Context(), acc.AccountID, id); err != nil {
		s.shopFailed(w, r, "delete cart item", err)
		return
	}
	s.audit(r, "cart.item_removed", acc.AccountID, "cart_item_id", id)
	s.writeCart(w, r, acc.AccountID)
}

// ---------- 优惠券 ----------

// handleListAvailableCoupons 当前可领的券（含已领完的，can_claim=false），并标出本人已领张数。
func (s *Server) handleListAvailableCoupons(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	page := readPage(r)
	items, total, err := s.shop().AvailableCoupons(r.Context(), acc.AccountID, page)
	if err != nil {
		s.shopFailed(w, r, "list coupons", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, page, total, func(c shop.AvailableCoupon) shop.AvailableCoupon { return c }))
}

func (s *Server) handleListMyCoupons(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	page := readPage(r)
	items, total, err := s.shop().MyCoupons(r.Context(), acc.AccountID, strings.TrimSpace(r.URL.Query().Get("status")), page)
	if err != nil {
		s.shopFailed(w, r, "list my coupons", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, page, total, func(c shop.UserCoupon) shop.UserCoupon { return c }))
}

// handleCouponAction 处理 POST /coupons/{id}:claim（路径段内带动作，与上游一致）。
func (s *Server) handleCouponAction(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id, ok := strings.CutSuffix(r.PathValue("action"), ":claim")
	if !ok || id == "" {
		writeError(w, ErrNotFound)
		return
	}
	got, err := s.shop().ClaimCoupon(r.Context(), acc.AccountID, id)
	if err != nil {
		s.shopFailed(w, r, "claim coupon", err)
		return
	}
	s.audit(r, "coupon.claimed", acc.AccountID, "coupon_id", id, "user_coupon_id", got.UserCouponID)
	writeJSON(w, http.StatusOK, got)
}
