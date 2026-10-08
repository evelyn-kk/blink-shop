package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.IOException;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;

import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;

/** REV-016：结果未知的结算提交必须冻结，重试只能原样重发。 */
public class PendingCheckoutTest {

    /** 内存里的存储（App 里是 SharedPreferences）。 */
    private static final class MemoryStore implements PendingCheckout.Store {
        String saved;

        @Override
        public PendingCheckout load() {
            return PendingCheckout.fromJson(saved);
        }

        @Override
        public void save(PendingCheckout p) {
            saved = p.toJson();
        }

        @Override
        public void clear() {
            saved = null;
        }
    }

    private MockWebServer server;
    private final AtomicBoolean online = new AtomicBoolean(true);
    private final MemoryStore store = new MemoryStore();
    private ShopApi api;

    @Before
    public void setUp() throws IOException {
        server = new MockWebServer();
        server.start();
        api = new ShopApi(new ApiClient(ApiClient.defaultHttp().readTimeout(1, TimeUnit.SECONDS).build(),
                new ApiConfig(server.url("/api/v1").toString()), new FakeSession("tok"), online::get));
    }

    @After
    public void tearDown() throws IOException {
        server.shutdown();
    }

    private static String created(boolean replayed) {
        return "{\"checkout_request_id\":\"cr_1\",\"replayed\":" + replayed
                + ",\"items\":[{\"order_id\":\"o_1\",\"status\":\"pending_payment\",\"pay_amount\":\"3687.05\"}]}";
    }

    private ApiException submitFails(List<String> coupons, String expected) {
        try {
            PendingCheckout.submit(api, store, coupons, expected);
        } catch (ApiException e) {
            return e;
        }
        fail("expected failure");
        return null;
    }

    private static JSONObject body(RecordedRequest r) throws Exception {
        return new JSONObject(r.getBody().readUtf8());
    }

    @Test
    public void serverSucceededButClientTimedOut_retryKeepsFirstChoices() throws Exception {
        // 第一次：服务端其实已经下单，但响应晚于客户端超时
        server.enqueue(new MockResponse().setResponseCode(201).setBody(created(false)).setHeadersDelay(3, TimeUnit.SECONDS));
        ApiException e = submitFails(null, "3687.05");
        assertEquals(ApiException.Kind.TIMEOUT, e.kind());
        RecordedRequest first = server.takeRequest();
        JSONObject b1 = body(first);
        assertFalse(b1.has("user_coupon_ids")); // 自动选券
        assertEquals("3687.05", b1.getString("expected_pay_amount"));
        assertNotNull(store.load());

        // 用户在确认页改成“不用券”、页面金额也变了，再提交：必须原样重发第一次
        server.enqueue(new MockResponse().setResponseCode(200).setBody(created(true)));
        ShopApi.CheckoutResult r = PendingCheckout.submit(api, store, Collections.emptyList(), "3707.05");
        JSONObject b2 = body(server.takeRequest());
        assertEquals(b1.getString("idempotency_key"), b2.getString("idempotency_key"));
        assertFalse("不能带上新的用券选择", b2.has("user_coupon_ids"));
        assertEquals("3687.05", b2.getString("expected_pay_amount"));
        assertTrue(r.replayed);
        assertNull("拿到结果后清除", store.load());
    }

    @Test
    public void requestNeverArrived_retryKeepsFirstChoicesToo() throws Exception {
        online.set(false);
        ApiException e = submitFails(Arrays.asList("uc_a", "uc_b"), "100.00");
        assertEquals(ApiException.Kind.OFFLINE, e.kind());
        assertEquals(0, server.getRequestCount());
        PendingCheckout p = store.load();
        assertEquals(Arrays.asList("uc_a", "uc_b"), p.couponIds);

        online.set(true);
        server.enqueue(new MockResponse().setResponseCode(201).setBody(created(false)));
        PendingCheckout.submit(api, store, null, "120.00");
        JSONObject b = body(server.takeRequest());
        assertEquals(p.key, b.getString("idempotency_key"));
        assertEquals("uc_a", b.getJSONArray("user_coupon_ids").getString(0));
        assertEquals(2, b.getJSONArray("user_coupon_ids").length());
        assertEquals("100.00", b.getString("expected_pay_amount"));
        assertNull(store.load());
    }

