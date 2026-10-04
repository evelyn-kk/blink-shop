package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func cartCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"CartAddAccumulates", testCartAdd},
		{"CartConcurrentAdd", testCartConcurrentAdd},
		{"CartLinesShowCurrentInfo", testCartLines},
		{"CartUpdateAndDeleteOwnOnly", testCartUpdateDelete},
		{"CouponClaimRules", testCouponClaim},
		{"CouponConcurrentClaim", testCouponConcurrentClaim},
		{"UserCouponStatus", testUserCouponStatus},
	}
}

// couponAt 是种子券有效期内的时间点。
var couponAt = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

func seeded(t *testing.T, s store.Store) {
	t.Helper()
	if _, err := s.ApplySeed(context.Background(), DevSeed(t)); err != nil {
		t.Fatal(err)
	}
}

func add(n int) func(line store.CartLine, lines int) (int, error) {
	return func(line store.CartLine, _ int) (int, error) { return line.Quantity + n, nil }
}

func testCartAdd(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	first, err := s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_mouse_gray", add(2))
	if err != nil || first.Quantity != 2 || !first.Selected || first.CartItemID == "" {
		t.Fatalf("first add = %+v, %v", first, err)
	}
	// 取消选中后再加购：数量累加、重新选中、仍是同一行。
	if _, err := s.UpdateCartItem(ctx, seed.UserID, first.CartItemID, func(it *domain.CartItem, _ store.CartLine) error { it.Selected = false; return nil }); err != nil {
		t.Fatal(err)
	}
	var sawCurrent, sawLines int
	var saw store.CartLine
	second, err := s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_mouse_gray", func(line store.CartLine, lines int) (int, error) {
		sawCurrent, sawLines, saw = line.Quantity, lines, line
		return line.Quantity + 3, nil
	})
	if err != nil || second.CartItemID != first.CartItemID || second.Quantity != 5 || !second.Selected || sawCurrent != 2 || sawLines != 1 {
		t.Fatalf("second add = %+v (current %d, lines %d), %v", second, sawCurrent, sawLines, err)
	}
	// 回调收到的是事务内读取的商品、店铺和规格状态。
	if !saw.SkuFound || saw.ProductStatus != domain.ProductActive || saw.MerchantStatus != domain.StatusActive || saw.StockQuantity != 150 ||
		saw.UnitPrice.String() != "129.00" || saw.MerchantID != seed.DigitalMerchant || saw.ProductName == "" {
		t.Fatalf("line state = %+v", saw)
	}
	// fn 返回错误或非正数时不修改。
	if _, err := s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_mouse_gray", func(store.CartLine, int) (int, error) { return 0, errBoom }); !errors.Is(err, errBoom) {
		t.Fatalf("fn error = %v", err)
	}
	if _, err := s.AddCartItem(ctx, seed.UserID, "p_seed_nova", "sku_seed_nova_128", func(store.CartLine, int) (int, error) { return 0, nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("zero quantity = %v", err)
	}
	lines, _ := s.ListCartLines(ctx, seed.UserID)
	if len(lines) != 1 || lines[0].Quantity != 5 {
		t.Fatalf("lines after rejected adds = %+v", lines)
	}
	_, err = s.AddCartItem(ctx, "acct_missing", "p_seed_mouse", "sku_seed_mouse_gray", add(1))
	expectNotFound(t, err, "add for missing account")
	// 其他账户的同一规格是独立的一行。
	other, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_mouse", "sku_seed_mouse_gray", add(1))
	if err != nil || other.CartItemID == first.CartItemID || other.Quantity != 1 {
		t.Fatalf("other account = %+v, %v", other, err)
	}
}

func testCartConcurrentAdd(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	var wg sync.WaitGroup
	errs := make([]error, 30)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.AddCartItem(ctx, seed.UserID, "p_seed_nova", "sku_seed_nova_128", add(1))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}
	lines, err := s.ListCartLines(ctx, seed.UserID)
	if err != nil || len(lines) != 1 || lines[0].Quantity != 30 {
		t.Fatalf("after concurrent adds: %+v, %v", lines, err)
	}
}

