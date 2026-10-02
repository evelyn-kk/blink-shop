package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const cartPath = "/api/v1/cart"

func (ts *testServer) addToCart(t *testing.T, tok string, body map[string]any) cartResponse {
	t.Helper()
	rec := ts.call(t, http.MethodPost, cartPath+"/items", tok, body)
	expectStatus(t, rec, http.StatusOK, "")
	return decodeBody[cartResponse](t, rec)
}

func itemOf(c cartResponse, productID string) cartItemView {
	for _, it := range c.Items {
		if it.ProductID == productID {
			return it
		}
	}
	return cartItemView{}
}

func lineSummary(p discountPreview) []string {
	var out []string
	for _, l := range p.Lines {
		out = append(out, fmt.Sprintf("%s:%s=%s", l.Type, l.ID, l.Amount))
	}
	return out
}

// TestCartFlow：blink_user2 持有一张未使用的平台券（满 200 减 20）。
// 耳机 95 折（不可叠加）+ Nova（数码店满 1000 减 80）+ 台灯；平台满 300 减 30 只算 Nova 和台灯。
func TestCartFlow(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.User2Username, seed.DevPassword).Token

	rec := ts.call(t, http.MethodGet, cartPath, tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if c := decodeBody[cartResponse](t, rec); len(c.Items) != 0 || c.Summary.PayAmount != 0 || c.Summary.ItemCount != 0 {
		t.Fatalf("empty cart = %+v", c)
	}

	c := ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_earbuds"}) // 不传规格：默认规格，数量 1
	earbuds := itemOf(c, "p_seed_earbuds")
	if earbuds.SkuID != "sku_seed_earbuds_white" || earbuds.Quantity != 1 || !earbuds.Selected || !earbuds.Available ||
		earbuds.UnitPrice.String() != "599.00" || earbuds.PayAmount.String() != "569.05" || earbuds.MerchantName == "" || earbuds.ProductName == "" {
		t.Fatalf("earbuds = %+v", earbuds)
	}
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_nova", "sku_id": "sku_seed_nova_128", "quantity": 1})
	c = ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp"})
	if c.Summary.ItemCount != 3 || c.Summary.SelectedCount != 3 || c.Summary.TotalAmount.String() != "3847.00" ||
		c.Summary.DiscountAmount.String() != "159.95" || c.Summary.PayAmount.String() != "3687.05" {
		t.Fatalf("summary = %+v", c.Summary)
	}

	preview := func(query string) discountPreview {
		rec := ts.call(t, http.MethodGet, cartPath+"/discount-preview"+query, tok, nil)
		expectStatus(t, rec, http.StatusOK, "")
		return decodeBody[discountPreview](t, rec)
	}
	p := preview("")
	want := []string{"promotion:promo_seed_earbuds=29.95", "promotion:promo_seed_digital=80.00", "promotion:promo_seed_platform=30.00", "coupon:uc_seed_user2_platform=20.00"}
	if got := lineSummary(p); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lines = %v", got)
	}
	if p.TotalAmount.String() != "3847.00" || p.PayAmount.String() != "3687.05" || len(p.Merchants) != 2 ||
		strings.Join(p.UserCouponIDs, ",") != "uc_seed_user2_platform" || len(p.Items) != 3 {
		t.Fatalf("preview = %+v", p)
	}
	for _, l := range p.Lines {
		if l.ID == "promo_seed_platform" && (len(l.CartItemIDs) != 2 || contains(l.CartItemIDs, earbuds.CartItemID)) {
			t.Fatalf("non-stackable earbuds joined platform promotion: %+v", l)
		}
		if l.Description == "" {
			t.Fatalf("line without description: %+v", l)
		}
	}
	// 显式不用券 / 指定不属于自己的券。
	if p := preview("?user_coupon_ids="); p.DiscountAmount.String() != "139.95" || len(p.UserCouponIDs) != 0 {
		t.Fatalf("no coupon = %+v", p)
	}
	rec = ts.call(t, http.MethodGet, cartPath+"/discount-preview?user_coupon_ids=uc_seed_user_platform_used", tok, nil)
	expectStatus(t, rec, http.StatusBadRequest, "coupon_not_applicable")
	if e := decodeError(t, rec); e.Field != "user_coupon_ids" {
		t.Fatalf("error = %+v", e)
	}

	// 重复加购累加到同一行；数量修改；取消选中后不计价；删除。
	c = ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_nova", "sku_id": "sku_seed_nova_128", "quantity": 2})
	nova := itemOf(c, "p_seed_nova")
	if len(c.Items) != 3 || nova.Quantity != 3 {
		t.Fatalf("accumulate = %+v", c.Items)
	}
	lamp := itemOf(c, "p_seed_lamp")
	rec = ts.call(t, http.MethodPatch, cartPath+"/items/"+lamp.CartItemID, tok, map[string]any{"selected": false})
	expectStatus(t, rec, http.StatusOK, "")
	c = decodeBody[cartResponse](t, rec)
	if l := itemOf(c, "p_seed_lamp"); l.Selected || l.DiscountAmount != 0 || l.PayAmount.String() != "249.00" {
		t.Fatalf("unselected lamp = %+v", l)
	}
	if c.Summary.TotalAmount.String() != "9596.00" || c.Summary.SelectedCount != 4 { // 599 + 3 × 2999
		t.Fatalf("summary after unselect = %+v", c.Summary)
	}
	rec = ts.call(t, http.MethodPatch, cartPath+"/items/"+nova.CartItemID, tok, map[string]any{"quantity": 50})
	expectStatus(t, rec, http.StatusOK, "")
	for _, bad := range []struct {
		body   map[string]any
		status int
		code   string
	}{
		{map[string]any{"quantity": 0}, 400, "invalid_argument"},
		{map[string]any{"quantity": -1}, 400, "invalid_argument"},
		{map[string]any{"quantity": 100}, 400, "invalid_argument"},
		{map[string]any{"quantity": 51}, 409, "insufficient_stock"},
		{map[string]any{"quantity": "2"}, 400, "invalid_argument"},
		{map[string]any{}, 400, "invalid_argument"},
	} {
		expectStatus(t, ts.call(t, http.MethodPatch, cartPath+"/items/"+nova.CartItemID, tok, bad.body), bad.status, bad.code)
	}
	rec = ts.call(t, http.MethodDelete, cartPath+"/items/"+lamp.CartItemID, tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if c := decodeBody[cartResponse](t, rec); len(c.Items) != 2 || itemOf(c, "p_seed_nova").Quantity != 50 {
		t.Fatalf("after delete = %+v", c.Items)
	}
	expectStatus(t, ts.call(t, http.MethodDelete, cartPath+"/items/"+lamp.CartItemID, tok, nil), 404, "cart_item_not_found")
	for _, action := range []string{"cart.item_added", "cart.item_updated", "cart.item_removed"} {
		if !strings.Contains(ts.logs.String(), `"action":"`+action+`"`) {
			t.Errorf("missing audit %s", action)
		}
	}
}

