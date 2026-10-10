package com.blink.shop.chat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.concurrent.Executor;
import java.util.concurrent.atomic.AtomicInteger;

import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.net.ApiException;
import com.blink.shop.net.SseClient;
import com.blink.shop.net.SseParser;

/** 控制器逻辑：用可手动推事件的假后端，执行器都是同步的，便于逐步断言。 */
public class ChatControllerTest {

    /** 假后端：记录调用，流由测试手动推送事件。 */
    static final class FakeBackend implements ChatBackend {
        final List<String> calls = new ArrayList<>();
        final List<String> streamed = new ArrayList<>(); // sessionId|clientId|content
        final List<String> cancelled = new ArrayList<>();
        SseClient.Listener listener;
        FakeStream stream;
        ApiException createError;
        JSONObject detail;
        ApiException detailError;
        int created;

        @Override
        public String createSession() throws ApiException {
            calls.add("create");
            if (createError != null) {
                throw createError;
            }
            return "s_" + (++created);
        }

        @Override
        public JSONObject sessionDetail(String sessionId) throws ApiException {
            calls.add("detail:" + sessionId);
            if (detailError != null) {
                throw detailError;
            }
            return detail;
        }

        @Override
        public SseClient.Stream stream(String sessionId, String clientMessageId, String content, SseClient.Listener l) {
            streamed.add(sessionId + "|" + clientMessageId + "|" + content);
            listener = l;
            stream = new FakeStream();
            return stream.asStream();
        }

        @Override
        public void cancelRun(String runId) {
            cancelled.add(runId);
        }

        void event(String name, String json) {
            listener.onEvent(new SseParser.Event(name, json, ""));
        }

        void text(String delta) {
            event("text_delta", "{\"delta\":" + JSONObject.quote(delta) + "}");
        }

        void start(String runId) {
            event("message_start", "{\"run_id\":\"" + runId + "\",\"message_id\":\"m1\",\"session_id\":\"s_1\",\"trace_id\":\"t1\",\"replayed\":false}");
        }

        void done(String runId) {
            event("message_done", "{\"run_id\":\"" + runId + "\",\"status\":\"completed\"}");
        }
    }

    /** SseClient.Stream 只有包内构造器：用反射造一个可观察取消的实例。 */
    static final class FakeStream {
        boolean cancelled;
        private final okhttp3.Call call;

        FakeStream() {
            okhttp3.OkHttpClient c = new okhttp3.OkHttpClient();
            call = c.newCall(new okhttp3.Request.Builder().url("http://127.0.0.1:1/never").build());
        }

        SseClient.Stream asStream() {
            try {
                java.lang.reflect.Constructor<SseClient.Stream> ctor = SseClient.Stream.class.getDeclaredConstructor(okhttp3.Call.class);
                ctor.setAccessible(true);
                return ctor.newInstance(call);
            } catch (ReflectiveOperationException e) {
                throw new IllegalStateException(e);
            }
        }

        boolean isCancelled() {
            return call.isCanceled();
        }
    }

    private FakeBackend backend;
    private ChatController c;
    private final AtomicInteger changes = new AtomicInteger();
    private final List<String> sessions = new ArrayList<>();
    private final Executor direct = Runnable::run;

    @Before
    public void setUp() {
        backend = new FakeBackend();
        AtomicInteger seq = new AtomicInteger();
        c = new ChatController(backend, direct, direct, () -> "cm-" + seq.incrementAndGet());
        c.attach(new ChatController.Listener() {
            @Override
            public void onChanged() {
                changes.incrementAndGet();
            }

            @Override
            public void onSessionChanged(String sessionId) {
                sessions.add(sessionId);
            }
        });
    }

    private ChatTurn last() {
        return c.turns().get(c.turns().size() - 1);
    }

