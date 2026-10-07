package com.blink.shop.settings;

import android.os.Bundle;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.TextView;

import com.blink.shop.BuildConfig;
import com.blink.shop.R;
import com.blink.shop.data.ApiBaseStore;
import com.blink.shop.data.ShopApi;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.AuthInterceptor;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;

/** 调试版修改服务地址；Release 构建直接关闭本页（入口也不显示）。 */
public final class ApiSettingsActivity extends BaseActivity {

    private EditText input;
    private TextView result;
    private Button testButton;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ApiBaseStore.editable()) {
            finish();
            return;
        }
        setContentView(R.layout.activity_api_settings);
        input = findViewById(R.id.api_base_input);
        result = findViewById(R.id.api_base_result);
        testButton = findViewById(R.id.api_test);
        if (savedInstanceState == null) {
            input.setText(app.apiBase().get());
        }
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        testButton.setOnClickListener(v -> testConnection());
        findViewById(R.id.api_reset).setOnClickListener(v -> {
            input.setText(BuildConfig.DEFAULT_API_BASE);
            showResult("已填入默认地址，点“保存”生效", false);
        });
        findViewById(R.id.api_save).setOnClickListener(v -> save());
    }

    private String normalized() {
        String s = input.getText().toString().trim();
        while (s.endsWith("/")) {
            s = s.substring(0, s.length() - 1);
        }
        return s;
    }

    private boolean validate() {
        String err = ApiConfig.validate(input.getText().toString());
        if (err != null) {
            input.setError(err);
            showResult(err, true);
            return false;
        }
        input.setError(null);
        return true;
    }

    private void testConnection() {
        if (!validate()) {
            return;
        }
        String base = normalized();
        testButton.setEnabled(false);
        showResult("正在连接…", false);
        // 用临时客户端测试，不影响当前会话
        ShopApi probe = new ShopApi(new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig(base), new NoSession(),
                app.connectivity()));
        call(() -> {
            probe.health();
            return null;
        }, new Async.Callback<Object>() {
            @Override
            public void onSuccess(Object value) {
                testButton.setEnabled(true);
                showResult("连接成功", false);
            }

            @Override
            public void onError(ApiException e) {
                testButton.setEnabled(true);
                showResult("连接失败：" + e.getMessage(), true);
            }
        });
    }

    private void save() {
        if (!validate()) {
            return;
        }
        String base = normalized();
        if (base.equals(app.apiBase().get())) {
            toast("地址没有变化");
            finish();
            return;
        }
        boolean wasLoggedIn = app.sessions().isLoggedIn();
        app.switchApiBase(base.equals(BuildConfig.DEFAULT_API_BASE) ? null : base);
        toast(wasLoggedIn ? "已切换服务地址，请重新登录" : "已切换服务地址");
        finish();
    }

    private void showResult(String text, boolean error) {
        result.setVisibility(View.VISIBLE);
        result.setText(text);
        result.setTextColor(getColor(error ? R.color.danger : R.color.text_secondary));
    }

    private static final class NoSession implements AuthInterceptor.SessionSource {
        @Override
        public String token() {
            return null;
        }

        @Override
        public void onUnauthorized(String rejectedToken) {
        }
    }
}
