package com.blink.shop.order;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.RadioGroup;
import android.widget.TextView;

import java.util.ArrayList;
import java.util.List;
import java.util.TimeZone;

import com.blink.shop.R;
import com.blink.shop.catalog.ProductListActivity;
import com.blink.shop.model.Money;
import com.blink.shop.model.Order;
import com.blink.shop.model.Times;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Busy;
import com.blink.shop.ui.OrderStatusLabel;
import com.blink.shop.ui.Rows;
import com.blink.shop.ui.StateView;

/**
 * 支付一次结算产生的订单（按店铺拆成的多个订单一起付）。订单 ID 放在 Intent 里，
 * App 被杀后重进会重新查询订单状态，已付的不会再付。
 */
public final class PaymentActivity extends BaseActivity {

    private static final String EXTRA_ORDER_IDS = "order_ids";

    public static Intent intent(Context c, ArrayList<String> orderIds) {
        return new Intent(c, PaymentActivity.class).putStringArrayListExtra(EXTRA_ORDER_IDS, orderIds);
    }

    /** 一次支付的结果：各订单的最新状态，以及第一个失败的原因（全部成功为 null）。 */
    private static final class PayResult {
        final List<Order> orders;
        final ApiException error;

        PayResult(List<Order> orders, ApiException error) {
            this.orders = orders;
            this.error = error;
        }
    }

    private final Handler ticker = new Handler(Looper.getMainLooper());
    private ArrayList<String> orderIds;
    private List<Order> orders = new ArrayList<>();
    private StateView state;
    private Busy busy;
    private Button payButton;
    private boolean paying;
    /** 最近一次支付的问题；刷新倒计时时继续显示。 */
    private ApiException lastError;

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
        orderIds = getIntent().getStringArrayListExtra(EXTRA_ORDER_IDS);
        if (orderIds == null || orderIds.isEmpty()) {
            finish();
            return;
        }
        setContentView(R.layout.activity_payment);
        View root = findViewById(android.R.id.content);
        state = new StateView(root);
        busy = new Busy(root);
        payButton = findViewById(R.id.pay_button);
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        payButton.setOnClickListener(v -> payAll());
        findViewById(R.id.view_orders).setOnClickListener(v -> {
            startActivity(new Intent(this, OrderListActivity.class));
            finish();
        });
        findViewById(R.id.keep_shopping).setOnClickListener(v -> {
            startActivity(new Intent(this, ProductListActivity.class).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP));
            finish();
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (!isFinishing() && !paying) {
            load();
        }
        ticker.post(tick);
    }

    @Override
    protected void onPause() {
        ticker.removeCallbacks(tick);
        super.onPause();
    }

    /** 每 30 秒刷新剩余支付时间。 */
    private final Runnable tick = new Runnable() {
        @Override
        public void run() {
            if (!orders.isEmpty()) {
                render(lastError);
            }
            ticker.postDelayed(this, 30_000);
        }
    };

    private void load() {
        if (orders.isEmpty()) {
            state.loading("正在查询订单…");
        }
        call(() -> fetchAll(), new Async.Callback<List<Order>>() {
            @Override
            public void onSuccess(List<Order> value) {
                orders = value;
                state.hide();
                render(lastError);
            }

            @Override
            public void onError(ApiException e) {
                if (orders.isEmpty()) {
                    state.error("订单查询失败", e, PaymentActivity.this::load);
                } else {
                    toast(e.getMessage());
                }
            }
        });
    }

    private List<Order> fetchAll() throws ApiException {
        List<Order> out = new ArrayList<>();
        for (String id : orderIds) {
            out.add(app.api().order(id));
        }
        return out;
    }

    private String method() {
        int id = ((RadioGroup) findViewById(R.id.method_group)).getCheckedRadioButtonId();
        if (id == R.id.method_wechat) {
            return "mock_wechat";
        }
        if (id == R.id.method_balance) {
            return "mock_balance";
        }
        return "mock_alipay";
    }

    private void payAll() {
        if (paying) {
            return;
        }
        List<String> pending = new ArrayList<>();
        for (Order o : orders) {
            if (Order.PENDING_PAYMENT.equals(o.status)) {
                pending.add(o.orderId);
            }
        }
        if (pending.isEmpty()) {
            return;
        }
        String method = method();
        paying = true;
        payButton.setEnabled(false);
        busy.show("正在支付…");
        call(() -> {
            ApiException first = null;
            for (String id : pending) {
                try {
                    app.api().pay(id, method);
                } catch (ApiException e) {
                    if (first == null) {
                        first = e;
                    }
                    if (e.isNetwork()) {
                        break; // 网络断了，后面的也不会成功；先查清已付的
                    }
                }
            }
            // 无论成败都以服务端为准重新查询：网络失败时支付可能其实已成功
            List<Order> latest;
            try {
                latest = fetchAll();
            } catch (ApiException e) {
                latest = null;
                if (first == null) {
                    first = e;
                }
            }
            return new PayResult(latest, first);
        }, new Async.Callback<PayResult>() {
            @Override
            public void onSuccess(PayResult r) {
                paying = false;
                busy.hide();
                if (r.orders != null) {
                    orders = r.orders;
                }
                render(r.error);
            }

            @Override
            public void onError(ApiException e) {
                paying = false;
                busy.hide();
                render(e);
            }
        });
    }

