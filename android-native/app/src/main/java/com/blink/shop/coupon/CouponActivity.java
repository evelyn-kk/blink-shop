package com.blink.shop.coupon;

import android.graphics.Typeface;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.swiperefreshlayout.widget.SwipeRefreshLayout;

import java.util.List;
import java.util.TimeZone;

import com.blink.shop.R;
import com.blink.shop.model.Coupon;
import com.blink.shop.model.Money;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Times;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Chips;
import com.blink.shop.ui.Rows;
import com.blink.shop.ui.StateView;

/** 优惠券：领券中心（可领取的券）和我的券（按未使用 / 已使用 / 已过期筛选）。能否领取以服务端为准。 */
public final class CouponActivity extends BaseActivity {

    private static final String STATE_TAB = "tab";
    /** 第一个是领券中心，其余是我的券的状态。 */
    private static final String[][] TABS = {
            {"center", "领券中心"}, {Coupon.Mine.UNUSED, "未使用"}, {Coupon.Mine.USED, "已使用"}, {Coupon.Mine.EXPIRED, "已过期"},
    };

    private String tab = "center";
    private StateView state;
    private SwipeRefreshLayout refresh;
    private int generation;
    private String claiming;

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
        setContentView(R.layout.activity_coupons);
        if (savedInstanceState != null) {
            tab = savedInstanceState.getString(STATE_TAB, "center");
        }
        state = new StateView(findViewById(android.R.id.content));
        refresh = findViewById(R.id.refresh);
        refresh.setColorSchemeColors(getColor(R.color.brand));
        refresh.setOnRefreshListener(() -> load(false));
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        renderTabs();
        load(true);
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putString(STATE_TAB, tab);
    }

    private void renderTabs() {
        LinearLayout tabs = findViewById(R.id.coupon_tabs);
        tabs.removeAllViews();
        for (String[] t : TABS) {
            tabs.addView(Chips.make(this, t[1], t[0].equals(tab), v -> {
                if (!t[0].equals(tab)) {
                    tab = t[0];
                    renderTabs();
                    load(true);
                }
            }));
        }
    }

    private void load(boolean showLoading) {
        int gen = ++generation;
        if (showLoading) {
            ((LinearLayout) findViewById(R.id.coupon_list)).removeAllViews();
            state.loading("正在加载优惠券…");
        }
        if ("center".equals(tab)) {
            call(() -> app.api().availableCoupons(), new Async.Callback<PageResult<Coupon>>() {
                @Override
                public void onSuccess(PageResult<Coupon> page) {
                    if (gen == generation) {
                        refresh.setRefreshing(false);
                        renderCenter(page.items);
                    }
                }

                @Override
                public void onError(ApiException e) {
                    failed(gen, e);
                }
            });
        } else {
            String status = tab;
            call(() -> app.api().myCoupons(status), new Async.Callback<PageResult<Coupon.Mine>>() {
                @Override
                public void onSuccess(PageResult<Coupon.Mine> page) {
                    if (gen == generation) {
                        refresh.setRefreshing(false);
                        renderMine(page.items);
                    }
                }

                @Override
                public void onError(ApiException e) {
                    failed(gen, e);
                }
            });
        }
    }

    private void failed(int gen, ApiException e) {
        if (gen != generation) {
            return;
        }
        refresh.setRefreshing(false);
        ((LinearLayout) findViewById(R.id.coupon_list)).removeAllViews();
        state.error("优惠券加载失败", e, () -> load(true));
    }

    private void renderCenter(List<Coupon> coupons) {
        LinearLayout list = findViewById(R.id.coupon_list);
        list.removeAllViews();
        if (coupons.isEmpty()) {
            state.empty("暂时没有可以领取的券", "过段时间再来看看");
            return;
        }
        state.hide();
        for (Coupon c : coupons) {
            Button action = new Button(this, null, 0, R.style.SecondaryButton);
            action.setMinWidth(dp(80));
            if (c.canClaim) {
                action.setText(c.couponId.equals(claiming) ? "领取中…" : "领取");
                action.setEnabled(claiming == null);
                action.setContentDescription("领取 " + c.name);
                action.setOnClickListener(v -> claim(c));
            } else {
                action.setText(c.claimedByMe > 0 ? "已领取" : (c.soldOut() ? "已领完" : "不可领"));
                action.setEnabled(false);
                action.setAlpha(0.6f);
            }
            list.addView(couponCard(c, c.claimedByMe > 0 ? "已领 " + c.claimedByMe + " 张，每人限领 " + c.perUserLimit + " 张" : "", action));
        }
    }

    private void renderMine(List<Coupon.Mine> mine) {
        LinearLayout list = findViewById(R.id.coupon_list);
        list.removeAllViews();
        if (mine.isEmpty()) {
            state.empty("没有" + labelOf(tab) + "的券", Coupon.Mine.UNUSED.equals(tab) ? "去领券中心看看" : "");
            return;
        }
        state.hide();
        for (Coupon.Mine m : mine) {
            TextView status = new TextView(this);
            status.setText(m.statusLabel());
            status.setTextColor(getColor(Coupon.Mine.UNUSED.equals(m.status) ? R.color.brand : R.color.text_secondary));
            status.setTextSize(14);
            status.setGravity(Gravity.CENTER);
            status.setMinWidth(dp(64));
            String extra = Coupon.Mine.UNUSED.equals(m.status) ? "下单时自动使用最优的券，也可以在确认订单页自己选择" : "";
            list.addView(couponCard(m.coupon, extra, status));
        }
    }

    private static String labelOf(String t) {
        for (String[] x : TABS) {
            if (x[0].equals(t)) {
                return x[1];
            }
        }
        return "";
    }

    private View couponCard(Coupon c, String note, View action) {
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.HORIZONTAL);
        card.setGravity(Gravity.CENTER_VERTICAL);
        card.setBackgroundResource(R.drawable.bg_card);
        card.setPadding(dp(12), dp(12), dp(12), dp(12));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dp(12);
        card.setLayoutParams(lp);

        TextView amount = new TextView(this);
        amount.setText(Money.format(c.discountAmount));
        amount.setTextSize(22);
        amount.setTypeface(null, Typeface.BOLD);
        amount.setTextColor(getColor(R.color.price));
        amount.setMinWidth(dp(88));
        card.addView(amount);

        LinearLayout mid = new LinearLayout(this);
        mid.setOrientation(LinearLayout.VERTICAL);
        mid.setPadding(dp(8), 0, dp(8), 0);
        TextView name = Rows.text(this, c.name, R.color.text_primary, 15);
        name.setTypeface(null, Typeface.BOLD);
        mid.addView(name);
        mid.addView(Rows.text(this, c.scopeLabel() + " · " + c.description, R.color.text_secondary, 13));
        TimeZone zone = TimeZone.getDefault();
        mid.addView(Rows.text(this, "有效期 " + Times.formatLocal(c.startAt, zone) + " 至 " + Times.formatLocal(c.endAt, zone),
                R.color.text_hint, 12));
        if (!note.isEmpty()) {
            mid.addView(Rows.text(this, note, R.color.text_hint, 12));
        }
        card.addView(mid, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        card.addView(action, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, dp(44)));
        return card;
    }

    private void claim(Coupon c) {
        if (claiming != null) {
            return;
        }
        claiming = c.couponId;
        load(false);
        call(() -> app.api().claimCoupon(c.couponId), new Async.Callback<Coupon.Mine>() {
            @Override
            public void onSuccess(Coupon.Mine value) {
                claiming = null;
                toast("领取成功：" + c.name);
                load(false);
            }

            @Override
            public void onError(ApiException e) {
                claiming = null;
                toast(e.getMessage());
                load(false);
            }
        });
    }
}
