package com.blink.shop.cart;

import android.app.AlertDialog;
import android.os.Bundle;
import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import com.blink.shop.R;
import com.blink.shop.data.Drafts;
import com.blink.shop.data.ShopApi;
import com.blink.shop.model.Cart;
import com.blink.shop.model.Coupon;
import com.blink.shop.model.DiscountPreview;
import com.blink.shop.model.Money;
import com.blink.shop.model.Order;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Session;
import com.blink.shop.net.ApiException;
import com.blink.shop.order.PaymentActivity;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Busy;
import com.blink.shop.ui.Rows;
import com.blink.shop.ui.StateView;

/**
 * 确认订单：展示服务端试算（同一套计算用于下单），选择用券方式，提交时带上确认页金额和幂等键。
 * 幂等键保存在本地草稿里：网络失败或 App 被杀后再提交不会重复下单（见 Drafts）。
 */
public final class CheckoutActivity extends BaseActivity {

    private static final String STATE_MODE = "coupon_mode";
    private static final String STATE_COUPONS = "coupon_ids";

    /** 用券方式：自动最优（不传券）、不用券（空列表）、指定这些券。 */
    static final int AUTO = 0;
    static final int NONE = 1;
    static final int CUSTOM = 2;

    private static final class Loaded {
        final Cart cart;
        final DiscountPreview preview;

        Loaded(Cart cart, DiscountPreview preview) {
            this.cart = cart;
            this.preview = preview;
        }
    }

    private StateView state;
    private Busy busy;
    private Button submit;
    private int mode = AUTO;
    private ArrayList<String> customCoupons = new ArrayList<>();
    private Cart cart;
    private DiscountPreview preview;
    private boolean submitting;

    @Override
    protected boolean requiresLogin() {
        return true;
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (isFinishing()) {
            return;
        }
        setContentView(R.layout.activity_checkout);
        if (savedInstanceState != null) {
            mode = savedInstanceState.getInt(STATE_MODE, AUTO);
            ArrayList<String> ids = savedInstanceState.getStringArrayList(STATE_COUPONS);
            customCoupons = ids == null ? new ArrayList<>() : ids;
        }
        View root = findViewById(android.R.id.content);
        state = new StateView(root);
        busy = new Busy(root);
        submit = findViewById(R.id.submit_button);
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        findViewById(R.id.coupon_row).setOnClickListener(v -> chooseCoupons());
        submit.setOnClickListener(v -> submit());
        Session s = app.sessions().current();
        findViewById(R.id.pending_banner).setVisibility(
                s != null && app.drafts().hasPendingCheckout(s.account.accountId) ? View.VISIBLE : View.GONE);
        load();
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putInt(STATE_MODE, mode);
        out.putStringArrayList(STATE_COUPONS, customCoupons);
    }

    private List<String> couponArg() {
        switch (mode) {
            case NONE:
                return Collections.emptyList();
            case CUSTOM:
                return customCoupons;
            default:
                return null;
        }
    }

    private void load() {
        state.loading("正在计算优惠…");
        findViewById(R.id.bottom_bar).setVisibility(View.GONE);
        List<String> coupons = couponArg();
        call(() -> new Loaded(app.api().cart(), app.api().discountPreview(coupons)), new Async.Callback<Loaded>() {
            @Override
            public void onSuccess(Loaded value) {
                cart = value.cart;
                preview = value.preview;
                if (cart.checkoutCount() == 0) {
                    state.empty("没有可以结算的商品", "回购物车选择商品后再结算");
                    return;
                }
                state.hide();
                render();
            }

            @Override
            public void onError(ApiException e) {
                if ("coupon_not_applicable".equals(e.code()) && mode != AUTO) {
                    // 指定的券不能用了（如已过期）：提示后改回自动选择
                    toast(e.getMessage());
                    mode = AUTO;
                    customCoupons = new ArrayList<>();
                    load();
                    return;
                }
                state.error("订单信息加载失败", e, CheckoutActivity.this::load);
            }
        });
    }

