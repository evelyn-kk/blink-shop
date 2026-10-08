package com.blink.shop.model;

import java.math.BigDecimal;
import java.text.NumberFormat;
import java.util.Locale;

/** 金额显示。服务端金额是两位小数的十进制字符串，这里只做格式化，不参与计算。 */
public final class Money {

    private Money() {
    }

    /** "1299.00" -> "¥1,299.00"；无法解析时原样返回。 */
    public static String format(String amount) {
        if (amount == null || amount.trim().isEmpty()) {
            return "";
        }
        try {
            BigDecimal v = new BigDecimal(amount.trim());
            NumberFormat f = NumberFormat.getCurrencyInstance(Locale.CHINA);
            f.setMinimumFractionDigits(2);
            f.setMaximumFractionDigits(2);
            return f.format(v);
        } catch (NumberFormatException e) {
            return amount;
        }
    }

    /** 几个服务端金额相加（如多个订单的待付合计），返回两位小数字符串；无法解析的忽略。 */
    public static String sum(java.util.List<String> amounts) {
        BigDecimal total = BigDecimal.ZERO;
        for (String a : amounts) {
            try {
                total = total.add(new BigDecimal(a.trim()));
            } catch (RuntimeException ignored) {
                // 忽略
            }
        }
        return total.setScale(2, java.math.RoundingMode.HALF_UP).toPlainString();
    }

    /** a 比 b 大（用于判断是否显示划线价）；任一无法解析时返回 false。 */
    public static boolean greater(String a, String b) {
        try {
            return new BigDecimal(a.trim()).compareTo(new BigDecimal(b.trim())) > 0;
        } catch (RuntimeException e) {
            return false;
        }
    }
}
