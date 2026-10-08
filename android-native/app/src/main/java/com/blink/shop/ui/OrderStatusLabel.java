package com.blink.shop.ui;

import android.graphics.drawable.GradientDrawable;
import android.widget.TextView;

import com.blink.shop.model.Order;

/** 订单状态标签：文字为主，底色只是辅助（色弱用户也能分辨）。 */
public final class OrderStatusLabel {

    private OrderStatusLabel() {
    }

    public static void apply(TextView v, String status) {
        int fg;
        int bg;
        switch (status) {
            case Order.PENDING_PAYMENT:
                fg = 0xFFB54708;
                bg = 0xFFFFF4E5;
                break;
            case Order.PAID:
                fg = 0xFF1D4ED8;
                bg = 0xFFE8EEFC;
                break;
            case Order.SHIPPED:
                fg = 0xFF6D28D9;
                bg = 0xFFF1EAFE;
                break;
            case Order.COMPLETED:
                fg = 0xFF2E7D32;
                bg = 0xFFE8F5E9;
                break;
            default:
                fg = 0xFF5B6270;
                bg = 0xFFEEF0F3;
        }
        String label = Order.statusLabel(status);
        v.setText(label);
        v.setTextColor(fg);
        GradientDrawable d = new GradientDrawable();
        d.setColor(bg);
        d.setCornerRadius(4 * v.getResources().getDisplayMetrics().density);
        v.setBackground(d);
        int h = Math.round(8 * v.getResources().getDisplayMetrics().density);
        int vpad = Math.round(2 * v.getResources().getDisplayMetrics().density);
        v.setPadding(h, vpad, h, vpad);
        v.setContentDescription("订单状态：" + label);
    }
}
