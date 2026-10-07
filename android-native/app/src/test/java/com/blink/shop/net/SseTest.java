package com.blink.shop.net;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import java.io.IOException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;

public class SseTest {

    private static List<SseParser.Event> parse(String... lines) {
        List<SseParser.Event> out = new ArrayList<>();
        SseParser p = new SseParser(out::add);
        for (String l : lines) {
            p.line(l);
        }
        p.finish();
        return out;
    }

    @Test
    public void parsesEventsDataAndIds() {
        List<SseParser.Event> ev = parse(
                ": heartbeat", "",
                "event: message_start", "id: 1", "data: {\"run_id\":\"r1\"}", "",
                "data: 第一行", "data:第二行", "",
                "event: text_delta", "data", "",
                "event: message_done", "retry: 3000", "");
        assertEquals(3, ev.size());
        assertEquals("message_start", ev.get(0).event);
        assertEquals("{\"run_id\":\"r1\"}", ev.get(0).data);
        assertEquals("1", ev.get(0).id);
        assertEquals("message", ev.get(1).event);
        assertEquals("第一行\n第二行", ev.get(1).data);
        assertEquals("1", ev.get(1).id); // id 沿用上一次
        assertEquals("text_delta", ev.get(2).event);
        assertEquals("", ev.get(2).data);
    }

    @Test
    public void dropsUnterminatedLastEvent() {
        List<SseParser.Event> ev = parse("event: a", "data: 1", "", "event: b", "data: 2");
        assertEquals(1, ev.size());
        assertEquals("a", ev.get(0).event);
    }

    private MockWebServer server;
    private FakeSession session;
    private SseClient sse;

    @Before
    public void setUp() throws IOException {
        server = new MockWebServer();
        server.start();
        session = new FakeSession("tok_1");
        ApiClient api = new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig(server.url("/api/v1").toString()),
                session, () -> true);
        sse = new SseClient(api);
    }

    @After
    public void tearDown() throws IOException {
        server.shutdown();
    }

    private static final class Recorder implements SseClient.Listener {
        final List<String> log = Collections.synchronizedList(new ArrayList<>());
        final CountDownLatch done = new CountDownLatch(1);
        volatile ApiException error;

        @Override
        public void onEvent(SseParser.Event event) {
            log.add(event.event + "=" + event.data);
        }

        @Override
        public void onClosed() {
            log.add("closed");
            done.countDown();
        }

        @Override
        public void onError(ApiException e) {
            error = e;
            log.add("error");
            done.countDown();
        }
    }

    @Test
    public void streamsEventsThenCloses() throws Exception {
        server.enqueue(new MockResponse().setHeader("Content-Type", "text/event-stream")
                .setBody("event: message_start\r\ndata: {}\r\n\r\nevent: text_delta\ndata: 你好\n\nevent: message_done\ndata: {}\n\n"));
        Recorder r = new Recorder();
        sse.post("/agent/sessions/s1/messages:stream", new JSONObject().put("content", "hi"), r);
        assertTrue(r.done.await(5, TimeUnit.SECONDS));
        assertEquals(java.util.Arrays.asList("message_start={}", "text_delta=你好", "message_done={}", "closed"), r.log);
        RecordedRequest req = server.takeRequest();
        assertEquals("POST", req.getMethod());
        assertEquals("text/event-stream", req.getHeader("Accept"));
        assertEquals("Bearer tok_1", req.getHeader("Authorization"));
        assertEquals("hi", new JSONObject(req.getBody().readUtf8()).getString("content"));
    }

    @Test
    public void httpErrorEndsWithError() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(401).setBody("{\"code\":\"unauthorized\",\"message\":\"请先登录\"}"));
        Recorder r = new Recorder();
        sse.post("/agent/sessions/s1/messages:stream", new JSONObject(), r);
        assertTrue(r.done.await(5, TimeUnit.SECONDS));
        assertEquals(Collections.singletonList("error"), r.log);
        assertTrue(r.error.isUnauthorized());
        assertEquals(Collections.singletonList("tok_1"), session.rejected);
    }

    @Test
    public void cancelStopsCallbacks() throws Exception {
        server.enqueue(new MockResponse().setHeader("Content-Type", "text/event-stream")
                .setBody("event: a\ndata: 1\n\n").setBodyDelay(1, TimeUnit.SECONDS));
        Recorder r = new Recorder();
        SseClient.Stream s = sse.post("/x", new JSONObject(), r);
        s.cancel();
        Thread.sleep(1500);
        assertTrue(r.log.isEmpty());
        assertTrue(s.isDone());
    }
}
