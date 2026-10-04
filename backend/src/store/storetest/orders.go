package storetest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func orderCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"CheckoutFlowAndRelease", testCheckoutFlow},
		{"CheckoutRollsBackOnError", testCheckoutRollback},
		{"CheckoutConcurrentStock", testCheckoutConcurrentStock},
		{"CheckoutSameKeyConcurrent", testCheckoutSameKey},
		{"CheckoutKeepsCartOrder", testCheckoutItemOrder},
		{"OrderUpdateRules", testOrderUpdateRules},
		{"ExpiredOrderIDs", testExpiredOrders},
		{"CreateReviewRules", testCreateReview},
	}
}

// planBy 按店铺拆单、不计优惠；discounts / coupons 按店铺指定优惠金额和用券。
func planBy(st store.CheckoutState, discounts map[string]string, coupons map[string][]string) store.CheckoutPlan {
	byMerchant := map[string]*store.PlannedOrder{}
	var order []string
	for _, l := range st.Lines {
		po := byMerchant[l.MerchantID]
		if po == nil {
			po = &store.PlannedOrder{MerchantID: l.MerchantID, UserCouponIDs: coupons[l.MerchantID]}
			byMerchant[l.MerchantID] = po
			order = append(order, l.MerchantID)
		}
		po.Items = append(po.Items, store.PlannedItem{CartItemID: l.CartItemID, OrderItem: domain.OrderItem{
			ProductID: l.ProductID, SkuID: l.SkuID, Name: l.ProductName, SkuName: l.SkuName, ImageURL: l.ImageURL, Price: l.UnitPrice,
			Quantity: l.Quantity, MerchantID: l.MerchantID, MerchantName: l.MerchantName}})
		po.TotalAmount += l.UnitPrice.Mul(l.Quantity)
	}
	plan := store.CheckoutPlan{PaymentDeadline: couponAt.Add(30 * time.Minute)}
	for _, id := range order {
		po := byMerchant[id]
		if d, ok := discounts[id]; ok {
			po.DiscountAmount = domain.MustMoney(d)
		}
		po.PayAmount = po.TotalAmount - po.DiscountAmount
		plan.Orders = append(plan.Orders, *po)
	}
	return plan
}

func simplePlan(_ context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
	return planBy(st, nil, nil), nil
}

