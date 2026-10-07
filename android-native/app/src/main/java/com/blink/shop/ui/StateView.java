package com.blink.shop.ui;

import android.view.View;
import android.widget.Button;
import android.widget.ProgressBar;
import android.widget.TextView;

import com.blink.shop.R;
import com.blink.shop.net.ApiException;

/** 控制 view_state 布局：加载中、空数据、出错（带重试）。 */
public final class StateView {

    private final View root;
    private final ProgressBar progress;
    private final TextView title;
    private final TextView message;
    private final Button retry;

    public StateView(View root) {
        this.root = root.findViewById(R.id.state_view);
        progress = root.findViewById(R.id.state_progress);
        title = root.findViewById(R.id.state_title);
        message = root.findViewById(R.id.state_message);
        retry = root.findViewById(R.id.state_retry);
    }

    public void loading(String text) {
        show(true, text, "", null);
    }

    public void empty(String titleText, String hint) {
        show(false, titleText, hint, null);
    }

    /** 网络类错误提示检查网络；服务端错误直接显示服务端说明。 */
    public void error(String titleText, ApiException e, Runnable onRetry) {
        show(false, titleText, e.getMessage(), onRetry);
    }

    public void hide() {
        root.setVisibility(View.GONE);
    }

    public boolean isShowingError() {
        return root.getVisibility() == View.VISIBLE && retry.getVisibility() == View.VISIBLE;
    }

    private void show(boolean loading, String t, String m, Runnable onRetry) {
        root.setVisibility(View.VISIBLE);
        progress.setVisibility(loading ? View.VISIBLE : View.GONE);
        title.setText(t);
        title.setVisibility(t.isEmpty() ? View.GONE : View.VISIBLE);
        message.setText(m);
        message.setVisibility(m == null || m.isEmpty() ? View.GONE : View.VISIBLE);
        retry.setVisibility(onRetry == null ? View.GONE : View.VISIBLE);
        retry.setOnClickListener(onRetry == null ? null : v -> onRetry.run());
        // 读屏：状态变化时朗读标题
        root.announceForAccessibility(m == null || m.isEmpty() ? t : t + "，" + m);
    }
}
