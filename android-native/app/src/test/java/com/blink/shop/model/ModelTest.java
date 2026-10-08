package com.blink.shop.model;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.Collections;
import java.util.TimeZone;

import org.json.JSONObject;
import org.junit.Test;

public class ModelTest {

    @Test
    public void parsesIsoTimes() {
        assertEquals(0L, Times.parseIsoMillis("1970-01-01T00:00:00Z"));
        assertEquals(1_500L, Times.parseIsoMillis("1970-01-01T00:00:01.5Z"));
        assertEquals(1_234L, Times.parseIsoMillis("1970-01-01T00:00:01.234567Z"));
        assertEquals(Times.parseIsoMillis("2026-09-24T06:00:00Z"), Times.parseIsoMillis("2026-09-24T14:00:00+08:00"));
        assertEquals(0L, Times.parseIsoMillis("<timestamp>"));
        assertEquals(0L, Times.parseIsoMillis(null));
    }

    @Test
    public void formatsLocalTime() {
        assertEquals("2026-09-24 14:00", Times.formatLocal("2026-09-24T06:00:00Z", TimeZone.getTimeZone("Asia/Shanghai")));
        assertEquals("bad", Times.formatLocal("bad", TimeZone.getDefault()));
    }

    @Test
    public void formatsMoney() {
        assertEquals("¥1,299.00", Money.format("1299.00"));
        assertEquals("¥0.50", Money.format("0.5"));
        assertEquals("abc", Money.format("abc"));
        assertEquals("", Money.format(""));
        assertTrue(Money.greater("159.00", "129.00"));
        assertFalse(Money.greater("129.00", "129.00"));
        assertFalse(Money.greater("", "129.00"));
    }

    @Test
    public void pageHasMore() {
        assertTrue(new PageResult<>(Collections.emptyList(), 1, 20, 21).hasMore());
        assertFalse(new PageResult<>(Collections.emptyList(), 2, 20, 40).hasMore());
        assertFalse(new PageResult<>(Collections.emptyList(), 1, 20, 0).hasMore());
    }

    @Test
    public void accountRoundTripAndStatusHint() throws Exception {
        Account a = Account.fromJson(new JSONObject().put("username", "blink_user").put("display_name", "").put("status", "risk")
                .put("future_field", 1));
        assertEquals("blink_user", a.name());
        assertFalse(a.statusHint().isEmpty());
        Account back = Account.fromJsonString(a.toJsonString());
        assertEquals("blink_user", back.username);
        assertTrue(back.toJsonString().contains("future_field"));
        assertNull(Account.fromJsonString("not json"));
        assertEquals("", Account.fromJson(new JSONObject().put("status", "active")).statusHint());
    }

    @Test
    public void productFallbacks() throws Exception {
        Product p = Product.fromJson(new JSONObject().put("image_url", "/a.png").put("merchant_name", "店").put("tags", JSONObject.NULL));
        assertEquals(Collections.singletonList("/a.png"), p.gallery());
        assertEquals("店", p.byline());
        assertTrue(p.tags.isEmpty());
        assertEquals("有货", Product.stockLabel("in_stock"));
        assertEquals("", Product.stockLabel("unknown"));
        Sku s = Sku.fromJson(new JSONObject().put("sku_name", "默认规格"));
        assertEquals("默认规格", s.label());
    }

    @Test
    public void sumsAmounts() {
        assertEquals("3671.05", Money.sum(java.util.Arrays.asList("3441.98", "229.07")));
        assertEquals("0.00", Money.sum(java.util.Collections.emptyList()));
        assertEquals("1.50", Money.sum(java.util.Arrays.asList("1.5", "x")));
    }

    @Test
    public void orderStatusLabelsAndReviews() throws Exception {
        assertEquals("待支付", Order.statusLabel("pending_payment"));
        assertEquals("待发货", Order.statusLabel("paid"));
        assertEquals("已发货", Order.statusLabel("shipped"));
        assertEquals("已完成", Order.statusLabel("completed"));
        assertEquals("已取消", Order.statusLabel("cancelled"));
        org.json.JSONObject o = new org.json.JSONObject().put("status", "shipped")
                .put("items", new org.json.JSONArray().put(new org.json.JSONObject().put("order_item_id", "i1").put("review_id", "")))
                .put("paid_at", org.json.JSONObject.NULL);
        Order order = Order.fromJson(o);
        assertFalse(order.hasUnreviewed()); // 还没完成不能评价
        assertEquals("", order.paidAt);
        assertNull(order.payment);
        o.put("status", "completed");
        assertTrue(Order.fromJson(o).hasUnreviewed());
    }

    @Test
    public void cartCountsOnlySelectedAvailableForCheckout() throws Exception {
        org.json.JSONArray items = new org.json.JSONArray()
                .put(new org.json.JSONObject().put("cart_item_id", "a").put("quantity", 2).put("selected", true).put("available", true)
                        .put("stock_quantity", 200))
                .put(new org.json.JSONObject().put("cart_item_id", "b").put("quantity", 3).put("selected", false).put("available", false)
                        .put("unavailable_reason", "已售罄").put("stock_quantity", 0));
        Cart c = Cart.fromJson(new org.json.JSONObject().put("items", items));
        assertEquals(1, c.checkoutCount());
        assertEquals(5, c.totalQuantity());
        assertEquals(99, c.items.get(0).maxQuantity()); // 不超过 99
        assertEquals(1, c.items.get(1).maxQuantity());
    }
}
