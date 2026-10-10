package com.blink.shop.settings;

import android.content.Intent;
import android.os.Bundle;
import android.view.View;
import android.view.ViewGroup;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.blink.shop.BuildConfig;
import com.blink.shop.R;
import com.blink.shop.data.ApiBaseStore;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Rows;
import com.blink.shop.voice.VoicePrefs;

/** 设置：帮助、接口设置（仅调试版）、版本信息。不需要登录。 */
public final class SettingsActivity extends BaseActivity {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_simple_page);
        ((TextView) findViewById(R.id.top_title)).setText("设置与帮助");
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
    }

    @Override
    protected void onResume() {
        super.onResume();
        render();
    }

    private void render() {
        LinearLayout content = findViewById(R.id.page_content);
        content.removeAllViews();
        LinearLayout card = card(content);
        card.addView(row("帮助", "购物、支付、优惠券和账户的常见问题", v -> startActivity(new Intent(this, HelpActivity.class))));
        boolean voiceOn = VoicePrefs.enabled(this);
        card.addView(row("语音输入与朗读", voiceOn ? "已开启：聊天里显示麦克风和“朗读”按钮（服务端开通时）" : "已关闭：聊天里不显示语音按钮", v -> {
            VoicePrefs.setEnabled(this, !VoicePrefs.enabled(this));
            render();
        }));
        card.addView(row("语音隐私说明", "录音什么时候开始、发给谁、会不会保存", v -> new android.app.AlertDialog.Builder(this)
                .setTitle("语音隐私说明").setMessage(VoicePrefs.PRIVACY).setPositiveButton("知道了", null).show()));
        if (ApiBaseStore.editable()) {
            card.addView(row(getString(R.string.api_settings), app.apiBase().get(),
                    v -> startActivity(new Intent(this, ApiSettingsActivity.class))));
        }
        LinearLayout about = card(content);
        about.addView(Rows.title(this, "关于 Blink Shop"));
        about.addView(Rows.pair(this, "版本", BuildConfig.VERSION_NAME + (BuildConfig.DEBUG ? "（调试版）" : ""), R.color.text_primary, false));
        about.addView(Rows.text(this, "演示用的导购商城，支付为模拟支付，不会产生真实扣款。", R.color.text_secondary, 13));
    }

    private LinearLayout card(LinearLayout parent) {
        LinearLayout c = new LinearLayout(this);
        c.setOrientation(LinearLayout.VERTICAL);
        c.setBackgroundResource(R.drawable.bg_card);
        c.setPadding(dp(16), dp(8), dp(16), dp(8));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dp(12);
        parent.addView(c, lp);
        return c;
    }

    private View row(String title, String subtitle, View.OnClickListener onClick) {
        LinearLayout r = new LinearLayout(this);
        r.setOrientation(LinearLayout.VERTICAL);
        r.setMinimumHeight(dp(56));
        r.setPadding(0, dp(8), 0, dp(8));
        r.setBackgroundResource(android.R.drawable.list_selector_background);
        r.addView(Rows.text(this, title, R.color.text_primary, 16));
        r.addView(Rows.text(this, subtitle, R.color.text_secondary, 13));
        r.setOnClickListener(onClick);
        r.setContentDescription(title + "，" + subtitle);
        return r;
    }
}