func skuStock(t *testing.T, s store.Store, productID, skuID string) (sku, product int) {
	t.Helper()
	p, err := s.GetProduct(context.Background(), productID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p.SKUs {
		if it.SkuID == skuID {
			if it.StockStatus != domain.StockStatusOf(it.StockQuantity) {
				t.Fatalf("sku %s stock_status %s for %d", skuID, it.StockStatus, it.StockQuantity)
			}
			sku = it.StockQuantity
		}
	}
	if p.StockStatus != domain.StockStatusOf(p.StockQuantity) {
		t.Fatalf("product %s stock_status %s for %d", productID, p.StockStatus, p.StockQuantity)
	}
	return sku, p.StockQuantity
}

func couponState(t *testing.T, s store.Store, accountID, userCouponID string) (status, orderID string) {
	t.Helper()
	list, _, err := s.ListUserCoupons(context.Background(), store.UserCouponQuery{AccountID: accountID, At: couponAt, Page: store.Page{Page: 1, PageSize: 100}})
	if err != nil {
		t.Fatal(err)
	}
	for _, oc := range list {
		if oc.UserCouponID == userCouponID {
			return oc.Status, oc.OrderID
		}
	}
	t.Fatalf("coupon %s not found", userCouponID)
	return "", ""
}

func cancel(reason string) func(o *domain.Order, p *domain.Payment) error {
	return func(o *domain.Order, p *domain.Payment) error {
		now := couponAt.Add(time.Minute)
		o.Status, o.ClosedAt, o.CancelReason = domain.OrderCancelled, &now, reason
		if p != nil {
			p.Status = domain.PaymentClosed
		}
		return nil
	}
}

func testCheckoutFlow(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	digitalCoupon, err := s.ClaimCoupon(ctx, seed.User2ID, "coupon_seed_digital", couponAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_earbuds", "sku_seed_earbuds_white", add(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_lamp", "sku_seed_lamp_white", add(1)); err != nil {
		t.Fatal(err)
	}
	mouse, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_mouse", "sku_seed_mouse_gray", add(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCartItem(ctx, seed.User2ID, mouse.CartItemID, func(it *domain.CartItem, _ store.CartLine) error { it.Selected = false; return nil }); err != nil {
		t.Fatal(err)
	}

	calls := 0
	res, err := s.Checkout(ctx, seed.User2ID, "key-1", func(txCtx context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
		calls++
		// fn 的 ctx 属于结算事务：计价数据在事务里读取（内存实现若不是同一事务会在全局锁上卡死）。
		if promos, _, err := s.ListActivePromotions(txCtx, store.PromotionQuery{At: couponAt, Page: store.Page{Page: 1, PageSize: 100}}); err != nil || len(promos) == 0 {
			return store.CheckoutPlan{}, fmt.Errorf("promotions in tx = %v, %v", promos, err)
		}
		if len(st.Lines) != 2 || st.Lines[0].ProductID != "p_seed_earbuds" || st.Lines[0].StockQuantity != 5 || st.Lines[1].MerchantName == "" {
			return store.CheckoutPlan{}, fmt.Errorf("unexpected lines %+v", st.Lines)
		}
		ids := map[string]bool{}
		for _, c := range st.Coupons {
			ids[c.UserCouponID] = c.Status == domain.UserCouponUnused && c.Coupon.CouponID != ""
		}
		if len(st.Coupons) != 2 || !ids[digitalCoupon.UserCouponID] || !ids["uc_seed_user2_platform"] {
			return store.CheckoutPlan{}, fmt.Errorf("unexpected coupons %+v", st.Coupons)
		}
		// 数码店：店铺券 50 + 平台券分摊 10；家居店：平台券分摊 10。平台券同时用在两个订单上。
		return planBy(st, map[string]string{seed.DigitalMerchant: "60", seed.HomeMerchant: "10"}, map[string][]string{
			seed.DigitalMerchant: {digitalCoupon.UserCouponID, "uc_seed_user2_platform"}, seed.HomeMerchant: {"uc_seed_user2_platform"}}), nil
	})
	if err != nil || calls != 1 || res.Replayed || res.RequestID == "" || len(res.Orders) != 2 {
		t.Fatalf("checkout = %+v, %v (calls %d)", res, err, calls)
	}
	digital, home := res.Orders[0], res.Orders[1]
	if digital.MerchantID != seed.DigitalMerchant || digital.Status != domain.OrderPendingPayment || digital.TotalAmount.String() != "1198.00" ||
		digital.DiscountAmount.String() != "60.00" || digital.PayAmount.String() != "1138.00" || digital.MerchantName == "" ||
		digital.CheckoutRequestID != res.RequestID || len(digital.OrderNo) != 22 || digital.PaymentDeadlineAt == nil ||
		!digital.PaymentDeadlineAt.Equal(couponAt.Add(30*time.Minute)) {
		t.Fatalf("digital order = %+v", digital)
	}
	if len(digital.Items) != 1 || digital.Items[0].Name != "Blink Air 降噪耳机" || digital.Items[0].SkuName != "Blink Air 白色" ||
		digital.Items[0].Price.String() != "599.00" || digital.Items[0].Quantity != 2 || digital.Items[0].OrderItemID == "" {
		t.Fatalf("digital items = %+v", digital.Items)
	}
	if p := digital.Payment; p == nil || p.Status != domain.PaymentPending || p.Amount != digital.PayAmount || !p.ExpiresAt.Equal(couponAt.Add(30*time.Minute)) {
		t.Fatalf("payment = %+v", digital.Payment)
	}
	if home.MerchantID != seed.HomeMerchant || home.PayAmount.String() != "239.00" || home.OrderNo == digital.OrderNo {
		t.Fatalf("home order = %+v", home)
	}
	if sku, product := skuStock(t, s, "p_seed_earbuds", "sku_seed_earbuds_white"); sku != 3 || product != 3 {
		t.Fatalf("earbuds stock = %d / %d", sku, product)
	}
	if sku, _ := skuStock(t, s, "p_seed_lamp", "sku_seed_lamp_white"); sku != 59 {
		t.Fatalf("lamp stock = %d", sku)
	}
	lines, _ := s.ListCartLines(ctx, seed.User2ID)
	if len(lines) != 1 || lines[0].CartItemID != mouse.CartItemID {
		t.Fatalf("cart after checkout = %+v", lines)
	}
	if st, oid := couponState(t, s, seed.User2ID, digitalCoupon.UserCouponID); st != domain.UserCouponUsed || oid != digital.OrderID {
		t.Fatalf("digital coupon = %s %s", st, oid)
	}
	if st, oid := couponState(t, s, seed.User2ID, "uc_seed_user2_platform"); st != domain.UserCouponUsed || oid != digital.OrderID {
		t.Fatalf("platform coupon = %s %s", st, oid)
	}

	// 同一幂等键：不再调用 fn，返回同一批订单。
	again, err := s.Checkout(ctx, seed.User2ID, "key-1", func(context.Context, store.CheckoutState) (store.CheckoutPlan, error) {
		t.Fatal("fn called on replay")
		return store.CheckoutPlan{}, nil
	})
	if err != nil || !again.Replayed || again.RequestID != res.RequestID || len(again.Orders) != 2 || again.Orders[0].OrderID != digital.OrderID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	// 另一个账户用同一个键互不影响（购物车为空，fn 收到空列表）。
	if _, err := s.Checkout(ctx, seed.UserID, "key-1", func(_ context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
		if len(st.Lines) != 0 {
			t.Fatalf("other account lines = %+v", st.Lines)
		}
		return store.CheckoutPlan{}, errors.New("empty")
	}); err == nil || err.Error() != "empty" {
		t.Fatalf("other account = %v", err)
	}

	// 查询。
	page := store.Page{Page: 1, PageSize: 10}
	for _, c := range []struct {
		q    store.OrderQuery
		want int
	}{
		{store.OrderQuery{AccountID: seed.User2ID, Page: page}, 2},
		{store.OrderQuery{AccountID: seed.User2ID, MerchantID: seed.HomeMerchant, Page: page}, 1},
		{store.OrderQuery{MerchantID: seed.HomeMerchant, Page: page}, 1},
		{store.OrderQuery{AccountID: seed.UserID, Page: page}, 5},
		{store.OrderQuery{AccountID: seed.UserID, Status: domain.OrderShipped, Page: page}, 1},
		{store.OrderQuery{OrderNo: home.OrderNo, Page: page}, 1},
		{store.OrderQuery{Page: page}, 7},
		{store.OrderQuery{AccountID: seed.UserID, Page: store.Page{Page: 2, PageSize: 3}}, 5},
	} {
		got, total, err := s.ListOrders(ctx, c.q)
		if err != nil || total != c.want {
			t.Fatalf("ListOrders(%+v) total = %d, %v", c.q, total, err)
		}
		for _, d := range got {
			if len(d.Items) == 0 || d.MerchantName == "" {
				t.Fatalf("list item without details: %+v", d)
			}
		}
		if c.q.Page.Page == 2 && (len(got) != 2 || got[0].OrderID != "o_seed_paid" || got[1].OrderID != "o_seed_pending") {
			t.Fatalf("page 2 = %+v", got)
		}
	}
	list, _, _ := s.ListOrders(ctx, store.OrderQuery{AccountID: seed.UserID, Page: page})
	for i := 1; i < len(list); i++ {
		if list[i].CreatedAt.After(list[i-1].CreatedAt) {
			t.Fatalf("orders not newest first: %v", list)
		}
	}

	// 取消家居订单：台灯库存回补，平台券还用在数码订单上，不退。
	if _, err := s.UpdateOrder(ctx, home.OrderID, cancel("不想要了")); err != nil {
		t.Fatal(err)
	}
	if sku, _ := skuStock(t, s, "p_seed_lamp", "sku_seed_lamp_white"); sku != 60 {
		t.Fatalf("lamp stock after cancel = %d", sku)
	}
	if st, _ := couponState(t, s, seed.User2ID, "uc_seed_user2_platform"); st != domain.UserCouponUsed {
		t.Fatalf("platform coupon released early: %s", st)
	}
	// 取消数码订单：耳机库存回补，店铺券和平台券都退回。
	got, err := s.UpdateOrder(ctx, digital.OrderID, cancel("买错了"))
	if err != nil || got.Status != domain.OrderCancelled || got.Payment.Status != domain.PaymentClosed || got.CancelReason != "买错了" || got.ClosedAt == nil {
		t.Fatalf("cancel digital = %+v, %v", got, err)
	}
	if sku, product := skuStock(t, s, "p_seed_earbuds", "sku_seed_earbuds_white"); sku != 5 || product != 5 {
		t.Fatalf("earbuds stock after cancel = %d / %d", sku, product)
	}
	for _, id := range []string{digitalCoupon.UserCouponID, "uc_seed_user2_platform"} {
		if st, oid := couponState(t, s, seed.User2ID, id); st != domain.UserCouponUnused || oid != "" {
			t.Fatalf("coupon %s after cancel = %s %s", id, st, oid)
		}
	}
	// 已取消的订单再写一次 cancelled 是“状态未变”（由调用方拒绝）：不会再次回补库存。
	if _, err := s.UpdateOrder(ctx, digital.OrderID, cancel("again")); err != nil {
		t.Fatalf("same-status update = %v", err)
	}
	if sku, _ := skuStock(t, s, "p_seed_earbuds", "sku_seed_earbuds_white"); sku != 5 {
		t.Fatalf("stock restocked twice: %d", sku)
	}
	// 已取消的订单不能再支付。
	if _, err := s.UpdateOrder(ctx, digital.OrderID, func(o *domain.Order, p *domain.Payment) error {
		o.Status, p.Status = domain.OrderPaid, domain.PaymentPaid
		return nil
	}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("pay cancelled = %v", err)
	}
}

