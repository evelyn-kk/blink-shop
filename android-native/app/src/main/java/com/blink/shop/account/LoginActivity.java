package com.blink.shop.account;

import android.content.Intent;
import android.graphics.Typeface;
import android.os.Bundle;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.widget.Button;
import android.widget.EditText;
import android.widget.TextView;

import com.blink.shop.R;
import com.blink.shop.data.ApiBaseStore;
import com.blink.shop.model.Session;
import com.blink.shop.net.ApiException;
import com.blink.shop.settings.ApiSettingsActivity;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;

/**
 * 登录 / 注册。只检查必填，格式规则以服务端为准：服务端返回的字段错误标在对应输入框上。
 * 成功后保存会话并返回上一页。
 */
public final class LoginActivity extends BaseActivity {

    private static final String STATE_REGISTER = "register";

    private boolean registerMode;
    private boolean submitting;
    private TextView tabLogin;
    private TextView tabRegister;
    private EditText username;
    private EditText password;
    private EditText displayName;
    private View displayNameGroup;
    private View usernameRule;
    private View passwordRule;
    private TextView formError;
    private Button submit;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_login);
        tabLogin = findViewById(R.id.tab_login);
        tabRegister = findViewById(R.id.tab_register);
        username = findViewById(R.id.username);
        password = findViewById(R.id.password);
        displayName = findViewById(R.id.display_name);
        displayNameGroup = findViewById(R.id.display_name_group);
        usernameRule = findViewById(R.id.username_rule);
        passwordRule = findViewById(R.id.password_rule);
        formError = findViewById(R.id.form_error);
        submit = findViewById(R.id.submit);

        registerMode = savedInstanceState != null && savedInstanceState.getBoolean(STATE_REGISTER);
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        tabLogin.setOnClickListener(v -> setMode(false));
        tabRegister.setOnClickListener(v -> setMode(true));
        submit.setOnClickListener(v -> submit());
        password.setOnEditorActionListener((v, actionId, event) -> {
            if (actionId == EditorInfo.IME_ACTION_DONE && !registerMode) {
                submit();
                return true;
            }
            return false;
        });
        displayName.setOnEditorActionListener((v, actionId, event) -> {
            if (actionId == EditorInfo.IME_ACTION_DONE) {
                submit();
                return true;
            }
            return false;
        });

        TextView apiSettings = findViewById(R.id.api_settings);
        if (ApiBaseStore.editable()) {
            apiSettings.setOnClickListener(v -> startActivity(new Intent(this, ApiSettingsActivity.class)));
        } else {
            apiSettings.setVisibility(View.GONE);
        }
        setMode(registerMode);
    }

    @Override
    protected void onResume() {
        super.onResume();
        TextView apiSettings = findViewById(R.id.api_settings);
        if (ApiBaseStore.editable()) {
            apiSettings.setText("服务地址：" + app.apiBase().get() + "（点击修改）");
        }
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putBoolean(STATE_REGISTER, registerMode);
    }

    private void setMode(boolean register) {
        registerMode = register;
        ((TextView) findViewById(R.id.top_title)).setText(register ? R.string.register : R.string.login);
        styleTab(tabLogin, !register);
        styleTab(tabRegister, register);
        displayNameGroup.setVisibility(register ? View.VISIBLE : View.GONE);
        usernameRule.setVisibility(register ? View.VISIBLE : View.GONE);
        passwordRule.setVisibility(register ? View.VISIBLE : View.GONE);
        password.setImeOptions(register ? EditorInfo.IME_ACTION_NEXT : EditorInfo.IME_ACTION_DONE);
        submit.setText(register ? "注册并登录" : "登录");
        clearErrors();
    }

    private void styleTab(TextView tab, boolean selected) {
        tab.setTextColor(getColor(selected ? R.color.brand : R.color.text_secondary));
        tab.setTypeface(null, selected ? Typeface.BOLD : Typeface.NORMAL);
        tab.setBackgroundResource(selected ? R.drawable.bg_chip_selected : 0);
        tab.setSelected(selected);
        tab.setContentDescription(tab.getText() + (selected ? "，已选中" : ""));
    }

    private void submit() {
        if (submitting) {
            return;
        }
        clearErrors();
        String u = username.getText().toString().trim();
        String p = password.getText().toString();
        if (u.isEmpty()) {
            showFieldError(username, "请输入账号");
            return;
        }
        if (p.isEmpty()) {
            showFieldError(password, "请输入密码");
            return;
        }
        String name = displayName.getText().toString();
        boolean register = registerMode;
        setSubmitting(true);
        call(() -> register ? app.api().register(u, p, name) : app.api().login(u, p), new Async.Callback<Session>() {
            @Override
            public void onSuccess(Session session) {
                setSubmitting(false);
                app.sessions().signIn(session);
                toast(register ? "注册成功，已登录" : "登录成功");
                setResult(RESULT_OK);
                finish();
            }

            @Override
            public void onError(ApiException e) {
                setSubmitting(false);
                EditText target = fieldFor(e.field());
                if (target != null) {
                    showFieldError(target, e.getMessage());
                } else {
                    formError.setText(e.getMessage());
                    formError.setVisibility(View.VISIBLE);
                }
            }
        });
    }

    private EditText fieldFor(String field) {
        switch (field) {
            case "username":
                return username;
            case "password":
                return password;
            case "display_name":
                return displayName;
            default:
                return null;
        }
    }

    private void showFieldError(EditText field, String message) {
        field.setError(message);
        field.requestFocus();
        formError.setText(message);
        formError.setVisibility(View.VISIBLE);
    }

    private void clearErrors() {
        username.setError(null);
        password.setError(null);
        displayName.setError(null);
        formError.setVisibility(View.GONE);
    }

    private void setSubmitting(boolean on) {
        submitting = on;
        submit.setEnabled(!on);
        if (on) {
            submit.setText(registerMode ? "注册中…" : "登录中…");
        } else {
            submit.setText(registerMode ? "注册并登录" : "登录");
        }
    }
}
