package com.blink.shop.order;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.recyclerview.widget.LinearLayoutManager;
import androidx.recyclerview.widget.RecyclerView;
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout;

import java.util.ArrayList;
import java.util.List;
import java.util.TimeZone;

import com.blink.shop.R;
import com.blink.shop.model.Money;
import com.blink.shop.model.Order;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Times;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Chips;
import com.blink.shop.ui.OrderStatusLabel;
import com.blink.shop.ui.Pager;
import com.blink.shop.ui.StateView;

/** 我的订单：按状态筛选（服务端筛选）、分页；每次回到页面重新查询（状态可能已被商家或超时改变）。 */
public final class OrderListActivity extends BaseActivity {

    private static final String EXTRA_STATUS = "status";
    private static final String STATE_STATUS = "status";
    private static final String[][] TABS = {
            {"", "全部"}, {Order.PENDING_PAYMENT, "待支付"}, {Order.PAID, "待发货"}, {Order.SHIPPED, "已发货"},
            {Order.COMPLETED, "已完成"}, {Order.CANCELLED, "已取消"},
    };

    public static Intent intent(Context c, String status) {
        return new Intent(c, OrderListActivity.class).putExtra(EXTRA_STATUS, status);
    }

    private final Pager<Order> pager = new Pager<>(o -> o.orderId);
    private final Adapter adapter = new Adapter();
    private String status = "";
    private StateView state;
    private SwipeRefreshLayout refresh;
    private boolean loadedOnce;

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
        setContentView(R.layout.activity_order_list);
        String s = savedInstanceState != null ? savedInstanceState.getString(STATE_STATUS) : getIntent().getStringExtra(EXTRA_STATUS);
        status = s == null ? "" : s;
        state = new StateView(findViewById(android.R.id.content));
        refresh = findViewById(R.id.refresh);
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        RecyclerView list = findViewById(R.id.order_list);
        LinearLayoutManager lm = new LinearLayoutManager(this);
        list.setLayoutManager(lm);
        list.setAdapter(adapter);
        list.addOnScrollListener(new RecyclerView.OnScrollListener() {
            @Override
            public void onScrolled(RecyclerView rv, int dx, int dy) {
                if (dy > 0 && lm.findLastVisibleItemPosition() >= adapter.getItemCount() - 3) {
                    loadMore();
                }
            }
        });
        refresh.setColorSchemeColors(getColor(R.color.brand));
        refresh.setOnRefreshListener(() -> reload(false));
        renderTabs();
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (!isFinishing()) {
            reload(!loadedOnce);
        }
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putString(STATE_STATUS, status);
    }

    private void renderTabs() {
        LinearLayout tabs = findViewById(R.id.status_tabs);
        tabs.removeAllViews();
        for (String[] t : TABS) {
            tabs.addView(Chips.make(this, t[1], t[0].equals(status), v -> {
                if (!t[0].equals(status)) {
                    status = t[0];
                    renderTabs();
                    reload(true);
                }
            }));
        }
    }

    private void reload(boolean showLoading) {
        int gen = pager.reset();
        if (showLoading) {
            adapter.set(new ArrayList<>(), false, false);
            state.loading("正在加载订单…");
        }
        String st = status;
        call(() -> app.api().orders(st, 1), new Async.Callback<PageResult<Order>>() {
            @Override
            public void onSuccess(PageResult<Order> page) {
                if (pager.accept(gen, page)) {
                    loadedOnce = true;
                    refresh.setRefreshing(false);
                    render();
                }
            }

            @Override
            public void onError(ApiException e) {
                if (pager.fail(gen)) {
                    refresh.setRefreshing(false);
                    adapter.set(new ArrayList<>(), false, false);
                    state.error("订单加载失败", e, () -> reload(true));
                }
            }
        });
    }

    private void loadMore() {
        int gen = pager.next();
        if (gen >= 0) {
            fetchNext(gen);
        }
    }

    private void fetchNext(int gen) {
        render();
        String st = status;
        int page = pager.nextPage();
        call(() -> app.api().orders(st, page), new Async.Callback<PageResult<Order>>() {
            @Override
            public void onSuccess(PageResult<Order> r) {
                if (pager.accept(gen, r)) {
                    render();
                }
            }

            @Override
            public void onError(ApiException e) {
                if (pager.fail(gen)) {
                    toast(e.getMessage());
                    render();
                }
            }
        });
    }

    private void render() {
        List<Order> items = pager.items();
        if (items.isEmpty() && !pager.isLoading()) {
            adapter.set(items, false, false);
            state.empty(status.isEmpty() ? "还没有订单" : "没有" + Order.statusLabel(status) + "的订单", "去首页挑挑商品吧");
            return;
        }
        state.hide();
        adapter.set(items, pager.isLoading(), pager.isFailed());
    }

    // ---------- 列表 ----------

    private final class Adapter extends RecyclerView.Adapter<RecyclerView.ViewHolder> {
        private final List<Order> items = new ArrayList<>();
        private boolean loading;
        private boolean failed;

        void set(List<Order> list, boolean loadingMore, boolean failedMore) {
            items.clear();
            items.addAll(list);
            loading = loadingMore;
            failed = failedMore;
            notifyDataSetChanged();
        }

        @Override
        public int getItemCount() {
            return items.size() + (loading || failed ? 1 : 0);
        }

        @Override
        public int getItemViewType(int position) {
            return position < items.size() ? 0 : 1;
        }

        @Override
        public RecyclerView.ViewHolder onCreateViewHolder(ViewGroup parent, int viewType) {
            LayoutInflater inf = LayoutInflater.from(parent.getContext());
            if (viewType == 1) {
                return new RecyclerView.ViewHolder(inf.inflate(R.layout.item_list_footer, parent, false)) {
                };
            }
            return new Holder(inf.inflate(R.layout.item_order, parent, false));
        }

        @Override
        public void onBindViewHolder(RecyclerView.ViewHolder holder, int position) {
            if (holder instanceof Holder) {
                ((Holder) holder).bind(items.get(position));
                return;
            }
            TextView t = (TextView) holder.itemView;
            t.setText(failed ? "加载失败，点击重试" : "正在加载更多…");
            t.setOnClickListener(failed ? v -> {
                int gen = pager.retryNext();
                if (gen >= 0) {
                    fetchNext(gen);
                }
            } : null);
        }
    }

    private final class Holder extends RecyclerView.ViewHolder {
        final TextView merchant;
        final TextView status;
        final ImageView image;
        final TextView items;
        final TextView total;
        final TextView hint;

        Holder(View v) {
            super(v);
            merchant = v.findViewById(R.id.order_merchant);
            status = v.findViewById(R.id.order_status);
            image = v.findViewById(R.id.order_image);
            items = v.findViewById(R.id.order_items);
            total = v.findViewById(R.id.order_total);
            hint = v.findViewById(R.id.order_hint);
        }

        void bind(Order o) {
            merchant.setText(o.merchantName);
            OrderStatusLabel.apply(status, o.status);
            StringBuilder names = new StringBuilder();
            for (Order.Item it : o.items) {
                if (names.length() > 0) {
                    names.append("、");
                }
                names.append(it.name);
            }
            items.setText(names);
            if (!o.items.isEmpty()) {
                app.images().load(image, o.items.get(0).imageUrl, dp(64));
            }
            total.setText("共 " + o.quantity() + " 件，实付 " + Money.format(o.payAmount));
            String h = "";
            if (Order.PENDING_PAYMENT.equals(o.status)) {
                h = "请在 " + Times.formatLocal(o.paymentDeadlineAt, TimeZone.getDefault()) + " 前支付";
            } else if (o.hasUnreviewed()) {
                h = "有商品待评价";
            }
            hint.setText(h);
            hint.setVisibility(h.isEmpty() ? View.GONE : View.VISIBLE);
            itemView.setContentDescription(o.merchantName + "，" + Order.statusLabel(o.status) + "，" + names + "，实付 "
                    + Money.format(o.payAmount));
            itemView.setOnClickListener(v -> startActivity(OrderDetailActivity.intent(OrderListActivity.this, o.orderId)));
        }
    }
}
