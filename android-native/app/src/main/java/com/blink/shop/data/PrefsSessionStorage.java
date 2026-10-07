package com.blink.shop.data;

import android.content.Context;
import android.content.SharedPreferences;

import com.blink.shop.model.Account;
import com.blink.shop.model.Session;

/** 用 SharedPreferences 保存会话（应用私有存储，已关闭备份）。 */
public final class PrefsSessionStorage implements SessionManager.Storage {

    private static final String FILE = "blink_session";
    private static final String KEY_TOKEN = "token";
    private static final String KEY_EXPIRES_AT = "expires_at_ms";
    private static final String KEY_ACCOUNT = "account_json";

    private final SharedPreferences prefs;

    public PrefsSessionStorage(Context context) {
        prefs = context.getSharedPreferences(FILE, Context.MODE_PRIVATE);
    }

    @Override
    public Session load() {
        String token = prefs.getString(KEY_TOKEN, "");
        if (token == null || token.isEmpty()) {
            return null;
        }
        Account account = Account.fromJsonString(prefs.getString(KEY_ACCOUNT, "{}"));
        return new Session(token, prefs.getLong(KEY_EXPIRES_AT, 0), account == null ? Account.fromJson(null) : account);
    }

    @Override
    public void save(Session s) {
        // commit：进程随后被杀也不会丢会话
        prefs.edit().putString(KEY_TOKEN, s.token).putLong(KEY_EXPIRES_AT, s.expiresAtMillis)
                .putString(KEY_ACCOUNT, s.account.toJsonString()).commit();
    }

    @Override
    public void clear() {
        prefs.edit().clear().commit();
    }
}
