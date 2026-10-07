package com.blink.shop.ui;

import android.content.Context;
import android.graphics.Typeface;
import android.view.Gravity;
import android.view.View;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.blink.shop.R;

/** 可选中的胶囊按钮（分类、规格）。选中状态同时用边框、字重和读屏文字表达。 */
public final class Chips {

    private Chips() {
    }

    public static TextView make(Context c, String text, boolean selected, View.OnClickListener onClick) {
        float d = c.getResources().getDisplayMetrics().density;
        TextView v = new TextView(c);
        v.setText(text);
        v.setTextSize(14);
        v.setGravity(Gravity.CENTER);
        v.setMinHeight(Math.round(40 * d));
        v.setMinWidth(Math.round(48 * d));
        int h = Math.round(14 * d);
        v.setPadding(h, 0, h, 0);
        v.setSelected(selected);
        v.setBackgroundResource(selected ? R.drawable.bg_chip_selected : R.drawable.bg_chip);
        v.setTextColor(c.getColor(selected ? R.color.brand : R.color.text_primary));
        v.setTypeface(null, selected ? Typeface.BOLD : Typeface.NORMAL);
        v.setContentDescription(selected ? text + "，已选中" : text);
        v.setOnClickListener(onClick);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT,
                Math.round(44 * d));
        lp.setMarginEnd(Math.round(8 * d));
        lp.topMargin = Math.round(2 * d);
        lp.bottomMargin = Math.round(2 * d);
        v.setLayoutParams(lp);
        return v;
    }
}
