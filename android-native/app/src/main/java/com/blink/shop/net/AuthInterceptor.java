package com.blink.shop.net;

import java.io.IOException;

import okhttp3.Interceptor;
import okhttp3.Request;
import okhttp3.Response;

/**
 * 给请求带上当前会话的 Bearer token；带 token 的请求返回 401 时通知会话失效。
 * 没带 token 的请求（如登录密码错误的 401）不算会话失效。
 */
public final class AuthInterceptor implements Interceptor {

    /** 当前会话。实现要线程安全：拦截器在网络线程上调用。 */
    public interface SessionSource {
        /** 当前 token；未登录返回 null 或空串。 */
        String token();

        /** 服务端拒绝了 rejectedToken。只有它仍是当前 token 时才应清除会话（避免清掉刚登录的新会话）。 */
        void onUnauthorized(String rejectedToken);
    }

    private final SessionSource session;

    public AuthInterceptor(SessionSource session) {
        this.session = session;
    }

    @Override
    public Response intercept(Chain chain) throws IOException {
        Request request = chain.request();
        String token = null;
        if (request.header("Authorization") == null) {
            token = session.token();
            if (token != null && !token.isEmpty()) {
                request = request.newBuilder().header("Authorization", "Bearer " + token).build();
            } else {
                token = null;
            }
        }
        Response response = chain.proceed(request);
        if (response.code() == 401 && token != null) {
            session.onUnauthorized(token);
        }
        return response;
    }
}
