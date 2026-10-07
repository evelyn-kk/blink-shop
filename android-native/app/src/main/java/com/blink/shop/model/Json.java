package com.blink.shop.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

/** 读取服务端 JSON 的小工具：缺省字段返回空串/空列表，不抛异常。 */
final class Json {

    private Json() {
    }

    static String str(JSONObject o, String key) {
        if (o == null || o.isNull(key)) {
            return "";
        }
        return o.optString(key, "");
    }

    static List<String> strings(JSONObject o, String key) {
        JSONArray arr = o == null ? null : o.optJSONArray(key);
        if (arr == null || arr.length() == 0) {
            return Collections.emptyList();
        }
        List<String> out = new ArrayList<>(arr.length());
        for (int i = 0; i < arr.length(); i++) {
            String s = arr.optString(i, "");
            if (!s.isEmpty()) {
                out.add(s);
            }
        }
        return Collections.unmodifiableList(out);
    }
}
