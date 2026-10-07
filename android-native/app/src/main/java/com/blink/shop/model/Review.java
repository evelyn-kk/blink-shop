package com.blink.shop.model;

import java.util.List;

import org.json.JSONObject;

/** 公开商品评价。 */
public final class Review {

    public final String reviewId;
    public final String reviewerName;
    public final int rating;
    public final String content;
    public final List<String> tags;
    public final String merchantReply;
    public final String createdAt;

    private Review(JSONObject o) {
        reviewId = Json.str(o, "review_id");
        reviewerName = Json.str(o, "reviewer_name");
        rating = Math.max(0, Math.min(5, o.optInt("rating", 0)));
        content = Json.str(o, "content");
        tags = Json.strings(o, "tags");
        merchantReply = Json.str(o, "merchant_reply");
        createdAt = Json.str(o, "created_at");
    }

    public static Review fromJson(JSONObject o) {
        return new Review(o == null ? new JSONObject() : o);
    }

    /** 星级文字，如 ★★★★☆；同时配“4 分”文字给读屏。 */
    public String stars() {
        StringBuilder b = new StringBuilder(5);
        for (int i = 1; i <= 5; i++) {
            b.append(i <= rating ? '★' : '☆');
        }
        return b.toString();
    }
}
