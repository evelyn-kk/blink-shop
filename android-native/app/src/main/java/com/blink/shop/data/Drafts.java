package com.blink.shop.data;

import android.content.Context;
import android.content.SharedPreferences;

import org.json.JSONException;
import org.json.JSONObject;


/**
 * 本地草稿（应用私有存储，App 被杀后仍在）：
 * <ul>
 * <li>结果未确认的结算提交（幂等键、用券选择、确认金额一起冻结，见 PendingCheckout）。</li>
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

    /** 某个账户结果未确认的结算提交（见 PendingCheckout）。 */
    public PendingCheckout.Store checkout(String accountId) {
        String name = "checkout_pending." + accountId;
        return new PendingCheckout.Store() {
            @Override
            public PendingCheckout load() {
                return PendingCheckout.fromJson(prefs.getString(name, null));
            }

            @Override
            public void save(PendingCheckout p) {
                // commit：发请求前必须已经落盘，App 随后被杀也不会丢
                prefs.edit().putString(name, p.toJson()).commit();
            }

            @Override
            public void clear() {
                prefs.edit().remove(name).commit();
            }
        };
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
