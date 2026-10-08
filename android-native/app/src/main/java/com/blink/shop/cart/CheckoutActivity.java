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
import com.blink.shop.data.PendingCheckout;
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
 * 上一次提交结果未知时（超时、断网、App 被杀等），页面进入“恢复”状态：只能原样重发那一次（同一个键、券和金额），
 * 不能换券，直到拿到明确结果（见 PendingCheckout）。
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
    private PendingCheckout.Store pendingStore;
    /** 结果未确认的上一次提交；不为 null 时只能原样重发。 */
    private PendingCheckout pending;

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
        if (s == null) {
            finish();
            return;
        }
        pendingStore = app.drafts().checkout(s.account.accountId);
        pending = pendingStore.load();
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
        if (pending != null) {
            // 恢复不依赖当前购物车和试算：上次如果已经成功，购物车已被清空，试算也算不出那一单
            state.hide();
            renderRecovery();
            return;
        }
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

        findViewById(R.id.pending_banner).setVisibility(View.GONE);
        findViewById(R.id.discount_card).setVisibility(View.VISIBLE);
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
        TextView note = findViewById(R.id.pay_note);
        ((TextView) findViewById(R.id.pay_amount)).setText("实付 " + Money.format(preview.payAmount));
        boolean discounted = Money.greater(preview.discountAmount, "0");
        note.setText(discounted ? "已优惠 " + Money.format(preview.discountAmount) : "");
        note.setVisibility(discounted ? View.VISIBLE : View.GONE);
        submit.setText("提交订单");
    }

    /**
     * 恢复界面：只用本地冻结的上一次提交（不拉购物车和试算）。上次如果其实已经成功，购物车已清空，
     * 原样重发会拿到服务端回放的那组订单；如果没成功，会按上次的内容下单或被明确拒绝。
     */
    private void renderRecovery() {
        TextView banner = findViewById(R.id.pending_banner);
        banner.setVisibility(View.VISIBLE);
        banner.setText("上次提交的订单（实付 " + Money.format(pending.expectedPayAmount) + "，" + pending.couponDescription()
                + "）没有收到结果。为避免重复下单或进入与当时确认内容不同的订单，只能原样重新提交这一笔："
                + "如果上次已经成功，会直接显示那次的订单；拿到结果后才能修改或重新结算。");
        LinearLayout items = findViewById(R.id.items_card);
        items.removeAllViews();
        items.addView(Rows.title(this, "上次提交的订单"));
        items.addView(Rows.pair(this, "确认时的实付金额", Money.format(pending.expectedPayAmount), R.color.price, true));
        items.addView(Rows.pair(this, "用券方式", pending.couponDescription(), R.color.text_primary, false));
        items.addView(Rows.text(this, "商品以服务端结果为准：上次成功时会显示那次的订单，购物车里已结算的商品不会再次下单。",
                R.color.text_secondary, 13));
        TextView couponValue = findViewById(R.id.coupon_value);
        couponValue.setText("上次提交：" + pending.couponDescription());
        findViewById(R.id.coupon_row).setContentDescription("优惠券：上次提交，" + pending.couponDescription() + "，结果确认前不能修改");
        findViewById(R.id.discount_card).setVisibility(View.GONE);
        findViewById(R.id.bottom_bar).setVisibility(View.VISIBLE);
        ((TextView) findViewById(R.id.pay_amount)).setText("实付 " + Money.format(pending.expectedPayAmount));
        TextView note = findViewById(R.id.pay_note);
        note.setText("上次提交的金额");
        note.setVisibility(View.VISIBLE);
        submit.setText("重新提交上次的订单");
        submit.setEnabled(!submitting);
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
        if (pending != null) {
            toast("上一笔订单的结果还没确认，确认后才能修改优惠券");
            return;
        }
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
        // 恢复时不需要（也可能拿不到）当前试算：PendingCheckout 只用冻结的参数
        if (submitting || (pending == null && preview == null)) {
            return;
        }
        List<String> coupons = couponArg();
        String expected = preview == null ? null : preview.payAmount;
        submitting = true;
        submit.setEnabled(false);
        busy.show(pending != null ? "正在确认上次的订单…" : "正在提交订单…");
        // 有未确认的上一次提交时，PendingCheckout 会忽略当前选择、原样重发上一次
        call(() -> PendingCheckout.submit(app.api(), pendingStore, coupons, expected), new Async.Callback<ShopApi.CheckoutResult>() {
            @Override
            public void onSuccess(ShopApi.CheckoutResult r) {
                pending = null;
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
                boolean wasRecovery = pending != null;
                pending = pendingStore.load();
                if (wasRecovery && pending == null) {
                    // 原样重发被服务端明确拒绝：上次没有下单成功，可以按当前页面重新确认
                    toast("上次的订单没有提交成功：" + e.getMessage() + "。请确认当前订单后再提交");
                    load();
                    return;
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
                if (pending != null) {
                    toast(e.getMessage() + "。订单可能未提交，可以直接再次提交，不会重复下单");
                    renderRecovery();
                } else {
                    toast(e.getMessage());
                }
        }
    }
}