func testCartLines(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	a, _ := s.AddCartItem(ctx, seed.UserID, "p_seed_lamp", "sku_seed_lamp_white", add(1))
	time.Sleep(2 * time.Millisecond) // 加入时间精确到毫秒，隔开以验证按加入顺序排列
	b, _ := s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_mouse_gray", add(2))
	// 规格不属于该商品（或已被删除）：SkuFound 为 false。
	c, _ := s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_lamp_white", add(1))
	lines, err := s.ListCartLines(ctx, seed.UserID)
	if err != nil || len(lines) != 3 {
		t.Fatalf("lines = %+v, %v", lines, err)
	}
	byID := map[string]store.CartLine{}
	for _, l := range lines {
		byID[l.CartItemID] = l
	}
	lamp := byID[a.CartItemID]
	if lamp.ProductName != "Blink 护眼台灯 L1" || lamp.MerchantID != seed.HomeMerchant || lamp.MerchantName == "" || lamp.MerchantStatus != domain.StatusActive ||
		lamp.ProductStatus != domain.ProductActive || lamp.CategoryID != "c_lamp" || !lamp.SkuFound || lamp.SkuName != "Blink L1 白色" ||
		lamp.UnitPrice.String() != "249.00" || lamp.StockQuantity != 60 || lamp.ImageURL == "" {
		t.Fatalf("lamp line = %+v", lamp)
	}
	if byID[b.CartItemID].Quantity != 2 || byID[c.CartItemID].SkuFound {
		t.Fatalf("mouse lines = %+v / %+v", byID[b.CartItemID], byID[c.CartItemID])
	}
	if lines[0].CartItemID != a.CartItemID {
		t.Fatalf("lines not in insertion order: %v", lines[0].CartItemID)
	}
	if other, _ := s.ListCartLines(ctx, seed.User2ID); len(other) != 0 {
		t.Fatalf("other account sees %d lines", len(other))
	}
}