func TestAddCartItemRejected(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
		field  string
	}{
		{"missing product", map[string]any{"quantity": 1}, 400, "invalid_argument", "product_id"},
		{"unknown product", map[string]any{"product_id": "p_missing"}, 404, "product_not_found", ""},
		{"inactive product", map[string]any{"product_id": "p_seed_speaker"}, 404, "product_not_found", ""},
		{"risk product", map[string]any{"product_id": "p_seed_powerbank"}, 404, "product_not_found", ""},
		{"deleted product", map[string]any{"product_id": "p_seed_legacy"}, 404, "product_not_found", ""},
		{"sku of another product", map[string]any{"product_id": "p_seed_nova", "sku_id": "sku_seed_lamp_white"}, 400, "invalid_argument", "sku_id"},
		{"out of stock", map[string]any{"product_id": "p_seed_keyboard"}, 409, "out_of_stock", ""},
		{"more than stock", map[string]any{"product_id": "p_seed_earbuds", "quantity": 6}, 409, "insufficient_stock", ""},
		{"zero", map[string]any{"product_id": "p_seed_mouse", "quantity": 0}, 400, "invalid_argument", "quantity"},
		{"negative", map[string]any{"product_id": "p_seed_mouse", "quantity": -3}, 400, "invalid_argument", "quantity"},
		{"over line limit", map[string]any{"product_id": "p_seed_mouse", "quantity": 100}, 400, "invalid_argument", "quantity"},
		{"fractional", map[string]any{"product_id": "p_seed_mouse", "quantity": 1.5}, 400, "invalid_argument", "quantity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := ts.call(t, http.MethodPost, cartPath+"/items", tok, c.body)
			expectStatus(t, rec, c.status, c.code)
			if f := decodeError(t, rec).Field; f != c.field {
				t.Fatalf("field = %q, want %q", f, c.field)
			}
		})
	}
	if lines, _ := ts.mem.ListCartLines(context.Background(), seed.UserID); len(lines) != 0 {
		t.Fatalf("rejected adds wrote %d lines", len(lines))
	}

	// 累加后超过库存：提示购物车里已有的数量，原数量不变。
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_earbuds", "quantity": 5})
	rec := ts.call(t, http.MethodPost, cartPath+"/items", tok, map[string]any{"product_id": "p_seed_earbuds"})
	expectStatus(t, rec, http.StatusConflict, "insufficient_stock")
	if msg := decodeError(t, rec).Message; !strings.Contains(msg, "已有 5 件") {
		t.Fatalf("message = %q", msg)
	}
	// 单行上限 99（鼠标库存 150）。
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_mouse", "quantity": 99})
	expectStatus(t, ts.call(t, http.MethodPost, cartPath+"/items", tok, map[string]any{"product_id": "p_seed_mouse"}), 409, "quantity_limit")
	lines, _ := ts.mem.ListCartLines(context.Background(), seed.UserID)
	if len(lines) != 2 || lines[0].Quantity != 5 || lines[1].Quantity != 99 {
		t.Fatalf("lines = %+v", lines)
	}

	// 购物车满 100 种商品后不能再加新商品，但已有的商品仍可加数量。
	full := ts.login(t, seed.User2Username, seed.DevPassword).Token
	for i := 0; i < store.MaxCartLines; i++ {
		if _, err := ts.mem.AddCartItem(context.Background(), seed.User2ID, fmt.Sprintf("p_filler_%d", i), "sku_x", func(int, int) (int, error) { return 1, nil }); err != nil {
			t.Fatal(err)
		}
	}
	expectStatus(t, ts.call(t, http.MethodPost, cartPath+"/items", full, map[string]any{"product_id": "p_seed_mouse"}), 409, "cart_full")
}

