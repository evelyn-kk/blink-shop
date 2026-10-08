package com.blink.shop.data;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.UUID;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import com.blink.shop.net.ApiException;

/**
 * 一次结算提交的完整意图：幂等键 + 用券选择 + 确认页金额。
 * <p>
 * 服务端按“账户 + 幂等键”回放第一次成功的订单，不比较后来的参数。所以结果未知（超时、断网、5xx、429）时，
 * 这三样必须一起冻结：重试只能原样再发一次，直到拿到明确结果（成功，或被服务端明确拒绝——说明没有下单）才能结束；
 * 期间不允许换券或按新金额提交，否则可能静默地进入旧订单，或者按网络时序多下一单。
 */
public final class PendingCheckout {

    /** 冻结的提交保存在哪里（App 里是按账户隔离的本地草稿）。 */
    public interface Store {
        PendingCheckout load();

        void save(PendingCheckout p);

        void clear();
    }

    public final String key;
    /** null 表示自动选最优券；空列表表示不用券。 */
    public final List<String> couponIds;
    public final String expectedPayAmount;

    public PendingCheckout(String key, List<String> couponIds, String expectedPayAmount) {
        this.key = key;
        this.couponIds = couponIds == null ? null : Collections.unmodifiableList(new ArrayList<>(couponIds));
        this.expectedPayAmount = expectedPayAmount;
    }

    /** 新的一次提交：生成新键（1–128 位字母数字和 . _ : -，服务端规则）。 */
    public static PendingCheckout create(List<String> couponIds, String expectedPayAmount) {
        return new PendingCheckout("and-" + UUID.randomUUID(), couponIds, expectedPayAmount);
    }

    /** 提交失败后是否仍然结果未知（需要保留并冻结）：网络失败、超时、取消、响应无法识别、5xx、429。 */
    public static boolean unknownAfter(ApiException e) {
        if (e.kind() != ApiException.Kind.HTTP) {
            return true;
        }
        return e.status() >= 500 || e.status() == 429;
    }

    /** 给用户看的用券方式。 */
    public String couponDescription() {
        if (couponIds == null) {
            return "自动选择最优券";
        }
        return couponIds.isEmpty() ? "不使用优惠券" : "指定 " + couponIds.size() + " 张券";
    }

    public String toJson() {
        JSONObject o = new JSONObject();
        try {
            o.put("key", key).put("expected_pay_amount", expectedPayAmount);
            if (couponIds != null) {
                o.put("coupon_ids", new JSONArray(couponIds));
            }
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
        return o.toString();
    }

    /** 无法解析时返回 null。 */
    public static PendingCheckout fromJson(String s) {
        if (s == null || s.isEmpty()) {
            return null;
        }
        try {
            JSONObject o = new JSONObject(s);
            String key = o.optString("key", "");
            if (key.isEmpty()) {
                return null;
            }
            List<String> ids = null;
            JSONArray arr = o.optJSONArray("coupon_ids");
            if (arr != null) {
                ids = new ArrayList<>();
                for (int i = 0; i < arr.length(); i++) {
                    ids.add(arr.getString(i));
                }
            }
            return new PendingCheckout(key, ids, o.optString("expected_pay_amount", ""));
        } catch (JSONException e) {
            return null;
        }
    }

    /**
     * 提交结算。有未确认结果的上一次提交时，忽略当前页面的选择（可以为 null，例如重启后购物车已清空、没有试算），
     * 原样重发上一次；否则按当前选择新建一次。
     * 请求发出前先保存；成功或被明确拒绝后清除，结果未知时保留（下次仍只能原样重发）。
     */
    public static ShopApi.CheckoutResult submit(ShopApi api, Store store, List<String> currentCoupons, String currentExpected)
            throws ApiException {
        PendingCheckout p = store.load();
        if (p == null) {
            if (currentExpected == null) {
                throw new IllegalStateException("no pending checkout and no confirmed amount");
            }
            p = create(currentCoupons, currentExpected);
            store.save(p);
        }
        try {
            ShopApi.CheckoutResult r = api.checkout(p.key, p.couponIds, p.expectedPayAmount);
            store.clear();
            return r;
        } catch (ApiException e) {
            if (!unknownAfter(e)) {
                // 服务端明确拒绝：这个键没有下过单（失败的请求不占用键），可以换新的选择重新来
                store.clear();
            }
            throw e;
        }
    }
}
