package com.blink.shop.data;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.io.IOException;
import java.util.concurrent.TimeUnit;

import org.junit.Test;

import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;

import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;

public class DraftsTest {

    private static ApiException respond(MockResponse resp) throws IOException {
        MockWebServer server = new MockWebServer();
        server.start();
        try {
            server.enqueue(resp);
            ApiClient api = new ApiClient(ApiClient.defaultHttp().readTimeout(1, TimeUnit.SECONDS).build(),
                    new ApiConfig(server.url("/api/v1").toString()), new FakeSession("t"), () -> true);
            api.get("/x");
            throw new AssertionError("expected failure");
        } catch (ApiException e) {
            return e;
        } finally {
            server.shutdown();
        }
    }

    @Test
    public void keepsKeyWhenResultUnknown() throws IOException {
        // 超时：服务端可能已经下单，必须用同一个键重试
        assertTrue(Drafts.keepKeyAfter(respond(new MockResponse().setBody("{}").setHeadersDelay(3, TimeUnit.SECONDS))));
        assertTrue(Drafts.keepKeyAfter(ApiException.offline()));
        assertTrue(Drafts.keepKeyAfter(respond(new MockResponse().setResponseCode(502))));
        assertTrue(Drafts.keepKeyAfter(respond(new MockResponse().setResponseCode(429))));
        assertTrue(Drafts.keepKeyAfter(ApiException.badResponse(null)));
    }

    @Test
    public void dropsKeyWhenServerRejected() throws IOException {
        assertFalse(Drafts.keepKeyAfter(respond(new MockResponse().setResponseCode(409).setBody("{\"code\":\"price_changed\"}"))));
        assertFalse(Drafts.keepKeyAfter(respond(new MockResponse().setResponseCode(400).setBody("{\"code\":\"empty_cart\"}"))));
    }

    @Test
    public void newKeysMatchServerFormat() {
        String k = Drafts.newCheckoutKey();
        assertTrue(k, k.matches("[A-Za-z0-9._:-]{1,128}"));
        assertFalse(k.equals(Drafts.newCheckoutKey()));
    }
}
