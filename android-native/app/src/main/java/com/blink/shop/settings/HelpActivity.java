package com.blink.shop.settings;

import android.os.Bundle;
import android.view.ViewGroup;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.blink.shop.R;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Rows;

/** 帮助：常见问题（静态内容）。 */
public final class HelpActivity extends BaseActivity {

    private static final String[][] SECTIONS = {
            {"怎么下单？", "在商品详情选择规格后加入购物车，到购物车勾选要买的商品，点“去结算”。确认页会显示服务端计算的优惠和实付金额，"
                    + "提交后按店铺拆成多个订单。"},
            {"支付有时间限制吗？", "下单后 30 分钟内需要完成支付，超时订单会自动取消，库存和优惠券会退回。演示环境的支付是模拟支付，不会真实扣款。"},
            {"网络断了，订单到底提交了没有？", "确认页提交时如果网络中断，可以直接再次提交：同一次结算不会重复下单，如果上次已经成功，会直接显示那次的订单。"
                    + "也可以在“我的 → 我的订单”里查看。"},
            {"优惠券怎么用？", "在“我的 → 优惠券 → 领券中心”领取。下单时默认自动使用最优惠的券，也可以在确认订单页选择不用券或自己挑选。"},
            {"可以取消订单吗？", "待支付的订单可以在订单详情里取消；已经支付的订单不能取消。"},
            {"怎么评价？", "商家发货后，在订单详情点“确认收货”，之后就可以评价每件商品。评价会公开显示在商品页，每件商品只能评价一次。"
                    + "没写完的评价会自动保存为草稿。"},
            {"账户被提示停用或风控？", "停用的账户只能查看和退出登录，风控中的账户只能浏览，请联系平台处理。"},
    };

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_simple_page);
        ((TextView) findViewById(R.id.top_title)).setText("帮助");
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        LinearLayout content = findViewById(R.id.page_content);
        for (String[] s : SECTIONS) {
            LinearLayout c = new LinearLayout(this);
            c.setOrientation(LinearLayout.VERTICAL);
            c.setBackgroundResource(R.drawable.bg_card);
            c.setPadding(dp(16), dp(14), dp(16), dp(14));
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
            lp.topMargin = dp(12);
            c.addView(Rows.title(this, s[0]));
            TextView body = Rows.text(this, s[1], R.color.text_primary, 14);
            body.setLineSpacing(dp(3), 1f);
            c.addView(body);
            content.addView(c, lp);
        }
    }
}