func testCheckoutRollback(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	if _, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_earbuds", "sku_seed_earbuds_white", add(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_lamp", "sku_seed_lamp_white", add(1)); err != nil {
		t.Fatal(err)
	}
	unchanged := func(label string) {
		t.Helper()
		if lines, _ := s.ListCartLines(ctx, seed.User2ID); len(lines) != 2 {
			t.Fatalf("%s: cart = %+v", label, lines)
		}
		if sku, product := skuStock(t, s, "p_seed_earbuds", "sku_seed_earbuds_white"); sku != 5 || product != 5 {
			t.Fatalf("%s: stock = %d / %d", label, sku, product)
		}
		if _, total, _ := s.ListOrders(ctx, store.OrderQuery{AccountID: seed.User2ID, Page: store.Page{Page: 1, PageSize: 10}}); total != 0 {
			t.Fatalf("%s: %d orders created", label, total)
		}
		if st, _ := couponState(t, s, seed.User2ID, "uc_seed_user2_platform"); st != domain.UserCouponUnused {
			t.Fatalf("%s: coupon %s", label, st)
		}
	}
	boom := errors.New("boom")
	if _, err := s.Checkout(ctx, seed.User2ID, "k", func(context.Context, store.CheckoutState) (store.CheckoutPlan, error) {
		return store.CheckoutPlan{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("fn error = %v", err)
	}
	unchanged("fn error")

	bad := []struct {
		name   string
		mutate func(p *store.CheckoutPlan)
	}{
		{"no orders", func(p *store.CheckoutPlan) { p.Orders = nil }},
		{"empty order", func(p *store.CheckoutPlan) { p.Orders[0].Items = nil }},
		{"quantity differs from cart", func(p *store.CheckoutPlan) {
			p.Orders[0].Items[0].Quantity = 1
			p.Orders[0].TotalAmount = domain.MustMoney("599")
		}},
		{"price differs from sku", func(p *store.CheckoutPlan) {
			p.Orders[0].Items[0].Price = domain.MustMoney("1")
			p.Orders[0].TotalAmount = domain.MustMoney("2")
		}},
		{"over stock", func(p *store.CheckoutPlan) {
			p.Orders[0].Items[0].Quantity = 6
			p.Orders[0].TotalAmount = domain.MustMoney("3594")
		}},
		{"unknown cart item", func(p *store.CheckoutPlan) { p.Orders[0].Items[0].CartItemID = "ci_unknown" }},
		{"duplicate item", func(p *store.CheckoutPlan) { p.Orders[1].Items = append(p.Orders[1].Items, p.Orders[0].Items[0]) }},
		{"wrong product", func(p *store.CheckoutPlan) { p.Orders[0].Items[0].ProductID = "p_seed_lamp" }},
		{"wrong merchant", func(p *store.CheckoutPlan) { p.Orders[0].MerchantID = seed.HomeMerchant }},
		{"total mismatch", func(p *store.CheckoutPlan) { p.Orders[0].TotalAmount += 1 }},
		{"pay mismatch", func(p *store.CheckoutPlan) { p.Orders[0].PayAmount -= 1 }},
		{"discount over total", func(p *store.CheckoutPlan) {
			p.Orders[0].DiscountAmount = p.Orders[0].TotalAmount + 1
			p.Orders[0].PayAmount = -1
		}},
		{"foreign coupon", func(p *store.CheckoutPlan) { p.Orders[0].UserCouponIDs = []string{"uc_seed_user_platform_used"} }},
		{"unknown coupon", func(p *store.CheckoutPlan) { p.Orders[0].UserCouponIDs = []string{"uc_missing"} }},
		// 最后一个订单才出错：前面的订单已写入，必须一起回滚。
		{"late failure", func(p *store.CheckoutPlan) {
			p.Orders[1].Items[0].Quantity = 61
			p.Orders[1].TotalAmount = domain.MustMoney("15189")
		}},
	}
	for _, c := range bad {
		_, err := s.Checkout(ctx, seed.User2ID, "k", func(_ context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
			p := planBy(st, nil, nil)
			for i := range p.Orders {
				p.Orders[i].PayAmount = p.Orders[i].TotalAmount
			}
			c.mutate(&p)
			if c.name == "over stock" || c.name == "late failure" || c.name == "quantity differs from cart" || c.name == "price differs from sku" {
				for i := range p.Orders {
					p.Orders[i].PayAmount = p.Orders[i].TotalAmount
				}
			}
			return p, nil
		})
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("%s: err = %v", c.name, err)
		}
		unchanged(c.name)
	}
	// 失败的键可以再用：成功一次后才固定。
	if res, err := s.Checkout(ctx, seed.User2ID, "k", simplePlan); err != nil || res.Replayed || len(res.Orders) != 2 {
		t.Fatalf("checkout after failures = %+v, %v", res, err)
	}
}

