package com.blink.shop.order;

import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.text.InputFilter;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.swiperefreshlayout.widget.SwipeRefreshLayout;

import java.util.ArrayList;
import java.util.Collections;
import java.util.TimeZone;

import com.blink.shop.R;
import com.blink.shop.catalog.ProductDetailActivity;
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
 * 订单详情：状态、商品、金额和时间线；底部按状态给出操作（待支付：取消 / 去支付；已发货：确认收货；已完成：评价）。
 * 每次回到页面重新查询，操作冲突（如订单已超时关闭）时提示并刷新。
 */
public final class OrderDetailActivity extends BaseActivity {

    private static final String EXTRA_ORDER_ID = "order_id";

    public static Intent intent(Context c, String orderId) {
        return new Intent(c, OrderDetailActivity.class).putExtra(EXTRA_ORDER_ID, orderId);
    }

    private String orderId;
    private Order order;
    private StateView state;
    private Busy busy;
    private SwipeRefreshLayout refresh;
    private boolean acting;

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
        orderId = getIntent().getStringExtra(EXTRA_ORDER_ID);
        if (orderId == null || orderId.isEmpty()) {
            finish();
            return;
        }
        setContentView(R.layout.activity_order_detail);
        View root = findViewById(android.R.id.content);
        state = new StateView(root);
        busy = new Busy(root);
        refresh = findViewById(R.id.refresh);
        refresh.setColorSchemeColors(getColor(R.color.brand));
        refresh.setOnRefreshListener(() -> load(false));
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        findViewById(R.id.bottom_bar).setVisibility(View.GONE);
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (!isFinishing() && !acting) {
            load(order == null);
        }
    }

    private void load(boolean showLoading) {
        if (showLoading) {
            state.loading("正在加载订单…");
        }
        call(() -> app.api().order(orderId), new Async.Callback<Order>() {
            @Override
            public void onSuccess(Order value) {
                refresh.setRefreshing(false);
                state.hide();
                render(value);
            }

            @Override
            public void onError(ApiException e) {
                refresh.setRefreshing(false);
                if ("order_not_found".equals(e.code())) {
                    state.empty("订单不存在", "可能已被删除或不属于当前账号");
                } else if (order == null) {
                    state.error("订单加载失败", e, () -> load(true));
                } else {
                    toast(e.getMessage());
                }
            }
        });
    }

    private void render(Order o) {
        order = o;
        TimeZone zone = TimeZone.getDefault();
        LinearLayout content = findViewById(R.id.detail_content);
        content.removeAllViews();

        LinearLayout head = card(content);
        TextView label = new TextView(this);
        label.setTextSize(15);
        OrderStatusLabel.apply(label, o.status);
        head.addView(label, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        head.addView(Rows.text(this, statusDescription(o, zone), R.color.text_primary, 14));

        LinearLayout items = card(content);
        items.addView(Rows.title(this, o.merchantName));
        for (Order.Item it : o.items) {
            items.addView(itemRow(o, it));
        }

        LinearLayout amounts = card(content);
        amounts.addView(Rows.pair(this, "商品总额", Money.format(o.totalAmount), R.color.text_primary, false));
        if (Money.greater(o.discountAmount, "0")) {
            amounts.addView(Rows.pair(this, "优惠", "−" + Money.format(o.discountAmount), R.color.price, false));
        }
        amounts.addView(Rows.pair(this, "实付", Money.format(o.payAmount), R.color.price, true));

        LinearLayout info = card(content);
        info.addView(Rows.title(this, "订单信息"));
        info.addView(Rows.pair(this, "订单号", o.orderNo, R.color.text_primary, false));
        addTime(info, "下单时间", o.createdAt, zone);
        addTime(info, "支付时间", o.paidAt, zone);
        if (o.payment != null && !o.payment.method.isEmpty()) {
            info.addView(Rows.pair(this, "支付方式", methodLabel(o.payment.method), R.color.text_primary, false));
            if (!o.payment.transactionNo.isEmpty()) {
                info.addView(Rows.pair(this, "支付流水号", o.payment.transactionNo, R.color.text_primary, false));
            }
        }
        addTime(info, "发货时间", o.shippedAt, zone);
        addTime(info, "完成时间", o.completedAt, zone);
        addTime(info, "关闭时间", o.closedAt, zone);

        renderActions(o);
    }

    private String statusDescription(Order o, TimeZone zone) {
        switch (o.status) {
            case Order.PENDING_PAYMENT:
                return "请在 " + Times.formatLocal(o.paymentDeadlineAt, zone) + " 前完成支付，超时订单会自动取消。";
            case Order.PAID:
                return "已支付，等待商家发货。";
            case Order.SHIPPED:
                return "商家已发货，收到商品后请确认收货。";
            case Order.COMPLETED:
                return o.hasUnreviewed() ? "交易完成，欢迎评价购买的商品。" : "交易完成。";
            case Order.CANCELLED:
                return o.cancelReason.isEmpty() ? "订单已取消。" : "订单已取消：" + o.cancelReason;
            default:
                return "";
        }
    }

    private View itemRow(Order o, Order.Item it) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);
        row.setPadding(0, dp(8), 0, dp(8));
        ImageView img = new ImageView(this);
        img.setBackgroundResource(R.drawable.bg_image);
        img.setScaleType(ImageView.ScaleType.CENTER_CROP);
        img.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        row.addView(img, new LinearLayout.LayoutParams(dp(64), dp(64)));
        app.images().load(img, it.imageUrl, dp(64));
        LinearLayout text = new LinearLayout(this);
        text.setOrientation(LinearLayout.VERTICAL);
        text.setPadding(dp(12), 0, dp(8), 0);
        text.addView(Rows.text(this, it.name, R.color.text_primary, 14));
        text.addView(Rows.text(this, it.skuName + " · " + Money.format(it.price) + " × " + it.quantity, R.color.text_secondary, 13));
        row.addView(text, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        text.setOnClickListener(v -> startActivity(ProductDetailActivity.intent(this, it.productId, it.name)));
        if (Order.COMPLETED.equals(o.status)) {
            Button b = new Button(this, null, 0, R.style.SecondaryButton);
            b.setMinWidth(dp(80));
            if (it.reviewed()) {
                b.setText("已评价");
                b.setEnabled(false);
                b.setAlpha(0.6f);
            } else {
                b.setText("评价");
                b.setContentDescription("评价 " + it.name);
                b.setOnClickListener(v -> startActivity(ReviewActivity.intent(this, o.orderId, it)));
            }
            row.addView(b, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, dp(44)));
        }
        return row;
    }

    private void renderActions(Order o) {
        View bar = findViewById(R.id.bottom_bar);
        Button primary = findViewById(R.id.primary_action);
        Button secondary = findViewById(R.id.secondary_action);
        primary.setEnabled(true);
        secondary.setEnabled(true);
        secondary.setVisibility(View.GONE);
        switch (o.status) {
            case Order.PENDING_PAYMENT:
                bar.setVisibility(View.VISIBLE);
                secondary.setVisibility(View.VISIBLE);
                secondary.setText("取消订单");
                secondary.setOnClickListener(v -> confirmCancel());
                primary.setText("去支付 " + Money.format(o.payAmount));
                primary.setOnClickListener(v -> startActivity(PaymentActivity.intent(this, new ArrayList<>(Collections.singletonList(o.orderId)))));
                break;
            case Order.SHIPPED:
                bar.setVisibility(View.VISIBLE);
                primary.setText("确认收货");
                primary.setOnClickListener(v -> confirmReceipt());
                break;
            case Order.COMPLETED:
                Order.Item next = null;
                for (Order.Item it : o.items) {
                    if (!it.reviewed()) {
                        next = it;
                        break;
                    }
                }
                if (next == null) {
                    bar.setVisibility(View.GONE);
                } else {
                    Order.Item target = next;
                    bar.setVisibility(View.VISIBLE);
                    primary.setText("去评价");
                    primary.setOnClickListener(v -> startActivity(ReviewActivity.intent(this, o.orderId, target)));
                }
                break;
            default:
                bar.setVisibility(View.GONE);
        }
    }

    // ---------- 操作 ----------

    private void confirmCancel() {
        EditText reason = new EditText(this);
        reason.setHint("取消原因（选填）");
        reason.setSingleLine(true);
        reason.setFilters(new InputFilter[] {new InputFilter.LengthFilter(100)});
        reason.setMinHeight(dp(48));
        LinearLayout box = new LinearLayout(this);
        box.setPadding(dp(20), dp(8), dp(20), 0);
        box.addView(reason, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        new AlertDialog.Builder(this).setTitle("取消订单").setMessage("取消后库存和优惠券会退回，确定取消吗？").setView(box)
                .setNegativeButton("再想想", null)
                .setPositiveButton("取消订单", (d, w) -> act("正在取消…", "订单已取消",
                        () -> app.api().cancelOrder(orderId, reason.getText().toString())))
                .show();
    }

    private void confirmReceipt() {
        new AlertDialog.Builder(this).setTitle("确认收货").setMessage("确认已经收到商品了吗？")
                .setNegativeButton("还没有", null)
                .setPositiveButton("确认收货", (d, w) -> act("正在确认…", "已确认收货", () -> app.api().confirmReceipt(orderId)))
                .show();
    }

    /** 执行一个订单操作；遮罩挡住重复点击。状态冲突时提示服务端说明并刷新。 */
    private void act(String busyText, String doneText, Async.Task<Order> task) {
        if (acting) {
            return;
        }
        acting = true;
        busy.show(busyText);
        call(task, new Async.Callback<Order>() {
            @Override
            public void onSuccess(Order value) {
                acting = false;
                busy.hide();
                toast(doneText);
                render(value);
            }

            @Override
            public void onError(ApiException e) {
                acting = false;
                busy.hide();
                toast(e.getMessage());
                load(false);
            }
        });
    }

    // ---------- 工具 ----------

    private LinearLayout card(LinearLayout parent) {
        LinearLayout c = new LinearLayout(this);
        c.setOrientation(LinearLayout.VERTICAL);
        c.setBackgroundResource(R.drawable.bg_card);
        c.setPadding(dp(16), dp(14), dp(16), dp(14));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dp(12);
        parent.addView(c, lp);
        return c;
    }

    private void addTime(LinearLayout parent, String label, String iso, TimeZone zone) {
        if (iso != null && !iso.isEmpty() && !"null".equals(iso)) {
            parent.addView(Rows.pair(this, label, Times.formatLocal(iso, zone), R.color.text_primary, false));
        }
    }

    static String methodLabel(String method) {
        switch (method) {
            case "mock_alipay":
                return "支付宝（模拟）";
            case "mock_wechat":
                return "微信支付（模拟）";
            case "mock_balance":
                return "余额（模拟）";
            default:
                return "模拟支付";
        }
    }
}
