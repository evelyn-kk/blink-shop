package pricing

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

var (
	now   = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	start = now.Add(-24 * time.Hour)
	end   = now.Add(24 * time.Hour)
	m     = domain.MustMoney
)

func line(id, product, merchant, price string, qty int, cats ...string) Line {
	return Line{CartItemID: id, ProductID: product, MerchantID: merchant, CategoryIDs: cats, UnitPrice: m(price), Quantity: qty}
}

func fullReduction(id, scope, target, threshold, amount string, stackable bool) domain.PromotionRule {
	p := domain.PromotionRule{PromotionID: id, Name: id, Scope: scope, Type: domain.PromotionFullReduction,
		ThresholdAmount: m(threshold), DiscountAmount: m(amount), Stackable: stackable, StartAt: start, EndAt: end, Status: domain.StatusActive}
	setTarget(&p, scope, target)
	return p
}

func discount(id, scope, target, rate string, stackable bool) domain.PromotionRule {
	p := domain.PromotionRule{PromotionID: id, Name: id, Scope: scope, Type: domain.PromotionDiscount,
		DiscountRate: domain.MustRate(rate), Stackable: stackable, StartAt: start, EndAt: end, Status: domain.StatusActive}
	setTarget(&p, scope, target)
	return p
}

func setTarget(p *domain.PromotionRule, scope, target string) {
	switch scope {
	case domain.ScopeMerchant:
		p.MerchantID = target
	case domain.ScopeProduct:
		p.ProductID = target
	case domain.ScopeCategory:
		p.CategoryID = target
	}
}

func coupon(ucID, scope, merchant, threshold, amount string) OwnedCoupon {
	return OwnedCoupon{UserCouponID: ucID, Status: domain.UserCouponUnused, Coupon: domain.Coupon{
		CouponID: "c_" + ucID, Name: ucID, Scope: scope, MerchantID: merchant, Type: domain.CouponFixedAmount,
		ThresholdAmount: m(threshold), DiscountAmount: m(amount), StartAt: start, EndAt: end, Status: domain.StatusActive,
	}}
}

// summary 把结果压成便于比较的字符串：合计/优惠/实付，每条优惠明细，每个商品的实付。
type summary struct {
	Total, Discount, Pay string
	Lines                []string
	Items                map[string]string
}

func summarize(r Result) summary {
	s := summary{Total: r.TotalAmount.String(), Discount: r.DiscountAmount.String(), Pay: r.PayAmount.String(), Items: map[string]string{}}
	for _, l := range r.Lines {
		s.Lines = append(s.Lines, fmt.Sprintf("%s:%s=%s", l.Type, l.ID, l.Amount))
	}
	for _, it := range r.Items {
		s.Items[it.CartItemID] = it.PayAmount.String()
	}
	return s
}

func mustCompute(t *testing.T, in Input) Result {
	t.Helper()
	if in.Now.IsZero() {
		in.Now = now
	}
	r, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	checkInvariants(t, in, r)
	return r
}

// checkInvariants：分摊之和等于明细之和等于总优惠；商品实付不为负；店铺小计相加等于整单。
func checkInvariants(t *testing.T, in Input, r Result) {
	t.Helper()
	var itemDiscount, lineDiscount, total domain.Money
	for _, it := range r.Items {
		if it.PayAmount < 0 || it.Discount < 0 || it.Amount != it.Discount+it.PayAmount {
			t.Fatalf("item %+v", it)
		}
		itemDiscount += it.Discount
		total += it.Amount
	}
	for _, l := range r.Lines {
		if l.Amount <= 0 {
			t.Fatalf("non-positive discount line %+v", l)
		}
		lineDiscount += l.Amount
	}
	if itemDiscount != lineDiscount || lineDiscount != r.DiscountAmount || total != r.TotalAmount || r.PayAmount != r.TotalAmount-r.DiscountAmount {
		t.Fatalf("totals: items=%s lines=%s discount=%s total=%s pay=%s", itemDiscount, lineDiscount, r.DiscountAmount, r.TotalAmount, r.PayAmount)
	}
	var mt, md, mp domain.Money
	for _, mr := range r.Merchants {
		mt, md, mp = mt+mr.TotalAmount, md+mr.DiscountAmount, mp+mr.PayAmount
	}
	if mt != r.TotalAmount || md != r.DiscountAmount || mp != r.PayAmount {
		t.Fatalf("merchant subtotals do not add up: %+v", r.Merchants)
	}
}

