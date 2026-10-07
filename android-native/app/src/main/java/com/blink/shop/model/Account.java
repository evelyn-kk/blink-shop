package com.blink.shop.model;

import org.json.JSONException;
import org.json.JSONObject;

/** 账户资料（auth/me、account/profile 的响应，以及会话里的 account）。 */
public final class Account {

    public static final String STATUS_ACTIVE = "active";
    public static final String STATUS_INACTIVE = "inactive";
    public static final String STATUS_RISK = "risk";

    public final String accountId;
    public final String username;
    public final String displayName;
    public final String avatarUrl;
    public final String phone;
    public final String email;
    public final String role;
    public final String status;

    private final JSONObject raw;

    private Account(JSONObject o) {
        raw = o;
        accountId = Json.str(o, "account_id");
        username = Json.str(o, "username");
        displayName = Json.str(o, "display_name");
        avatarUrl = Json.str(o, "avatar_url");
        phone = Json.str(o, "phone");
        email = Json.str(o, "email");
        role = Json.str(o, "role");
        status = Json.str(o, "status");
    }

    public static Account fromJson(JSONObject o) {
        return new Account(o == null ? new JSONObject() : o);
    }

    /** 本地保存用：原样保留服务端字段。 */
    public String toJsonString() {
        return raw.toString();
    }

    public static Account fromJsonString(String s) {
        try {
            return fromJson(new JSONObject(s));
        } catch (JSONException e) {
            return null;
        }
    }

    /** 展示名：昵称为空时用账号。 */
    public String name() {
        return displayName.isEmpty() ? username : displayName;
    }

    /** 状态说明；正常账户返回空串。服务端据状态限制操作，这里只做提示。 */
    public String statusHint() {
        switch (status) {
            case STATUS_INACTIVE:
                return "账户已停用，暂时只能查看和退出登录";
            case STATUS_RISK:
                return "账户处于风控中，暂时只能浏览";
            default:
                return "";
        }
    }
}
