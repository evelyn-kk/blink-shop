package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

const ordersPath = "/api/v1/orders"

// checkout 发起结算；key 为空时不带幂等键。
func (ts *testServer) checkout(t *testing.T, tok, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if key != "" {
		body["idempotency_key"] = key
	}
	return ts.call(t, http.MethodPost, ordersPath+":checkout", tok, body)
}

func (ts *testServer) stockOf(t *testing.T, productID, skuID string) int {
	t.Helper()
	p, err := ts.mem.GetProduct(t.Context(), productID)
	if err != nil {
		t.Fatal(err)
	}
	for _, sku := range p.SKUs {
		if sku.SkuID == skuID {
			return sku.StockQuantity
		}
	}
	t.Fatalf("sku %s not found", skuID)
	return 0
}

func (ts *testServer) cartLen(t *testing.T, tok string) int {
	t.Helper()
	rec := ts.call(t, http.MethodGet, cartPath, tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	return len(decodeBody[cartResponse](t, rec).Items)
}

func byMerchant(orders []orderView, merchantID string) orderView {
	for _, o := range orders {
		if o.MerchantID == merchantID {
			return o
		}
	}
	return orderView{}
}

// TestCheckoutToReviewFlow：blink_user2 下单（耳机 + Nova 来自数码店，台灯来自家居店），金额与试算一致；
// 重复提交返回同一批订单；支付 → 商家发货 → 确认收货 → 评价；另一单取消后库存回补。
func TestCheckoutToReviewFlow(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.User2Username, seed.DevPassword).Token
	merchantTok := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_earbuds"})
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_nova", "sku_id": "sku_seed_nova_128"})
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp"})

	rec := ts.call(t, http.MethodGet, cartPath+"/discount-preview", tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	preview := decodeBody[discountPreview](t, rec)
	if preview.PayAmount.String() != "3687.05" {
		t.Fatalf("preview = %+v", preview)
	}

	// 幂等键放在请求头。
	req := httptest.NewRequest(http.MethodPost, ordersPath+":checkout", strings.NewReader(`{"expected_pay_amount":"3687.05"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "flow-1")
	rec = ts.do(req)
	expectStatus(t, rec, http.StatusCreated, "")
	res := decodeBody[checkoutResponse](t, rec)
	if res.Replayed || res.CheckoutRequestID == "" || len(res.Items) != 2 {
		t.Fatalf("checkout = %+v", res)
	}
	var pay domain.Money
	for _, m := range preview.Merchants {
		o := byMerchant(res.Items, m.MerchantID)
		if o.TotalAmount != m.TotalAmount || o.DiscountAmount != m.DiscountAmount || o.PayAmount != m.PayAmount || o.MerchantName != m.MerchantName {
			t.Fatalf("order for %s = %+v, preview %+v", m.MerchantID, o, m)
		}
		if o.Status != domain.OrderPendingPayment || o.Payment == nil || o.Payment.Status != domain.PaymentPending || o.Payment.Amount != o.PayAmount ||
			o.PaymentDeadlineAt == nil || !o.PaymentDeadlineAt.Equal(testNow.Add(30*time.Minute)) || o.PaidAt != nil || o.ClosedAt != nil {
			t.Fatalf("new order = %+v", o)
		}
		pay += o.PayAmount
	}
	if pay.String() != "3687.05" {
		t.Fatalf("sum of orders = %s", pay)
	}
	digital, home := byMerchant(res.Items, seed.DigitalMerchant), byMerchant(res.Items, seed.HomeMerchant)
	if len(digital.Items) != 2 || digital.Items[0].Name == "" || digital.Items[0].Amount != digital.Items[0].Price.Mul(digital.Items[0].Quantity) {
		t.Fatalf("digital items = %+v", digital.Items)
	}
	if ts.cartLen(t, tok) != 0 || ts.stockOf(t, "p_seed_earbuds", "sku_seed_earbuds_white") != 4 || ts.stockOf(t, "p_seed_lamp", "sku_seed_lamp_white") != 59 {
		t.Fatal("cart not cleared or stock not deducted")
	}
	rec = ts.call(t, http.MethodGet, "/api/v1/coupons/mine?status=used", tok, nil)
	if mine := decodeBody[pageResponse[userCouponView]](t, rec); mine.Total != 1 || mine.Items[0].UserCouponID != "uc_seed_user2_platform" || mine.Items[0].OrderID == "" {
		t.Fatalf("used coupons = %+v", mine)
	}

	// 同一个键再提交（这次放在请求体）：200，同一批订单，不再扣库存。
	rec = ts.checkout(t, tok, "flow-1", nil)
	expectStatus(t, rec, http.StatusOK, "")
	again := decodeBody[checkoutResponse](t, rec)
	if !again.Replayed || again.CheckoutRequestID != res.CheckoutRequestID || len(again.Items) != 2 || byMerchant(again.Items, seed.DigitalMerchant).OrderID != digital.OrderID {
		t.Fatalf("replay = %+v", again)
	}
	if ts.stockOf(t, "p_seed_earbuds", "sku_seed_earbuds_white") != 4 {
		t.Fatal("replay deducted stock again")
	}

	// 列表与详情。
	rec = ts.call(t, http.MethodGet, ordersPath, tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if list := decodeBody[pageResponse[orderView]](t, rec); list.Total != 2 || list.Items[0].Payment != nil || len(list.Items[0].Items) == 0 {
		t.Fatalf("list = %+v", list)
	}
	rec = ts.call(t, http.MethodGet, ordersPath+"?status=paid", tok, nil)
	if list := decodeBody[pageResponse[orderView]](t, rec); list.Total != 0 {
		t.Fatalf("paid list = %+v", list)
	}
	expectStatus(t, ts.call(t, http.MethodGet, ordersPath+"?status=weird", tok, nil), http.StatusBadRequest, "invalid_argument")

	// 支付数码店订单。
	rec = ts.call(t, http.MethodPost, ordersPath+"/"+digital.OrderID+":pay", tok, map[string]any{"method": "mock_wechat"})
	expectStatus(t, rec, http.StatusOK, "")
	paid := decodeBody[payResponse](t, rec)
	if paid.Order.Status != domain.OrderPaid || paid.Order.PaidAt == nil || paid.Payment.Status != domain.PaymentPaid ||
		paid.Payment.Method != "mock_wechat" || !strings.HasPrefix(paid.Payment.TransactionNo, "MOCK") || paid.Payment.PaidAt == nil {
		t.Fatalf("pay = %+v", paid)
	}
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/"+digital.OrderID+":pay", tok, nil), http.StatusConflict, "order_status_conflict")
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/"+digital.OrderID+":cancel", tok, nil), http.StatusConflict, "order_status_conflict")
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/"+digital.OrderID+":confirm-receipt", tok, nil), http.StatusConflict, "order_status_conflict")

	// 商家看到并发货。
	rec = ts.call(t, http.MethodGet, "/api/v1/merchant/orders?status=paid", merchantTok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if list := decodeBody[pageResponse[orderView]](t, rec); list.Total != 2 { // 种子里已有 1 个待发货订单
		t.Fatalf("merchant paid orders = %+v", list)
	}
	rec = ts.call(t, http.MethodPatch, "/api/v1/merchant/orders/"+digital.OrderID, merchantTok, map[string]any{"status": "shipped"})
	expectStatus(t, rec, http.StatusOK, "")
	if o := decodeBody[orderView](t, rec); o.Status != domain.OrderShipped || o.ShippedAt == nil {
		t.Fatalf("shipped = %+v", o)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/merchant/orders/"+digital.OrderID, merchantTok, map[string]any{"status": "shipped"}),
		http.StatusConflict, "order_status_conflict")

	// 确认收货后评价。
	item := digital.Items[0]
	reviewPath := ordersPath + "/" + digital.OrderID + "/items/" + item.OrderItemID + ":review"
	expectStatus(t, ts.call(t, http.MethodPost, reviewPath, tok, map[string]any{"rating": 5, "content": "好"}), http.StatusConflict, "order_not_completed")
	rec = ts.call(t, http.MethodPost, ordersPath+"/"+digital.OrderID+":confirm-receipt", tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if o := decodeBody[orderView](t, rec); o.Status != domain.OrderCompleted || o.CompletedAt == nil {
		t.Fatalf("completed = %+v", o)
	}
	rec = ts.call(t, http.MethodPost, reviewPath, tok, map[string]any{"rating": 4, "content": "  降噪不错  ", "tags": []string{"降噪", "降噪", " 续航 "}})
	expectStatus(t, rec, http.StatusCreated, "")
	review := decodeBody[myReviewView](t, rec)
	if review.Rating != 4 || review.Content != "降噪不错" || strings.Join(review.Tags, ",") != "降噪,续航" || review.ProductID != item.ProductID || review.Status != "visible" {
		t.Fatalf("review = %+v", review)
	}
	expectStatus(t, ts.call(t, http.MethodPost, reviewPath, tok, map[string]any{"rating": 5, "content": "again"}), http.StatusConflict, "review_exists")
	rec = ts.call(t, http.MethodGet, "/api/v1/products/"+item.ProductID+"/reviews", "", nil)
	if list := decodeBody[pageResponse[reviewView]](t, rec); list.Total == 0 || list.Items[0].ReviewID != review.ReviewID {
		t.Fatalf("public reviews = %+v", list)
	}
	rec = ts.call(t, http.MethodGet, ordersPath+"/"+digital.OrderID, tok, nil)
	if o := decodeBody[orderView](t, rec); o.Items[0].ReviewID != review.ReviewID || o.Items[1].ReviewID != "" || o.Payment == nil {
		t.Fatalf("detail after review = %+v", o)
	}

	// 取消家居店订单：台灯库存回补；平台券还用在已完成的数码订单上，不退。
	rec = ts.call(t, http.MethodPost, ordersPath+"/"+home.OrderID+":cancel", tok, map[string]any{"reason": "买重了"})
	expectStatus(t, rec, http.StatusOK, "")
	if o := decodeBody[orderView](t, rec); o.Status != domain.OrderCancelled || o.CancelReason != "买重了" || o.ClosedAt == nil || o.Payment.Status != domain.PaymentClosed {
		t.Fatalf("cancelled = %+v", o)
	}
	if ts.stockOf(t, "p_seed_lamp", "sku_seed_lamp_white") != 60 {
		t.Fatal("lamp not restocked")
	}
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/"+home.OrderID+":pay", tok, nil), http.StatusConflict, "order_status_conflict")

	for _, action := range []string{"order.checkout", "order.paid", "order.shipped", "order.completed", "order.cancelled", "review.created"} {
		if !strings.Contains(ts.logs.String(), `"action":"`+action+`"`) {
			t.Errorf("audit %s missing", action)
		}
	}
}

func TestCheckoutValidation(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.User2Username, seed.DevPassword).Token

	expectStatus(t, ts.checkout(t, tok, "", nil), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.checkout(t, tok, strings.Repeat("k", 129), nil), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.checkout(t, tok, "has space", nil), http.StatusBadRequest, "invalid_argument")
	req := httptest.NewRequest(http.MethodPost, ordersPath+":checkout", strings.NewReader(`{"idempotency_key":"a"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Idempotency-Key", "b")
	if e := decodeError(t, ts.do(req)); e.Field != "idempotency_key" {
		t.Fatalf("mismatched keys = %+v", e)
	}
	expectStatus(t, ts.checkout(t, tok, "k1", map[string]any{"user_coupon_ids": "x"}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.checkout(t, tok, "k1", nil), http.StatusBadRequest, "empty_cart")

	c := ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp", "quantity": 2})
	lamp := itemOf(c, "p_seed_lamp")
	unchanged := func(label string) {
		t.Helper()
		if ts.cartLen(t, tok) != 1 || ts.stockOf(t, "p_seed_lamp", "sku_seed_lamp_white") != 60 {
			t.Fatalf("%s: cart or stock changed", label)
		}
		rec := ts.call(t, http.MethodGet, ordersPath, tok, nil)
		if decodeBody[pageResponse[orderView]](t, rec).Total != 0 {
			t.Fatalf("%s: order created", label)
		}
	}
	// 试算金额变了（客户端以为是 400）。
	rec := ts.checkout(t, tok, "k1", map[string]any{"expected_pay_amount": "400.00"})
	expectStatus(t, rec, http.StatusConflict, "price_changed")
	if e := decodeError(t, rec); !strings.Contains(e.Message, "448.00") { // 2 × 249 − 平台满 300 减 30 − 平台券 20
		t.Fatalf("price_changed message = %q", e.Message)
	}
	unchanged("price_changed")
	rec = ts.checkout(t, tok, "k1", map[string]any{"user_coupon_ids": []string{"uc_seed_user_platform_used"}})
	expectStatus(t, rec, http.StatusBadRequest, "coupon_not_applicable")
	unchanged("coupon")

	// 选中的商品失效：整单失败，不悄悄跳过。
	if _, err := ts.mem.UpdateProduct(t.Context(), "p_seed_lamp", func(p *domain.Product) error { p.Status = domain.ProductInactive; return nil }); err != nil {
		t.Fatal(err)
	}
	rec = ts.checkout(t, tok, "k1", nil)
	expectStatus(t, rec, http.StatusConflict, "item_unavailable")
	if e := decodeError(t, rec); !strings.Contains(e.Message, "护眼台灯") || !strings.Contains(e.Message, "下架") {
		t.Fatalf("unavailable message = %q", e.Message)
	}
	unchanged("unavailable")
	if _, err := ts.mem.UpdateProduct(t.Context(), "p_seed_lamp", func(p *domain.Product) error {
		p.Status = domain.ProductActive
		p.SKUs[0].StockQuantity = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, ts.checkout(t, tok, "k1", nil), http.StatusConflict, "item_unavailable") // 购物车 2 件，库存只剩 1 件
	// 改成 1 件后成功；失败过的键可以继续用，显式不用券。
	expectStatus(t, ts.call(t, http.MethodPatch, cartPath+"/items/"+lamp.CartItemID, tok, map[string]any{"quantity": 1}), http.StatusOK, "")
	rec = ts.checkout(t, tok, "k1", map[string]any{"user_coupon_ids": []string{}, "expected_pay_amount": "249.00"})
	expectStatus(t, rec, http.StatusCreated, "")
	if o := decodeBody[checkoutResponse](t, rec).Items[0]; o.PayAmount.String() != "249.00" || o.DiscountAmount != 0 {
		t.Fatalf("order = %+v", o)
	}
	if ts.stockOf(t, "p_seed_lamp", "sku_seed_lamp_white") != 0 {
		t.Fatal("stock not deducted")
	}
}

// TestCheckoutConcurrentHTTP：8 个用户同时结算库存 5 件的耳机，恰好 5 单成功，其余 409，库存为 0。
func TestCheckoutConcurrentHTTP(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	var tokens []string
	for i := range 8 {
		tok := ts.tokenFor(t, ts.addAccount(t, fmt.Sprintf("buyer%d", i), "h", domain.RoleUser, "", domain.StatusActive))
		ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_earbuds"})
		tokens = append(tokens, tok)
	}
	var wg sync.WaitGroup
	codes := make([]int, len(tokens))
	for i, tok := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := strings.NewReader(`{"idempotency_key":"buy"}`)
			req := httptest.NewRequest(http.MethodPost, ordersPath+":checkout", body)
			req.Header.Set("Authorization", "Bearer "+tok)
			codes[i] = ts.do(req).Code
		}()
	}
	wg.Wait()
	created, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	if created != 5 || conflict != 3 || ts.stockOf(t, "p_seed_earbuds", "sku_seed_earbuds_white") != 0 {
		t.Fatalf("codes = %v, stock = %d", codes, ts.stockOf(t, "p_seed_earbuds", "sku_seed_earbuds_white"))
	}
}

func TestOrderAccessControl(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	user2 := ts.login(t, seed.User2Username, seed.DevPassword).Token
	merchant := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
	merchant2 := ts.login(t, seed.Merchant2Username, seed.DevPassword).Token
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token

	// 别人的订单一律 404，不暴露是否存在。
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, ordersPath + "/o_seed_pending"},
		{http.MethodPost, ordersPath + "/o_seed_pending:pay"},
		{http.MethodPost, ordersPath + "/o_seed_pending:cancel"},
		{http.MethodPost, ordersPath + "/o_seed_shipped:confirm-receipt"},
		{http.MethodPost, ordersPath + "/o_seed_completed/items/o_seed_completed_item2:review"},
		{http.MethodGet, ordersPath + "/o_missing"},
	} {
		body := map[string]any{"rating": 5, "content": "x"}
		expectStatus(t, ts.call(t, c.method, c.path, user2, body), http.StatusNotFound, "order_not_found")
	}
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/o_seed_pending:refund", user, nil), http.StatusNotFound, "not_found")
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/o_seed_pending", user, nil), http.StatusNotFound, "not_found")
	if d, _ := ts.mem.GetOrder(t.Context(), "o_seed_pending"); d.Status != domain.OrderPendingPayment {
		t.Fatal("other user's request changed the order")
	}
	// 商家、管理员不能用用户交易接口；未登录 401。
	expectStatus(t, ts.call(t, http.MethodGet, ordersPath, merchant, nil), http.StatusForbidden, "forbidden")
	expectStatus(t, ts.checkout(t, admin, "k", nil), http.StatusForbidden, "forbidden")
	expectStatus(t, ts.call(t, http.MethodGet, ordersPath, "", nil), http.StatusUnauthorized, "unauthorized")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/orders", user, nil), http.StatusForbidden, "forbidden")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/orders", merchant, nil), http.StatusForbidden, "forbidden")

	// 商家只看本店订单；他店订单 404；只能把已支付订单改为已发货。
	rec := ts.call(t, http.MethodGet, "/api/v1/merchant/orders", merchant2, nil)
	if list := decodeBody[pageResponse[orderView]](t, rec); list.Total != 0 {
		t.Fatalf("home merchant sees %d orders", list.Total)
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/orders/o_seed_paid", merchant2, nil), http.StatusNotFound, "order_not_found")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/merchant/orders/o_seed_paid", merchant2, map[string]any{"status": "shipped"}), http.StatusNotFound, "order_not_found")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/merchant/orders/o_seed_paid", merchant, map[string]any{"status": "completed"}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/merchant/orders/o_seed_pending", merchant, map[string]any{"status": "shipped"}), http.StatusConflict, "order_status_conflict")
	rec = ts.call(t, http.MethodGet, "/api/v1/merchant/orders/o_seed_completed", merchant, nil)
	if o := decodeBody[orderView](t, rec); o.Status != domain.OrderCompleted || o.Items[0].ReviewID != "rv_seed_mouse" || o.Payment == nil {
		t.Fatalf("merchant detail = %+v", o)
	}

	// 管理员：按条件查询，代发货，取消待支付订单（回补库存），不能取消已支付订单。
	for query, want := range map[string]int{
		"": 5, "?status=pending_payment": 1, "?merchant_id=" + seed.HomeMerchant: 0, "?account_id=" + seed.UserID: 5, "?order_no=BS2026092020001": 1,
	} {
		rec := ts.call(t, http.MethodGet, "/api/v1/admin/orders"+query, admin, nil)
		expectStatus(t, rec, http.StatusOK, "")
		if got := decodeBody[pageResponse[orderView]](t, rec).Total; got != want {
			t.Fatalf("admin orders %q = %d, want %d", query, got, want)
		}
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/orders/o_seed_paid", admin, map[string]any{"status": "cancelled"}), http.StatusConflict, "order_status_conflict")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/orders/o_seed_paid", admin, map[string]any{"status": "refunded"}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/orders/o_missing", admin, map[string]any{"status": "shipped"}), http.StatusNotFound, "order_not_found")
	rec = ts.call(t, http.MethodPatch, "/api/v1/admin/orders/o_seed_paid", admin, map[string]any{"status": "shipped"})
	expectStatus(t, rec, http.StatusOK, "")
	before := ts.stockOf(t, "p_seed_mouse", "sku_seed_mouse_gray")
	rec = ts.call(t, http.MethodPatch, "/api/v1/admin/orders/o_seed_pending", admin, map[string]any{"status": "cancelled", "reason": "疑似刷单"})
	expectStatus(t, rec, http.StatusOK, "")
	if o := decodeBody[orderView](t, rec); o.Status != domain.OrderCancelled || o.CancelReason != "疑似刷单" {
		t.Fatalf("admin cancel = %+v", o)
	}
	if ts.stockOf(t, "p_seed_mouse", "sku_seed_mouse_gray") != before+2 {
		t.Fatal("admin cancel did not restock")
	}
	if !strings.Contains(ts.logs.String(), `"by":"admin"`) {
		t.Error("admin audit missing")
	}
}