// testCheckoutConcurrentStock：8 个用户同时结算库存只有 5 件的耳机，恰好 5 个成功，库存为 0，不会为负。
func testCheckoutConcurrentStock(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	errSoldOut := errors.New("sold out")
	var accounts []string
	for i := range 8 {
		acc, err := s.CreateAccount(ctx, newUser(fmt.Sprintf("buyer_%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddCartItem(ctx, acc.AccountID, "p_seed_earbuds", "sku_seed_earbuds_white", add(1)); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, acc.AccountID)
	}
	var wg sync.WaitGroup
	results := make([]error, len(accounts))
	for i, acc := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = s.Checkout(ctx, acc, "buy", func(_ context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
				for _, l := range st.Lines {
					if l.Quantity > l.StockQuantity {
						return store.CheckoutPlan{}, errSoldOut
					}
				}
				return simplePlan(ctx, st)
			})
		}()
	}
	wg.Wait()
	ok, soldOut := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errSoldOut):
			soldOut++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 5 || soldOut != 3 {
		t.Fatalf("ok = %d, sold out = %d", ok, soldOut)
	}
	if sku, product := skuStock(t, s, "p_seed_earbuds", "sku_seed_earbuds_white"); sku != 0 || product != 0 {
		t.Fatalf("stock = %d / %d", sku, product)
	}
	_, total, _ := s.ListOrders(ctx, store.OrderQuery{MerchantID: seed.DigitalMerchant, Status: domain.OrderPendingPayment, Page: store.Page{Page: 1, PageSize: 100}})
	if total != 5+1 { // 加上种子里的 1 个待支付订单
		t.Fatalf("pending orders = %d", total)
	}
}

