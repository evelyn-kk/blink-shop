package com.blink.shop.cart;

import android.app.AlertDialog;
import android.content.Intent;
import android.os.Bundle;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.ImageView;
import android.widget.TextView;

import androidx.recyclerview.widget.LinearLayoutManager;
import androidx.recyclerview.widget.RecyclerView;
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout;

import java.util.ArrayList;
import java.util.List;

import com.blink.shop.R;
import com.blink.shop.catalog.ProductDetailActivity;
import com.blink.shop.model.Cart;
import com.blink.shop.model.Money;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.StateView;

/**
 * 购物车：勾选、改数量、删除；金额和是否可买都以服务端返回为准（每次修改返回整个购物车）。
 * 同一时间只发一个修改请求，进行中时忽略新的点击，避免连点产生并发修改。
 */
public final class CartActivity extends BaseActivity {

    private StateView state;
    private SwipeRefreshLayout refresh;
    private RecyclerView list;
    private TextView selectAll;
    private TextView payView;
    private TextView discountView;
    private Button checkoutButton;
    private View bottomBar;
    private final Adapter adapter = new Adapter();
    private Cart cart;
    private boolean mutating;

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
        setContentView(R.layout.activity_cart);
        state = new StateView(findViewById(android.R.id.content));
        refresh = findViewById(R.id.refresh);
        list = findViewById(R.id.cart_list);
        selectAll = findViewById(R.id.select_all);
        payView = findViewById(R.id.cart_pay);
        discountView = findViewById(R.id.cart_discount);
        checkoutButton = findViewById(R.id.checkout_button);
        bottomBar = findViewById(R.id.bottom_bar);
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        list.setLayoutManager(new LinearLayoutManager(this));
        list.setAdapter(adapter);
        refresh.setColorSchemeColors(getColor(R.color.brand));
        refresh.setOnRefreshListener(() -> load(false));
        selectAll.setOnClickListener(v -> toggleAll());
        checkoutButton.setOnClickListener(v -> goCheckout());
        state.loading("正在加载购物车…");
        bottomBar.setVisibility(View.GONE);
    }

    @Override
    protected void onResume() {
        super.onResume();
        // 每次回到购物车都重新拉取：别处加购、下单或库存变化后数据可能已变
        if (!isFinishing()) {
            load(cart == null);
        }
    }

    private void load(boolean showLoading) {
        if (mutating) {
            refresh.setRefreshing(false);
            return;
        }
        if (showLoading) {
            state.loading("正在加载购物车…");
        }
        call(() -> app.api().cart(), new Async.Callback<Cart>() {
            @Override
            public void onSuccess(Cart value) {
                refresh.setRefreshing(false);
                render(value);
            }

            @Override
            public void onError(ApiException e) {
                refresh.setRefreshing(false);
                if (cart == null) {
                    state.error("购物车加载失败", e, () -> load(true));
                } else {
                    toast(e.getMessage());
                }
            }
        });
    }

    private void render(Cart c) {
        cart = c;
        ((TextView) findViewById(R.id.top_title)).setText(c.itemCount > 0 ? "购物车（" + c.itemCount + "）" : "购物车");
        if (c.items.isEmpty()) {
            state.empty("购物车是空的", "去首页挑挑商品吧");
            bottomBar.setVisibility(View.GONE);
            adapter.set(c.items);
            return;
        }
        state.hide();
        bottomBar.setVisibility(View.VISIBLE);
        adapter.set(c.items);
        boolean all = true;
        boolean anySelectable = false;
        for (Cart.Item it : c.items) {
            if (it.available) {
                anySelectable = true;
                all &= it.selected;
            }
        }
        all &= anySelectable;
        selectAll.setText(all ? "☑ 全选" : "☐ 全选");
        selectAll.setContentDescription(all ? "全选，已选中" : "全选，未选中");
        selectAll.setEnabled(anySelectable);
        payView.setText("合计 " + Money.format(c.payAmount));
        boolean discounted = Money.greater(c.discountAmount, "0");
        discountView.setText(discounted ? "已优惠 " + Money.format(c.discountAmount) : "");
        discountView.setVisibility(discounted ? View.VISIBLE : View.GONE);
        int n = c.checkoutCount();
        checkoutButton.setText(n > 0 ? "去结算（" + n + "）" : "去结算");
        checkoutButton.setEnabled(n > 0);
    }

    // ---------- 修改 ----------

    /** 执行一次修改；进行中时返回 false（忽略这次点击）。 */
    private boolean mutate(Async.Task<Cart> task) {
        if (mutating) {
            return false;
        }
        mutating = true;
        list.setAlpha(0.6f);
        call(task, new Async.Callback<Cart>() {
            @Override
            public void onSuccess(Cart value) {
                mutating = false;
                list.setAlpha(1f);
                render(value);
            }

            @Override
            public void onError(ApiException e) {
                mutating = false;
                list.setAlpha(1f);
                toast(e.getMessage());
                // 被拒绝的修改服务端不改购物车；重新拉一次，显示最新库存和状态
                load(false);
            }
        });
        return true;
    }

    private void setQuantity(Cart.Item it, int quantity) {
        mutate(() -> app.api().updateCartItem(it.cartItemId, quantity, null));
    }

    private void setSelected(Cart.Item it, boolean selected) {
        if (!mutate(() -> app.api().updateCartItem(it.cartItemId, null, selected))) {
            adapter.notifyDataSetChanged(); // 恢复复选框到真实状态
        }
    }

    private void confirmDelete(Cart.Item it) {
        new AlertDialog.Builder(this).setMessage("从购物车删除“" + it.productName + "”？")
                .setNegativeButton("取消", null)
                .setPositiveButton("删除", (d, w) -> mutate(() -> app.api().deleteCartItem(it.cartItemId)))
                .show();
    }

    /** 全选/全不选：只改可购买且状态不同的项，逐个提交，最后返回最新购物车。 */
    private void toggleAll() {
        if (cart == null) {
            return;
        }
        boolean target = false;
        for (Cart.Item it : cart.items) {
            if (it.available && !it.selected) {
                target = true;
                break;
            }
        }
        List<String> ids = new ArrayList<>();
        for (Cart.Item it : cart.items) {
            if (it.selected != target && (it.available || !target)) {
                ids.add(it.cartItemId);
            }
        }
        if (ids.isEmpty()) {
            return;
        }
        boolean select = target;
        mutate(() -> {
            Cart last = null;
            for (String id : ids) {
                last = app.api().updateCartItem(id, null, select);
            }
            return last;
        });
    }

    private void goCheckout() {
        if (cart == null || cart.checkoutCount() == 0) {
            toast("请先选择要结算的商品");
            return;
        }
        startActivity(new Intent(this, CheckoutActivity.class));
    }

    // ---------- 列表 ----------

    private final class Adapter extends RecyclerView.Adapter<Holder> {
        private final List<Cart.Item> items = new ArrayList<>();

        void set(List<Cart.Item> list) {
            items.clear();
            items.addAll(list);
            notifyDataSetChanged();
        }

        @Override
        public Holder onCreateViewHolder(ViewGroup parent, int viewType) {
            return new Holder(LayoutInflater.from(parent.getContext()).inflate(R.layout.item_cart, parent, false));
        }

        @Override
        public void onBindViewHolder(Holder h, int position) {
            h.bind(items.get(position));
        }

        @Override
        public int getItemCount() {
            return items.size();
        }
    }

    private final class Holder extends RecyclerView.ViewHolder {
        final TextView merchant;
        final CheckBox check;
        final ImageView image;
        final TextView name;
        final TextView sku;
        final TextView unavailable;
        final TextView price;
        final TextView delete;
        final TextView minus;
        final TextView quantity;
        final TextView plus;

        Holder(View v) {
            super(v);
            merchant = v.findViewById(R.id.cart_merchant);
            check = v.findViewById(R.id.cart_check);
            image = v.findViewById(R.id.cart_image);
            name = v.findViewById(R.id.cart_name);
            sku = v.findViewById(R.id.cart_sku);
            unavailable = v.findViewById(R.id.cart_unavailable);
            price = v.findViewById(R.id.cart_price);
            delete = v.findViewById(R.id.cart_delete);
            minus = v.findViewById(R.id.cart_minus);
            quantity = v.findViewById(R.id.cart_quantity);
            plus = v.findViewById(R.id.cart_plus);
        }

        void bind(Cart.Item it) {
            merchant.setText(it.merchantName);
            name.setText(it.productName);
            sku.setText(it.skuName + " · 单价 " + Money.format(it.unitPrice));
            // 不可购买的原因（下架、售罄、数量超过库存等）由服务端给出
            String reason = it.available ? "" : (it.unavailableReason.isEmpty() ? "暂时不能购买" : it.unavailableReason);
            unavailable.setText(reason);
            unavailable.setVisibility(reason.isEmpty() ? View.GONE : View.VISIBLE);
            // 有优惠时显示服务端分摊后的金额
            price.setText(Money.greater(it.discountAmount, "0")
                    ? "优惠后 " + Money.format(it.payAmount) + "（原价 " + Money.format(it.amount) + "）"
                    : Money.format(it.amount));
            check.setOnCheckedChangeListener(null);
            check.setChecked(it.selected);
            check.setEnabled(it.available || it.selected);
            check.setContentDescription((it.selected ? "取消选择 " : "选择 ") + it.productName);
            check.setOnCheckedChangeListener((b, checked) -> setSelected(it, checked));
            quantity.setText(String.valueOf(it.quantity));
            quantity.setContentDescription("数量 " + it.quantity);
            minus.setEnabled(it.quantity > 1);
            minus.setAlpha(it.quantity > 1 ? 1f : 0.35f);
            minus.setContentDescription("减少 " + it.productName + " 的数量");
            minus.setOnClickListener(v -> setQuantity(it, it.quantity - 1));
            boolean canPlus = it.available && it.quantity < it.maxQuantity();
            plus.setEnabled(canPlus);
            plus.setAlpha(canPlus ? 1f : 0.35f);
            plus.setContentDescription("增加 " + it.productName + " 的数量");
            plus.setOnClickListener(v -> setQuantity(it, it.quantity + 1));
            delete.setContentDescription("删除 " + it.productName);
            delete.setOnClickListener(v -> confirmDelete(it));
            image.setContentDescription(null);
            app.images().load(image, it.imageUrl, dp(72));
            itemView.setOnClickListener(v -> startActivity(ProductDetailActivity.intent(CartActivity.this, it.productId, it.productName)));
        }
    }
}
