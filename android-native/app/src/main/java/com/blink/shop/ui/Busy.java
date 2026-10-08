package com.blink.shop.ui;

import android.view.View;
import android.widget.TextView;

import com.blink.shop.R;

/** 控制 view_busy 遮罩：提交时显示，挡住整页点击（防止重复提交）。 */
public final class Busy {

    private final View root;
    private final TextView text;

    public Busy(View root) {
        this.root = root.findViewById(R.id.busy_view);
        text = root.findViewById(R.id.busy_text);
    }

    public void show(String message) {
        text.setText(message);
        root.setVisibility(View.VISIBLE);
        root.announceForAccessibility(message);
    }

    public void hide() {
        root.setVisibility(View.GONE);
    }

    public boolean isShowing() {
        return root.getVisibility() == View.VISIBLE;
    }
}
