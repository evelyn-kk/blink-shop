package com.blink.shop.catalog;

import android.view.View;
import android.widget.TextView;

import com.blink.shop.model.Product;

/** 库存状态：文字为主，颜色只是辅助。 */
public final class StockLabels {

    private StockLabels() {
    }

    public static void apply(TextView view, String status) {
        String label = Product.stockLabel(status);
        view.setText(label);
        view.setVisibility(label.isEmpty() ? View.GONE : View.VISIBLE);
        int color;
        switch (status) {
            case "low_stock":
                color = 0xFF8A4B00;
                break;
            case "out_of_stock":
                color = 0xFFC62828;
                break;
            default:
                color = 0xFF2E7D32;
        }
        view.setTextColor(color);
    }
}
