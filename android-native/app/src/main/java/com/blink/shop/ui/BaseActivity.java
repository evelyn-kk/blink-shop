package com.blink.shop.ui;

import android.app.Activity;
import android.content.Intent;
import android.os.Bundle;
import android.widget.Toast;

import java.util.ArrayList;
import java.util.List;

import com.blink.shop.ShopApp;
import com.blink.shop.account.LoginActivity;
import com.blink.shop.data.SessionManager;
import com.blink.shop.model.Session;

/**
 * 页面基类：跟踪后台调用（销毁时取消回调），监听会话变化。
 * 需要登录的页面（requiresLogin）在未登录或登录失效时跳到登录页并关闭自己；
 * 子类在 super.onCreate 之后先检查 isFinishing()。
 */
public abstract class BaseActivity extends Activity implements SessionManager.Listener {

    protected ShopApp app;
    private final List<Async.Handle> calls = new ArrayList<>();
    private Toast toast;

    protected boolean requiresLogin() {
        return false;
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        app = ShopApp.of(this);
        if (requiresLogin() && !app.sessions().isLoggedIn()) {
            redirectToLogin();
        }
    }

    @Override
    protected void onStart() {
        super.onStart();
        app.sessions().addListener(this);
        // 不可见期间（如在登录页）错过的会话变化，回到前台时补上
        if (!isFinishing()) {
            onSessionUpdated(app.sessions().current());
        }
    }

    @Override
    protected void onResume() {
        super.onResume();
        showExpiredNotice();
        if (requiresLogin() && !app.sessions().isLoggedIn() && !isFinishing()) {
            redirectToLogin();
        }
    }

    @Override
    protected void onStop() {
        app.sessions().removeListener(this);
        super.onStop();
    }

    @Override
    protected void onDestroy() {
        for (Async.Handle h : calls) {
            h.cancel();
        }
        calls.clear();
        super.onDestroy();
    }

    @Override
    public void onSessionChanged(Session session, boolean expired) {
        if (expired) {
            showExpiredNotice();
        }
        if (session == null && requiresLogin() && !isFinishing()) {
            redirectToLogin();
            return;
        }
        onSessionUpdated(session);
    }

    /** 会话或账户资料变化（登录、刷新资料、退出）。 */
    protected void onSessionUpdated(Session session) {
    }

    protected <T> Async.Handle call(Async.Task<T> task, Async.Callback<T> callback) {
        Async.Handle h = Async.run(task, callback);
        calls.add(h);
        return h;
    }

    public void toast(String message) {
        if (toast != null) {
            toast.cancel();
        }
        toast = Toast.makeText(this, message, Toast.LENGTH_SHORT);
        toast.show();
    }

    private void showExpiredNotice() {
        if (app.consumeExpiredNotice()) {
            toast("登录已失效，请重新登录");
        }
    }

    private void redirectToLogin() {
        startActivity(new Intent(this, LoginActivity.class));
        finish();
    }

    protected int dp(float v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}
