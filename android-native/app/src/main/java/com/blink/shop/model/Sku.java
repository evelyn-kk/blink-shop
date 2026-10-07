package com.blink.shop.model;

import java.util.Collections;
import java.util.Iterator;
import java.util.TreeMap;
import java.util.Map;

import org.json.JSONObject;

/** 商品规格。 */
public final class Sku {

    public final String skuId;
    public final String productId;
    public final String skuName;
    public final String price;
    public final int stockQuantity;
    public final String stockStatus;
    public final Map<String, String> specs;
    public final boolean isDefault;

    private Sku(JSONObject o) {
        skuId = Json.str(o, "sku_id");
        productId = Json.str(o, "product_id");
        skuName = Json.str(o, "sku_name");
        price = Json.str(o, "price");
        stockQuantity = o.optInt("stock_quantity", 0);
        stockStatus = Json.str(o, "stock_status");
        // JSON 对象的键没有顺序，按键名排序（与服务端输出顺序一致）
        Map<String, String> m = new TreeMap<>();
        JSONObject s = o.optJSONObject("specs");
        if (s != null) {
            Iterator<String> keys = s.keys();
            while (keys.hasNext()) {
                String k = keys.next();
                m.put(k, s.optString(k, ""));
            }
        }
        specs = Collections.unmodifiableMap(m);
        isDefault = o.optBoolean("is_default", false);
    }

    public static Sku fromJson(JSONObject o) {
        return new Sku(o == null ? new JSONObject() : o);
    }

    /** 规格按钮上的文字：有规格值时用“128GB / 曜石黑”，否则用规格名。 */
    public String label() {
        if (specs.isEmpty()) {
            return skuName;
        }
        StringBuilder b = new StringBuilder();
        for (String v : specs.values()) {
            if (v.isEmpty()) {
                continue;
            }
            if (b.length() > 0) {
                b.append(" / ");
            }
            b.append(v);
        }
        return b.length() == 0 ? skuName : b.toString();
    }
}