func TestRules(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want summary
	}{
		{"empty cart", Input{}, summary{Total: "0.00", Discount: "0.00", Pay: "0.00", Items: map[string]string{}}},
		{"platform full reduction met", Input{
			Lines:      []Line{line("a", "p1", "m1", "150", 2)},
			Promotions: []domain.PromotionRule{fullReduction("pf", domain.ScopePlatform, "", "300", "30", true)},
		}, summary{"300.00", "30.00", "270.00", []string{"promotion:pf=30.00"}, map[string]string{"a": "270.00"}}},
		{"threshold not met", Input{
			Lines:      []Line{line("a", "p1", "m1", "299.99", 1)},
			Promotions: []domain.PromotionRule{fullReduction("pf", domain.ScopePlatform, "", "300", "30", true)},
		}, summary{"299.99", "0.00", "299.99", nil, map[string]string{"a": "299.99"}}},
		{"merchant promotion only counts that merchant", Input{
			Lines:      []Line{line("a", "p1", "m1", "600", 1), line("b", "p2", "m2", "600", 1)},
			Promotions: []domain.PromotionRule{fullReduction("pm", domain.ScopeMerchant, "m1", "1000", "80", true)},
		}, summary{"1200.00", "0.00", "1200.00", nil, map[string]string{"a": "600.00", "b": "600.00"}}},
		{"non-stackable product discount excludes item from platform promotion", Input{
			Lines: []Line{line("a", "earbuds", "m1", "200", 1), line("b", "mouse", "m1", "150", 1)},
			Promotions: []domain.PromotionRule{
				discount("p95", domain.ScopeProduct, "earbuds", "0.95", false),
				fullReduction("pf", domain.ScopePlatform, "", "300", "30", true),
			},
		}, summary{"350.00", "10.00", "340.00", []string{"promotion:p95=10.00"}, map[string]string{"a": "190.00", "b": "150.00"}}},
		{"stackable levels apply narrow to broad on reduced amounts", Input{
			Lines: []Line{line("a", "p1", "m1", "320", 1, "c_mouse", "c_digital")},
			Promotions: []domain.PromotionRule{
				fullReduction("pf", domain.ScopePlatform, "", "300", "30", true),
				discount("pc", domain.ScopeCategory, "c_digital", "0.9", true), // 先打 9 折：288，平台满 300 不再满足
			},
		}, summary{"320.00", "32.00", "288.00", []string{"promotion:pc=32.00"}, map[string]string{"a": "288.00"}}},
		{"same level picks the larger discount first, both apply when stackable", Input{
			Lines: []Line{line("a", "p1", "m1", "1000", 1)},
			Promotions: []domain.PromotionRule{
				fullReduction("pm_small", domain.ScopeMerchant, "m1", "500", "20", true),
				fullReduction("pm_big", domain.ScopeMerchant, "m1", "900", "100", true),
			},
		}, summary{"1000.00", "120.00", "880.00", []string{"promotion:pm_big=100.00", "promotion:pm_small=20.00"}, map[string]string{"a": "880.00"}}},
		{"non-stackable promotion skips items already promoted", Input{
			Lines: []Line{line("a", "p1", "m1", "100", 1), line("b", "p2", "m1", "100", 1)},
			Promotions: []domain.PromotionRule{
				discount("pp", domain.ScopeProduct, "p1", "0.9", true),
				fullReduction("pm", domain.ScopeMerchant, "m1", "0", "50", false), // 只作用于 b
			},
		}, summary{"200.00", "60.00", "140.00", []string{"promotion:pp=10.00", "promotion:pm=50.00"}, map[string]string{"a": "90.00", "b": "50.00"}}},
		{"discount larger than amount is capped", Input{
			Lines:      []Line{line("a", "p1", "m1", "49.99", 1)},
			Promotions: []domain.PromotionRule{fullReduction("pf", domain.ScopePlatform, "", "0", "100", true)},
		}, summary{"49.99", "49.99", "0.00", []string{"promotion:pf=49.99"}, map[string]string{"a": "0.00"}}},
		{"product promotion of another merchant does not apply", Input{
			Lines: []Line{line("a", "p1", "m1", "100", 1)},
			Promotions: []domain.PromotionRule{func() domain.PromotionRule {
				p := discount("px", domain.ScopeProduct, "p1", "0.5", true)
				p.MerchantID = "m2"
				return p
			}()},
		}, summary{"100.00", "0.00", "100.00", nil, map[string]string{"a": "100.00"}}},
		{"end_at is exclusive", Input{
			Lines: []Line{line("a", "p1", "m1", "500", 1)},
			Promotions: func() []domain.PromotionRule {
				p := fullReduction("ending", domain.ScopePlatform, "", "0", "1", true)
				p.EndAt = now
				return []domain.PromotionRule{p}
			}(),
			Coupons: func() []OwnedCoupon {
				c := coupon("uc_ending", domain.ScopePlatform, "", "0", "2")
				c.Coupon.EndAt = now
				return []OwnedCoupon{c}
			}(),
		}, summary{"500.00", "0.00", "500.00", nil, map[string]string{"a": "500.00"}}},
		{"inactive, expired and future promotions are ignored", Input{
			Lines: []Line{line("a", "p1", "m1", "500", 1)},
			Promotions: func() []domain.PromotionRule {
				off := fullReduction("off", domain.ScopePlatform, "", "0", "1", true)
				off.Status = domain.StatusInactive
				old := fullReduction("old", domain.ScopePlatform, "", "0", "2", true)
				old.EndAt = now.Add(-time.Second)
				future := fullReduction("future", domain.ScopePlatform, "", "0", "3", true)
				future.StartAt = now.Add(time.Second)
				return []domain.PromotionRule{off, old, future}
			}(),
		}, summary{"500.00", "0.00", "500.00", nil, map[string]string{"a": "500.00"}}},
		{"rounding: 95 折 rounds half up and allocates to the cent", Input{
			Lines:      []Line{line("a", "p1", "m1", "33.33", 1), line("b", "p2", "m1", "33.33", 1), line("c", "p3", "m1", "33.33", 1)},
			Promotions: []domain.PromotionRule{discount("pm", domain.ScopeMerchant, "m1", "0.95", true)},
			// 99.99 × 0.95 = 94.9905 → 94.99，优惠 5.00，按 1.67 / 1.67 / 1.66 分摊
		}, summary{"99.99", "5.00", "94.99", []string{"promotion:pm=5.00"}, map[string]string{"a": "31.66", "b": "31.66", "c": "31.67"}}},
		{"auto coupons: best merchant coupon then platform coupon after promotions", Input{
			Lines:      []Line{line("a", "p1", "m1", "600", 1), line("b", "p2", "m2", "100", 1)},
			Promotions: []domain.PromotionRule{fullReduction("pf", domain.ScopePlatform, "", "300", "30", true)},
			Coupons: []OwnedCoupon{
				coupon("uc_m1_small", domain.ScopeMerchant, "m1", "100", "10"),
				coupon("uc_m1_big", domain.ScopeMerchant, "m1", "500", "50"),
				coupon("uc_m2", domain.ScopeMerchant, "m2", "200", "20"), // m2 只有 100，门槛不够
				coupon("uc_pf", domain.ScopePlatform, "", "600", "60"),   // 活动和店铺券后为 700−30−50 = 620，满 600
			},
			// 满减 30 按 600:100 分摊为 25.71 / 4.29；店铺券 50 只给 a（524.29）；平台券 60 按 524.29:95.71 分摊为 50.74 / 9.26。
		}, summary{"700.00", "140.00", "560.00", []string{"promotion:pf=30.00", "coupon:uc_m1_big=50.00", "coupon:uc_pf=60.00"},
			map[string]string{"a": "473.55", "b": "86.45"}}},
		{"locked items cannot use coupons", Input{
			Lines:      []Line{line("a", "earbuds", "m1", "500", 1)},
			Promotions: []domain.PromotionRule{discount("p95", domain.ScopeProduct, "earbuds", "0.95", false)},
			Coupons:    []OwnedCoupon{coupon("uc_pf", domain.ScopePlatform, "", "0", "20")},
		}, summary{"500.00", "25.00", "475.00", []string{"promotion:p95=25.00"}, map[string]string{"a": "475.00"}}},
		{"used and expired coupons are ignored", Input{
			Lines: []Line{line("a", "p1", "m1", "500", 1)},
			Coupons: func() []OwnedCoupon {
				used := coupon("uc_used", domain.ScopePlatform, "", "0", "10")
				used.Status = domain.UserCouponUsed
				expired := coupon("uc_expired", domain.ScopePlatform, "", "0", "20")
				expired.Coupon.EndAt = now.Add(-time.Minute)
				return []OwnedCoupon{used, expired}
			}(),
		}, summary{"500.00", "0.00", "500.00", nil, map[string]string{"a": "500.00"}}},
		{"explicit empty choice uses no coupon", Input{
			Lines:        []Line{line("a", "p1", "m1", "500", 1)},
			Coupons:      []OwnedCoupon{coupon("uc_pf", domain.ScopePlatform, "", "0", "20")},
			CouponChoice: []string{},
		}, summary{"500.00", "0.00", "500.00", nil, map[string]string{"a": "500.00"}}},
		{"explicit choice overrides the automatic best", Input{
			Lines:        []Line{line("a", "p1", "m1", "500", 1)},
			Coupons:      []OwnedCoupon{coupon("uc_small", domain.ScopePlatform, "", "0", "5"), coupon("uc_big", domain.ScopePlatform, "", "0", "50")},
			CouponChoice: []string{"uc_small"},
		}, summary{"500.00", "5.00", "495.00", []string{"coupon:uc_small=5.00"}, map[string]string{"a": "495.00"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := summarize(mustCompute(t, c.in))
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestHintsAndDescriptions(t *testing.T) {
	r := mustCompute(t, Input{
		Lines: []Line{line("a", "p1", "m1", "250", 1)},
		Promotions: []domain.PromotionRule{
			fullReduction("pf", domain.ScopePlatform, "", "300", "30", true),
			discount("pc", domain.ScopeCategory, "c_x", "0.88", true),
		},
	})
	if len(r.Hints) != 1 || r.Hints[0].PromotionID != "pf" || r.Hints[0].Shortfall != m("50") {
		t.Fatalf("hints = %+v", r.Hints)
	}
	for in, want := range map[domain.PromotionRule]string{
		fullReduction("a", domain.ScopePlatform, "", "300", "30", true): "满 300 减 30",
		fullReduction("b", domain.ScopePlatform, "", "0", "9.90", true): "立减 9.9",
		discount("c", domain.ScopePlatform, "", "0.95", true):           "9.5 折",
		discount("d", domain.ScopePlatform, "", "0.8", true):            "8 折",
		discount("e", domain.ScopePlatform, "", "0.885", true):          "8.85 折",
		func() domain.PromotionRule {
			p := discount("f", domain.ScopePlatform, "", "0.9", true)
			p.ThresholdAmount = m("199")
			return p
		}(): "满 199 打 9 折",
	} {
		if got := describePromotion(in); got != want {
			t.Errorf("%s: %q, want %q", in.PromotionID, got, want)
		}
	}
}

func TestExplicitCouponErrors(t *testing.T) {
	lines := []Line{line("a", "p1", "m1", "100", 1), line("b", "p2", "m2", "100", 1)}
	used := coupon("uc_used", domain.ScopePlatform, "", "0", "1")
	used.Status = domain.UserCouponUsed
	expired := coupon("uc_expired", domain.ScopePlatform, "", "0", "1")
	expired.Coupon.EndAt = now.Add(-time.Second)
	future := coupon("uc_future", domain.ScopePlatform, "", "0", "1")
	future.Coupon.StartAt = now.Add(time.Hour)
	off := coupon("uc_off", domain.ScopePlatform, "", "0", "1")
	off.Coupon.Status = domain.StatusInactive
	owned := []OwnedCoupon{used, expired, future, off,
		coupon("uc_p1", domain.ScopePlatform, "", "0", "1"), coupon("uc_p2", domain.ScopePlatform, "", "0", "2"),
		coupon("uc_m1a", domain.ScopeMerchant, "m1", "0", "1"), coupon("uc_m1b", domain.ScopeMerchant, "m1", "0", "2"),
		coupon("uc_big", domain.ScopePlatform, "", "1000", "100"), coupon("uc_m3", domain.ScopeMerchant, "m3", "0", "1"),
	}
	for _, c := range []struct {
		choice []string
		id     string
	}{
		{[]string{"uc_missing"}, "uc_missing"},
		{[]string{"uc_used"}, "uc_used"},
		{[]string{"uc_expired"}, "uc_expired"},
		{[]string{"uc_future"}, "uc_future"},
		{[]string{"uc_off"}, "uc_off"},
		{[]string{"uc_p1", "uc_p2"}, "uc_p2"},
		{[]string{"uc_m1a", "uc_m1b"}, "uc_m1b"},
		{[]string{"uc_big"}, "uc_big"}, // 门槛不够
		{[]string{"uc_m3"}, "uc_m3"},   // 购物车里没有 m3 的商品
	} {
		_, err := Compute(Input{Lines: lines, Coupons: owned, CouponChoice: c.choice, Now: now})
		var ce *CouponError
		if !errors.As(err, &ce) || ce.UserCouponID != c.id || ce.Reason == "" {
			t.Errorf("%v: err = %v, want CouponError for %s", c.choice, err, c.id)
		}
	}
	// 一张平台券 + 每个店铺一张店铺券可以同时使用。
	r := mustCompute(t, Input{Lines: lines, Coupons: owned, CouponChoice: []string{"uc_p1", "uc_m1a"}})
	if !reflect.DeepEqual(r.UserCouponIDs, []string{"uc_m1a", "uc_p1"}) {
		t.Fatalf("used = %v", r.UserCouponIDs)
	}
	if _, err := Compute(Input{Lines: []Line{line("a", "p1", "m1", "1", 0)}, Now: now}); !errors.Is(err, ErrInvalidLine) {
		t.Fatalf("zero quantity err = %v", err)
	}
}

// TestRandomInvariants：随机购物车、活动和券，验证金额不变量始终成立，且结果与输入顺序无关。
func TestRandomInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(20261002))
	merchants := []string{"m1", "m2", "m3"}
	cats := []string{"c1", "c2"}
	for iter := 0; iter < 2000; iter++ {
		var in Input
		in.Now = now
		for i := 0; i < 1+rng.Intn(6); i++ {
			in.Lines = append(in.Lines, Line{
				CartItemID: fmt.Sprintf("ci%d", i), ProductID: fmt.Sprintf("p%d", rng.Intn(4)), MerchantID: merchants[rng.Intn(3)],
				CategoryIDs: []string{cats[rng.Intn(2)]}, UnitPrice: domain.Money(rng.Int63n(100000)), Quantity: 1 + rng.Intn(5),
			})
		}
		for i := 0; i < rng.Intn(6); i++ {
			scope := promotionLevels[rng.Intn(4)]
			target := map[string]string{domain.ScopeMerchant: merchants[rng.Intn(3)], domain.ScopeProduct: fmt.Sprintf("p%d", rng.Intn(4)), domain.ScopeCategory: cats[rng.Intn(2)]}[scope]
			id := fmt.Sprintf("promo%d", i)
			if rng.Intn(2) == 0 {
				in.Promotions = append(in.Promotions, fullReduction(id, scope, target, fmt.Sprint(rng.Intn(1000)), fmt.Sprint(rng.Intn(300)), rng.Intn(2) == 0))
			} else {
				in.Promotions = append(in.Promotions, discount(id, scope, target, fmt.Sprintf("0.%02d", 50+rng.Intn(50)), rng.Intn(2) == 0))
			}
		}
		for i := 0; i < rng.Intn(4); i++ {
			if rng.Intn(2) == 0 {
				in.Coupons = append(in.Coupons, coupon(fmt.Sprintf("uc%d", i), domain.ScopePlatform, "", fmt.Sprint(rng.Intn(800)), fmt.Sprint(1+rng.Intn(100))))
			} else {
				in.Coupons = append(in.Coupons, coupon(fmt.Sprintf("uc%d", i), domain.ScopeMerchant, merchants[rng.Intn(3)], fmt.Sprint(rng.Intn(800)), fmt.Sprint(1+rng.Intn(100))))
			}
		}
		r := mustCompute(t, in)
		// 打乱活动和券的顺序，结果不变。
		shuffled := in
		shuffled.Promotions = append([]domain.PromotionRule{}, in.Promotions...)
		shuffled.Coupons = append([]OwnedCoupon{}, in.Coupons...)
		rng.Shuffle(len(shuffled.Promotions), func(i, j int) {
			shuffled.Promotions[i], shuffled.Promotions[j] = shuffled.Promotions[j], shuffled.Promotions[i]
		})
		rng.Shuffle(len(shuffled.Coupons), func(i, j int) { shuffled.Coupons[i], shuffled.Coupons[j] = shuffled.Coupons[j], shuffled.Coupons[i] })
		if r2 := mustCompute(t, shuffled); !reflect.DeepEqual(summarize(r), summarize(r2)) {
			t.Fatalf("order-dependent result at iteration %d:\n%+v\n%+v", iter, summarize(r), summarize(r2))
		}
	}
}
