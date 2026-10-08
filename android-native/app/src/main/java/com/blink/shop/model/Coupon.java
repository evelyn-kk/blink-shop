package com.blink.shop.model;

import org.json.JSONObject;

/** 优惠券模板（可领取列表），以及我领到的券（UserCoupon）。 */
public final class Coupon {

    public final String couponId;
    public final String name;
    public final String scope;
    public final String merchantId;
    public final String description;
    public final String thresholdAmount;
    public final String discountAmount;
    public final String startAt;
    public final String endAt;
    public final int totalCount;
    public final int claimedCount;
    public final int perUserLimit;
    public final int claimedByMe;
    public final boolean canClaim;

    private Coupon(JSONObject o) {
        couponId = Json.str(o, "coupon_id");
        name = Json.str(o, "name");
        scope = Json.str(o, "scope");
        merchantId = Json.str(o, "merchant_id");
        description = Json.str(o, "description");
        thresholdAmount = Json.str(o, "threshold_amount");
        discountAmount = Json.str(o, "discount_amount");
        startAt = Json.str(o, "start_at");
        endAt = Json.str(o, "end_at");
        totalCount = o.optInt("total_count", 0);
        claimedCount = o.optInt("claimed_count", 0);
        perUserLimit = o.optInt("per_user_limit", 0);
        claimedByMe = o.optInt("claimed_by_me", 0);
        canClaim = o.optBoolean("can_claim", false);
    }

    public static Coupon fromJson(JSONObject o) {
        return new Coupon(o == null ? new JSONObject() : o);
    }

    public String scopeLabel() {
        return "platform".equals(scope) ? "平台券" : "店铺券";
    }

    /** 已领完（区别于“已达到个人上限”）。 */
    public boolean soldOut() {
        return totalCount > 0 && claimedCount >= totalCount;
    }

    /** 我领到的券。 */
    public static final class Mine {
        public static final String UNUSED = "unused";
        public static final String USED = "used";
        public static final String EXPIRED = "expired";

        public final String userCouponId;
        public final String status;
        public final String orderId;
        public final String claimedAt;
        public final String usedAt;
        public final Coupon coupon;

        private Mine(JSONObject o) {
            userCouponId = Json.str(o, "user_coupon_id");
            status = Json.str(o, "status");
            orderId = Json.str(o, "order_id");
            claimedAt = Json.str(o, "claimed_at");
            usedAt = Json.str(o, "used_at");
            coupon = Coupon.fromJson(o.optJSONObject("coupon"));
        }

        public static Mine fromJson(JSONObject o) {
            return new Mine(o == null ? new JSONObject() : o);
        }

        public String statusLabel() {
            switch (status) {
                case UNUSED:
                    return "未使用";
                case USED:
                    return "已使用";
                case EXPIRED:
                    return "已过期";
                default:
                    return status;
            }
        }
    }
}
