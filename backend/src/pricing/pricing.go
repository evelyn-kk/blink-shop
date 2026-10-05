// Package pricing 是购物车与结算共用的优惠计算：纯函数，不访问数据库，金额一律用 domain.Money（分）。
// 规则见 backend/README.md“营销规则”，4.2 结算下单使用同一个 Compute，保证试算与实付一致。
package pricing

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// Line 是一条参与计算的购物车项（只传已选中且可购买的项）。CategoryIDs 包含商品分类及其所有上级分类。
type Line struct {
	CartItemID  string
	ProductID   string
	MerchantID  string
	CategoryIDs []string
	UnitPrice   domain.Money
	Quantity    int
}

// OwnedCoupon 是用户持有的一张券。
type OwnedCoupon struct {
	UserCouponID string
	Status       string // unused / used / expired
	Coupon       domain.Coupon
}

// Input：CouponChoice 为 nil 时自动选择最优券；非 nil（可以为空切片）时只使用列出的券。
type Input struct {
	Lines        []Line
	Promotions   []domain.PromotionRule
	Coupons      []OwnedCoupon
	CouponChoice []string
	Now          time.Time
}

// DiscountLine 是一条优惠明细；Amount 已分摊到 CartItemIDs 对应的购物车项。
type DiscountLine struct {
	Type        string // promotion / coupon
	ID          string // promotion_id 或 user_coupon_id
	Name        string
	Scope       string
	MerchantID  string
	Amount      domain.Money
	Description string
	CartItemIDs []string
}

// ItemResult 是单个购物车项的金额：Amount = 单价 × 数量，Discount 为分摊到该项的优惠，PayAmount = Amount − Discount。
type ItemResult struct {
	CartItemID string
	Amount     domain.Money
	Discount   domain.Money
	PayAmount  domain.Money
}

type MerchantResult struct {
	MerchantID     string
	TotalAmount    domain.Money
	DiscountAmount domain.Money
	PayAmount      domain.Money
}

// Hint 提示某个满减活动还差多少金额达到门槛（凑单提示）。
type Hint struct {
	PromotionID string
	Name        string
	Shortfall   domain.Money
}

type Result struct {
	Items          []ItemResult
	Merchants      []MerchantResult
	TotalAmount    domain.Money
	DiscountAmount domain.Money
	PayAmount      domain.Money
	Lines          []DiscountLine
	Hints          []Hint
	UserCouponIDs  []string // 实际使用的券
}

// CouponError 表示指定的券不能用于本次购物车（只在 CouponChoice 非 nil 时返回）。
type CouponError struct {
	UserCouponID string
	Reason       string
}

func (e *CouponError) Error() string { return fmt.Sprintf("优惠券 %s %s", e.UserCouponID, e.Reason) }

// ErrInvalidLine 表示输入的购物车项不合法（数量不大于 0）。
var ErrInvalidLine = errors.New("pricing: invalid line")

// 活动按范围从窄到宽依次计算。
var promotionLevels = []string{domain.ScopeProduct, domain.ScopeCategory, domain.ScopeMerchant, domain.ScopePlatform}

type lineState struct {
	Line
	amount   domain.Money // 原价金额
	current  domain.Money // 已减去前面优惠后的金额
	promoted bool         // 已参与过活动
	locked   bool         // 参与了不可叠加的活动：不再参与其他活动和用券
}