// testCheckoutSameKey：同一账户用同一个键并发结算 8 次，只下一次单，全部返回同一个结算请求。
func testCheckoutSameKey(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	if _, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_lamp", "sku_seed_lamp_white", add(3)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	requests := map[string]int{}
	fresh := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.Checkout(ctx, seed.User2ID, "same-key", simplePlan)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			requests[res.RequestID]++
			if !res.Replayed {
				fresh++
			}
		}()
	}
	wg.Wait()
	if len(requests) != 1 || fresh != 1 {
		t.Fatalf("requests = %v, fresh = %d", requests, fresh)
	}
	if sku, _ := skuStock(t, s, "p_seed_lamp", "sku_seed_lamp_white"); sku != 57 {
		t.Fatalf("lamp stock = %d", sku)
	}
	if _, total, _ := s.ListOrders(ctx, store.OrderQuery{AccountID: seed.User2ID, Page: store.Page{Page: 1, PageSize: 10}}); total != 1 {
		t.Fatalf("orders = %d", total)
	}
}

func testOrderUpdateRules(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	if _, err := s.UpdateOrder(ctx, "o_missing", func(*domain.Order, *domain.Payment) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	boom := errors.New("boom")
	if _, err := s.UpdateOrder(ctx, "o_seed_pending", func(o *domain.Order, p *domain.Payment) error {
		o.Status, p.Status = domain.OrderPaid, domain.PaymentPaid
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("fn error = %v", err)
	}
	if d, _ := s.GetOrder(ctx, "o_seed_pending"); d.Status != domain.OrderPendingPayment || d.Payment.Status != domain.PaymentPending {
		t.Fatalf("changed after fn error: %+v", d)
	}
	invalid := []struct {
		name string
		id   string
		fn   func(o *domain.Order, p *domain.Payment)
	}{
		{"paid without payment", "o_seed_pending", func(o *domain.Order, _ *domain.Payment) { o.Status = domain.OrderPaid }},
		{"payment without order", "o_seed_pending", func(_ *domain.Order, p *domain.Payment) { p.Status = domain.PaymentPaid }},
		{"skip to shipped", "o_seed_pending", func(o *domain.Order, p *domain.Payment) { o.Status, p.Status = domain.OrderShipped, domain.PaymentPaid }},
		{"change amount", "o_seed_pending", func(o *domain.Order, _ *domain.Payment) { o.PayAmount += 1 }},
		{"change owner", "o_seed_pending", func(o *domain.Order, _ *domain.Payment) { o.AccountID = seed.User2ID }},
		{"change payment amount", "o_seed_pending", func(_ *domain.Order, p *domain.Payment) { p.Amount += 1 }},
		{"cancel paid", "o_seed_paid", func(o *domain.Order, p *domain.Payment) { o.Status = domain.OrderCancelled }},
		{"reopen completed", "o_seed_completed", func(o *domain.Order, _ *domain.Payment) { o.Status = domain.OrderShipped }},
		{"pay closed payment", "o_seed_cancelled", func(o *domain.Order, p *domain.Payment) { o.Status, p.Status = domain.OrderPaid, domain.PaymentPaid }},
	}
	for _, c := range invalid {
		if _, err := s.UpdateOrder(ctx, c.id, func(o *domain.Order, p *domain.Payment) error { c.fn(o, p); return nil }); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("%s: err = %v", c.name, err)
		}
	}
	paidAt := couponAt
	got, err := s.UpdateOrder(ctx, "o_seed_pending", func(o *domain.Order, p *domain.Payment) error {
		o.Status, o.PaidAt = domain.OrderPaid, &paidAt
		p.Status, p.PaidAt, p.Method, p.TransactionNo = domain.PaymentPaid, &paidAt, "mock_balance", "MOCKTXN1"
		return nil
	})
	if err != nil || got.Status != domain.OrderPaid || got.PaidAt == nil || !got.PaidAt.Equal(paidAt) || got.Payment.Status != domain.PaymentPaid ||
		got.Payment.TransactionNo != "MOCKTXN1" || got.Payment.Method != "mock_balance" || len(got.Items) != 1 {
		t.Fatalf("pay = %+v, %v", got, err)
	}
	stored, _ := s.GetOrder(ctx, "o_seed_pending")
	if stored.Status != domain.OrderPaid || stored.Payment.PaidAt == nil || !stored.UpdatedAt.After(stored.CreatedAt) {
		t.Fatalf("stored = %+v", stored)
	}
	for _, step := range []domain.OrderStatus{domain.OrderShipped, domain.OrderCompleted} {
		if _, err := s.UpdateOrder(ctx, "o_seed_pending", func(o *domain.Order, _ *domain.Payment) error { o.Status = step; return nil }); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	// 没有结算请求的订单（种子）取消：回补库存，退回记在它上面的券。
	if _, err := s.UpdateOrder(ctx, "o_seed_paid", func(o *domain.Order, p *domain.Payment) error { return nil }); err != nil {
		t.Fatalf("no-op update = %v", err)
	}
}

func testExpiredOrders(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	deadline := time.Date(2026, 9, 20, 2, 30, 0, 0, time.UTC) // 种子待支付订单的支付期限
	for _, c := range []struct {
		at    time.Time
		limit int
		want  []string
	}{
		{deadline.Add(-time.Millisecond), 10, []string{}},
		{deadline, 10, []string{"o_seed_pending"}},
		{deadline.Add(time.Hour), 0, []string{}},
	} {
		got, err := s.ListExpiredOrderIDs(ctx, c.at, c.limit)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Fatalf("ListExpiredOrderIDs(%s, %d) = %v, %v", c.at, c.limit, got, err)
		}
	}
	// 结算产生的订单按期限先后返回；已支付的不再出现。
	if _, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_lamp", "sku_seed_lamp_white", add(1)); err != nil {
		t.Fatal(err)
	}
	res, err := s.Checkout(ctx, seed.User2ID, "k", simplePlan)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListExpiredOrderIDs(ctx, couponAt.Add(time.Hour), 10)
	if fmt.Sprint(got) != fmt.Sprint([]string{"o_seed_pending", res.Orders[0].OrderID}) {
		t.Fatalf("expired = %v", got)
	}
	if _, err := s.UpdateOrder(ctx, "o_seed_pending", cancel("支付超时自动关闭")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListExpiredOrderIDs(ctx, couponAt.Add(time.Hour), 10); fmt.Sprint(got) != fmt.Sprint([]string{res.Orders[0].OrderID}) {
		t.Fatalf("expired after close = %v", got)
	}
}

func testCreateReview(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	const order, mouseItem, legacyItem = "o_seed_completed", "o_seed_completed_item1", "o_seed_completed_item2"
	write := func(rating int) func(domain.Order, domain.OrderItem) (domain.ProductReview, error) {
		return func(o domain.Order, it domain.OrderItem) (domain.ProductReview, error) {
			if o.OrderID != order || it.OrderID != order {
				return domain.ProductReview{}, fmt.Errorf("unexpected %s / %+v", o.OrderID, it)
			}
			return domain.ProductReview{Rating: rating, Content: "还不错", Tags: []string{"耐用"}, Status: domain.ReviewVisible}, nil
		}
	}
	if _, err := s.CreateReview(ctx, seed.User2ID, order, legacyItem, write(4)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account = %v", err)
	}
	if _, err := s.CreateReview(ctx, seed.UserID, "o_missing", legacyItem, write(4)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing order = %v", err)
	}
	if _, err := s.CreateReview(ctx, seed.UserID, order, "o_seed_paid_item1", write(4)); !errors.Is(err, store.ErrOrderItemNotFound) {
		t.Fatalf("item of another order = %v", err)
	}
	boom := errors.New("boom")
	if _, err := s.CreateReview(ctx, seed.UserID, order, legacyItem, func(domain.Order, domain.OrderItem) (domain.ProductReview, error) {
		return domain.ProductReview{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("fn error = %v", err)
	}
	r, err := s.CreateReview(ctx, seed.UserID, order, legacyItem, write(4))
	if err != nil || r.ReviewID == "" || r.ProductID != "p_seed_legacy" || r.SkuID != "sku_seed_legacy" || r.AccountID != seed.UserID ||
		r.OrderItemID != legacyItem || r.Rating != 4 || r.CreatedAt.IsZero() {
		t.Fatalf("review = %+v, %v", r, err)
	}
	var ce *store.ConflictError
	if _, err := s.CreateReview(ctx, seed.UserID, order, legacyItem, write(5)); !errors.As(err, &ce) || ce.Key != store.KeyReviewOrderItem {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := s.CreateReview(ctx, seed.UserID, order, mouseItem, write(5)); !errors.As(err, &ce) {
		t.Fatalf("duplicate of seed review = %v", err)
	}
	d, _ := s.GetOrder(ctx, order)
	ids := []string{}
	for item, review := range d.ReviewIDs {
		ids = append(ids, item+"="+review)
	}
	sort.Strings(ids)
	if fmt.Sprint(ids) != fmt.Sprint([]string{mouseItem + "=rv_seed_mouse", legacyItem + "=" + r.ReviewID}) {
		t.Fatalf("review ids = %v", ids)
	}
}

// testCheckoutItemOrder：同一店铺的多件商品按购物车中的先后顺序成为订单项，读回时顺序不变。
func testCheckoutItemOrder(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	want := []string{"p_seed_nova", "p_seed_earbuds", "p_seed_mouse"}
	for _, p := range [][2]string{{"p_seed_nova", "sku_seed_nova_256"}, {"p_seed_earbuds", "sku_seed_earbuds_white"}, {"p_seed_mouse", "sku_seed_mouse_gray"}} {
		if _, err := s.AddCartItem(ctx, seed.User2ID, p[0], p[1], add(1)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // 加入时间精确到毫秒
	}
	res, err := s.Checkout(ctx, seed.User2ID, "k", simplePlan)
	if err != nil || len(res.Orders) != 1 {
		t.Fatalf("checkout = %+v, %v", res, err)
	}
	stored, _ := s.GetOrder(ctx, res.Orders[0].OrderID)
	listed, _, _ := s.ListOrders(ctx, store.OrderQuery{AccountID: seed.User2ID, Page: store.Page{Page: 1, PageSize: 10}})
	for _, items := range [][]domain.OrderItem{res.Orders[0].Items, stored.Items, listed[0].Items} {
		var got []string
		for _, it := range items {
			got = append(got, it.ProductID)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("item order = %v, want %v", got, want)
		}
	}
}