    @Test
    public void sendCreatesSessionThenStreamsInOrder() {
        assertFalse(c.send("   "));
        assertTrue(c.send(" 推荐一款通勤降噪耳机 "));
        assertEquals(Arrays.asList("create"), backend.calls);
        assertEquals(Arrays.asList("s_1"), sessions);
        assertEquals(Arrays.asList("s_1|cm-1|推荐一款通勤降噪耳机"), backend.streamed);
        ChatTurn t = last();
        assertEquals(ChatTurn.Status.SENDING, t.status());
        backend.start("run_1");
        assertEquals(ChatTurn.Status.STREAMING, t.status());
        assertEquals("run_1", t.runId());
        backend.event("thinking", "{\"step\":{\"id\":\"understand\",\"title\":\"理解你的问题\",\"status\":\"running\"}}");
        backend.event("thinking", "{\"step\":{\"id\":\"understand\",\"title\":\"理解你的问题\",\"status\":\"done\"}}");
        backend.event("thinking", "{\"step\":{\"id\":\"tool-1\",\"title\":\"搜索商品\",\"status\":\"running\"}}");
        assertEquals(2, t.steps().size());
        assertTrue(t.steps().get(0).done);
        assertFalse(t.steps().get(1).done);
        // 慢速、细碎的 delta 按顺序拼接
        for (String d : new String[]{"按“通勤", "降噪耳机”", "找到 1 件", "在售商品。"}) {
            backend.text(d);
        }
        assertEquals("按“通勤降噪耳机”找到 1 件在售商品。", t.text());
        backend.event("block", "{\"block\":{\"type\":\"product_list\",\"title\":\"x\",\"products\":[{\"product_id\":\"p_seed_earbuds\"}]}}");
        backend.event("followups", "{\"questions\":[\"把第一个加入购物车\",\"有什么优惠券\"]}");
        assertEquals(1, t.blocks().size());
        assertEquals("product_list", t.blocks().get(0).optString("type"));
        assertEquals(2, t.followups().size());
        assertTrue(c.isStreaming());
        backend.done("run_1");
        assertEquals(ChatTurn.Status.DONE, t.status());
        assertFalse(c.isStreaming());
        // 第二条消息复用会话，不再创建
        assertTrue(c.send("把第一个加入购物车"));
        assertEquals(1, backend.created);
        assertEquals("s_1|cm-2|把第一个加入购物车", backend.streamed.get(1));
    }

    @Test
    public void duplicateSendClicksAreIgnoredWhileBusy() {
        assertTrue(c.send("你好"));
        assertFalse(c.send("你好"));
        assertFalse(c.send("再发一条"));
        backend.start("r1");
        assertFalse(c.send("还在生成"));
        assertEquals(1, backend.streamed.size());
        assertEquals(1, c.turns().size());
        backend.done("r1");
        assertTrue(c.send("现在可以了"));
        assertEquals(2, backend.streamed.size());
    }

    @Test
    public void eventsFromOtherRunsAndStaleStreamsAreIgnored() {
        c.send("你好");
        backend.start("r1");
        backend.text("A");
        // 另一个 run id 的事件不接受
        backend.event("text_delta", "{\"run_id\":\"r_other\",\"delta\":\"X\"}");
        backend.event("message_done", "{\"run_id\":\"r_other\",\"status\":\"completed\"}");
        assertEquals("A", last().text());
        assertEquals(ChatTurn.Status.STREAMING, last().status());
        // 坏 JSON 跳过，不中断
        backend.event("text_delta", "{not json");
        backend.text("B");
        assertEquals("AB", last().text());
        // 停止后旧流迟到的事件忽略
        SseClient.Listener old = backend.listener;
        c.stop();
        assertEquals(ChatTurn.Status.CANCELLED, last().status());
        assertTrue(backend.stream.isCancelled());
        assertEquals(Arrays.asList("r1"), backend.cancelled);
        old.onEvent(new SseParser.Event("text_delta", "{\"delta\":\"C\"}", ""));
        old.onEvent(new SseParser.Event("message_done", "{\"run_id\":\"r1\"}", ""));
        old.onClosed();
        assertEquals("AB", last().text());
        assertEquals(ChatTurn.Status.CANCELLED, last().status());
        assertEquals("已停止生成", last().errorMessage());
        // 停止前还没拿到 run id：只断开，不调用取消接口
        c.send("再问");
        c.stop();
        assertEquals(1, backend.cancelled.size());
        assertEquals(ChatTurn.Status.CANCELLED, last().status());
        // 没有进行中的生成时 stop 什么都不做
        c.stop();
        assertFalse(c.isStreaming());
    }