// Compute 计算一次购物车优惠。规则：
//
//  1. 活动按“单品 → 品类 → 店铺 → 平台”的顺序计算；同一层级内每次选当前优惠最大的活动（相同时按 ID），直到没有可用活动。
//     每个活动的基数是其范围内商品的“当前金额”（已减去前面层级的优惠），满足门槛（基数 ≥ 门槛）才生效。
//  2. 满减：优惠 = min(减额, 基数)。折扣：discount_rate 是实付比例（0.95 = 9.5 折），优惠 = 基数 − 基数 × 比例（四舍五入到分）。
//  3. 不可叠加（stackable=false）的活动只作用于还没参加过任何活动的商品，生效后这些商品不再参加后续活动，也不能用券。
//  4. 优惠按各商品当前金额比例分摊到分，余数按最大余数法分配，单个商品的优惠不会超过其金额。
//  5. 用券在活动之后：每个店铺最多一张店铺券、整单最多一张平台券；先用店铺券再用平台券，门槛按用券前的当前金额判断。
//     自动选择时每个范围取优惠最大的一张（相同时先用快过期的）。
func Compute(in Input) (Result, error) {
	states := make([]*lineState, len(in.Lines))
	for i, l := range in.Lines {
		if l.Quantity <= 0 {
			return Result{}, fmt.Errorf("%w: %s 数量为 %d", ErrInvalidLine, l.CartItemID, l.Quantity)
		}
		amount := l.UnitPrice.Mul(l.Quantity)
		states[i] = &lineState{Line: l, amount: amount, current: amount}
	}
	var res Result
	hinted := map[string]bool{}

	for _, level := range promotionLevels {
		remaining := activePromotions(in.Promotions, level, in.Now)
		for len(remaining) > 0 {
			bestIdx, bestDiscount := -1, domain.Money(0)
			for i, p := range remaining {
				eligible := promotionLines(states, p)
				base := sum(eligible)
				d := promotionDiscount(p, base)
				if d == 0 && base > 0 && base < p.ThresholdAmount && !hinted[p.PromotionID] {
					res.Hints = append(res.Hints, Hint{PromotionID: p.PromotionID, Name: p.Name, Shortfall: p.ThresholdAmount - base})
					hinted[p.PromotionID] = true
				}
				if d > bestDiscount || (d == bestDiscount && d > 0 && p.PromotionID < remaining[bestIdx].PromotionID) {
					bestIdx, bestDiscount = i, d
				}
			}
			if bestIdx < 0 {
				break
			}
			p := remaining[bestIdx]
			eligible := promotionLines(states, p)
			allocate(eligible, bestDiscount)
			for _, s := range eligible {
				s.promoted = true
				if !p.Stackable {
					s.locked = true
				}
			}
			res.Lines = append(res.Lines, DiscountLine{
				Type: "promotion", ID: p.PromotionID, Name: p.Name, Scope: p.Scope, MerchantID: p.MerchantID,
				Amount: bestDiscount, Description: DescribePromotion(p), CartItemIDs: ids(eligible),
			})
			remaining = append(remaining[:bestIdx:bestIdx], remaining[bestIdx+1:]...)
		}
	}

	used, err := applyCoupons(states, in, &res)
	if err != nil {
		return Result{}, err
	}
	res.UserCouponIDs = used

	byMerchant := map[string]*MerchantResult{}
	var order []string
	for _, s := range states {
		discount := s.amount - s.current
		res.Items = append(res.Items, ItemResult{CartItemID: s.CartItemID, Amount: s.amount, Discount: discount, PayAmount: s.current})
		res.TotalAmount += s.amount
		res.DiscountAmount += discount
		m, ok := byMerchant[s.MerchantID]
		if !ok {
			m = &MerchantResult{MerchantID: s.MerchantID}
			byMerchant[s.MerchantID] = m
			order = append(order, s.MerchantID)
		}
		m.TotalAmount += s.amount
		m.DiscountAmount += discount
		m.PayAmount += s.current
	}
	for _, id := range order {
		res.Merchants = append(res.Merchants, *byMerchant[id])
	}
	res.PayAmount = res.TotalAmount - res.DiscountAmount
	return res, nil
}

func activePromotions(all []domain.PromotionRule, level string, now time.Time) []domain.PromotionRule {
	var out []domain.PromotionRule
	for _, p := range all {
		// 有效期是 [start_at, end_at)，与 Store 查询有效促销的条件一致。
		if p.Scope == level && p.Status == domain.StatusActive && !now.Before(p.StartAt) && now.Before(p.EndAt) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PromotionID < out[j].PromotionID })
	return out
}

// promotionLines 返回活动范围内、当前还能参加该活动的商品。
func promotionLines(states []*lineState, p domain.PromotionRule) []*lineState {
	var out []*lineState
	for _, s := range states {
		if s.locked || (!p.Stackable && s.promoted) || s.current == 0 {
			continue
		}
		match := false
		switch p.Scope {
		case domain.ScopePlatform:
			match = true
		case domain.ScopeMerchant:
			match = s.MerchantID == p.MerchantID
		case domain.ScopeProduct:
			match = s.ProductID == p.ProductID
		case domain.ScopeCategory:
			match = contains(s.CategoryIDs, p.CategoryID)
		}
		// 活动属于某个店铺时，只作用于该店铺的商品（单品、品类活动也一样）。
		if match && (p.MerchantID == "" || s.MerchantID == p.MerchantID) {
			out = append(out, s)
		}
	}
	return out
}