// TestPaymentTimeout：超时后支付返回 409 并关闭订单（回补库存、退券）；定时任务关闭其余超时订单，重复执行不重复关闭。
func TestPaymentTimeout(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.User2Username, seed.DevPassword).Token
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp"})
	rec := ts.checkout(t, tok, "a", nil)
	expectStatus(t, rec, http.StatusCreated, "")
	first := decodeBody[checkoutResponse](t, rec).Items[0]
	if first.DiscountAmount.String() != "20.00" { // 自动用了平台券
		t.Fatalf("first = %+v", first)
	}
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_mouse", "quantity": 3})
	rec = ts.checkout(t, tok, "b", nil)
	expectStatus(t, rec, http.StatusCreated, "")
	second := decodeBody[checkoutResponse](t, rec).Items[0]

	ts.now = func() time.Time { return testNow.Add(30 * time.Minute) } // 期限当刻即视为超时
	rec = ts.call(t, http.MethodPost, ordersPath+"/"+first.OrderID+":pay", tok, nil)
	expectStatus(t, rec, http.StatusConflict, "order_expired")
	d, _ := ts.mem.GetOrder(t.Context(), first.OrderID)
	if d.Status != domain.OrderCancelled || d.CancelReason != timeoutCancelReason || d.Payment.Status != domain.PaymentClosed {
		t.Fatalf("expired order = %+v", d)
	}
	if ts.stockOf(t, "p_seed_lamp", "sku_seed_lamp_white") != 60 {
		t.Fatal("lamp not restocked")
	}
	rec = ts.call(t, http.MethodGet, "/api/v1/coupons/mine?status=unused", tok, nil)
	if decodeBody[pageResponse[userCouponView]](t, rec).Total != 1 {
		t.Fatal("platform coupon not returned")
	}

	// 定时关闭：种子里过期的待支付订单和第二单都被关闭。
	mouse := ts.stockOf(t, "p_seed_mouse", "sku_seed_mouse_gray")
	n, err := ts.CloseExpiredOrders(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("closed = %d, %v", n, err)
	}
	if n, _ := ts.CloseExpiredOrders(t.Context()); n != 0 {
		t.Fatalf("closed again = %d", n)
	}
	if d, _ := ts.mem.GetOrder(t.Context(), second.OrderID); d.Status != domain.OrderCancelled || d.CancelReason != timeoutCancelReason {
		t.Fatalf("second = %+v", d)
	}
	if ts.stockOf(t, "p_seed_mouse", "sku_seed_mouse_gray") != mouse+3+2 {
		t.Fatal("mouse not restocked by closer")
	}
	if !strings.Contains(ts.logs.String(), `"reason":"payment_timeout"`) {
		t.Error("close audit missing")
	}
	// 未到期的订单不受影响。
	ts.now = func() time.Time { return testNow }
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp"})
	expectStatus(t, ts.checkout(t, tok, "c", nil), http.StatusCreated, "")
	if n, _ := ts.CloseExpiredOrders(t.Context()); n != 0 {
		t.Fatalf("closed fresh order: %d", n)
	}
}