func TestCartConcurrentAddOneRow(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = ts.call(t, http.MethodPost, cartPath+"/items", tok, map[string]any{"product_id": "p_seed_nova", "quantity": 2}).Code
		}()
	}
	wg.Wait()
	for _, c := range codes {
		if c != 200 {
			t.Fatalf("codes = %v", codes)
		}
	}
	lines, _ := ts.mem.ListCartLines(context.Background(), seed.UserID)
	if len(lines) != 1 || lines[0].Quantity != 20 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestCartOwnershipAndRoles(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	owner := ts.login(t, seed.User2Username, seed.DevPassword).Token
	other := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	item := itemOf(ts.addToCart(t, owner, map[string]any{"product_id": "p_seed_lamp"}), "p_seed_lamp")
	expectStatus(t, ts.call(t, http.MethodPatch, cartPath+"/items/"+item.CartItemID, other, map[string]any{"quantity": 2}), 404, "cart_item_not_found")
	expectStatus(t, ts.call(t, http.MethodDelete, cartPath+"/items/"+item.CartItemID, other, nil), 404, "cart_item_not_found")
	if c := decodeBody[cartResponse](t, ts.call(t, http.MethodGet, cartPath, other, nil)); len(c.Items) != 0 {
		t.Fatalf("other user sees %d items", len(c.Items))
	}
	if c := decodeBody[cartResponse](t, ts.call(t, http.MethodGet, cartPath, owner, nil)); itemOf(c, "p_seed_lamp").Quantity != 1 {
		t.Fatal("owner item changed")
	}
	for _, who := range []string{seed.MerchantUsername, seed.AdminUsername} {
		tok := ts.login(t, who, seed.DevPassword).Token
		expectStatus(t, ts.call(t, http.MethodGet, cartPath, tok, nil), 403, "forbidden")
		expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/coupons/coupon_seed_digital:claim", tok, nil), 403, "forbidden")
	}
	expectStatus(t, ts.call(t, http.MethodGet, cartPath, "", nil), 401, "unauthorized")
}

