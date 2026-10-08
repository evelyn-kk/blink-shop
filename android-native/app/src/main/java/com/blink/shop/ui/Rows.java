package com.blink.shop.ui;

import android.content.Context;
import android.graphics.Typeface;
import android.os.Build;
import android.view.Gravity;
import android.view.ViewGroup;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.blink.shop.R;

/** 卡片里常用的几种行：小标题、“名称……值”行、正文。 */
public final class Rows {

    private Rows() {
    }

    private static int dp(Context c, float v) {
        return Math.round(v * c.getResources().getDisplayMetrics().density);
    }

    public static TextView title(Context c, String text) {
        TextView t = new TextView(c);
        t.setText(text);
        t.setTextSize(16);
        t.setTextColor(c.getColor(R.color.text_primary));
        t.setTypeface(null, Typeface.BOLD);
        t.setPadding(0, 0, 0, dp(c, 6));
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            t.setAccessibilityHeading(true);
        }
        return t;
    }

    public static TextView text(Context c, String text, int colorRes, float sp) {
        TextView t = new TextView(c);
        t.setText(text);
        t.setTextSize(sp);
        t.setTextColor(c.getColor(colorRes));
        t.setPadding(0, dp(c, 3), 0, dp(c, 3));
        return t;
    }

    /** 左边名称、右边值；整行作为一个读屏单元。 */
    public static LinearLayout pair(Context c, String label, String value, int valueColorRes, boolean bold) {
        LinearLayout row = new LinearLayout(c);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);
        row.setMinimumHeight(dp(c, 32));
        TextView l = text(c, label, R.color.text_secondary, 14);
        row.addView(l, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        TextView v = text(c, value, valueColorRes, bold ? 16 : 14);
        v.setGravity(Gravity.END);
        if (bold) {
            v.setTypeface(null, Typeface.BOLD);
        }
        LinearLayout.LayoutParams vp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        vp.setMarginStart(dp(c, 12));
        row.addView(v, vp);
        row.setContentDescription(label + "：" + value);
        row.setFocusable(true);
        return row;
    }
}