func TestReviewValidation(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	path := ordersPath + "/o_seed_completed/items/o_seed_completed_item2:review"
	for _, c := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"content": "好"}, "rating"},
		{map[string]any{"rating": 0, "content": "好"}, "rating"},
		{map[string]any{"rating": 6, "content": "好"}, "rating"},
		{map[string]any{"rating": 5, "content": "   "}, "content"},
		{map[string]any{"rating": 5, "content": strings.Repeat("好", 501)}, "content"},
		{map[string]any{"rating": 5, "content": "好", "tags": []string{"a", "b", "c", "d", "e", "f"}}, "tags"},
		{map[string]any{"rating": 5, "content": "好", "tags": []string{strings.Repeat("长", 21)}}, "tags"},
		{map[string]any{"rating": "5", "content": "好"}, "rating"},
	} {
		rec := ts.call(t, http.MethodPost, path, tok, c.body)
		expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")
		if e := decodeError(t, rec); e.Field != c.field {
			t.Fatalf("%v: field = %q", c.body, e.Field)
		}
	}
	good := map[string]any{"rating": 5, "content": strings.Repeat("好", 500), "tags": []string{"a", "b", "c", "d", "e", "a"}}
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/o_seed_completed/items/o_seed_paid_item1:review", tok, good), http.StatusNotFound, "order_item_not_found")
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/o_seed_paid/items/o_seed_paid_item1:review", tok, good), http.StatusConflict, "order_not_completed")
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/o_seed_completed/items/o_seed_completed_item2:rate", tok, good), http.StatusNotFound, "not_found")
	expectStatus(t, ts.call(t, http.MethodPost, path, tok, good), http.StatusCreated, "")
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/o_seed_completed/items/o_seed_completed_item1:review", tok, good), http.StatusConflict, "review_exists")
}