    @Test
    public void serverErrorsKeepPendingFrozen() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(503).setBody("{\"code\":\"not_ready\",\"message\":\"服务暂不可用\"}"));
        submitFails(null, "50.00");
        String key = body(server.takeRequest()).getString("idempotency_key");
        server.enqueue(new MockResponse().setResponseCode(429).setBody("{\"code\":\"rate_limited\",\"message\":\"请求过于频繁\"}"));
        submitFails(Collections.singletonList("uc_x"), "60.00");
        JSONObject b = body(server.takeRequest());
        assertEquals(key, b.getString("idempotency_key"));
        assertFalse(b.has("user_coupon_ids"));
        assertNotNull(store.load());
    }

    @Test
    public void definiteRejectionEndsItAndNextSubmitIsNew() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(409).setBody("{\"code\":\"price_changed\",\"message\":\"实付金额变为 ¥229.00\"}"));
        ApiException e = submitFails(null, "249.00");
        assertEquals("price_changed", e.code());
        String key1 = body(server.takeRequest()).getString("idempotency_key");
        assertNull("明确拒绝说明没有下单，结束这一次", store.load());

        server.enqueue(new MockResponse().setResponseCode(201).setBody(created(false)));
        PendingCheckout.submit(api, store, Collections.emptyList(), "229.00");
        JSONObject b = body(server.takeRequest());
        assertNotEquals(key1, b.getString("idempotency_key"));
        assertEquals(0, b.getJSONArray("user_coupon_ids").length());
        assertEquals("229.00", b.getString("expected_pay_amount"));
    }

    @Test
    public void frozenResendRejectedThenFreeToChange() throws Exception {
        online.set(false);
        submitFails(null, "80.00");
        online.set(true);
        // 原样重发被明确拒绝（上次其实没下单、期间价格变了）
        server.enqueue(new MockResponse().setResponseCode(409).setBody("{\"code\":\"price_changed\",\"message\":\"金额变化\"}"));
        submitFails(Collections.emptyList(), "90.00");
        assertFalse(body(server.takeRequest()).has("user_coupon_ids"));
        assertNull(store.load());
        server.enqueue(new MockResponse().setResponseCode(201).setBody(created(false)));
        PendingCheckout.submit(api, store, Collections.emptyList(), "90.00");
        JSONObject b = body(server.takeRequest());
        assertEquals("90.00", b.getString("expected_pay_amount"));
        assertEquals(0, b.getJSONArray("user_coupon_ids").length());
    }

    @Test
    public void unknownAfterRules() throws Exception {
        assertTrue(PendingCheckout.unknownAfter(ApiException.offline()));
        assertTrue(PendingCheckout.unknownAfter(ApiException.badResponse(null)));
        assertTrue(PendingCheckout.unknownAfter(ApiException.fromHttp(502, "", null)));
        assertTrue(PendingCheckout.unknownAfter(ApiException.fromHttp(429, "", null)));
        assertFalse(PendingCheckout.unknownAfter(ApiException.fromHttp(409, "{\"code\":\"price_changed\"}", null)));
        assertFalse(PendingCheckout.unknownAfter(ApiException.fromHttp(400, "{\"code\":\"empty_cart\"}", null)));
    }

    @Test
    public void jsonRoundTripKeepsAutoNoneAndList() {
        String k = PendingCheckout.create(null, "1.00").key;
        assertTrue(k, k.matches("[A-Za-z0-9._:-]{1,128}"));
        assertNull(PendingCheckout.fromJson(PendingCheckout.create(null, "1.00").toJson()).couponIds);
        assertEquals(Collections.emptyList(), PendingCheckout.fromJson(PendingCheckout.create(Collections.emptyList(), "1.00").toJson()).couponIds);
        PendingCheckout p = PendingCheckout.fromJson(PendingCheckout.create(Arrays.asList("a", "b"), "2.50").toJson());
        assertEquals(Arrays.asList("a", "b"), p.couponIds);
        assertEquals("2.50", p.expectedPayAmount);
        assertEquals("指定 2 张券", p.couponDescription());
        assertNull(PendingCheckout.fromJson("not json"));
        assertNull(PendingCheckout.fromJson(null));
    }
}
