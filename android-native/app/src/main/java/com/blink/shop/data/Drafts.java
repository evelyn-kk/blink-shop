package com.blink.shop.data;

import android.content.Context;
import android.content.SharedPreferences;

import java.util.UUID;

import org.json.JSONException;
import org.json.JSONObject;

import com.blink.shop.net.ApiException;

/**
 * 本地草稿（应用私有存储，App 被杀后仍在）：
 * <ul>
 * <li>结算幂等键：第一次提交前生成并保存，网络失败、超时或 App 被杀后重新提交都用同一个键——
 * 如果上次其实已经下单成功，服务端直接返回那次的订单，不会重复下单；下单成功或被服务端明确拒绝后清除。</li>
 * <li>评价草稿：按订单项保存评分、内容和标签，提交成功后清除。</li>
 * </ul>
 * 按账户隔离，换号后看不到别人的草稿。
 */
public final class Drafts {

    private static final String FILE = "blink_drafts";

    private final SharedPreferences prefs;

    public Drafts(Context context) {
        prefs = context.getSharedPreferences(FILE, Context.MODE_PRIVATE);
    }

    // ---------- 结算 ----------

    /** 当前未完成的结算幂等键；没有则生成并保存。 */
    public String checkoutKey(String accountId) {
        String k = prefs.getString(checkoutKeyName(accountId), null);
        if (k == null || k.isEmpty()) {
            k = newCheckoutKey();
            prefs.edit().putString(checkoutKeyName(accountId), k).commit();
        }
        return k;
    }

    /** 是否有一次提交过、结果未知的结算（用于提示“上次提交未完成”）。 */
    public boolean hasPendingCheckout(String accountId) {
        return prefs.getBoolean(checkoutKeyName(accountId) + ".sent", false);
    }

    /** 请求发出前调用：之后若没有收到明确结果，下次提交仍用这个键。 */
    public void markCheckoutSent(String accountId) {
        prefs.edit().putBoolean(checkoutKeyName(accountId) + ".sent", true).commit();
    }

    /** 下单成功或被服务端明确拒绝：丢弃这个键，下次提交是新的一单。 */
    public void clearCheckout(String accountId) {
        prefs.edit().remove(checkoutKeyName(accountId)).remove(checkoutKeyName(accountId) + ".sent").commit();
    }

    /**
     * 提交失败后是否保留幂等键：网络失败、超时、取消、响应无法识别、5xx、429 时结果未知或可重试，保留；
     * 服务端明确拒绝（其他 4xx，如价格变化、购物车为空、券不可用）时没有下单，丢弃。
     */
    public static boolean keepKeyAfter(ApiException e) {
        if (e.kind() != ApiException.Kind.HTTP) {
            return true;
        }
        return e.status() >= 500 || e.status() == 429;
    }

    /** 1–128 位字母数字和 . _ : -（服务端规则）。 */
    static String newCheckoutKey() {
        return "and-" + UUID.randomUUID().toString();
    }

    private static String checkoutKeyName(String accountId) {
        return "checkout_key." + accountId;
    }

    // ---------- 评价 ----------

    /** 评价草稿。 */
    public static final class Review {
        public final int rating;
        public final String content;
        public final String tags;

        public Review(int rating, String content, String tags) {
            this.rating = rating;
            this.content = content;
            this.tags = tags;
        }
    }

    public Review review(String accountId, String orderItemId) {
        String raw = prefs.getString(reviewName(accountId, orderItemId), null);
        if (raw == null) {
            return null;
        }
        try {
            JSONObject o = new JSONObject(raw);
            return new Review(o.optInt("rating", 5), o.optString("content", ""), o.optString("tags", ""));
        } catch (JSONException e) {
            return null;
        }
    }

    public void saveReview(String accountId, String orderItemId, Review r) {
        JSONObject o = new JSONObject();
        try {
            o.put("rating", r.rating).put("content", r.content).put("tags", r.tags);
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
        prefs.edit().putString(reviewName(accountId, orderItemId), o.toString()).apply();
    }

    public void clearReview(String accountId, String orderItemId) {
        prefs.edit().remove(reviewName(accountId, orderItemId)).apply();
    }

    private static String reviewName(String accountId, String orderItemId) {
        return "review." + accountId + "." + orderItemId;
    }
}
