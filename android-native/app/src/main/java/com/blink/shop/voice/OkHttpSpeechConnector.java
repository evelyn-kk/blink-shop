package com.blink.shop.voice;

import com.blink.shop.data.ShopApi;
import com.blink.shop.net.ApiException;

import okhttp3.Response;
import okhttp3.WebSocket;
import okhttp3.WebSocketListener;
import okio.ByteString;

/** 用 OkHttp 的 WebSocket 连接服务端实时识别（token 由会话拦截器带上）。 */
public final class OkHttpSpeechConnector implements VoiceInputController.Connector {

    private final ShopApi api;

    public OkHttpSpeechConnector(ShopApi api) {
        this.api = api;
    }

    @Override
    public VoiceInputController.Socket open(VoiceInputController.SocketListener l) {
        WebSocket ws;
        try {
            ws = api.speechSocket(new WebSocketListener() {
                @Override
                public void onMessage(WebSocket webSocket, String text) {
                    l.onMessage(text);
                }

                @Override
                public void onClosing(WebSocket webSocket, int code, String reason) {
                    webSocket.close(1000, null);
                }

                @Override
                public void onClosed(WebSocket webSocket, int code, String reason) {
                    l.onClosed();
                }

                @Override
                public void onFailure(WebSocket webSocket, Throwable t, Response response) {
                    l.onFailure(response == null ? 0 : response.code(), t == null ? "" : String.valueOf(t.getMessage()));
                }
            });
        } catch (ApiException e) {
            l.onFailure(0, e.getMessage());
            return new VoiceInputController.Socket() {
                @Override
                public boolean sendBinary(byte[] data) {
                    return false;
                }

                @Override
                public boolean sendText(String text) {
                    return false;
                }

                @Override
                public void cancel() {
                }
            };
        }
        return new VoiceInputController.Socket() {
            @Override
            public boolean sendBinary(byte[] data) {
                return ws.send(ByteString.of(data));
            }

            @Override
            public boolean sendText(String text) {
                return ws.send(text);
            }

            @Override
            public void cancel() {
                ws.cancel();
            }
        };
    }
}
