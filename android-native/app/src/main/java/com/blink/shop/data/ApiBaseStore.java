package com.blink.shop.data;

import android.content.Context;
import android.content.SharedPreferences;

import com.blink.shop.BuildConfig;

/** 接口地址：Debug 可以在高级设置里覆盖；Release 永远使用构建时注入的地址。 */
public final class ApiBaseStore {

    private static final String FILE = "blink_settings";
    private static final String KEY_API_BASE = "api_base";

    private final SharedPreferences prefs;

    public ApiBaseStore(Context context) {
        prefs = context.getSharedPreferences(FILE, Context.MODE_PRIVATE);
    }

    public static boolean editable() {
        return BuildConfig.DEBUG;
    }

    public String get() {
        if (!editable()) {
            return BuildConfig.DEFAULT_API_BASE;
        }
        String v = prefs.getString(KEY_API_BASE, "");
        return v == null || v.isEmpty() ? BuildConfig.DEFAULT_API_BASE : v;
    }

    public boolean isCustom() {
        return editable() && !get().equals(BuildConfig.DEFAULT_API_BASE);
    }

    /** null 表示恢复默认。 */
    public void set(String base) {
        if (!editable()) {
            throw new IllegalStateException("release build cannot change api base");
        }
        if (base == null) {
            prefs.edit().remove(KEY_API_BASE).commit();
        } else {
            prefs.edit().putString(KEY_API_BASE, base).commit();
        }
    }
}
