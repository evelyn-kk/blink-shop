package com.blink.shop.account;

/** 账户页显示联系方式时打码（本人数据，仅防旁人窥屏）。 */
public final class Masking {

    private Masking() {
    }

    /** 13800001234 -> 138****1234；7 位以下原样显示。 */
    public static String phone(String s) {
        if (s == null) {
            return "";
        }
        String digits = s.trim();
        if (digits.length() < 7) {
            return digits;
        }
        int keepEnd = 4;
        int keepStart = 3;
        StringBuilder b = new StringBuilder(digits.substring(0, keepStart));
        for (int i = keepStart; i < digits.length() - keepEnd; i++) {
            b.append('*');
        }
        return b.append(digits.substring(digits.length() - keepEnd)).toString();
    }

    /** user@example.com -> u***@example.com；没有 @ 的原样显示。 */
    public static String email(String s) {
        if (s == null) {
            return "";
        }
        String e = s.trim();
        int at = e.indexOf('@');
        if (at < 1) {
            return e;
        }
        return e.charAt(0) + "***" + e.substring(at);
    }
}
