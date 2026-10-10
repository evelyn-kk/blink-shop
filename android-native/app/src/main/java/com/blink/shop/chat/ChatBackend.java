package com.blink.shop.chat;

import org.json.JSONObject;

import com.blink.shop.data.ShopApi;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.SseClient;

/** 控制器用到的接口，便于测试替换。默认实现直接转给 ShopApi。 */
public interface ChatBackend {

    String createSession() throws ApiException;

    JSONObject sessionDetail(String sessionId) throws ApiException;

    SseClient.Stream stream(String sessionId, String clientMessageId, String content, SseClient.Listener listener);

    void cancelRun(String runId) throws ApiException;

    static ChatBackend of(ShopApi api) {
        return new ChatBackend() {
            @Override
            public String createSession() throws ApiException {
                return api.createChatSession();
            }

            @Override
            public JSONObject sessionDetail(String sessionId) throws ApiException {
                return api.chatSessionDetail(sessionId);
            }

            @Override
            public SseClient.Stream stream(String sessionId, String clientMessageId, String content, SseClient.Listener listener) {
                return api.streamAgentMessage(sessionId, clientMessageId, content, listener);
            }

            @Override
            public void cancelRun(String runId) throws ApiException {
                api.cancelAgentRun(runId);
            }
        };
    }
}