func promotionDiscount(p domain.PromotionRule, base domain.Money) domain.Money {
	if base <= 0 || base < p.ThresholdAmount {
		return 0
	}
	switch p.Type {
	case domain.PromotionFullReduction:
		return min(p.DiscountAmount, base)
	case domain.PromotionDiscount:
		if p.DiscountRate.Validate() != nil {
			return 0
		}
		return base - base.MulRate(p.DiscountRate)
	}
	return 0
}

func couponDiscount(c domain.Coupon, base domain.Money) domain.Money {
	if base <= 0 || base < c.ThresholdAmount || c.Type != domain.CouponFixedAmount {
		return 0
	}
	return min(c.DiscountAmount, base)
}

// couponUsable 判断券本身是否可用（不看门槛）；不可用时返回原因。
func couponUsable(c OwnedCoupon, now time.Time) string {
	switch {
	case c.Status == domain.UserCouponUsed:
		return "已使用"
	case c.Status != domain.UserCouponUnused || !now.Before(c.Coupon.EndAt):
		return "已过期"
	case now.Before(c.Coupon.StartAt):
		return "还未到使用时间"
	case c.Coupon.Status != domain.StatusActive:
		return "已停用"
	case c.Coupon.Type != domain.CouponFixedAmount:
		return "类型不支持"
	}
	return ""
}

// couponLines 返回券范围内可以用券的商品。
func couponLines(states []*lineState, c domain.Coupon) []*lineState {
	var out []*lineState
	for _, s := range states {
		if s.locked || s.current == 0 {
			continue
		}
		if c.Scope == domain.ScopePlatform || (c.Scope == domain.ScopeMerchant && s.MerchantID == c.MerchantID) {
			out = append(out, s)
		}
	}
	return out
}

func applyCoupons(states []*lineState, in Input, res *Result) ([]string, error) {
	owned := map[string]OwnedCoupon{}
	for _, c := range in.Coupons {
		owned[c.UserCouponID] = c
	}
	apply := func(c OwnedCoupon, d domain.Money, lines []*lineState) {
		allocate(lines, d)
		res.Lines = append(res.Lines, DiscountLine{
			Type: "coupon", ID: c.UserCouponID, Name: c.Coupon.Name, Scope: c.Coupon.Scope, MerchantID: c.Coupon.MerchantID,
			Amount: d, Description: describeThreshold(c.Coupon.ThresholdAmount, c.Coupon.DiscountAmount), CartItemIDs: ids(lines),
		})
	}

	if in.CouponChoice != nil {
		var merchantCoupons, platformCoupons []OwnedCoupon
		seenMerchant := map[string]bool{}
		for _, id := range in.CouponChoice {
			c, ok := owned[id]
			if !ok {
				return nil, &CouponError{UserCouponID: id, Reason: "不存在"}
			}
			if reason := couponUsable(c, in.Now); reason != "" {
				return nil, &CouponError{UserCouponID: id, Reason: reason}
			}
			switch c.Coupon.Scope {
			case domain.ScopeMerchant:
				if seenMerchant[c.Coupon.MerchantID] {
					return nil, &CouponError{UserCouponID: id, Reason: "与同店铺的另一张券不能同时使用"}
				}
				seenMerchant[c.Coupon.MerchantID] = true
				merchantCoupons = append(merchantCoupons, c)
			case domain.ScopePlatform:
				if len(platformCoupons) > 0 {
					return nil, &CouponError{UserCouponID: id, Reason: "与另一张平台券不能同时使用"}
				}
				platformCoupons = append(platformCoupons, c)
			default:
				return nil, &CouponError{UserCouponID: id, Reason: "范围不支持"}
			}
		}
		var used []string
		for _, c := range append(merchantCoupons, platformCoupons...) {
			lines := couponLines(states, c.Coupon)
			d := couponDiscount(c.Coupon, sum(lines))
			if d == 0 {
				return nil, &CouponError{UserCouponID: c.UserCouponID, Reason: "未满足使用门槛或没有适用商品"}
			}
			apply(c, d, lines)
			used = append(used, c.UserCouponID)
		}
		return used, nil
	}

	// 自动选择：先每个店铺选一张店铺券，再选一张平台券。
	var usable []OwnedCoupon
	for _, c := range in.Coupons {
		if couponUsable(c, in.Now) == "" {
			usable = append(usable, c)
		}
	}
	sort.Slice(usable, func(i, j int) bool {
		if !usable[i].Coupon.EndAt.Equal(usable[j].Coupon.EndAt) {
			return usable[i].Coupon.EndAt.Before(usable[j].Coupon.EndAt)
		}
		return usable[i].UserCouponID < usable[j].UserCouponID
	})
	pickBest := func(match func(OwnedCoupon) bool) (OwnedCoupon, domain.Money, []*lineState, bool) {
		var best OwnedCoupon
		var bestD domain.Money
		var bestLines []*lineState
		for _, c := range usable {
			if !match(c) {
				continue
			}
			lines := couponLines(states, c.Coupon)
			if d := couponDiscount(c.Coupon, sum(lines)); d > bestD {
				best, bestD, bestLines = c, d, lines
			}
		}
		return best, bestD, bestLines, bestD > 0
	}
	var used []string
	var merchants []string
	for _, s := range states {
		if !contains(merchants, s.MerchantID) {
			merchants = append(merchants, s.MerchantID)
		}
	}
	for _, m := range merchants {
		if c, d, lines, ok := pickBest(func(c OwnedCoupon) bool {
			return c.Coupon.Scope == domain.ScopeMerchant && c.Coupon.MerchantID == m
		}); ok {
			apply(c, d, lines)
			used = append(used, c.UserCouponID)
		}
	}
	if c, d, lines, ok := pickBest(func(c OwnedCoupon) bool { return c.Coupon.Scope == domain.ScopePlatform }); ok {
		apply(c, d, lines)
		used = append(used, c.UserCouponID)
	}
	return used, nil
}

