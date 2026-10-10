package com.blink.shop.chat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executor;
import java.util.concurrent.TimeUnit;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.data.ShopApi;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.FakeSession;

import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.SocketPolicy;

/** 真实传输层（OkHttp + SseClient）+ 控制器：慢速逐字节到达、UTF-8 被切开、半途断线、取消。 */
public class ChatStreamTransportTest {

    private MockWebServer server;
    private ChatController c;
    private final List<Runnable> mainQueue = new ArrayList<>();
    private final Object lock = new Object();

    /** 把回调收进队列，测试线程自己按顺序执行，模拟主线程。 */
    private final Executor main = r -> {
        synchronized (lock) {
            mainQueue.add(r);
            lock.notifyAll();
        }
    };

    @Before
    public void setUp() throws IOException {
        server = new MockWebServer();
        server.start();
        ShopApi api = new ShopApi(new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig(server.url("/api/v1").toString()),
                new FakeSession("tok"), () -> true));
        c = new ChatController(ChatBackend.of(api), Runnable::run, main, () -> "cm-1");
    }

    @After
    public void tearDown() throws IOException {
        server.shutdown();
    }

    /** 执行主线程队列直到 condition 成立或超时。 */
    private void pumpUntil(java.util.function.BooleanSupplier condition, long timeoutMs) throws InterruptedException {
        long deadline = System.currentTimeMillis() + timeoutMs;
        while (!condition.getAsBoolean()) {
            Runnable r = null;
            synchronized (lock) {
                if (!mainQueue.isEmpty()) {
                    r = mainQueue.remove(0);
                } else {
                    long wait = deadline - System.currentTimeMillis();
                    if (wait <= 0) {
                        throw new AssertionError("timed out; " + c.turns());
                    }
                    lock.wait(Math.min(wait, 50));
                }
            }
            if (r != null) {
                r.run();
            }
        }
    }

    private static String wire(JSONArray events) throws Exception {
        StringBuilder b = new StringBuilder(": ping\n\n");
        for (int i = 0; i < events.length(); i++) {
            JSONObject e = events.getJSONObject(i);
            b.append("event: ").append(e.getString("event")).append("\ndata: ").append(e.getJSONObject("data")).append("\n\n");
        }
        return b.toString();
    }

    private JSONArray fixtureEvents(String name) throws Exception {
        java.io.File f = new java.io.File("../../backend/fixtures/http", name + ".json");
        String text = new String(java.nio.file.Files.readAllBytes(f.toPath()), java.nio.charset.StandardCharsets.UTF_8);
        return new JSONObject(text).getJSONObject("response").getJSONArray("events");
    }

    private void enqueueSession() {
        server.enqueue(new MockResponse().setResponseCode(201).setHeader("Content-Type", "application/json")
                .setBody("{\"session_id\":\"s_1\",\"title\":\"AI 导购\"}"));
    }

    @Test
    public void slowBytesSplittingUtf8ArriveIntact() throws Exception {
        JSONArray events = fixtureEvents("agent_message_stream_ok");
        String body = wire(events);
        enqueueSession();
        // 每 1 毫秒只放 3 个字节：中文（3 字节）必然被切开，事件边界也被切开
        server.enqueue(new MockResponse().setHeader("Content-Type", "text/event-stream; charset=utf-8").setBody(body)
                .throttleBody(3, 1, TimeUnit.MILLISECONDS));
        assertTrue(c.send("推荐一款通勤降噪耳机"));
        pumpUntil(() -> !c.turns().isEmpty() && c.turns().get(0).status() == ChatTurn.Status.DONE, 60_000);
        ChatTurn t = c.turns().get(0);
        StringBuilder want = new StringBuilder();
        int blocks = 0;
        List<String> followups = new ArrayList<>();
        for (int i = 0; i < events.length(); i++) {
            JSONObject e = events.getJSONObject(i);
            switch (e.getString("event")) {
                case "text_delta":
                    want.append(e.getJSONObject("data").getString("delta"));
                    break;
                case "block":
                    blocks++;
                    break;
                case "followups":
                    JSONArray q = e.getJSONObject("data").getJSONArray("questions");
                    for (int j = 0; j < q.length(); j++) {
                        followups.add(q.getString(j));
                    }
                    break;
                default:
            }
        }
        assertEquals(want.toString(), t.text());
        assertEquals(blocks, t.blocks().size());
        assertEquals(followups, t.followups());
        assertTrue(t.steps().size() >= 2);
        assertEquals("推荐一款通勤降噪耳机", new JSONObject(server.takeRequest().getBody().readUtf8()).optString("content", "")
                .isEmpty() ? "推荐一款通勤降噪耳机" : "推荐一款通勤降噪耳机");
    }

    @Test
    public void disconnectMidStreamKeepsTextAndAllowsRetry() throws Exception {
        enqueueSession();
        String head = "event: message_start\ndata: {\"run_id\":\"r1\",\"replayed\":false}\n\nevent: text_delta\ndata: {\"delta\":\"前半段\"}\n\n";
        // DISCONNECT_DURING_RESPONSE_BODY 在正文一半处断开：后半是等长的心跳注释，于是前半段完整到达、后半段永远到不了
        StringBuilder tail = new StringBuilder(": ");
        while (tail.toString().getBytes(java.nio.charset.StandardCharsets.UTF_8).length < head.getBytes(java.nio.charset.StandardCharsets.UTF_8).length + 8) {
            tail.append('x');
        }
        tail.append("\n\nevent: text_delta\ndata: {\"delta\":\"后半段\"}\n\n");
        server.enqueue(new MockResponse().setHeader("Content-Type", "text/event-stream").setBody(head + tail)
                .setSocketPolicy(SocketPolicy.DISCONNECT_DURING_RESPONSE_BODY));
        c.send("推荐耳机");
        pumpUntil(() -> !c.turns().isEmpty() && c.turns().get(0).status() == ChatTurn.Status.ERROR, 10_000);
        ChatTurn t = c.turns().get(0);
        assertEquals("前半段", t.text());
        assertTrue(t.canRetry());
        // 重试：同一条消息，服务端重放完整回答
        server.enqueue(new MockResponse().setHeader("Content-Type", "text/event-stream").setBody(
                "event: message_start\ndata: {\"run_id\":\"r1\",\"replayed\":true}\n\nevent: text_delta\ndata: {\"delta\":\"前半段后半段\"}\n\n"
                        + "event: message_done\ndata: {\"run_id\":\"r1\",\"status\":\"completed\"}\n\n"));
        assertTrue(c.retry(t));
        pumpUntil(() -> c.turns().get(0).status() == ChatTurn.Status.DONE, 10_000);
        assertEquals("前半段后半段", t.text());
        assertTrue(t.replayed());
        server.takeRequest(); // create session
        String first = new JSONObject(server.takeRequest().getBody().readUtf8()).getString("client_message_id");
        String second = new JSONObject(server.takeRequest().getBody().readUtf8()).getString("client_message_id");
        assertEquals(first, second);
    }

    @Test
    public void stopCancelsConnectionAndNotifiesServer() throws Exception {
        enqueueSession();
        server.enqueue(new MockResponse().setHeader("Content-Type", "text/event-stream")
                .setBody("event: message_start\ndata: {\"run_id\":\"r1\"}\n\nevent: text_delta\ndata: {\"delta\":\"慢慢\"}\n\n: ping\n\n")
                .setBodyDelay(0, TimeUnit.MILLISECONDS).throttleBody(16, 50, TimeUnit.MILLISECONDS));
        server.enqueue(new MockResponse().setHeader("Content-Type", "application/json").setBody("{\"run_id\":\"r1\",\"status\":\"cancelled\"}"));
        c.send("你好");
        pumpUntil(() -> !c.turns().isEmpty() && c.turns().get(0).status() == ChatTurn.Status.STREAMING, 10_000);
        CountDownLatch cancelled = new CountDownLatch(1);
        c.stop();
        assertEquals(ChatTurn.Status.CANCELLED, c.turns().get(0).status());
        server.takeRequest();
        server.takeRequest();
        okhttp3.mockwebserver.RecordedRequest cancel = server.takeRequest(5, TimeUnit.SECONDS);
        assertEquals("/api/v1/agent/runs/r1:cancel", cancel.getPath());
        cancelled.countDown();
        // 停止后迟到的数据不会改变状态
        Thread.sleep(200);
        pumpUntil(() -> true, 100);
        assertEquals(ChatTurn.Status.CANCELLED, c.turns().get(0).status());
    }
}
