package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/pricing"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const (
	// maxLineQuantity 是购物车单行的数量上限（与库存取较小值）。
	maxLineQuantity = 99
	// 计价时读取的活动和券数量上限，远大于实际数据量。
	pricingPromotionLimit = 1000
	pricingCouponLimit    = 200
)

var (
	ErrCartItemNotFound = &APIError{Status: http.StatusNotFound, Code: "cart_item_not_found", Message: "购物车项不存在"}
	ErrCartFull         = &APIError{Status: http.StatusConflict, Code: "cart_full", Message: fmt.Sprintf("购物车最多放 %d 种商品", store.MaxCartLines)}
	ErrOutOfStock       = &APIError{Status: http.StatusConflict, Code: "out_of_stock", Message: "该规格已售罄"}
	ErrItemUnavailable  = &APIError{Status: http.StatusConflict, Code: "item_unavailable", Message: "商品已失效，只能删除"}
)

func insufficientStock(stock, current int) *APIError {
	if current > 0 {
		return &APIError{Status: http.StatusConflict, Code: "insufficient_stock",
			Message: fmt.Sprintf("库存不足：库存 %d 件，购物车里已有 %d 件", stock, current)}
	}
	return &APIError{Status: http.StatusConflict, Code: "insufficient_stock", Message: fmt.Sprintf("库存不足，最多可买 %d 件", stock)}
}

func quantityLimit() *APIError {
	return &APIError{Status: http.StatusConflict, Code: "quantity_limit", Message: fmt.Sprintf("单个商品最多买 %d 件", maxLineQuantity)}
}

type cartItemView struct {
	CartItemID        string       `json:"cart_item_id"`
	ProductID         string       `json:"product_id"`
	SkuID             string       `json:"sku_id"`
	ProductName       string       `json:"product_name"`
	SkuName           string       `json:"sku_name"`
	ImageURL          string       `json:"image_url"`
	MerchantID        string       `json:"merchant_id"`
	MerchantName      string       `json:"merchant_name"`
	UnitPrice         domain.Money `json:"unit_price"`
	Quantity          int          `json:"quantity"`
	Selected          bool         `json:"selected"`
	StockQuantity     int          `json:"stock_quantity"`
	Available         bool         `json:"available"`
	UnavailableReason string       `json:"unavailable_reason"`
	Amount            domain.Money `json:"amount"`
	DiscountAmount    domain.Money `json:"discount_amount"`
	PayAmount         domain.Money `json:"pay_amount"`
}

type cartSummary struct {
	ItemCount      int          `json:"item_count"`
	SelectedCount  int          `json:"selected_count"`
	TotalAmount    domain.Money `json:"total_amount"`
	DiscountAmount domain.Money `json:"discount_amount"`
	PayAmount      domain.Money `json:"pay_amount"`
}

type cartResponse struct {
	Items   []cartItemView `json:"items"`
	Summary cartSummary    `json:"summary"`
}

