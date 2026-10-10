package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.IOException;
import java.util.Collections;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.model.ChatSession;
import com.blink.shop.model.PageResult;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;

import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;

/** 导购会话接口（列表、详情、置顶、重命名、删除、停止运行）与服务端样例一致。 */
public class ChatApiContractTest {

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

    @Test
    public void listSessions() throws Exception {
        Fixture f = serve("agent_sessions_list_ok");
        PageResult<ChatSession> page = api.chatSessions("降噪", 1);
        f.assertRequest(server.takeRequest());
        assertEquals(1, page.items.size());
        ChatSession s = page.items.get(0);
        assertEquals("耳机咨询", s.title);
        assertEquals("用户咨询：推荐一款通勤降噪耳机", s.summary);
        assertEquals(1, s.messageCount);
        assertFalse(s.pinned);
        assertFalse(s.lastMessageAt.isEmpty());
        assertEquals("耳机咨询", s.displayTitle());
        assertEquals("AI 导购", new ChatSession("s", "", "", 0, "", false).displayTitle());
    }

    @Test
    public void sessionDetailCarriesRunsAndBlocks() throws Exception {
        Fixture f = serve("agent_session_detail_ok");
        JSONObject detail = api.chatSessionDetail("s_1");
        RecordedRequest r = server.takeRequest();
        f.assertRequest(r, Collections.singletonMap("{setup_session_id}", "s_1"));
        JSONObject msg = detail.getJSONArray("messages").getJSONObject(0);
        assertEquals("fixture-1", msg.getString("client_message_id"));
        JSONObject run = msg.getJSONObject("run");
        assertEquals("completed", run.getString("status"));
        assertEquals("product_list", run.getJSONArray("blocks").getJSONObject(0).getString("type"));
        assertTrue(run.getJSONArray("followups").length() > 0);
    }

    @Test
    public void pinRenameDelete() throws Exception {
        Fixture pin = serve("agent_session_pin_ok");
        ChatSession pinned = api.pinChatSession("s_1", pin.request.getJSONObject("body").getBoolean("pinned"));
        pin.assertRequest(server.takeRequest(), Collections.singletonMap("{setup_session_id}", "s_1"));
        assertEquals(pin.body().getBoolean("pinned"), pinned.pinned);

        Fixture bad = serve("agent_session_rename_invalid");
        try {
            api.renameChatSession("s_1", "   ");
            fail("expected 400");
        } catch (ApiException e) {
            assertEquals("title", e.field());
            assertEquals(400, e.status());
        }
        bad.assertRequest(server.takeRequest(), Collections.singletonMap("{setup_session_id}", "s_1"));

        Fixture del = serve("agent_session_delete_ok");
        api.deleteChatSession("s_1");
        del.assertRequest(server.takeRequest(), Collections.singletonMap("{setup_session_id}", "s_1"));
    }

    @Test
    public void cancelFinishedRunIsConflict() throws Exception {
        Fixture f = serve("agent_run_cancel_finished");
        try {
            api.cancelAgentRun("run_1");
            fail("expected 409");
        } catch (ApiException e) {
            assertEquals("run_finished", e.code());
            assertEquals(409, e.status());
        }
        f.assertRequest(server.takeRequest(), Collections.singletonMap("{setup_run_id}", "run_1"));
    }
}
