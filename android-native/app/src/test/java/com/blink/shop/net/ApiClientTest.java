package com.blink.shop.net;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.IOException;
import java.net.ServerSocket;
import java.util.Collections;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;
import okhttp3.mockwebserver.SocketPolicy;

public class ApiClientTest {

    private MockWebServer server;
    private FakeSession session;
    private final AtomicBoolean online = new AtomicBoolean(true);
    private ApiClient api;

    @Before
    public void setUp() throws IOException {
        server = new MockWebServer();
        server.start();
        session = new FakeSession(null);
        api = new ApiClient(ApiClient.defaultHttp().readTimeout(1, TimeUnit.SECONDS).build(),
                new ApiConfig(server.url("/api/v1/").toString()), session, online::get);
    }

    @After
    public void tearDown() throws IOException {
        server.shutdown();
    }

    private static ApiException expectFailure(ThrowingRunnable r) {
        try {
            r.run();
        } catch (ApiException e) {
            return e;
        }
        fail("expected ApiException");
        return null;
    }

    private interface ThrowingRunnable {
        void run() throws ApiException;
    }

    @Test
    public void getParsesJsonAndSendsNoTokenWhenLoggedOut() throws Exception {
        server.enqueue(new MockResponse().setBody("{\"status\":\"ok\"}"));
        JSONObject r = api.get("/health");
        assertEquals("ok", r.getString("status"));
        RecordedRequest req = server.takeRequest();
        assertEquals("/api/v1/health", req.getPath());
        assertNull(req.getHeader("Authorization"));
        assertEquals("application/json", req.getHeader("Accept"));
    }

    @Test
    public void addsBearerTokenAndJsonBody() throws Exception {
        session.token = "tok_1";
        server.enqueue(new MockResponse().setBody("{}"));
        api.patch("/account/profile", new JSONObject().put("display_name", "小蓝"));
        RecordedRequest req = server.takeRequest();
        assertEquals("PATCH", req.getMethod());
        assertEquals("Bearer tok_1", req.getHeader("Authorization"));
        assertTrue(req.getHeader("Content-Type").startsWith("application/json"));
        assertEquals("小蓝", new JSONObject(req.getBody().readUtf8()).getString("display_name"));
    }

    @Test
    public void unauthorizedWithTokenReportsThatToken() {
        session.token = "tok_old";
        server.enqueue(new MockResponse().setResponseCode(401)
                .setBody("{\"code\":\"unauthorized\",\"message\":\"登录已失效，请重新登录\",\"request_id\":\"r1\"}"));
        ApiException e = expectFailure(() -> api.get("/auth/me"));
        assertTrue(e.isUnauthorized());
        assertEquals("登录已失效，请重新登录", e.getMessage());
        assertEquals(Collections.singletonList("tok_old"), session.rejected);
    }

    @Test
    public void unauthorizedWithoutTokenIsNotSessionExpiry() {
        // 登录密码错误也是 401，但请求没带 token，不能当成登录失效
        server.enqueue(new MockResponse().setResponseCode(401)
                .setBody("{\"code\":\"invalid_credential\",\"message\":\"账号或密码错误\",\"request_id\":\"r2\"}"));
        ApiException e = expectFailure(() -> api.post("/auth/login", new JSONObject()));
        assertEquals("invalid_credential", e.code());
        assertEquals("账号或密码错误", e.getMessage());
        assertEquals("r2", e.requestId());
        assertTrue(session.rejected.isEmpty());
    }

    @Test
    public void errorBodyFieldAndRequestIdHeader() {
        server.enqueue(new MockResponse().setResponseCode(400).setHeader("X-Request-ID", "hdr")
                .setBody("{\"code\":\"invalid_argument\",\"message\":\"账号只能包含字母、数字、下划线或短横线\",\"field\":\"username\"}"));
        ApiException e = expectFailure(() -> api.post("/auth/register", new JSONObject()));
        assertEquals(ApiException.Kind.HTTP, e.kind());
        assertEquals(400, e.status());
        assertEquals("username", e.field());
        assertEquals("hdr", e.requestId());
    }