    /** error 不为空时在顶部说明这次支付的问题。 */
    private void render(ApiException error) {
        lastError = error;
        TimeZone zone = TimeZone.getDefault();
        long now = System.currentTimeMillis();
        LinearLayout card = findViewById(R.id.orders_card);
        card.removeAllViews();
        card.addView(Rows.title(this, orders.size() > 1 ? "共 " + orders.size() + " 个订单（按店铺拆分）" : "订单"));
        List<String> pendingAmounts = new ArrayList<>();
        int paid = 0;
        int closed = 0;
        for (Order o : orders) {
            LinearLayout row = new LinearLayout(this);
            row.setOrientation(LinearLayout.HORIZONTAL);
            row.setPadding(0, dp(6), 0, dp(6));
            TextView name = Rows.text(this, o.merchantName + " · " + o.quantity() + " 件", R.color.text_primary, 15);
            row.addView(name, new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f));
            TextView status = new TextView(this);
            OrderStatusLabel.apply(status, o.status);
            row.addView(status);
            card.addView(row);
            card.addView(Rows.pair(this, "订单号 " + o.orderNo, Money.format(o.payAmount), R.color.price, true));
            if (Order.PENDING_PAYMENT.equals(o.status)) {
                pendingAmounts.add(o.payAmount);
                long deadline = Times.parseIsoMillis(o.paymentDeadlineAt);
                if (deadline > 0) {
                    long minutes = Math.max(0, (deadline - now) / 60_000);
                    card.addView(Rows.text(this, "请在 " + Times.formatLocal(o.paymentDeadlineAt, zone) + " 前支付（剩余约 " + minutes
                            + " 分钟），超时自动取消", R.color.warning_text, 13));
                }
            } else if (Order.CANCELLED.equals(o.status)) {
                closed++;
                card.addView(Rows.text(this, o.cancelReason.isEmpty() ? "订单已关闭" : "订单已关闭：" + o.cancelReason,
                        R.color.text_secondary, 13));
            } else {
                paid++;
            }
        }

        TextView result = findViewById(R.id.pay_result);
        boolean allDone = pendingAmounts.isEmpty();
        if (error != null) {
            result.setVisibility(View.VISIBLE);
            result.setBackgroundResource(R.drawable.bg_warning);
            result.setTextColor(getColor(R.color.warning_text));
            result.setText(payErrorText(error));
        } else if (allDone && paid > 0) {
            result.setVisibility(View.VISIBLE);
            result.setBackgroundResource(R.drawable.bg_tag);
            result.setTextColor(getColor(R.color.text_primary));
            result.setText(closed > 0 ? "已支付 " + paid + " 个订单，" + closed + " 个订单已关闭" : "支付成功，可以在“我的订单”查看进度");
        } else if (allDone) {
            result.setVisibility(View.VISIBLE);
            result.setBackgroundResource(R.drawable.bg_warning);
            result.setTextColor(getColor(R.color.warning_text));
            result.setText("订单已关闭，无需支付");
        } else {
            result.setVisibility(View.GONE);
        }

        findViewById(R.id.method_card).setVisibility(allDone ? View.GONE : View.VISIBLE);
        findViewById(R.id.done_actions).setVisibility(allDone ? View.VISIBLE : View.GONE);
        findViewById(R.id.bottom_bar).setVisibility(allDone ? View.GONE : View.VISIBLE);
        ((TextView) findViewById(R.id.pay_total)).setText("待付 " + Money.format(Money.sum(pendingAmounts)));
        payButton.setEnabled(!allDone && !paying);
        payButton.setText(error != null && !allDone ? "重新支付" : "确认支付");
    }

    private static String payErrorText(ApiException e) {
        switch (e.code()) {
            case "order_expired":
                return e.getMessage();
            case "order_status_conflict":
                return "订单状态已变化，已为你刷新：" + e.getMessage();
            default:
                return e.isNetwork() ? "支付失败：" + e.getMessage() + "。已重新查询订单，未支付的可以再次支付" : "支付失败：" + e.getMessage();
        }
    }
}