    private void render() {
        LinearLayout items = findViewById(R.id.items_card);
        items.removeAllViews();
        items.addView(Rows.title(this, "商品（" + cart.checkoutCount() + "）"));
        String lastMerchant = null;
        for (Cart.Item it : cart.items) {
            if (!it.selected || !it.available) {
                continue;
            }
            if (!it.merchantName.equals(lastMerchant)) {
                lastMerchant = it.merchantName;
                items.addView(Rows.text(this, it.merchantName, R.color.text_secondary, 13));
            }
            items.addView(Rows.pair(this, it.productName + "（" + it.skuName + "）× " + it.quantity, Money.format(it.amount),
                    R.color.text_primary, false));
        }

        TextView couponValue = findViewById(R.id.coupon_value);
        couponValue.setText(couponSummary());
        findViewById(R.id.coupon_row).setContentDescription("优惠券：" + couponSummary() + "，点击更换");

        LinearLayout d = findViewById(R.id.discount_card);
        d.removeAllViews();
        d.addView(Rows.title(this, "金额明细"));
        d.addView(Rows.pair(this, "商品总额", Money.format(preview.totalAmount), R.color.text_primary, false));
        for (DiscountPreview.Line l : preview.lines) {
            d.addView(Rows.pair(this, (l.isCoupon() ? "券 · " : "活动 · ") + l.name, "−" + Money.format(l.amount), R.color.price, false));
        }
        d.addView(Rows.pair(this, "共优惠", "−" + Money.format(preview.discountAmount), R.color.price, false));
        d.addView(Rows.pair(this, "实付", Money.format(preview.payAmount), R.color.price, true));
        if (preview.merchants.size() > 1) {
            d.addView(Rows.text(this, "将按店铺拆成 " + preview.merchants.size() + " 个订单：", R.color.text_secondary, 13));
            for (DiscountPreview.MerchantTotal m : preview.merchants) {
                d.addView(Rows.pair(this, m.merchantName, Money.format(m.payAmount), R.color.text_primary, false));
            }
        }
        for (DiscountPreview.Hint h : preview.hints) {
            d.addView(Rows.text(this, "再买 " + Money.format(h.shortfall) + " 可享“" + h.name + "”", R.color.brand, 13));
        }

        findViewById(R.id.bottom_bar).setVisibility(View.VISIBLE);
        ((TextView) findViewById(R.id.pay_amount)).setText("实付 " + Money.format(preview.payAmount));
        TextView note = findViewById(R.id.pay_note);
        boolean discounted = Money.greater(preview.discountAmount, "0");
        note.setText(discounted ? "已优惠 " + Money.format(preview.discountAmount) : "");
        note.setVisibility(discounted ? View.VISIBLE : View.GONE);
    }

    private String couponSummary() {
        int used = 0;
        for (DiscountPreview.Line l : preview.lines) {
            if (l.isCoupon()) {
                used++;
            }
        }
        switch (mode) {
            case NONE:
                return "不使用优惠券";
            case CUSTOM:
                return "已选 " + used + " 张";
            default:
                return used > 0 ? "自动选择最优（已用 " + used + " 张）" : "自动选择最优（暂无可用）";
        }
    }

    // ---------- 选券 ----------

    private void chooseCoupons() {
        if (preview == null || submitting) {
            return;
        }
        call(() -> app.api().myCoupons(Coupon.Mine.UNUSED), new Async.Callback<PageResult<Coupon.Mine>>() {
            @Override
            public void onSuccess(PageResult<Coupon.Mine> page) {
                showCouponDialog(page.items);
            }

            @Override
            public void onError(ApiException e) {
                toast("优惠券加载失败：" + e.getMessage());
            }
        });
    }

    private void showCouponDialog(List<Coupon.Mine> mine) {
        String[] options = {"自动选择最优", "不使用优惠券", "自己选择…"};
        new AlertDialog.Builder(this).setTitle("使用优惠券")
                .setSingleChoiceItems(options, mode, (dlg, which) -> {
                    dlg.dismiss();
                    if (which == CUSTOM) {
                        pickCustom(mine);
                    } else if (which != mode) {
                        mode = which;
                        customCoupons = new ArrayList<>();
                        load();
                    }
                })
                .setNegativeButton("取消", null)
                .show();
    }

