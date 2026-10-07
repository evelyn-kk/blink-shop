package com.blink.shop.net;

import java.util.ArrayList;
import java.util.List;

/** 测试用会话：记录收到的 401。 */
public final class FakeSession implements AuthInterceptor.SessionSource {

    public volatile String token;
    public final List<String> rejected = new ArrayList<>();

    public FakeSession(String token) {
        this.token = token;
    }

    @Override
    public String token() {
        return token;
    }

    @Override
    public synchronized void onUnauthorized(String rejectedToken) {
        rejected.add(rejectedToken);
    }
}