func testCartUpdateDelete(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	it, _ := s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_mouse_gray", add(1))
	updated, err := s.UpdateCartItem(ctx, seed.UserID, it.CartItemID, func(x *domain.CartItem, _ store.CartLine) error {
		x.Quantity, x.Selected = 4, false
		x.ProductID = "p_seed_nova" // 不能借更新改商品
		return nil
	})
	if err != nil || updated.Quantity != 4 || updated.Selected || updated.ProductID != "p_seed_mouse" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := s.UpdateCartItem(ctx, seed.UserID, it.CartItemID, func(x *domain.CartItem, _ store.CartLine) error { x.Quantity = 0; return nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("zero quantity = %v", err)
	}
	if _, err := s.UpdateCartItem(ctx, seed.UserID, it.CartItemID, func(x *domain.CartItem, _ store.CartLine) error { x.Quantity = 9; return errBoom }); !errors.Is(err, errBoom) {
		t.Fatalf("fn error = %v", err)
	}
	_, err = s.UpdateCartItem(ctx, seed.User2ID, it.CartItemID, func(*domain.CartItem, store.CartLine) error { return nil })
	expectNotFound(t, err, "update other account's item")
	expectNotFound(t, s.DeleteCartItem(ctx, seed.User2ID, it.CartItemID), "delete other account's item")
	if lines, _ := s.ListCartLines(ctx, seed.UserID); len(lines) != 1 || lines[0].Quantity != 4 {
		t.Fatalf("after rejected changes: %+v", lines)
	}
	if err := s.DeleteCartItem(ctx, seed.UserID, it.CartItemID); err != nil {
		t.Fatal(err)
	}
	expectNotFound(t, s.DeleteCartItem(ctx, seed.UserID, it.CartItemID), "delete twice")
}

// addCoupon 写入一张测试券（经种子写入，两种实现一致）。
func addCoupon(t *testing.T, s store.Store, c domain.Coupon) {
	t.Helper()
	if c.Name == "" {
		c.Name = c.CouponID
	}
	if c.Type == "" {
		c.Type = domain.CouponFixedAmount
	}
	if c.Scope == "" {
		c.Scope = domain.ScopePlatform
	}
	if c.StartAt.IsZero() {
		c.StartAt, c.EndAt = couponAt.Add(-time.Hour), couponAt.Add(time.Hour)
	}
	if c.Status == "" {
		c.Status = domain.StatusActive
	}
	c.DiscountAmount = domain.MustMoney("5")
	if _, err := s.ApplySeed(context.Background(), store.SeedData{Coupons: []domain.Coupon{c}}); err != nil {
		t.Fatal(err)
	}
}

func testCouponClaim(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	got, err := s.ClaimCoupon(ctx, seed.User2ID, "coupon_seed_digital", couponAt)
	if err != nil || got.Status != domain.UserCouponUnused || got.AccountID != seed.User2ID || got.Coupon.ClaimedCount != 1 || got.UserCouponID == "" {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	if _, err := s.ClaimCoupon(ctx, seed.User2ID, "coupon_seed_digital", couponAt); !errors.Is(err, store.ErrCouponLimitReached) {
		t.Fatalf("second claim = %v", err)
	}
	_, err = s.ClaimCoupon(ctx, seed.User2ID, "coupon_missing", couponAt)
	expectNotFound(t, err, "missing coupon")

	addCoupon(t, s, domain.Coupon{CouponID: "c_soldout", TotalCount: 1, ClaimedCount: 1, PerUserLimit: 1})
	addCoupon(t, s, domain.Coupon{CouponID: "c_inactive", Status: domain.StatusInactive, PerUserLimit: 1})
	addCoupon(t, s, domain.Coupon{CouponID: "c_future", StartAt: couponAt.Add(time.Minute), EndAt: couponAt.Add(time.Hour), PerUserLimit: 1})
	addCoupon(t, s, domain.Coupon{CouponID: "c_ended", StartAt: couponAt.Add(-time.Hour), EndAt: couponAt, PerUserLimit: 1}) // end_at 不含
	addCoupon(t, s, domain.Coupon{CouponID: "c_ghost_shop", Scope: domain.ScopeMerchant, MerchantID: "m_missing", PerUserLimit: 1})
	addCoupon(t, s, domain.Coupon{CouponID: "c_unlimited", TotalCount: 0, PerUserLimit: 3})
	for id, want := range map[string]error{
		"c_soldout": store.ErrCouponSoldOut, "c_inactive": store.ErrCouponUnavailable, "c_future": store.ErrCouponUnavailable,
		"c_ended": store.ErrCouponUnavailable, "c_ghost_shop": store.ErrCouponUnavailable,
	} {
		if _, err := s.ClaimCoupon(ctx, seed.User2ID, id, couponAt); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", id, err, want)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := s.ClaimCoupon(ctx, seed.User2ID, "c_unlimited", couponAt); err != nil {
			t.Fatalf("unlimited claim %d: %v", i, err)
		}
	}
	if _, err := s.ClaimCoupon(ctx, seed.User2ID, "c_unlimited", couponAt); !errors.Is(err, store.ErrCouponLimitReached) {
		t.Fatalf("4th claim = %v", err)
	}
	counts, err := s.CountClaimed(ctx, seed.User2ID, []string{"c_unlimited", "coupon_seed_digital", "coupon_seed_platform", "c_soldout"})
	if err != nil || counts["c_unlimited"] != 3 || counts["coupon_seed_digital"] != 1 || counts["coupon_seed_platform"] != 1 || counts["c_soldout"] != 0 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	claimable, total, err := s.ListClaimableCoupons(ctx, couponAt, store.Page{Page: 1, PageSize: 50})
	ids := map[string]bool{}
	for _, c := range claimable {
		ids[c.CouponID] = true
	}
	// 已领完的券仍会列出（客户端显示“已领完”）；停用、未开始、已结束、店铺不存在的不列出。
	if err != nil || total != len(claimable) || !ids["coupon_seed_platform"] || !ids["c_soldout"] || !ids["c_unlimited"] ||
		ids["c_inactive"] || ids["c_future"] || ids["c_ended"] || ids["c_ghost_shop"] {
		t.Fatalf("claimable = %v (total %d), %v", ids, total, err)
	}
}

func testCouponConcurrentClaim(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	addCoupon(t, s, domain.Coupon{CouponID: "c_hot", TotalCount: 5, PerUserLimit: 2})
	accounts := []string{}
	for i := 0; i < 10; i++ {
		acc, err := s.CreateAccount(ctx, store.NewAccount{Username: fmt.Sprintf("claimer%d", i), PasswordHash: "h", Role: domain.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, acc.AccountID)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, perAccount := 0, map[string]int{}
	// 10 个用户各并发领 3 次（每人限 2 张），总量只有 5 张。
	for _, acc := range accounts {
		for j := 0; j < 3; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.ClaimCoupon(ctx, acc, "c_hot", couponAt)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
					perAccount[acc]++
				case errors.Is(err, store.ErrCouponSoldOut), errors.Is(err, store.ErrCouponLimitReached):
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
	}
	wg.Wait()
	if ok != 5 {
		t.Fatalf("%d claims succeeded, want 5", ok)
	}
	for acc, n := range perAccount {
		if n > 2 {
			t.Fatalf("%s claimed %d", acc, n)
		}
	}
	list, _, _ := s.ListClaimableCoupons(ctx, couponAt, store.Page{Page: 1, PageSize: 50})
	for _, c := range list {
		if c.CouponID == "c_hot" && c.ClaimedCount != 5 {
			t.Fatalf("claimed_count = %d", c.ClaimedCount)
		}
	}
}

func testUserCouponStatus(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	addCoupon(t, s, domain.Coupon{CouponID: "c_short", StartAt: couponAt.Add(-time.Hour), EndAt: couponAt.Add(time.Minute), PerUserLimit: 1})
	if _, err := s.ClaimCoupon(ctx, seed.UserID, "c_short", couponAt); err != nil {
		t.Fatal(err)
	}
	list := func(status string, at time.Time) []string {
		items, total, err := s.ListUserCoupons(ctx, store.UserCouponQuery{AccountID: seed.UserID, Status: status, At: at, Page: store.Page{Page: 1, PageSize: 50}})
		if err != nil || total != len(items) {
			t.Fatalf("list %q: %v", status, err)
		}
		var out []string
		for _, it := range items {
			if status != "" && it.Status != status {
				t.Fatalf("status filter %q returned %q", status, it.Status)
			}
			if it.Coupon.CouponID != it.CouponID || it.Coupon.Name == "" {
				t.Fatalf("coupon not loaded: %+v", it)
			}
			out = append(out, it.CouponID+":"+it.Status)
		}
		return out
	}
	if got := list("", couponAt); len(got) != 2 || got[0] != "c_short:unused" || got[1] != "coupon_seed_platform:used" {
		t.Fatalf("all = %v", got)
	}
	if got := list(domain.UserCouponUnused, couponAt); len(got) != 1 {
		t.Fatalf("unused = %v", got)
	}
	// 到期后未使用的券显示为 expired。
	later := couponAt.Add(time.Minute)
	if got := list(domain.UserCouponExpired, later); len(got) != 1 || got[0] != "c_short:expired" {
		t.Fatalf("expired = %v", got)
	}
	if got := list(domain.UserCouponUnused, later); len(got) != 0 {
		t.Fatalf("unused after expiry = %v", got)
	}
	if got := list(domain.UserCouponUsed, later); len(got) != 1 {
		t.Fatalf("used = %v", got)
	}
	if _, _, err := s.ListUserCoupons(ctx, store.UserCouponQuery{AccountID: seed.UserID, Status: "bogus", Page: store.Page{Page: 1, PageSize: 1}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad status = %v", err)
	}
}
