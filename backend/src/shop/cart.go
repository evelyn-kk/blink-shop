package shop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/pricing"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// InsufficientStock 说明库存不足：current 是购物车里已有的件数（0 表示还没加）。
func InsufficientStock(stock, current int) *Error {
	if current > 0 {
		return &Error{Status: 409, Code: "insufficient_stock",
			Message: fmt.Sprintf("库存不足：库存 %d 件，购物车里已有 %d 件", stock, current)}
	}
	return &Error{Status: 409, Code: "insufficient_stock", Message: fmt.Sprintf("库存不足，最多可买 %d 件", stock)}
}

// QuantityLimit 说明单行数量超过上限。
func QuantityLimit() *Error {
	return &Error{Status: 409, Code: "quantity_limit", Message: fmt.Sprintf("单个商品最多买 %d 件", MaxLineQuantity)}
}

// CouponNotApplicable 把计价层的券错误转成可展示的错误。
func CouponNotApplicable(ce *pricing.CouponError) *Error {
	return &Error{Status: 400, Code: "coupon_not_applicable", Field: "user_coupon_ids", Message: "优惠券不能使用：" + ce.Reason}
}

// ---------- 视图 ----------

// CartItem 是购物车里的一项及其计价结果。
type CartItem struct {
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

type CartSummary struct {
	ItemCount      int          `json:"item_count"`
	SelectedCount  int          `json:"selected_count"`
	TotalAmount    domain.Money `json:"total_amount"`
	DiscountAmount domain.Money `json:"discount_amount"`
	PayAmount      domain.Money `json:"pay_amount"`
}

// Cart 是购物车接口的响应：全部项和已选中项的合计。
type Cart struct {
	Items   []CartItem  `json:"items"`
	Summary CartSummary `json:"summary"`
}

type DiscountLine struct {
	Type        string       `json:"type"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Scope       string       `json:"scope"`
	MerchantID  string       `json:"merchant_id"`
	Amount      domain.Money `json:"amount"`
	Description string       `json:"description"`
	CartItemIDs []string     `json:"cart_item_ids"`
}

type MerchantAmount struct {
	MerchantID     string       `json:"merchant_id"`
	MerchantName   string       `json:"merchant_name"`
	TotalAmount    domain.Money `json:"total_amount"`
	DiscountAmount domain.Money `json:"discount_amount"`
	PayAmount      domain.Money `json:"pay_amount"`
}

type ItemAmount struct {
	CartItemID     string       `json:"cart_item_id"`
	Amount         domain.Money `json:"amount"`
	DiscountAmount domain.Money `json:"discount_amount"`
	PayAmount      domain.Money `json:"pay_amount"`
}

type Hint struct {
	PromotionID string       `json:"promotion_id"`
	Name        string       `json:"name"`
	Shortfall   domain.Money `json:"shortfall"`
}

// DiscountPreview 是优惠试算结果。
type DiscountPreview struct {
	TotalAmount    domain.Money     `json:"total_amount"`
	DiscountAmount domain.Money     `json:"discount_amount"`
	PayAmount      domain.Money     `json:"pay_amount"`
	Lines          []DiscountLine   `json:"lines"`
	Merchants      []MerchantAmount `json:"merchants"`
	Items          []ItemAmount     `json:"items"`
	Hints          []Hint           `json:"hints"`
	UserCouponIDs  []string         `json:"user_coupon_ids"`
}

// PricedCart 是一次计价的完整结果：购物车每一项（含不参与计价的）及优惠计算结果。
type PricedCart struct {
	Lines  []store.CartLine
	Views  []CartItem
	Result pricing.Result
}

// Cart 把计价结果整理成购物车响应。
func (p PricedCart) Cart() Cart {
	out := Cart{Items: p.Views, Summary: CartSummary{
		ItemCount: len(p.Views), TotalAmount: p.Result.TotalAmount, DiscountAmount: p.Result.DiscountAmount, PayAmount: p.Result.PayAmount,
	}}
	if out.Items == nil {
		out.Items = []CartItem{}
	}
	for _, it := range p.Result.Items {
		for _, v := range p.Views {
			if v.CartItemID == it.CartItemID {
				out.Summary.SelectedCount += v.Quantity
			}
		}
	}
	return out
}

// Preview 把计价结果整理成试算响应。
func (p PricedCart) Preview() DiscountPreview {
	res := p.Result
	out := DiscountPreview{TotalAmount: res.TotalAmount, DiscountAmount: res.DiscountAmount, PayAmount: res.PayAmount,
		Lines: []DiscountLine{}, Merchants: []MerchantAmount{}, Items: []ItemAmount{}, Hints: []Hint{}, UserCouponIDs: []string{}}
	names := map[string]string{}
	for _, l := range p.Lines {
		names[l.MerchantID] = l.MerchantName
	}
	for _, l := range res.Lines {
		out.Lines = append(out.Lines, DiscountLine{Type: l.Type, ID: l.ID, Name: l.Name, Scope: l.Scope, MerchantID: l.MerchantID,
			Amount: l.Amount, Description: l.Description, CartItemIDs: l.CartItemIDs})
	}
	for _, m := range res.Merchants {
		out.Merchants = append(out.Merchants, MerchantAmount{MerchantID: m.MerchantID, MerchantName: names[m.MerchantID],
			TotalAmount: m.TotalAmount, DiscountAmount: m.DiscountAmount, PayAmount: m.PayAmount})
	}
	for _, it := range res.Items {
		out.Items = append(out.Items, ItemAmount{CartItemID: it.CartItemID, Amount: it.Amount, DiscountAmount: it.Discount, PayAmount: it.PayAmount})
	}
	for _, h := range res.Hints {
		out.Hints = append(out.Hints, Hint{PromotionID: h.PromotionID, Name: h.Name, Shortfall: h.Shortfall})
	}
	out.UserCouponIDs = append(out.UserCouponIDs, res.UserCouponIDs...)
	return out
}

// ---------- 可购买判断 ----------

// ProductReason 判断购物车项对应的商品和规格当前能否购买（不看数量）；返回空串表示可以。
func ProductReason(l store.CartLine) string {
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

// CheckPurchasable 判断购物车项按 quantity 件能否购买，不能时返回对应的错误。
func CheckPurchasable(l store.CartLine, quantity int) error {
	switch reason := ProductReason(l); {
	case reason == "已售罄":
		return ErrOutOfStock
	case reason != "":
		return ErrItemUnavailable
	case quantity > l.StockQuantity:
		return InsufficientStock(l.StockQuantity, 0)
	}
	return nil
}

// UnavailableReason 在 ProductReason 之外再检查数量是否超过当前库存。
func UnavailableReason(l store.CartLine) string {
	if reason := ProductReason(l); reason != "" {
		return reason
	}
	if l.Quantity > l.StockQuantity {
		return fmt.Sprintf("库存不足，仅剩 %d 件", l.StockQuantity)
	}
	return ""
}

// ---------- 计价 ----------

// PricingContext 是计价用到的、与购物车无关的数据：分类的上级关系和当前有效的促销。结算在事务内读取它。
type PricingContext struct {
	Now    time.Time
	parent map[string]string
	promos []domain.PromotionRule
}

// LoadPricingContext 读取当前时间、分类和有效促销。
func (s *Service) LoadPricingContext(ctx context.Context) (PricingContext, error) {
	pc := PricingContext{Now: s.now(), parent: map[string]string{}}
	cats, err := s.store.ListCategories(ctx)
	if err != nil {
		return PricingContext{}, err
	}
	for _, c := range cats {
		pc.parent[c.CategoryID] = c.ParentID
	}
	pc.promos, _, err = s.store.ListActivePromotions(ctx, store.PromotionQuery{At: pc.Now, Page: store.Page{Page: 1, PageSize: pricingPromotionLimit}})
	if err != nil {
		return PricingContext{}, err
	}
	return pc, nil
}

// Price 计价：只有选中且可购买的项参与计算；owned 中只有未使用的券可用。couponChoice 为 nil 时自动选券。
// 购物车试算和结算共用这一个函数，保证试算金额就是下单金额。
func (pc PricingContext) Price(lines []store.CartLine, owned []store.OwnedCoupon, couponChoice []string) (PricedCart, error) {
	in := pricing.Input{Promotions: pc.promos, CouponChoice: couponChoice, Now: pc.Now}
	for _, oc := range owned {
		if oc.Status == domain.UserCouponUnused {
			in.Coupons = append(in.Coupons, pricing.OwnedCoupon{UserCouponID: oc.UserCouponID, Status: oc.Status, Coupon: oc.Coupon})
		}
	}
	views := make([]CartItem, len(lines))
	for i, l := range lines {
		reason := UnavailableReason(l)
		amount := l.UnitPrice.Mul(l.Quantity)
		views[i] = CartItem{
			CartItemID: l.CartItemID, ProductID: l.ProductID, SkuID: l.SkuID, ProductName: l.ProductName, SkuName: l.SkuName,
			ImageURL: l.ImageURL, MerchantID: l.MerchantID, MerchantName: l.MerchantName, UnitPrice: l.UnitPrice, Quantity: l.Quantity,
			// 不可购买的项不显示为选中（与“不能选中”的规则一致）；保存的选中状态不变，商品恢复后仍是原来的选择。
			Selected: l.Selected && reason == "", StockQuantity: l.StockQuantity, Available: reason == "", UnavailableReason: reason,
			Amount: amount, PayAmount: amount,
		}
		if l.Selected && reason == "" {
			var ancestors []string
			for c := l.CategoryID; c != "" && !contains(ancestors, c); c = pc.parent[c] {
				ancestors = append(ancestors, c)
			}
			in.Lines = append(in.Lines, pricing.Line{CartItemID: l.CartItemID, ProductID: l.ProductID, MerchantID: l.MerchantID,
				CategoryIDs: ancestors, UnitPrice: l.UnitPrice, Quantity: l.Quantity})
		}
	}
	res, err := pricing.Compute(in)
	if err != nil {
		return PricedCart{}, err
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
	return PricedCart{Lines: lines, Views: views, Result: res}, nil
}

// PriceCart 读取购物车并计价。couponChoice 为 nil 时自动选券；券不可用时返回 *pricing.CouponError。
func (s *Service) PriceCart(ctx context.Context, accountID string, couponChoice []string) (PricedCart, error) {
	pc, err := s.LoadPricingContext(ctx)
	if err != nil {
		return PricedCart{}, err
	}
	lines, err := s.store.ListCartLines(ctx, accountID)
	if err != nil {
		return PricedCart{}, err
	}
	owned, _, err := s.store.ListUserCoupons(ctx, store.UserCouponQuery{AccountID: accountID, Status: domain.UserCouponUnused, At: pc.Now,
		Page: store.Page{Page: 1, PageSize: pricingCouponLimit}})
	if err != nil {
		return PricedCart{}, err
	}
	return pc.Price(lines, owned, couponChoice)
}

// Cart 读取购物车（自动选券计价）。
func (s *Service) Cart(ctx context.Context, accountID string) (Cart, error) {
	priced, err := s.PriceCart(ctx, accountID, nil)
	if err != nil {
		return Cart{}, err
	}
	return priced.Cart(), nil
}

// ParseCouponChoice 规范化指定的券：去空、去重、最多 MaxCouponChoice 张。nil 表示没有指定（自动选券）。
func ParseCouponChoice(ids []string) ([]string, error) {
	if ids == nil {
		return nil, nil
	}
	choice := []string{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !contains(choice, id) {
			choice = append(choice, id)
		}
	}
	if len(choice) > MaxCouponChoice {
		return nil, FieldError("user_coupon_ids", fmt.Sprintf("一次最多指定 %d 张券", MaxCouponChoice))
	}
	return choice, nil
}

// PreviewDiscount 试算：couponChoice 为 nil 时自动选最优券；非 nil（可以为空）只使用指定的券。
func (s *Service) PreviewDiscount(ctx context.Context, accountID string, couponChoice []string) (DiscountPreview, error) {
	priced, err := s.PriceCart(ctx, accountID, couponChoice)
	var ce *pricing.CouponError
	if errors.As(err, &ce) {
		return DiscountPreview{}, CouponNotApplicable(ce)
	}
	if err != nil {
		return DiscountPreview{}, err
	}
	return priced.Preview(), nil
}

// ---------- 修改购物车 ----------

// AddCartItemInput 是加购输入；SkuID 为空表示默认规格，Quantity 为 nil 表示 1。
type AddCartItemInput struct {
	ProductID string
	SkuID     string
	Quantity  *int
}

// AddCartItem 加购：商品必须公开可见；不传 sku_id 时用默认规格；同一规格累加数量（并重新选中），
// 累加后不能超过库存和单行上限。返回写入后的购物车项和本次加的件数。
func (s *Service) AddCartItem(ctx context.Context, accountID string, in AddCartItemInput) (domain.CartItem, int, error) {
	productID := strings.TrimSpace(in.ProductID)
	if productID == "" {
		return domain.CartItem{}, 0, FieldError("product_id", "请选择商品")
	}
	qty := 1
	if in.Quantity != nil {
		qty = *in.Quantity
	}
	if qty < 1 || qty > MaxLineQuantity {
		return domain.CartItem{}, 0, FieldError("quantity", fmt.Sprintf("数量必须是 1 到 %d 之间的整数", MaxLineQuantity))
	}
	product, err := s.store.GetVisibleProduct(ctx, productID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.CartItem{}, 0, ErrProductNotFound
	}
	if err != nil {
		return domain.CartItem{}, 0, err
	}
	sku, ok := PickSKU(product.SKUs, strings.TrimSpace(in.SkuID))
	if !ok {
		return domain.CartItem{}, 0, FieldError("sku_id", "该商品没有这个规格")
	}
	if sku.StockQuantity <= 0 {
		return domain.CartItem{}, 0, ErrOutOfStock
	}
	// 商品和库存以事务内重新读取的状态为准（line），上面的检查只用于尽早给出提示。
	item, err := s.store.AddCartItem(ctx, accountID, product.ProductID, sku.SkuID, func(line store.CartLine, lines int) (int, error) {
		if line.Quantity == 0 && lines >= store.MaxCartLines {
			return 0, ErrCartFull
		}
		next := line.Quantity + qty
		if next > MaxLineQuantity {
			return 0, QuantityLimit()
		}
		switch reason := ProductReason(line); {
		case reason == "已售罄":
			return 0, ErrOutOfStock
		case reason != "":
			return 0, ErrProductNotFound
		case next > line.StockQuantity:
			return 0, InsufficientStock(line.StockQuantity, line.Quantity)
		}
		return next, nil
	})
	if err != nil {
		return domain.CartItem{}, 0, err
	}
	return item, qty, nil
}

// PickSKU 返回指定规格；不指定时返回默认规格（没有标默认时取第一个）。
func PickSKU(skus []domain.ProductSKU, skuID string) (domain.ProductSKU, bool) {
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

// UpdateCartItemInput 是修改购物车项的输入，nil 表示不改。
type UpdateCartItemInput struct {
	Quantity *int
	Selected *bool
}

// UpdateCartItem 修改数量或选中状态。数量必须是 1–99 且不超过当前库存（0 和负数不会被当成删除）。
// 不可购买的项（下架、删除、规格失效、售罄、数量超库存）不能被选中，也不能保持选中去做其他修改：
// 只能取消选中、删除，或把“库存不足”的数量改到库存以内（之后可以再选中）。
func (s *Service) UpdateCartItem(ctx context.Context, accountID, cartItemID string, in UpdateCartItemInput) (domain.CartItem, error) {
	if in.Quantity == nil && in.Selected == nil {
		return domain.CartItem{}, InvalidArgument("请提供 quantity 或 selected")
	}
	if in.Quantity != nil && (*in.Quantity < 1 || *in.Quantity > MaxLineQuantity) {
		return domain.CartItem{}, FieldError("quantity", fmt.Sprintf("数量必须是 1 到 %d 之间的整数", MaxLineQuantity))
	}
	// line 是 Store 在购物车行锁所在的事务内重新读取（并加共享锁）的商品、店铺和规格状态：
	// 商家并发修改商品要么已提交（这里能读到），要么等本事务结束，不会按过期状态放行。
	updated, err := s.store.UpdateCartItem(ctx, accountID, cartItemID, func(it *domain.CartItem, line store.CartLine) error {
		quantityChanged := in.Quantity != nil && *in.Quantity != it.Quantity
		if in.Quantity != nil {
			it.Quantity = *in.Quantity
		}
		if in.Selected != nil {
			it.Selected = *in.Selected
		}
		// 改数量，或更新后仍为选中：商品必须可以购买，数量不能超过库存。只取消选中（或保持未选中）总是允许。
		if quantityChanged || it.Selected {
			return CheckPurchasable(line, it.Quantity)
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return domain.CartItem{}, ErrCartItemNotFound
	}
	if err != nil {
		return domain.CartItem{}, err
	}
	return updated, nil
}

// DeleteCartItem 删除购物车项；不存在（或不是本账户的）返回 ErrCartItemNotFound。
func (s *Service) DeleteCartItem(ctx context.Context, accountID, cartItemID string) error {
	err := s.store.DeleteCartItem(ctx, accountID, cartItemID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrCartItemNotFound
	}
	return err
}

// ---------- 优惠券 ----------

type Coupon struct {
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

// AvailableCoupon 是当前可领的券，带本人已领张数和能否再领。
type AvailableCoupon struct {
	Coupon
	ClaimedByMe int  `json:"claimed_by_me"`
	CanClaim    bool `json:"can_claim"`
}

// UserCoupon 是用户持有的券。
type UserCoupon struct {
	UserCouponID string     `json:"user_coupon_id"`
	CouponID     string     `json:"coupon_id"`
	Status       string     `json:"status"`
	OrderID      string     `json:"order_id"`
	ClaimedAt    time.Time  `json:"claimed_at"`
	UsedAt       *time.Time `json:"used_at"`
	Coupon       Coupon     `json:"coupon"`
}

func ToCoupon(c domain.Coupon) Coupon {
	return Coupon{CouponID: c.CouponID, Name: c.Name, Scope: c.Scope, MerchantID: c.MerchantID, Type: c.Type,
		ThresholdAmount: c.ThresholdAmount, DiscountAmount: c.DiscountAmount, Description: pricing.DescribeCoupon(c), TotalCount: c.TotalCount,
		ClaimedCount: c.ClaimedCount, PerUserLimit: max(c.PerUserLimit, 1), StartAt: c.StartAt, EndAt: c.EndAt}
}

func ToUserCoupon(oc store.OwnedCoupon) UserCoupon {
	return UserCoupon{UserCouponID: oc.UserCouponID, CouponID: oc.CouponID, Status: oc.Status, OrderID: oc.OrderID,
		ClaimedAt: oc.ClaimedAt, UsedAt: oc.UsedAt, Coupon: ToCoupon(oc.Coupon)}
}

// AvailableCoupons 列出当前可领的券（含已领完的，can_claim=false），并标出本人已领张数。
func (s *Service) AvailableCoupons(ctx context.Context, accountID string, page store.Page) ([]AvailableCoupon, int, error) {
	coupons, total, err := s.store.ListClaimableCoupons(ctx, s.now(), page)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(coupons))
	for i, c := range coupons {
		ids[i] = c.CouponID
	}
	claimed, err := s.store.CountClaimed(ctx, accountID, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]AvailableCoupon, 0, len(coupons))
	for _, c := range coupons {
		mine := claimed[c.CouponID]
		soldOut := c.TotalCount > 0 && c.ClaimedCount >= c.TotalCount
		out = append(out, AvailableCoupon{Coupon: ToCoupon(c), ClaimedByMe: mine, CanClaim: !soldOut && mine < max(c.PerUserLimit, 1)})
	}
	return out, total, nil
}

// ValidateUserCouponStatus 校验“我的券”的状态筛选：空、unused、used 或 expired。
func ValidateUserCouponStatus(status string) error {
	switch status {
	case "", domain.UserCouponUnused, domain.UserCouponUsed, domain.UserCouponExpired:
		return nil
	}
	return FieldError("status", "状态只能是 unused、used 或 expired")
}

// MyCoupons 列出本人的券；status 为空表示全部。
func (s *Service) MyCoupons(ctx context.Context, accountID, status string, page store.Page) ([]UserCoupon, int, error) {
	if err := ValidateUserCouponStatus(status); err != nil {
		return nil, 0, err
	}
	items, total, err := s.store.ListUserCoupons(ctx, store.UserCouponQuery{AccountID: accountID, Status: status, At: s.now(), Page: page})
	if err != nil {
		return nil, 0, err
	}
	out := make([]UserCoupon, 0, len(items))
	for _, it := range items {
		out = append(out, ToUserCoupon(it))
	}
	return out, total, nil
}

// ClaimCoupon 领券；失败原因转成对应的 *Error。
func (s *Service) ClaimCoupon(ctx context.Context, accountID, couponID string) (UserCoupon, error) {
	got, err := s.store.ClaimCoupon(ctx, accountID, couponID, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return UserCoupon{}, ErrCouponNotFound
	case errors.Is(err, store.ErrCouponUnavailable):
		return UserCoupon{}, ErrCouponUnavailable
	case errors.Is(err, store.ErrCouponSoldOut):
		return UserCoupon{}, ErrCouponSoldOut
	case errors.Is(err, store.ErrCouponLimitReached):
		return UserCoupon{}, ErrCouponLimitReached
	case err != nil:
		return UserCoupon{}, err
	}
	return ToUserCoupon(got), nil
}