    private void pickCustom(List<Coupon.Mine> mine) {
        if (mine.isEmpty()) {
            toast("没有未使用的优惠券");
            return;
        }
        String[] labels = new String[mine.size()];
        boolean[] checked = new boolean[mine.size()];
        List<String> current = mode == CUSTOM ? customCoupons : preview.userCouponIds;
        for (int i = 0; i < mine.size(); i++) {
            Coupon c = mine.get(i).coupon;
            labels[i] = c.scopeLabel() + " · " + c.name + "（" + c.description + "）";
            checked[i] = current.contains(mine.get(i).userCouponId);
        }
        new AlertDialog.Builder(this).setTitle("选择优惠券")
                .setMultiChoiceItems(labels, checked, (dlg, which, isChecked) -> checked[which] = isChecked)
                .setNegativeButton("取消", null)
                .setPositiveButton("确定", (dlg, w) -> {
                    ArrayList<String> ids = new ArrayList<>();
                    for (int i = 0; i < mine.size(); i++) {
                        if (checked[i]) {
                            ids.add(mine.get(i).userCouponId);
                        }
                    }
                    // 能否同时使用、是否满足门槛都由服务端判断，不能用时 load() 会提示并改回自动
                    mode = ids.isEmpty() ? NONE : CUSTOM;
                    customCoupons = ids;
                    load();
                })
                .show();
    }

    // ---------- 提交 ----------

    private void submit() {
        if (submitting || preview == null) {
            return;
        }
        Session s = app.sessions().current();
        if (s == null) {
            return;
        }
        String accountId = s.account.accountId;
        Drafts drafts = app.drafts();
        String key = drafts.checkoutKey(accountId);
        List<String> coupons = couponArg();
        String expected = preview.payAmount;
        submitting = true;
        submit.setEnabled(false);
        busy.show("正在提交订单…");
        drafts.markCheckoutSent(accountId);
        call(() -> app.api().checkout(key, coupons, expected), new Async.Callback<ShopApi.CheckoutResult>() {
            @Override
            public void onSuccess(ShopApi.CheckoutResult r) {
                drafts.clearCheckout(accountId);
                busy.hide();
                submitting = false;
                if (r.replayed) {
                    toast("上次提交已经成功，这是那次的订单");
                }
                ArrayList<String> ids = new ArrayList<>();
                for (Order o : r.orders) {
                    ids.add(o.orderId);
                }
                startActivity(PaymentActivity.intent(CheckoutActivity.this, ids));
                finish();
            }

            @Override
            public void onError(ApiException e) {
                busy.hide();
                submitting = false;
                submit.setEnabled(true);
                if (!Drafts.keepKeyAfter(e)) {
                    drafts.clearCheckout(accountId);
                }
                onSubmitFailed(e);
            }
        });
    }

    private void onSubmitFailed(ApiException e) {
        switch (e.code()) {
            case "price_changed":
                // 金额以服务端为准：提示新金额，重新试算后让用户再次确认
                new AlertDialog.Builder(this).setTitle("金额有变化").setMessage(e.getMessage())
                        .setPositiveButton("重新确认", (d, w) -> load()).setCancelable(false).show();
                break;
            case "empty_cart":
                toast(e.getMessage());
                finish();
                break;
            case "coupon_not_applicable":
                toast(e.getMessage());
                mode = AUTO;
                customCoupons = new ArrayList<>();
                load();
                break;
            case "item_unavailable":
            case "insufficient_stock":
            case "out_of_stock":
                new AlertDialog.Builder(this).setTitle("商品有变化").setMessage(e.getMessage())
                        .setPositiveButton("回购物车修改", (d, w) -> finish()).setCancelable(false).show();
                break;
            default:
                if (Drafts.keepKeyAfter(e)) {
                    findViewById(R.id.pending_banner).setVisibility(View.VISIBLE);
                    toast(e.getMessage() + "。订单可能未提交，可以直接再次提交，不会重复下单");
                } else {
                    toast(e.getMessage());
                }
        }
    }
}
