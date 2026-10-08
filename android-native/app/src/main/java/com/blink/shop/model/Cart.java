package com.blink.shop.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

/** 购物车（GET /cart 以及所有购物车写操作的响应）。金额由服务端计算（自动选最优券）。 */
public final class Cart {

    public static final class Item {
        public final String cartItemId;
        public final String productId;
        public final String skuId;
        public final String productName;
        public final String skuName;
        public final String imageUrl;
        public final String merchantId;
        public final String merchantName;
        public final String unitPrice;
        public final int quantity;
        public final boolean selected;
        public final int stockQuantity;
        public final boolean available;
        public final String unavailableReason;
        public final String amount;
        public final String discountAmount;
        public final String payAmount;

        Item(JSONObject o) {
            cartItemId = Json.str(o, "cart_item_id");
            productId = Json.str(o, "product_id");
            skuId = Json.str(o, "sku_id");
            productName = Json.str(o, "product_name");
            skuName = Json.str(o, "sku_name");
            imageUrl = Json.str(o, "image_url");
            merchantId = Json.str(o, "merchant_id");
            merchantName = Json.str(o, "merchant_name");
            unitPrice = Json.str(o, "unit_price");
            quantity = o.optInt("quantity", 1);
            selected = o.optBoolean("selected", false);
            stockQuantity = o.optInt("stock_quantity", 0);
            available = o.optBoolean("available", false);
            unavailableReason = Json.str(o, "unavailable_reason");
            amount = Json.str(o, "amount");
            discountAmount = Json.str(o, "discount_amount");
            payAmount = Json.str(o, "pay_amount");
        }

        /** 数量上限：服务端规则是 1–99 且不超过库存；这里只用来禁用加号，最终以服务端为准。 */
        public int maxQuantity() {
            return Math.max(1, Math.min(99, stockQuantity));
        }
    }

    public final List<Item> items;
    public final int itemCount;
    public final int selectedCount;
    public final String totalAmount;
    public final String discountAmount;
    public final String payAmount;

    private Cart(JSONObject o) {
        List<Item> list = new ArrayList<>();
        JSONArray arr = o.optJSONArray("items");
        if (arr != null) {
            for (int i = 0; i < arr.length(); i++) {
                JSONObject it = arr.optJSONObject(i);
                if (it != null) {
                    list.add(new Item(it));
                }
            }
        }
        items = Collections.unmodifiableList(list);
        JSONObject s = o.optJSONObject("summary");
        if (s == null) {
            s = new JSONObject();
        }
        itemCount = s.optInt("item_count", list.size());
        selectedCount = s.optInt("selected_count", 0);
        totalAmount = Json.str(s, "total_amount");
        discountAmount = Json.str(s, "discount_amount");
        payAmount = Json.str(s, "pay_amount");
    }

    public static Cart fromJson(JSONObject o) {
        return new Cart(o == null ? new JSONObject() : o);
    }

    /** 已选中且可购买的项数（能结算的）。 */
    public int checkoutCount() {
        int n = 0;
        for (Item it : items) {
            if (it.selected && it.available) {
                n++;
            }
        }
        return n;
    }

    /** 购物车角标：商品件数之和。 */
    public int totalQuantity() {
        int n = 0;
        for (Item it : items) {
            n += it.quantity;
        }
        return n;
    }
}
