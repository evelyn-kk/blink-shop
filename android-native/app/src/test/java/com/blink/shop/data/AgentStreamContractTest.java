package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import java.io.IOException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;
import com.blink.shop.net.SseClient;
import com.blink.shop.net.SseParser;

import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;

/** 用服务端录制的流式样例检查 Android 导购流式请求和事件解析。 */
public class AgentStreamContractTest {

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

    private static final class Recorder implements SseClient.Listener {
        final List<SseParser.Event> events = Collections.synchronizedList(new ArrayList<>());
        final CountDownLatch done = new CountDownLatch(1);
        volatile ApiException error;

        @Override
        public void onEvent(SseParser.Event e) {
            events.add(e);
        }

        @Override
        public void onClosed() {
            done.countDown();
        }

        @Override
        public void onError(ApiException e) {
            error = e;
            done.countDown();
        }
    }

    @Test
    public void streamsRecordedFixture() throws Exception {
        Fixture f = Fixture.load("agent_message_stream_ok");
        JSONArray want = f.response.getJSONArray("events");
        StringBuilder wire = new StringBuilder(": ping\n\n");
        for (int i = 0; i < want.length(); i++) {
            JSONObject e = want.getJSONObject(i);
            wire.append("event: ").append(e.getString("event")).append("\ndata: ").append(e.getJSONObject("data")).append("\n\n");
        }
        server.enqueue(new MockResponse().setHeader("Content-Type", "text/event-stream; charset=utf-8").setBody(wire.toString()));
        Recorder r = new Recorder();
        JSONObject body = f.request.getJSONObject("body");
        api.streamAgentMessage("s_1", body.getString("client_message_id"), body.getString("content"), r);
        assertTrue(r.done.await(5, TimeUnit.SECONDS));
        assertEquals(null, r.error);
        assertEquals(want.length(), r.events.size());
        for (int i = 0; i < want.length(); i++) {
            assertEquals(want.getJSONObject(i).getString("event"), r.events.get(i).event);
            JSONObject got = new JSONObject(r.events.get(i).data);
            assertEquals(want.getJSONObject(i).getJSONObject("data").toString().length(), got.toString().length());
        }
        assertEquals("message_start", r.events.get(0).event);
        assertEquals("message_done", r.events.get(r.events.size() - 1).event);
        StringBuilder text = new StringBuilder();
        boolean productList = false;
        for (SseParser.Event e : r.events) {
            if ("text_delta".equals(e.event)) {
                text.append(new JSONObject(e.data).getString("delta"));
            } else if ("block".equals(e.event)) {
                productList |= "product_list".equals(new JSONObject(e.data).getJSONObject("block").getString("type"));
            }
        }
        // 规则运行器的回答只用工具返回的商品：正文提到推荐的商品，并带 product_list 块
        assertTrue(text.toString().contains("Blink Air 降噪耳机"));
        assertTrue(productList);

        RecordedRequest req = server.takeRequest();
        assertEquals("POST", req.getMethod());
        assertEquals("/api/v1/agent/sessions/s_1/messages:stream", req.getPath());
        assertEquals("text/event-stream", req.getHeader("Accept"));
        assertEquals("Bearer tok", req.getHeader("Authorization"));
        JSONObject sent = new JSONObject(req.getBody().readUtf8());
        assertEquals("fixture-1", sent.getString("client_message_id"));
        assertEquals("推荐一款通勤降噪耳机", sent.getString("content"));
    }

    @Test
    public void validationErrorBeforeStream() throws Exception {
        Fixture f = Fixture.load("agent_message_stream_invalid");
        server.enqueue(f.mockResponse());
        Recorder r = new Recorder();
        api.streamAgentMessage("s_1", "", "你好", r);
        assertTrue(r.done.await(5, TimeUnit.SECONDS));
        assertEquals("client_message_id", r.error.field());
        assertTrue(r.events.isEmpty());
    }

    @Test
    public void createSession() throws Exception {
        Fixture f = Fixture.load("agent_session_create_ok");
        server.enqueue(f.mockResponse());
        String id = api.createChatSession();
        f.assertRequest(server.takeRequest());
        assertTrue(id.startsWith("<session_id>") || id.startsWith("s_"));
    }
}
