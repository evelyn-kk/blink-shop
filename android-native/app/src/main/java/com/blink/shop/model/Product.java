package com.blink.shop.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

/** 商品：列表卡片字段都有；详情字段（imageUrls 等）只在详情接口返回时有值。 */
public final class Product {

    /** 商品参数，如 续航 30 小时。 */
    public static final class Attribute {
        public final String key;
        public final String value;
        public final String unit;

        Attribute(String key, String value, String unit) {
            this.key = key;
            this.value = value;
            this.unit = unit;
        }

        public String display() {
            return unit.isEmpty() ? value : value + " " + unit;
        }
    }

    public final String productId;
    public final String skuId;
    public final String merchantId;
    public final String merchantName;
    public final String categoryId;
    public final String name;
    public final String brand;
    public final String imageUrl;
    public final String price;
    public final String marketPrice;
    public final String stockStatus;
    public final List<String> tags;
    public final List<String> sellingPoints;
    public final String recommendReason;
    public final List<String> riskNotes;

    public final List<String> imageUrls;
    /** 详情接口才有；列表卡片为 -1。 */
    public final int stockQuantity;
    public final List<Attribute> attributes;
    public final List<String> suitableFor;
    public final List<String> notSuitableFor;
    public final String description;

    private Product(JSONObject o) {
        productId = Json.str(o, "product_id");
        skuId = Json.str(o, "sku_id");
        merchantId = Json.str(o, "merchant_id");
        merchantName = Json.str(o, "merchant_name");
        categoryId = Json.str(o, "category_id");
        name = Json.str(o, "name");
        brand = Json.str(o, "brand");
        imageUrl = Json.str(o, "image_url");
        price = Json.str(o, "price");
        marketPrice = Json.str(o, "market_price");
        stockStatus = Json.str(o, "stock_status");
        tags = Json.strings(o, "tags");
        sellingPoints = Json.strings(o, "selling_points");
        recommendReason = Json.str(o, "recommend_reason");
        riskNotes = Json.strings(o, "risk_notes");
        imageUrls = Json.strings(o, "image_urls");
        stockQuantity = o.has("stock_quantity") ? o.optInt("stock_quantity", -1) : -1;
        List<Attribute> attrs = new ArrayList<>();
        JSONArray arr = o.optJSONArray("attributes");
        if (arr != null) {
            for (int i = 0; i < arr.length(); i++) {
                JSONObject a = arr.optJSONObject(i);
                if (a != null && !Json.str(a, "key").isEmpty()) {
                    attrs.add(new Attribute(Json.str(a, "key"), Json.str(a, "value"), Json.str(a, "unit")));
                }
            }
        }
        attributes = Collections.unmodifiableList(attrs);
        suitableFor = Json.strings(o, "suitable_for");
        notSuitableFor = Json.strings(o, "not_suitable_for");
        description = Json.str(o, "description");
    }

    public static Product fromJson(JSONObject o) {
        return new Product(o == null ? new JSONObject() : o);
    }

    /** 详情页图片：有图集用图集，否则用主图。 */
    public List<String> gallery() {
        if (!imageUrls.isEmpty()) {
            return imageUrls;
        }
        return imageUrl.isEmpty() ? Collections.emptyList() : Collections.singletonList(imageUrl);
    }

    /** 品牌 · 店铺。 */
    public String byline() {
        if (brand.isEmpty()) {
            return merchantName;
        }
        return merchantName.isEmpty() ? brand : brand + " · " + merchantName;
    }

    /** 库存状态文字（不只靠颜色表达）。 */
    public static String stockLabel(String status) {
        switch (status) {
            case "in_stock":
                return "有货";
            case "low_stock":
                return "库存紧张";
            case "out_of_stock":
                return "暂时缺货";
            default:
                return "";
        }
    }
}
