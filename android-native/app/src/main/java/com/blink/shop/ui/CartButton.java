package com.blink.shop.ui;

import android.content.Intent;
import android.widget.TextView;

import com.blink.shop.cart.CartActivity;
import com.blink.shop.model.Cart;
import com.blink.shop.net.ApiException;

/** 顶栏的“购物车”按钮：显示件数（超过 99 显示 99+），点开购物车。未登录时不显示件数，点击会去登录。 */
public final class CartButton {

    private final BaseActivity activity;
    private final TextView view;
    private Async.Handle pending;

    public CartButton(BaseActivity activity, TextView view) {
        this.activity = activity;
        this.view = view;
        view.setOnClickListener(v -> activity.startActivity(new Intent(activity, CartActivity.class)));
        show(0);
    }

    /** 回到页面时刷新件数（失败不提示，保留上次的数）。 */
    public void refresh() {
        if (!activity.app.sessions().isLoggedIn()) {
            show(0);
            return;
        }
        if (pending != null) {
            pending.cancel();
        }
        pending = activity.call(() -> activity.app.api().cart(), new Async.Callback<Cart>() {
            @Override
            public void onSuccess(Cart cart) {
                show(cart.totalQuantity());
            }

            @Override
            public void onError(ApiException e) {
            }
        });
    }

    /** 已拿到最新购物车时直接更新（如加购返回整个购物车）。 */
    public void show(int count) {
        if (count <= 0) {
            view.setText("购物车");
            view.setContentDescription("购物车");
        } else {
            view.setText("购物车 " + (count > 99 ? "99+" : String.valueOf(count)));
            view.setContentDescription("购物车，" + count + " 件商品");
        }
    }
}