// 商品下架、规格删除、库存减少后，购物车项标为不可购买，不参与计价，只能取消选中或删除。
func TestCartUnavailableItems(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	ctx := context.Background()
	mouse := itemOf(ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_mouse", "quantity": 3}), "p_seed_mouse")
	lamp := itemOf(ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp", "quantity": 2}), "p_seed_lamp")

	set := func(id string, fn func(p *domain.Product)) {
		if _, err := ts.mem.UpdateProduct(ctx, id, func(p *domain.Product) error { fn(p); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	set("p_seed_mouse", func(p *domain.Product) { p.Status = domain.ProductInactive })
	set("p_seed_lamp", func(p *domain.Product) { p.SKUs[0].StockQuantity = 1 })

	c := decodeBody[cartResponse](t, ts.call(t, http.MethodGet, cartPath, tok, nil))
	if m := itemOf(c, "p_seed_mouse"); m.Available || m.UnavailableReason != "商品已下架" || m.DiscountAmount != 0 {
		t.Fatalf("mouse = %+v", m)
	}
	if l := itemOf(c, "p_seed_lamp"); l.Available || l.UnavailableReason != "库存不足，仅剩 1 件" || l.StockQuantity != 1 {
		t.Fatalf("lamp = %+v", l)
	}
	if c.Summary.TotalAmount != 0 || c.Summary.SelectedCount != 0 {
		t.Fatalf("unavailable items priced: %+v", c.Summary)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, cartPath+"/items/"+mouse.CartItemID, tok, map[string]any{"quantity": 1}), 409, "item_unavailable")
	expectStatus(t, ts.call(t, http.MethodPatch, cartPath+"/items/"+mouse.CartItemID, tok, map[string]any{"selected": false}), 200, "")
	// 库存不足的商品可以把数量改到库存以内恢复。
	rec := ts.call(t, http.MethodPatch, cartPath+"/items/"+lamp.CartItemID, tok, map[string]any{"quantity": 1})
	expectStatus(t, rec, http.StatusOK, "")
	if l := itemOf(decodeBody[cartResponse](t, rec), "p_seed_lamp"); !l.Available || l.PayAmount.String() != "249.00" {
		t.Fatalf("lamp after fix = %+v", l)
	}
	set("p_seed_lamp", func(p *domain.Product) { p.SKUs[0].StockQuantity = 0 })
	expectStatus(t, ts.call(t, http.MethodPatch, cartPath+"/items/"+lamp.CartItemID, tok, map[string]any{"quantity": 2}), 409, "out_of_stock")
	expectStatus(t, ts.call(t, http.MethodDelete, cartPath+"/items/"+mouse.CartItemID, tok, nil), 200, "")
}

func TestPromotionHint(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp"})
	p := decodeBody[discountPreview](t, ts.call(t, http.MethodGet, cartPath+"/discount-preview", tok, nil))
	if len(p.Hints) != 1 || p.Hints[0].PromotionID != "promo_seed_platform" || p.Hints[0].Shortfall.String() != "51.00" || len(p.Lines) != 0 {
		t.Fatalf("preview = %+v", p)
	}
}

func TestCoupons(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	type availablePage struct {
		Items []availableCouponView `json:"items"`
		Total int                   `json:"total"`
	}
	type minePage struct {
		Items []userCouponView `json:"items"`
		Total int              `json:"total"`
	}
	avail := decodeBody[availablePage](t, ts.call(t, http.MethodGet, "/api/v1/coupons/available", tok, nil))
	byID := map[string]availableCouponView{}
	for _, c := range avail.Items {
		byID[c.CouponID] = c
	}
	if p := byID["coupon_seed_platform"]; p.ClaimedByMe != 1 || p.CanClaim || p.Description != "满 200 减 20" {
		t.Fatalf("platform coupon = %+v", p)
	}
	if d := byID["coupon_seed_digital"]; d.ClaimedByMe != 0 || !d.CanClaim || d.MerchantID != seed.DigitalMerchant || d.Scope != domain.ScopeMerchant {
		t.Fatalf("digital coupon = %+v", d)
	}

	rec := ts.call(t, http.MethodPost, "/api/v1/coupons/coupon_seed_digital:claim", tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	got := decodeBody[userCouponView](t, rec)
	if got.Status != domain.UserCouponUnused || got.CouponID != "coupon_seed_digital" || got.Coupon.ClaimedCount != 1 || got.UsedAt != nil {
		t.Fatalf("claimed = %+v", got)
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/coupons/coupon_seed_digital:claim", tok, nil), 409, "coupon_limit_reached")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/coupons/coupon_missing:claim", tok, nil), 404, "coupon_not_found")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/coupons/coupon_seed_digital:use", tok, nil), 404, "not_found")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/coupons/coupon_seed_digital", tok, nil), 404, "not_found")
	if !strings.Contains(ts.logs.String(), `"action":"coupon.claimed"`) {
		t.Fatal("claim not audited")
	}

	mine := decodeBody[minePage](t, ts.call(t, http.MethodGet, "/api/v1/coupons/mine", tok, nil))
	if mine.Total != 2 {
		t.Fatalf("mine = %+v", mine)
	}
	if used := decodeBody[minePage](t, ts.call(t, http.MethodGet, "/api/v1/coupons/mine?status=used", tok, nil)); used.Total != 1 || used.Items[0].OrderID != "o_seed_paid" || used.Items[0].UsedAt == nil {
		t.Fatalf("used = %+v", used)
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/coupons/mine?status=bogus", tok, nil), 400, "invalid_argument")

	// 领到的店铺券在试算中自动使用：Nova 2999 − 80（店铺满减）− 30（平台满减）= 2889 ≥ 500，再减 50。
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_nova"})
	p := decodeBody[discountPreview](t, ts.call(t, http.MethodGet, cartPath+"/discount-preview", tok, nil))
	if strings.Join(p.UserCouponIDs, ",") != got.UserCouponID || p.PayAmount.String() != "2839.00" {
		t.Fatalf("preview with claimed coupon = %+v", p)
	}
}

func TestCouponClaimConcurrentHTTP(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	ctx := context.Background()
	if _, err := ts.mem.ApplySeed(ctx, store.SeedData{Coupons: []domain.Coupon{{CouponID: "c_hot", Name: "抢券", Scope: domain.ScopePlatform,
		Type: domain.CouponFixedAmount, DiscountAmount: domain.MustMoney("5"), TotalCount: 3, PerUserLimit: 1,
		StartAt: testNow.Add(-time.Hour), EndAt: testNow.Add(time.Hour), Status: domain.StatusActive}}}); err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for i := 0; i < 8; i++ {
		tokens = append(tokens, ts.tokenFor(t, ts.addAccount(t, fmt.Sprintf("grab%d", i), "h", domain.RoleUser, "", domain.StatusActive)))
	}
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, len(tokens))
	for i, tok := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = ts.call(t, http.MethodPost, "/api/v1/coupons/c_hot:claim", tok, nil)
		}()
	}
	wg.Wait()
	ok, soldOut := 0, 0
	for _, rec := range results {
		switch rec.Code {
		case 200:
			ok++
		case 409:
			if decodeError(t, rec).Code == "coupon_sold_out" {
				soldOut++
			}
		}
	}
	if ok != 3 || soldOut != 5 {
		t.Fatalf("ok=%d soldOut=%d", ok, soldOut)
	}
}