    @Test
    public void disconnectIsRetriableWithSameIdAndKeepsPartialText() {
        c.send("推荐耳机");
        backend.start("r1");
        backend.text("前半段");
        backend.listener.onClosed(); // 服务端提前关闭
        ChatTurn t = last();
        assertEquals(ChatTurn.Status.ERROR, t.status());
        assertEquals("前半段", t.text());
        assertEquals("连接中断，请重试", t.errorMessage());
        assertTrue(t.canRetry());
        assertTrue(c.retry(t));
        // 同一个 client_message_id：服务端会重放或等它结束
        assertEquals("s_1|cm-1|推荐耳机", backend.streamed.get(1));
        assertEquals("", t.text()); // 重放前清空，避免拼接两遍
        backend.event("message_start", "{\"run_id\":\"r1\",\"replayed\":true}");
        assertTrue(t.replayed());
        backend.text("前半段后半段");
        backend.done("r1");
        assertEquals("前半段后半段", t.text());
        assertEquals(ChatTurn.Status.DONE, t.status());
    }

    @Test
    public void transportErrorAndCreateFailureAreRetriable() {
        backend.createError = ApiException.offline();
        c.send("你好");
        ChatTurn t = last();
        assertEquals(ChatTurn.Status.ERROR, t.status());
        assertEquals(ApiException.MSG_OFFLINE, t.errorMessage());
        assertTrue(backend.streamed.isEmpty());
        assertEquals("", c.sessionId());
        // 恢复后重试：再创建会话并发送同一条
        backend.createError = null;
        assertTrue(c.retry(t));
        assertEquals("s_1", c.sessionId());
        assertEquals(Arrays.asList("s_1|cm-1|你好"), backend.streamed);
        backend.listener.onError(ApiException.offline());
        assertEquals(ChatTurn.Status.ERROR, t.status());
        assertTrue(c.retry(t));
        assertEquals("s_1|cm-1|你好", backend.streamed.get(1));
    }

    @Test
    public void serverErrorRetriesWithNewId() {
        c.send("你好");
        backend.start("r1");
        backend.text("一半");
        backend.event("error", "{\"run_id\":\"r1\",\"code\":\"run_timeout\",\"message\":\"回答超时，请稍后重试\"}");
        ChatTurn t = last();
        assertEquals(ChatTurn.Status.ERROR, t.status());
        assertEquals("回答超时，请稍后重试", t.errorMessage());
        assertEquals("run_timeout", t.errorCode());
        assertTrue(c.retry(t));
        // 新 ID、同样的内容，替换掉失败的那一轮
        assertEquals(1, c.turns().size());
        assertEquals("s_1|cm-2|你好", backend.streamed.get(1));
        assertFalse(c.turns().contains(t));
        // 服务端重放的取消
        backend.event("message_start", "{\"run_id\":\"r2\",\"replayed\":true}");
        backend.event("error", "{\"run_id\":\"r2\",\"code\":\"cancelled\",\"message\":\"已停止生成\"}");
        assertEquals(ChatTurn.Status.CANCELLED, last().status());
        assertFalse(last().canRetry());
        assertTrue(c.resend(last()));
        assertEquals("s_1|cm-3|你好", backend.streamed.get(2));
    }

