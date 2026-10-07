package com.blink.shop.net;

import java.io.IOException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

import org.json.JSONObject;

import okhttp3.Call;
import okhttp3.Callback;
import okhttp3.MediaType;
import okhttp3.OkHttpClient;
import okhttp3.Request;
import okhttp3.RequestBody;
import okhttp3.Response;
import okhttp3.ResponseBody;
import okio.BufferedSource;

/**
 * SSE 基础客户端：POST JSON 后按事件流读取。回调在 OkHttp 的后台线程上，界面层自己切回主线程。
 * 每个流恰好以 onClosed（服务端正常结束）或 onError 之一结束；cancel() 之后不再回调。
 */
public final class SseClient {

    public interface Listener {
        void onEvent(SseParser.Event event);

        void onClosed();

        void onError(ApiException error);
    }

    /** 正在进行的流。 */
    public static final class Stream {
        private final Call call;
        private final AtomicBoolean done = new AtomicBoolean();

        Stream(Call call) {
            this.call = call;
        }

        public void cancel() {
            done.set(true);
            call.cancel();
        }

        public boolean isDone() {
            return done.get();
        }
    }

    /** 两次数据之间最长等待（服务端应在此之内发心跳）。 */
    static final long READ_TIMEOUT_SECONDS = 120;

    private final ApiClient api;
    private final OkHttpClient http;

    public SseClient(ApiClient api) {
        this.api = api;
        this.http = api.http().newBuilder().readTimeout(READ_TIMEOUT_SECONDS, TimeUnit.SECONDS).build();
    }

    public Stream post(String path, JSONObject body, Listener listener) {
        Request request = api.request(path)
                .header("Accept", "text/event-stream")
                .post(RequestBody.create(body.toString(), ApiClient.JSON))
                .build();
        Call call = http.newCall(request);
        Stream stream = new Stream(call);
        try {
            api.ensureOnline();
        } catch (ApiException e) {
            stream.done.set(true);
            listener.onError(e);
            return stream;
        }
        call.enqueue(new Callback() {
            @Override
            public void onFailure(Call c, IOException e) {
                finishWithError(stream, listener, api.networkFailure(e, c.isCanceled()));
            }

            @Override
            public void onResponse(Call c, Response response) {
                try (Response r = response) {
                    ResponseBody body = r.body();
                    if (!r.isSuccessful()) {
                        String text = body == null ? "" : body.string();
                        finishWithError(stream, listener, ApiException.fromHttp(r.code(), text, r.header("X-Request-ID")));
                        return;
                    }
                    // 2xx 但不是事件流（如代理返回的 JSON/HTML）是协议错误，不能当成正常结束
                    if (body == null || !isEventStream(body.contentType())) {
                        finishWithError(stream, listener, ApiException.badResponse(null));
                        return;
                    }
                    SseParser parser = new SseParser(event -> {
                        if (!stream.isDone()) {
                            listener.onEvent(event);
                        }
                    });
                    BufferedSource source = body.source();
                    String line;
                    while (!stream.isDone() && (line = source.readUtf8Line()) != null) {
                        parser.line(line);
                    }
                    parser.finish();
                    if (stream.done.compareAndSet(false, true)) {
                        listener.onClosed();
                    }
                } catch (IOException e) {
                    finishWithError(stream, listener, api.networkFailure(e, c.isCanceled()));
                }
            }
        });
        return stream;
    }

    /** 媒体类型是 text/event-stream（忽略大小写和 charset 等参数）。 */
    static boolean isEventStream(MediaType type) {
        return type != null && "text".equalsIgnoreCase(type.type()) && "event-stream".equalsIgnoreCase(type.subtype());
    }

    private static void finishWithError(Stream stream, Listener listener, ApiException error) {
        if (stream.done.compareAndSet(false, true)) {
            listener.onError(error);
        }
    }
}
