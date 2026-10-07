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
}