// allocate 把 discount 按各商品当前金额比例分摊（向下取整到分，余数按最大余数法分配，并列时按顺序）。
// 调用方保证 discount ≤ 这些商品当前金额之和。
func allocate(lines []*lineState, discount domain.Money) {
	base := sum(lines)
	if base == 0 || discount == 0 {
		return
	}
	shares := make([]int64, len(lines))
	rems := make([]int64, len(lines))
	var given int64
	for i, s := range lines {
		num := int64(discount) * int64(s.current)
		shares[i] = num / int64(base)
		rems[i] = num % int64(base)
		given += shares[i]
	}
	order := make([]int, len(lines))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rems[order[a]] > rems[order[b]] })
	for k := 0; given < int64(discount); k++ {
		i := order[k%len(order)]
		if shares[i] < int64(lines[i].current) {
			shares[i]++
			given++
		}
	}
	for i, s := range lines {
		s.current -= domain.Money(shares[i])
	}
}

func sum(lines []*lineState) domain.Money {
	var total domain.Money
	for _, s := range lines {
		total += s.current
	}
	return total
}

func ids(lines []*lineState) []string {
	out := make([]string, len(lines))
	for i, s := range lines {
		out[i] = s.CartItemID
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// DescribePromotion 返回促销的简短说明，例如“满 300 减 30”“9.5 折”“满 500 打 9 折”。
func DescribePromotion(p domain.PromotionRule) string {
	if p.Type == domain.PromotionDiscount {
		zhe := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", p.DiscountRate/1000, p.DiscountRate%1000), "0"), ".")
		if p.ThresholdAmount > 0 {
			return fmt.Sprintf("满 %s 打 %s 折", trimMoney(p.ThresholdAmount), zhe)
		}
		return zhe + " 折"
	}
	return describeThreshold(p.ThresholdAmount, p.DiscountAmount)
}

// DescribeCoupon 返回券的简短说明，例如“满 200 减 20”“立减 5”。
func DescribeCoupon(c domain.Coupon) string {
	return describeThreshold(c.ThresholdAmount, c.DiscountAmount)
}

func describeThreshold(threshold, amount domain.Money) string {
	if threshold == 0 {
		return "立减 " + trimMoney(amount)
	}
	return fmt.Sprintf("满 %s 减 %s", trimMoney(threshold), trimMoney(amount))
}

// trimMoney 去掉多余的 0：30.00 → 30，9.90 → 9.9。
func trimMoney(m domain.Money) string {
	s := m.String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
