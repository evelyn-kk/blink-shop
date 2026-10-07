package com.blink.shop.net;

import java.io.IOException;
import java.util.concurrent.TimeUnit;

import org.json.JSONException;
import org.json.JSONObject;

import okhttp3.Call;
import okhttp3.MediaType;
import okhttp3.MultipartBody;
import okhttp3.OkHttpClient;
import okhttp3.Request;
import okhttp3.RequestBody;
import okhttp3.Response;
import okhttp3.ResponseBody;

/**
 * 同步的 JSON 接口客户端，在后台线程调用（界面用 ui.Async 包装）。
 * 2xx 返回 JSON 对象（空响应体返回空对象）；其余情况抛出 ApiException，消息可以直接展示。
 */
public final class ApiClient {

    /** 设备是否联网。返回 false 时请求不发出，直接按“无网络”失败。 */
    public interface Connectivity {
        boolean isOnline();
    }

    public static final MediaType JSON = MediaType.get("application/json; charset=utf-8");

    private final OkHttpClient http;
    private final ApiConfig config;
    private final Connectivity connectivity;

    public ApiClient(OkHttpClient base, ApiConfig config, AuthInterceptor.SessionSource session, Connectivity connectivity) {
        this.http = base.newBuilder().addInterceptor(new AuthInterceptor(session)).build();
        this.config = config;
        this.connectivity = connectivity;
    }

    /** 默认超时：连接 10 秒，读写 20 秒。 */
    public static OkHttpClient.Builder defaultHttp() {
        return new OkHttpClient.Builder()
                .connectTimeout(10, TimeUnit.SECONDS)
                .readTimeout(20, TimeUnit.SECONDS)
                .writeTimeout(20, TimeUnit.SECONDS);
    }

    public ApiConfig config() {
        return config;
    }

    /** 带会话拦截器的 OkHttp 客户端（SSE 复用）。 */
    public OkHttpClient http() {
        return http;
    }

    public JSONObject get(String path) throws ApiException {
        return execute(request(path).get().build());
    }

    public JSONObject post(String path, JSONObject body) throws ApiException {
        return execute(request(path).post(jsonBody(body)).build());
    }

    public JSONObject patch(String path, JSONObject body) throws ApiException {
        return execute(request(path).patch(jsonBody(body)).build());
    }

    public JSONObject delete(String path) throws ApiException {
        return execute(request(path).delete().build());
    }

    /** multipart/form-data 上传单个文件。 */
    public JSONObject upload(String path, String field, String filename, String mimeType, byte[] data) throws ApiException {
        RequestBody file = RequestBody.create(data, MediaType.get(mimeType));
        MultipartBody body = new MultipartBody.Builder().setType(MultipartBody.FORM)
                .addFormDataPart(field, filename, file).build();
        return execute(request(path).post(body).build());
    }

    /** path 相对于接口地址，如 /products?page=1。 */
    public Request.Builder request(String path) {
        return new Request.Builder().url(config.url(path)).header("Accept", "application/json");
    }

    public JSONObject execute(Request request) throws ApiException {
        ensureOnline();
        Call call = http.newCall(request);
        try (Response response = call.execute()) {
            ResponseBody body = response.body();
            String text = body == null ? "" : body.string();
            if (!response.isSuccessful()) {
                throw ApiException.fromHttp(response.code(), text, response.header("X-Request-ID"));
            }
            if (text.trim().isEmpty()) {
                return new JSONObject();
            }
            try {
                return new JSONObject(text);
            } catch (JSONException e) {
                throw ApiException.badResponse(e);
            }
        } catch (IOException e) {
            throw networkFailure(e, call.isCanceled());
        }
    }

    void ensureOnline() throws ApiException {
        if (connectivity != null && !connectivity.isOnline()) {
            throw ApiException.offline();
        }
    }

    /** 请求过程中断网的，报“无网络”而不是“连不上服务器”。 */
    ApiException networkFailure(IOException e, boolean canceled) {
        if (!canceled && connectivity != null && !connectivity.isOnline()) {
            return ApiException.offline();
        }
        return ApiException.fromIOException(e, canceled);
    }

    private static RequestBody jsonBody(JSONObject body) {
        return RequestBody.create((body == null ? new JSONObject() : body).toString(), JSON);
    }
}
