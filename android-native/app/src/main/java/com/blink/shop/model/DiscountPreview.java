package com.blink.shop.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

/** 优惠试算（GET /cart/discount-preview），确认下单页用。 */
public final class DiscountPreview {

    /** 一条优惠（活动或券）。 */
    public static final class Line {
        public final String type;
        public final String id;
        public final String name;
        public final String scope;
        public final String amount;
        public final String description;

        Line(JSONObject o) {
            type = Json.str(o, "type");
            id = Json.str(o, "id");
            name = Json.str(o, "name");
            scope = Json.str(o, "scope");
            amount = Json.str(o, "amount");
            description = Json.str(o, "description");
        }

        public boolean isCoupon() {
            return "coupon".equals(type);
        }
    }

    /** 按店铺小计（下单时按店铺拆成多个订单）。 */
    public static final class MerchantTotal {
        public final String merchantId;
        public final String merchantName;
        public final String totalAmount;
        public final String discountAmount;
        public final String payAmount;

        MerchantTotal(JSONObject o) {
            merchantId = Json.str(o, "merchant_id");
            merchantName = Json.str(o, "merchant_name");
            totalAmount = Json.str(o, "total_amount");
            discountAmount = Json.str(o, "discount_amount");
            payAmount = Json.str(o, "pay_amount");
        }
    }

    /** 满减还差多少（凑单提示）。 */
    public static final class Hint {
        public final String promotionId;
        public final String name;
        public final String shortfall;

        Hint(JSONObject o) {
            promotionId = Json.str(o, "promotion_id");
            name = Json.str(o, "name");
            shortfall = Json.str(o, "shortfall");
        }
    }

    public final String totalAmount;
    public final String discountAmount;
    public final String payAmount;
    public final List<Line> lines;
    public final List<MerchantTotal> merchants;
    public final List<Hint> hints;
    public final List<String> userCouponIds;

    private DiscountPreview(JSONObject o) {
        totalAmount = Json.str(o, "total_amount");
        discountAmount = Json.str(o, "discount_amount");
        payAmount = Json.str(o, "pay_amount");
        List<Line> l = new ArrayList<>();
        List<MerchantTotal> m = new ArrayList<>();
        List<Hint> h = new ArrayList<>();
        JSONArray arr = o.optJSONArray("lines");
        for (int i = 0; arr != null && i < arr.length(); i++) {
            l.add(new Line(arr.optJSONObject(i) == null ? new JSONObject() : arr.optJSONObject(i)));
        }
        arr = o.optJSONArray("merchants");
        for (int i = 0; arr != null && i < arr.length(); i++) {
            m.add(new MerchantTotal(arr.optJSONObject(i) == null ? new JSONObject() : arr.optJSONObject(i)));
        }
        arr = o.optJSONArray("hints");
        for (int i = 0; arr != null && i < arr.length(); i++) {
            h.add(new Hint(arr.optJSONObject(i) == null ? new JSONObject() : arr.optJSONObject(i)));
        }
        lines = Collections.unmodifiableList(l);
        merchants = Collections.unmodifiableList(m);
        hints = Collections.unmodifiableList(h);
        userCouponIds = Json.strings(o, "user_coupon_ids");
    }

    public static DiscountPreview fromJson(JSONObject o) {
        return new DiscountPreview(o == null ? new JSONObject() : o);
    }
}
