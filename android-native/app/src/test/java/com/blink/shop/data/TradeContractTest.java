package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.IOException;
import java.util.Arrays;
import java.util.Collections;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.model.Cart;
import com.blink.shop.model.Coupon;
import com.blink.shop.model.DiscountPreview;
import com.blink.shop.model.Order;
import com.blink.shop.model.PageResult;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;

import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;

/** 购物车、优惠券、订单接口：请求与服务端样例一致，样例响应能被正确解析。 */
public class TradeContractTest {

    private MockWebServer server;
    private ShopApi api;

    @Before
    public void setUp() throws IOException {
        server = new MockWebServer();
        server.start();
        api = new ShopApi(new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig(server.url("/api/v1").toString()),
                new FakeSession("tok"), () -> true));
    }

    @After
    public void tearDown() throws IOException {
        server.shutdown();
    }

    private Fixture serve(String name) throws Exception {
        Fixture f = Fixture.load(name);
        server.enqueue(f.mockResponse());
        return f;
    }

    private interface Call {
        void run() throws ApiException;
    }

    private static ApiException fails(Call c) {
        try {
            c.run();
        } catch (ApiException e) {
            return e;
        }
        fail("expected ApiException");
        return null;
    }

    @Test
    public void emptyCart() throws Exception {
        Fixture f = serve("cart_get_empty_ok");
        Cart c = api.cart();
        f.assertRequest(server.takeRequest());
        assertTrue(c.items.isEmpty());
        assertEquals("0.00", c.payAmount);
        assertEquals(0, c.checkoutCount());
    }

    @Test
    public void addToCartWithDefaultSku() throws Exception {
        Fixture f = serve("cart_add_item_ok");
        // 样例请求只有 product_id：不传 sku_id，数量 1 由客户端显式传
        Cart c = api.addToCart("p_seed_earbuds", "", 1);
        RecordedRequest r = server.takeRequest();
        JSONObject body = new JSONObject(r.getBody().readUtf8());
        assertEquals("p_seed_earbuds", body.getString("product_id"));
        assertFalse(body.has("sku_id"));
        assertEquals(1, body.getInt("quantity"));
        assertEquals("/api/v1/cart/items", r.getPath());
        assertEquals("POST", f.request.getString("method"));
        Cart.Item it = c.items.get(0);
        assertEquals("Blink Air 白色", it.skuName);
        assertEquals(5, it.stockQuantity);
        assertTrue(it.available && it.selected);
        assertEquals("569.05", it.payAmount);
        assertEquals("29.95", c.discountAmount);
        assertEquals(1, c.totalQuantity());
        assertEquals(1, c.checkoutCount());
        assertEquals(5, it.maxQuantity());
    }

    @Test
    public void cartErrors() throws Exception {
        Fixture f = serve("cart_add_insufficient_stock");
        ApiException e = fails(() -> api.addToCart("p_seed_earbuds", null, 6));
        f.assertRequest(server.takeRequest());
        assertEquals("insufficient_stock", e.code());
        assertEquals("库存不足，最多可买 5 件", e.getMessage());

        Fixture q = serve("cart_quantity_invalid");
        e = fails(() -> api.addToCart("p_seed_mouse", null, 0));
        q.assertRequest(server.takeRequest());
        assertEquals("quantity", e.field());
    }

    @Test
    public void updateAndDeleteCartItem() throws Exception {
        serve("cart_add_item_ok");
        api.updateCartItem("ci_1", null, false);
        RecordedRequest r = server.takeRequest();
        assertEquals("PATCH", r.getMethod());
        assertEquals("/api/v1/cart/items/ci_1", r.getPath());
        JSONObject body = new JSONObject(r.getBody().readUtf8());
        assertFalse(body.has("quantity"));
        assertFalse(body.getBoolean("selected"));
        serve("cart_add_item_ok");
        api.updateCartItem("ci_1", 3, null);
        body = new JSONObject(server.takeRequest().getBody().readUtf8());
        assertEquals(3, body.getInt("quantity"));
        assertFalse(body.has("selected"));
        serve("cart_get_empty_ok");
        api.deleteCartItem("ci_1");
        r = server.takeRequest();
        assertEquals("DELETE", r.getMethod());
        assertEquals("/api/v1/cart/items/ci_1", r.getPath());
    }

    @Test
    public void discountPreview() throws Exception {
        Fixture f = serve("cart_discount_preview_ok");
        DiscountPreview p = api.discountPreview(null);
        RecordedRequest r = server.takeRequest();
        f.assertRequest(r);
        assertNull(r.getRequestUrl().queryParameter("user_coupon_ids")); // 自动选券：不传
        assertEquals("3687.05", p.payAmount);
        assertEquals(4, p.lines.size());
        assertTrue(p.lines.get(3).isCoupon());
        assertEquals(2, p.merchants.size());
        assertEquals(Collections.singletonList("uc_seed_user2_platform"), p.userCouponIds);

        serve("cart_discount_preview_ok");
        api.discountPreview(Collections.emptyList());
        assertEquals("", server.takeRequest().getRequestUrl().queryParameter("user_coupon_ids")); // 不用券：空值
        serve("cart_discount_preview_ok");
        api.discountPreview(Arrays.asList("uc_a", "uc_b"));
        assertEquals("uc_a,uc_b", server.takeRequest().getRequestUrl().queryParameter("user_coupon_ids"));
    }

    @Test
    public void couponNotApplicable() throws Exception {
        Fixture f = serve("cart_coupon_not_applicable");
        ApiException e = fails(() -> api.discountPreview(Collections.singletonList("uc_seed_user_platform_used")));
        f.assertRequest(server.takeRequest());
        assertEquals("coupon_not_applicable", e.code());
        assertEquals("user_coupon_ids", e.field());
    }

    @Test
    public void coupons() throws Exception {
        Fixture f = serve("coupons_available_ok");
        PageResult<Coupon> page = api.availableCoupons();
        f.assertRequest(server.takeRequest());
        assertFalse(page.items.get(0).canClaim);
        assertEquals(1, page.items.get(0).claimedByMe);
        assertEquals("平台券", page.items.get(0).scopeLabel());
        assertTrue(page.items.get(1).canClaim);
        assertEquals("店铺券", page.items.get(1).scopeLabel());

        Fixture claim = serve("coupon_claim_ok");
        Coupon.Mine m = api.claimCoupon("coupon_seed_digital");
        claim.assertRequest(server.takeRequest());
        assertEquals("未使用", m.statusLabel());
        assertEquals("满 500 减 50", m.coupon.description);

        Fixture limit = serve("coupon_claim_limit_reached");
        ApiException e = fails(() -> api.claimCoupon("coupon_seed_platform"));
        limit.assertRequest(server.takeRequest());
        assertEquals("coupon_limit_reached", e.code());

        serve("coupons_available_ok");
        api.myCoupons(Coupon.Mine.UNUSED);
        RecordedRequest r = server.takeRequest();
        assertEquals("/api/v1/coupons/mine", r.getRequestUrl().encodedPath());
        assertEquals("unused", r.getRequestUrl().queryParameter("status"));
    }

    @Test
    public void checkout() throws Exception {
        Fixture f = serve("order_checkout_created");
        ShopApi.CheckoutResult r = api.checkout("fixture-checkout-1", null, "3687.05");
        f.assertRequest(server.takeRequest()); // 自动选券时不传 user_coupon_ids，与样例一致
        assertFalse(r.replayed);
        assertEquals(2, r.orders.size());
        Order o = r.orders.get(0);
        assertEquals(Order.PENDING_PAYMENT, o.status);
        assertEquals("待支付", Order.statusLabel(o.status));
        assertEquals(2, o.quantity());
        assertEquals("3441.98", o.payAmount);
        assertEquals("pending", o.payment.status);

        serve("order_checkout_created");
        api.checkout("k2", Collections.emptyList(), "1.00");
        JSONObject body = new JSONObject(server.takeRequest().getBody().readUtf8());
        assertEquals(0, body.getJSONArray("user_coupon_ids").length()); // 不用券：空数组
        assertEquals("k2", body.getString("idempotency_key"));
    }

    @Test
    public void checkoutRejections() throws Exception {
        Fixture price = serve("order_checkout_price_changed");
        ApiException e = fails(() -> api.checkout("fixture-price", null, "249.00"));
        price.assertRequest(server.takeRequest());
        assertEquals("price_changed", e.code());
        assertTrue(e.getMessage().contains("¥229.00"));
        assertFalse(Drafts.keepKeyAfter(e)); // 明确拒绝：没下单，丢弃幂等键

        serve("order_checkout_empty_cart");
        e = fails(() -> api.checkout("fixture-empty", null, "0.00"));
        server.takeRequest();
        assertEquals("empty_cart", e.code());
    }

    @Test
    public void payCancelConfirmReview() throws Exception {
        Fixture pay = serve("order_pay_ok");
        Order paid = api.pay("o_test", "mock_alipay");
        pay.assertRequest(server.takeRequest(), Collections.singletonMap("{setup_order_id}", "o_test"));
        assertEquals(Order.PAID, paid.status);
        assertEquals("待发货", Order.statusLabel(paid.status));
        assertEquals("mock_alipay", paid.payment.method);

        Fixture expired = serve("order_pay_expired");
        ApiException e = fails(() -> api.pay("o_seed_pending", "mock_alipay"));
        RecordedRequest r = server.takeRequest();
        assertEquals("/api/v1/orders/o_seed_pending:pay", r.getPath());
        assertEquals("POST", expired.request.getString("method"));
        assertEquals("order_expired", e.code());

        Fixture cancel = serve("order_cancel_status_conflict");
        e = fails(() -> api.cancelOrder("o_seed_paid", "不想要了"));
        cancel.assertRequest(server.takeRequest());
        assertEquals("order_status_conflict", e.code());

        Fixture confirm = serve("order_confirm_receipt_ok");
        Order done = api.confirmReceipt("o_seed_shipped");
        confirm.assertRequest(server.takeRequest());
        assertEquals(Order.COMPLETED, done.status);
        assertTrue(done.hasUnreviewed());

        Fixture review = serve("order_review_created");
        String id = api.review("o_seed_completed", "o_seed_completed_item2", 4, "用了三年，电池还行", Collections.singletonList("耐用"));
        review.assertRequest(server.takeRequest());
        assertEquals("<review_id>", id);
    }

    @Test
    public void orderDetailAndList() throws Exception {
        Fixture f = serve("order_detail_ok");
        Order o = api.order("o_seed_completed");
        f.assertRequest(server.takeRequest());
        assertEquals("BS2026092040001", o.orderNo);
        assertTrue(o.items.get(0).reviewed());
        assertFalse(o.items.get(1).reviewed());
        assertTrue(o.hasUnreviewed());
        assertEquals("MOCKBS2026092040001", o.payment.transactionNo);
        assertEquals("", o.closedAt);

        serve("cart_get_empty_ok");
        api.orders(Order.SHIPPED, 2);
        RecordedRequest r = server.takeRequest();
        assertEquals("/api/v1/orders", r.getRequestUrl().encodedPath());
        assertEquals("shipped", r.getRequestUrl().queryParameter("status"));
        assertEquals("2", r.getRequestUrl().queryParameter("page"));
    }
}
