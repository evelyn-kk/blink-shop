package com.blink.shop.model;

import org.json.JSONObject;

/** 登录/注册返回的会话。 */
public final class Session {

    public final String token;
    /** 过期时间（毫秒时间戳）；解析失败为 0，视为未知、交给服务端判断。 */
    public final long expiresAtMillis;
    public final Account account;

    public Session(String token, long expiresAtMillis, Account account) {
        this.token = token;
        this.expiresAtMillis = expiresAtMillis;
        this.account = account;
    }

    public static Session fromJson(JSONObject o) {
        return new Session(Json.str(o, "token"), Times.parseIsoMillis(Json.str(o, "expires_at")),
                Account.fromJson(o.optJSONObject("account")));
    }

    public boolean isExpired(long nowMillis) {
        return expiresAtMillis > 0 && nowMillis >= expiresAtMillis;
    }

    public Session withAccount(Account a) {
        return new Session(token, expiresAtMillis, a);
    }
}