type discountLineView struct {
	Type        string       `json:"type"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Scope       string       `json:"scope"`
	MerchantID  string       `json:"merchant_id"`
	Amount      domain.Money `json:"amount"`
	Description string       `json:"description"`
	CartItemIDs []string     `json:"cart_item_ids"`
}

type merchantAmountView struct {
	MerchantID     string       `json:"merchant_id"`
	MerchantName   string       `json:"merchant_name"`
	TotalAmount    domain.Money `json:"total_amount"`
	DiscountAmount domain.Money `json:"discount_amount"`
	PayAmount      domain.Money `json:"pay_amount"`
}

type itemAmountView struct {
	CartItemID     string       `json:"cart_item_id"`
	Amount         domain.Money `json:"amount"`
	DiscountAmount domain.Money `json:"discount_amount"`
	PayAmount      domain.Money `json:"pay_amount"`
}

type hintView struct {
	PromotionID string       `json:"promotion_id"`
	Name        string       `json:"name"`
	Shortfall   domain.Money `json:"shortfall"`
}

type discountPreview struct {
	TotalAmount    domain.Money         `json:"total_amount"`
	DiscountAmount domain.Money         `json:"discount_amount"`
	PayAmount      domain.Money         `json:"pay_amount"`
	Lines          []discountLineView   `json:"lines"`
	Merchants      []merchantAmountView `json:"merchants"`
	Items          []itemAmountView     `json:"items"`
	Hints          []hintView           `json:"hints"`
	UserCouponIDs  []string             `json:"user_coupon_ids"`
}

// pricedCart 是一次计价的完整结果：购物车每一项（含不参与计价的）及优惠计算结果。
type pricedCart struct {
	lines  []store.CartLine
	views  []cartItemView
	result pricing.Result
}

// productReason 判断购物车项对应的商品和规格当前能否购买（不看数量）；返回空串表示可以。
func productReason(l store.CartLine) string {
	switch {
	case l.ProductName == "" || l.ProductStatus == domain.ProductDeleted:
		return "商品已删除"
	case l.ProductStatus != domain.ProductActive || l.MerchantStatus != domain.StatusActive:
		return "商品已下架"
	case !l.SkuFound:
		return "规格已失效"
	case l.StockQuantity <= 0:
		return "已售罄"
	}
	return ""
}

// checkPurchasable 判断购物车项按 quantity 件能否购买，不能时返回对应的接口错误。
func checkPurchasable(l store.CartLine, quantity int) error {
	switch reason := productReason(l); {
	case reason == "已售罄":
		return ErrOutOfStock
	case reason != "":
		return ErrItemUnavailable
	case quantity > l.StockQuantity:
		return insufficientStock(l.StockQuantity, 0)
	}
	return nil
}

// unavailableReason 在 productReason 之外再检查数量是否超过当前库存。
func unavailableReason(l store.CartLine) string {
	if reason := productReason(l); reason != "" {
		return reason
	}
	if l.Quantity > l.StockQuantity {
		return fmt.Sprintf("库存不足，仅剩 %d 件", l.StockQuantity)
	}
	return ""
}

// priceCart 读取购物车并计价：只有选中且可购买的项参与计算。couponChoice 为 nil 时自动选券。
func (s *Server) priceCart(ctx context.Context, accountID string, couponChoice []string) (pricedCart, error) {
	now := s.now()
	lines, err := s.store.ListCartLines(ctx, accountID)
	if err != nil {
		return pricedCart{}, err
	}
	cats, err := s.store.ListCategories(ctx)
	if err != nil {
		return pricedCart{}, err
	}
	parent := map[string]string{}
	for _, c := range cats {
		parent[c.CategoryID] = c.ParentID
	}
	promos, _, err := s.store.ListActivePromotions(ctx, store.PromotionQuery{At: now, Page: store.Page{Page: 1, PageSize: pricingPromotionLimit}})
	if err != nil {
		return pricedCart{}, err
	}
	owned, _, err := s.store.ListUserCoupons(ctx, store.UserCouponQuery{AccountID: accountID, Status: domain.UserCouponUnused, At: now,
		Page: store.Page{Page: 1, PageSize: pricingCouponLimit}})
	if err != nil {
		return pricedCart{}, err
	}

	in := pricing.Input{Promotions: promos, CouponChoice: couponChoice, Now: now}
	for _, oc := range owned {
		in.Coupons = append(in.Coupons, pricing.OwnedCoupon{UserCouponID: oc.UserCouponID, Status: oc.Status, Coupon: oc.Coupon})
	}
	views := make([]cartItemView, len(lines))
	for i, l := range lines {
		reason := unavailableReason(l)
		amount := l.UnitPrice.Mul(l.Quantity)
		views[i] = cartItemView{
			CartItemID: l.CartItemID, ProductID: l.ProductID, SkuID: l.SkuID, ProductName: l.ProductName, SkuName: l.SkuName,
			ImageURL: l.ImageURL, MerchantID: l.MerchantID, MerchantName: l.MerchantName, UnitPrice: l.UnitPrice, Quantity: l.Quantity,
			// 不可购买的项不显示为选中（与“不能选中”的规则一致）；保存的选中状态不变，商品恢复后仍是原来的选择。
			Selected: l.Selected && reason == "", StockQuantity: l.StockQuantity, Available: reason == "", UnavailableReason: reason,
			Amount: amount, PayAmount: amount,
		}
		if l.Selected && reason == "" {
			var ancestors []string
			for c := l.CategoryID; c != "" && !contains(ancestors, c); c = parent[c] {
				ancestors = append(ancestors, c)
			}
			in.Lines = append(in.Lines, pricing.Line{CartItemID: l.CartItemID, ProductID: l.ProductID, MerchantID: l.MerchantID,
				CategoryIDs: ancestors, UnitPrice: l.UnitPrice, Quantity: l.Quantity})
		}
	}
	res, err := pricing.Compute(in)
	if err != nil {
		return pricedCart{}, err
	}
	byID := map[string]pricing.ItemResult{}
	for _, it := range res.Items {
		byID[it.CartItemID] = it
	}
	for i := range views {
		if it, ok := byID[views[i].CartItemID]; ok {
			views[i].DiscountAmount, views[i].PayAmount = it.Discount, it.PayAmount
		}
	}
	return pricedCart{lines: lines, views: views, result: res}, nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func (p pricedCart) response() cartResponse {
	out := cartResponse{Items: p.views, Summary: cartSummary{
		ItemCount: len(p.views), TotalAmount: p.result.TotalAmount, DiscountAmount: p.result.DiscountAmount, PayAmount: p.result.PayAmount,
	}}
	for _, it := range p.result.Items {
		for _, v := range p.views {
			if v.CartItemID == it.CartItemID {
				out.Summary.SelectedCount += v.Quantity
			}
		}
	}
	return out
}

func (s *Server) writeCart(w http.ResponseWriter, r *http.Request, accountID string) {
	priced, err := s.priceCart(r.Context(), accountID, nil)
	if err != nil {
		s.storeFailed(w, r, "load cart", err)
		return
	}
	writeJSON(w, http.StatusOK, priced.response())
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
		choice = []string{}
		for _, id := range strings.Split(r.URL.Query().Get("user_coupon_ids"), ",") {
			if id = strings.TrimSpace(id); id != "" && !contains(choice, id) {
				choice = append(choice, id)
			}
		}
		if len(choice) > 20 {
			writeError(w, fieldError("user_coupon_ids", "一次最多指定 20 张券"))
			return
		}
	}
	priced, err := s.priceCart(r.Context(), acc.AccountID, choice)
	var ce *pricing.CouponError
	if errors.As(err, &ce) {
		writeError(w, &APIError{Status: http.StatusBadRequest, Code: "coupon_not_applicable", Field: "user_coupon_ids",
			Message: "优惠券不能使用：" + ce.Reason})
		return
	}
	if err != nil {
		s.storeFailed(w, r, "preview discount", err)
		return
	}
	writeJSON(w, http.StatusOK, s.previewView(priced))
}

func (s *Server) previewView(p pricedCart) discountPreview {
	res := p.result
	out := discountPreview{TotalAmount: res.TotalAmount, DiscountAmount: res.DiscountAmount, PayAmount: res.PayAmount,
		Lines: []discountLineView{}, Merchants: []merchantAmountView{}, Items: []itemAmountView{}, Hints: []hintView{}, UserCouponIDs: []string{}}
	names := map[string]string{}
	for _, l := range p.lines {
		names[l.MerchantID] = l.MerchantName
	}
	for _, l := range res.Lines {
		out.Lines = append(out.Lines, discountLineView{Type: l.Type, ID: l.ID, Name: l.Name, Scope: l.Scope, MerchantID: l.MerchantID,
			Amount: l.Amount, Description: l.Description, CartItemIDs: l.CartItemIDs})
	}
	for _, m := range res.Merchants {
		out.Merchants = append(out.Merchants, merchantAmountView{MerchantID: m.MerchantID, MerchantName: names[m.MerchantID],
			TotalAmount: m.TotalAmount, DiscountAmount: m.DiscountAmount, PayAmount: m.PayAmount})
	}
	for _, it := range res.Items {
		out.Items = append(out.Items, itemAmountView{CartItemID: it.CartItemID, Amount: it.Amount, DiscountAmount: it.Discount, PayAmount: it.PayAmount})
	}
	for _, h := range res.Hints {
		out.Hints = append(out.Hints, hintView{PromotionID: h.PromotionID, Name: h.Name, Shortfall: h.Shortfall})
	}
	out.UserCouponIDs = append(out.UserCouponIDs, res.UserCouponIDs...)
	return out
}

type addCartItemRequest struct {
	ProductID string `json:"product_id"`
	SkuID     string `json:"sku_id"`
	Quantity  *int   `json:"quantity"`
}

// handleAddCartItem 加购：商品必须公开可见；不传 sku_id 时用默认规格；同一规格累加数量（并重新选中），
// 累加后不能超过库存和单行上限。返回整个购物车（与上游一致）。
func (s *Server) handleAddCartItem(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in addCartItemRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	productID := strings.TrimSpace(in.ProductID)
	if productID == "" {
		writeError(w, fieldError("product_id", "请选择商品"))
		return
	}
	qty := 1
	if in.Quantity != nil {
		qty = *in.Quantity
	}
	if qty < 1 || qty > maxLineQuantity {
		writeError(w, fieldError("quantity", fmt.Sprintf("数量必须是 1 到 %d 之间的整数", maxLineQuantity)))
		return
	}
	product, err := s.store.GetVisibleProduct(r.Context(), productID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrProductNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "get product", err)
		return
	}
	sku, ok := pickSKU(product.SKUs, strings.TrimSpace(in.SkuID))
	if !ok {
		writeError(w, fieldError("sku_id", "该商品没有这个规格"))
		return
	}
	if sku.StockQuantity <= 0 {
		writeError(w, ErrOutOfStock)
		return
	}
	item, err := s.store.AddCartItem(r.Context(), acc.AccountID, product.ProductID, sku.SkuID, func(current, lines int) (int, error) {
		if current == 0 && lines >= store.MaxCartLines {
			return 0, ErrCartFull
		}
		next := current + qty
		if next > maxLineQuantity {
			return 0, quantityLimit()
		}
		if next > sku.StockQuantity {
			return 0, insufficientStock(sku.StockQuantity, current)
		}
		return next, nil
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			writeError(w, apiErr)
			return
		}
		s.storeFailed(w, r, "add cart item", err)
		return
	}
	s.audit(r, "cart.item_added", acc.AccountID, "cart_item_id", item.CartItemID, "product_id", item.ProductID, "sku_id", item.SkuID,
		"added", qty, "quantity", item.Quantity)
	s.writeCart(w, r, acc.AccountID)
}

// pickSKU 返回指定规格；不指定时返回默认规格（没有标默认时取第一个）。
func pickSKU(skus []domain.ProductSKU, skuID string) (domain.ProductSKU, bool) {
	if len(skus) == 0 {
		return domain.ProductSKU{}, false
	}
	for _, sku := range skus {
		if (skuID == "" && sku.IsDefault) || (skuID != "" && sku.SkuID == skuID) {
			return sku, true
		}
	}
	if skuID == "" {
		return skus[0], true
	}
	return domain.ProductSKU{}, false
}

type updateCartItemRequest struct {
	Quantity *int  `json:"quantity"`
	Selected *bool `json:"selected"`
}

// handleUpdateCartItem 修改数量或选中状态。数量必须是 1–99 且不超过当前库存（0 和负数不会被当成删除）。
// 不可购买的项（下架、删除、规格失效、售罄、数量超库存）不能被选中，也不能保持选中去做其他修改：
// 只能取消选中、删除，或把“库存不足”的数量改到库存以内（之后可以再选中）。
func (s *Server) handleUpdateCartItem(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in updateCartItemRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.Quantity == nil && in.Selected == nil {
		writeError(w, invalidArgument("请提供 quantity 或 selected"))
		return
	}
	if in.Quantity != nil && (*in.Quantity < 1 || *in.Quantity > maxLineQuantity) {
		writeError(w, fieldError("quantity", fmt.Sprintf("数量必须是 1 到 %d 之间的整数", maxLineQuantity)))
		return
	}
	id := r.PathValue("id")
	lines, err := s.store.ListCartLines(r.Context(), acc.AccountID)
	if err != nil {
		s.storeFailed(w, r, "load cart", err)
		return
	}
	var line *store.CartLine
	for i := range lines {
		if lines[i].CartItemID == id {
			line = &lines[i]
		}
	}
	if line == nil {
		writeError(w, ErrCartItemNotFound)
		return
	}
	updated, err := s.store.UpdateCartItem(r.Context(), acc.AccountID, id, func(it *domain.CartItem) error {
		// 在行锁内按“更新后的状态”判断，并发修改不会绕过检查。
		quantityChanged := in.Quantity != nil && *in.Quantity != it.Quantity
		if in.Quantity != nil {
			it.Quantity = *in.Quantity
		}
		if in.Selected != nil {
			it.Selected = *in.Selected
		}
		// 改数量，或更新后仍为选中：商品必须可以购买，数量不能超过库存。只取消选中（或保持未选中）总是允许。
		if quantityChanged || it.Selected {
			return checkPurchasable(*line, it.Quantity)
		}
		return nil
	})
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		writeError(w, apiErr)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrCartItemNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "update cart item", err)
		return
	}
	s.audit(r, "cart.item_updated", acc.AccountID, "cart_item_id", id, "quantity", updated.Quantity, "selected", updated.Selected)
	s.writeCart(w, r, acc.AccountID)
}

func (s *Server) handleDeleteCartItem(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id := r.PathValue("id")
	err := s.store.DeleteCartItem(r.Context(), acc.AccountID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrCartItemNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "delete cart item", err)
		return
	}
	s.audit(r, "cart.item_removed", acc.AccountID, "cart_item_id", id)
	s.writeCart(w, r, acc.AccountID)
}

// ---------- 优惠券 ----------

var (
	ErrCouponNotFound     = &APIError{Status: http.StatusNotFound, Code: "coupon_not_found", Message: "优惠券不存在"}
	ErrCouponUnavailable  = &APIError{Status: http.StatusConflict, Code: "coupon_unavailable", Message: "优惠券不在领取时间内或已停用"}
	ErrCouponSoldOut      = &APIError{Status: http.StatusConflict, Code: "coupon_sold_out", Message: "优惠券已领完"}
	ErrCouponLimitReached = &APIError{Status: http.StatusConflict, Code: "coupon_limit_reached", Message: "已达到这张券的领取上限"}
)

type couponView struct {
	CouponID        string       `json:"coupon_id"`
	Name            string       `json:"name"`
	Scope           string       `json:"scope"`
	MerchantID      string       `json:"merchant_id"`
	Type            string       `json:"type"`
	ThresholdAmount domain.Money `json:"threshold_amount"`
	DiscountAmount  domain.Money `json:"discount_amount"`
	Description     string       `json:"description"`
	TotalCount      int          `json:"total_count"`
	ClaimedCount    int          `json:"claimed_count"`
	PerUserLimit    int          `json:"per_user_limit"`
	StartAt         time.Time    `json:"start_at"`
	EndAt           time.Time    `json:"end_at"`
}

type availableCouponView struct {
	couponView
	ClaimedByMe int  `json:"claimed_by_me"`
	CanClaim    bool `json:"can_claim"`
}

type userCouponView struct {
	UserCouponID string     `json:"user_coupon_id"`
	CouponID     string     `json:"coupon_id"`
	Status       string     `json:"status"`
	OrderID      string     `json:"order_id"`
	ClaimedAt    time.Time  `json:"claimed_at"`
	UsedAt       *time.Time `json:"used_at"`
	Coupon       couponView `json:"coupon"`
}

func toCouponView(c domain.Coupon) couponView {
	return couponView{CouponID: c.CouponID, Name: c.Name, Scope: c.Scope, MerchantID: c.MerchantID, Type: c.Type,
		ThresholdAmount: c.ThresholdAmount, DiscountAmount: c.DiscountAmount, Description: pricing.DescribeCoupon(c), TotalCount: c.TotalCount,
		ClaimedCount: c.ClaimedCount, PerUserLimit: max(c.PerUserLimit, 1), StartAt: c.StartAt, EndAt: c.EndAt}
}

func toUserCouponView(oc store.OwnedCoupon) userCouponView {
	return userCouponView{UserCouponID: oc.UserCouponID, CouponID: oc.CouponID, Status: oc.Status, OrderID: oc.OrderID,
		ClaimedAt: oc.ClaimedAt, UsedAt: oc.UsedAt, Coupon: toCouponView(oc.Coupon)}
}

// handleListAvailableCoupons 当前可领的券（含已领完的，can_claim=false），并标出本人已领张数。
func (s *Server) handleListAvailableCoupons(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	page := readPage(r)
	coupons, total, err := s.store.ListClaimableCoupons(r.Context(), s.now(), page)
	if err != nil {
		s.storeFailed(w, r, "list coupons", err)
		return
	}
	ids := make([]string, len(coupons))
	for i, c := range coupons {
		ids[i] = c.CouponID
	}
	claimed, err := s.store.CountClaimed(r.Context(), acc.AccountID, ids)
	if err != nil {
		s.storeFailed(w, r, "count claimed coupons", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(coupons, page, total, func(c domain.Coupon) availableCouponView {
		mine := claimed[c.CouponID]
		soldOut := c.TotalCount > 0 && c.ClaimedCount >= c.TotalCount
		return availableCouponView{couponView: toCouponView(c), ClaimedByMe: mine, CanClaim: !soldOut && mine < max(c.PerUserLimit, 1)}
	}))
}

func (s *Server) handleListMyCoupons(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch status {
	case "", domain.UserCouponUnused, domain.UserCouponUsed, domain.UserCouponExpired:
	default:
		writeError(w, fieldError("status", "状态只能是 unused、used 或 expired"))
		return
	}
	page := readPage(r)
	items, total, err := s.store.ListUserCoupons(r.Context(), store.UserCouponQuery{AccountID: acc.AccountID, Status: status, At: s.now(), Page: page})
	if err != nil {
		s.storeFailed(w, r, "list my coupons", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, page, total, toUserCouponView))
}

// handleCouponAction 处理 POST /coupons/{id}:claim（路径段内带动作，与上游一致）。
func (s *Server) handleCouponAction(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id, ok := strings.CutSuffix(r.PathValue("action"), ":claim")
	if !ok || id == "" {
		writeError(w, ErrNotFound)
		return
	}
	got, err := s.store.ClaimCoupon(r.Context(), acc.AccountID, id, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrCouponNotFound)
	case errors.Is(err, store.ErrCouponUnavailable):
		writeError(w, ErrCouponUnavailable)
	case errors.Is(err, store.ErrCouponSoldOut):
		writeError(w, ErrCouponSoldOut)
	case errors.Is(err, store.ErrCouponLimitReached):
		writeError(w, ErrCouponLimitReached)
	case err != nil:
		s.storeFailed(w, r, "claim coupon", err)
	default:
		s.audit(r, "coupon.claimed", acc.AccountID, "coupon_id", id, "user_coupon_id", got.UserCouponID)
		writeJSON(w, http.StatusOK, toUserCouponView(got))
	}
}
