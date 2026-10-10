package com.blink.shop.chat;

import java.util.List;

import org.json.JSONObject;

import com.blink.shop.data.ShopApi;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.SseClient;

/** 控制器用到的接口，便于测试替换。默认实现直接转给 ShopApi。 */
public interface ChatBackend {

    String createSession() throws ApiException;

    JSONObject sessionDetail(String sessionId) throws ApiException;

    /** attachments 是本轮附带的 file_id（可以为空列表）。 */
    SseClient.Stream stream(String sessionId, String clientMessageId, String content, List<String> attachments, SseClient.Listener listener);

    /** 上传聊天附图，返回 file_id。 */
    String uploadImage(byte[] jpeg) throws ApiException;

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
            public SseClient.Stream stream(String sessionId, String clientMessageId, String content, List<String> attachments,
                    SseClient.Listener listener) {
                return api.streamAgentMessage(sessionId, clientMessageId, content, attachments, listener);
            }

            @Override
            public String uploadImage(byte[] jpeg) throws ApiException {
                return api.uploadFile(jpeg, "image/jpeg", "photo.jpg");
            }

            @Override
            public void cancelRun(String runId) throws ApiException {
                api.cancelAgentRun(runId);
            }
        };
    }
}