    @Test
    public void loadSessionRebuildsTurnsAndResumesRunningRun() throws Exception {
        backend.detail = new JSONObject("{\"session\":{\"session_id\":\"s_9\"},\"messages\":["
                + "{\"message_id\":\"m1\",\"client_message_id\":\"c-1\",\"content\":\"推荐耳机\",\"created_at\":\"2026-10-09T00:00:00Z\",\"run\":{\"run_id\":\"r1\",\"status\":\"completed\",\"content\":\"推荐 Blink Air\",\"blocks\":[{\"type\":\"product_list\",\"products\":[]}],\"followups\":[\"加购\"],\"error_code\":\"\"}},"
                + "{\"message_id\":\"m2\",\"client_message_id\":\"c-2\",\"content\":\"取消的\",\"run\":{\"run_id\":\"r2\",\"status\":\"cancelled\",\"content\":\"一半\",\"error_code\":\"cancelled\"}},"
                + "{\"message_id\":\"m3\",\"client_message_id\":\"c-3\",\"content\":\"失败的\",\"run\":{\"run_id\":\"r3\",\"status\":\"failed\",\"content\":\"\",\"error_code\":\"interrupted\"}},"
                + "{\"message_id\":\"m4\",\"client_message_id\":\"c-4\",\"content\":\"还在跑\",\"run\":{\"run_id\":\"r4\",\"status\":\"running\",\"content\":\"\"}}"
                + "]}");
        c.loadSession("s_9");
        assertEquals(Arrays.asList("detail:s_9"), backend.calls);
        assertEquals(Arrays.asList("s_9"), sessions);
        assertFalse(c.isLoading());
        List<ChatTurn> turns = c.turns();
        assertEquals(4, turns.size());
        assertEquals(ChatTurn.Status.DONE, turns.get(0).status());
        assertEquals("推荐 Blink Air", turns.get(0).text());
        assertEquals(1, turns.get(0).blocks().size());
        assertEquals(Arrays.asList("加购"), turns.get(0).followups());
        assertEquals("2026-10-09T00:00:00Z", turns.get(0).createdAt);
        assertEquals(ChatTurn.Status.CANCELLED, turns.get(1).status());
        assertEquals("一半", turns.get(1).text());
        assertEquals(ChatTurn.Status.ERROR, turns.get(2).status());
        assertEquals("服务重启中断了这次回答，请重新发送", turns.get(2).errorMessage());
        // 最后一轮还在运行：用同一个 client_message_id 重新连接，不是新消息
        assertEquals(ChatTurn.Status.SENDING, turns.get(3).status());
        assertEquals(Arrays.asList("s_9|c-4|还在跑"), backend.streamed);
        assertTrue(c.isStreaming());
        backend.event("message_start", "{\"run_id\":\"r4\",\"replayed\":true}");
        backend.text("终于好了");
        backend.done("r4");
        assertEquals(ChatTurn.Status.DONE, turns.get(3).status());
        assertEquals("终于好了", turns.get(3).text());
        // 加载失败
        backend.detailError = ApiException.offline();
        c.loadSession("s_10");
        assertEquals(ApiException.MSG_OFFLINE, c.loadError());
        assertTrue(c.turns().isEmpty());
        // 新会话：清空，服务端会话延后到第一条消息
        c.newSession();
        assertEquals("", c.sessionId());
        assertEquals("", sessions.get(sessions.size() - 1));
    }

    @Test
    public void switchingSessionAbandonsActiveStream() {
        c.send("你好");
        backend.start("r1");
        SseClient.Listener old = backend.listener;
        backend.detail = new JSONObject();
        c.loadSession("s_2");
        assertTrue(backend.stream.isCancelled());
        assertFalse(c.isStreaming());
        old.onEvent(new SseParser.Event("text_delta", "{\"delta\":\"迟到\"}", ""));
        assertTrue(c.turns().isEmpty());
        // 创建会话期间切走：创建完成后不再发送
        FakeBackend slow = new FakeBackend();
        List<Runnable> queued = new ArrayList<>();
        ChatController c2 = new ChatController(slow, queued::add, direct, () -> "x");
        c2.send("你好");
        c2.newSession();
        queued.get(0).run();
        assertTrue(slow.streamed.isEmpty());
        assertEquals("", c2.sessionId());
    }

    @Test
    public void detachedControllerKeepsReceivingAndAttachRendersLatest() {
        c.send("你好");
        backend.start("r1");
        c.detach(); // 页面进入后台
        int before = changes.get();
        backend.text("后台");
        backend.text("也在收");
        backend.done("r1");
        assertEquals(before, changes.get());
        assertEquals(ChatTurn.Status.DONE, last().status());
        assertEquals("后台也在收", last().text());
        c.attach(new ChatController.Listener() {
            @Override
            public void onChanged() {
                changes.incrementAndGet();
            }

            @Override
            public void onSessionChanged(String sessionId) {
            }
        });
        assertEquals(before + 1, changes.get()); // 回到前台立刻重绘一次
    }

    @Test
    public void disposeCancelsStream() {
        c.send("你好");
        backend.start("r1");
        c.dispose();
        assertTrue(backend.stream.isCancelled());
        assertFalse(c.isStreaming());
        backend.listener.onEvent(new SseParser.Event("text_delta", "{\"delta\":\"迟到\"}", ""));
        assertEquals("", c.turns().get(0).text());
    }
}