    @Test
    public void nonJsonErrorUsesStatusMessage() {
        server.enqueue(new MockResponse().setResponseCode(502).setBody("<html>Bad Gateway</html>"));
        ApiException e = expectFailure(() -> api.get("/products"));
        assertEquals(502, e.status());
        assertEquals("服务暂时不可用，请稍后重试（502）", e.getMessage());
        server.enqueue(new MockResponse().setResponseCode(429));
        assertEquals("请求过于频繁，请稍后再试", expectFailure(() -> api.get("/products")).getMessage());
    }

    @Test
    public void emptySuccessBodyIsEmptyObject() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(200));
        assertEquals(0, api.delete("/account").length());
    }

    @Test
    public void invalidJsonSuccessIsBadResponse() {
        server.enqueue(new MockResponse().setBody("not json"));
        ApiException e = expectFailure(() -> api.get("/products"));
        assertEquals(ApiException.Kind.BAD_RESPONSE, e.kind());
        assertEquals(ApiException.MSG_BAD_RESPONSE, e.getMessage());
    }

    @Test
    public void offlineFailsWithoutSendingRequest() {
        online.set(false);
        ApiException e = expectFailure(() -> api.get("/products"));
        assertEquals(ApiException.Kind.OFFLINE, e.kind());
        assertEquals(ApiException.MSG_OFFLINE, e.getMessage());
        assertTrue(e.isNetwork());
        assertEquals(0, server.getRequestCount());
    }

    @Test
    public void wrongServerAddressIsUnreachable() throws Exception {
        int port;
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        ApiClient wrong = new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig("http://127.0.0.1:" + port + "/api/v1"),
                session, online::get);
        ApiException e = expectFailure(() -> wrong.get("/health"));
        assertEquals(ApiException.Kind.UNREACHABLE, e.kind());
        assertEquals(ApiException.MSG_UNREACHABLE, e.getMessage());
        ApiClient unknownHost = new ApiClient(ApiClient.defaultHttp().build(),
                new ApiConfig("http://blink-shop.invalid/api/v1"), session, online::get);
        assertEquals(ApiException.Kind.UNREACHABLE, expectFailure(() -> unknownHost.get("/health")).kind());
    }

    @Test
    public void slowServerTimesOut() {
        server.enqueue(new MockResponse().setBody("{}").setHeadersDelay(3, TimeUnit.SECONDS));
        ApiException e = expectFailure(() -> api.get("/products"));
        assertEquals(ApiException.Kind.TIMEOUT, e.kind());
        assertEquals(ApiException.MSG_TIMEOUT, e.getMessage());
    }

    @Test
    public void connectionDroppedMidRequestWhileOfflineReportsOffline() {
        server.enqueue(new MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AT_START));
        AtomicBoolean first = new AtomicBoolean(true);
        // 第一次检查（发请求前）在线，失败后再检查时已断网
        ApiClient flaky = new ApiClient(ApiClient.defaultHttp().retryOnConnectionFailure(false).build(),
                new ApiConfig(server.url("/api/v1").toString()), session, () -> first.getAndSet(false));
        assertEquals(ApiException.Kind.OFFLINE, expectFailure(() -> flaky.get("/products")).kind());
    }

    @Test
    public void uploadSendsMultipartFile() throws Exception {
        session.token = "tok_1";
        server.enqueue(new MockResponse().setBody("{\"url\":\"/api/v1/uploads/avatar/a.jpg\"}"));
        api.upload("/uploads/avatar", "file", "avatar.jpg", "image/jpeg", new byte[] {1, 2, 3});
        RecordedRequest req = server.takeRequest();
        assertTrue(req.getHeader("Content-Type").startsWith("multipart/form-data"));
        String body = req.getBody().readUtf8();
        assertTrue(body.contains("name=\"file\"; filename=\"avatar.jpg\""));
        assertTrue(body.contains("Content-Type: image/jpeg"));
    }
}
