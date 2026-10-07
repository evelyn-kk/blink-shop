package com.blink.shop.model;

import java.text.ParseException;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.TimeZone;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** RFC 3339 时间解析和本地化显示（minSdk 24 没有 java.time）。 */
public final class Times {

    private static final Pattern ISO = Pattern.compile(
            "(\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2})(\\.\\d+)?(Z|[+-]\\d{2}:\\d{2})");

    private Times() {
    }

    /** 解析如 2026-09-24T06:00:00.123Z / +08:00；格式不对返回 0。 */
    public static long parseIsoMillis(String s) {
        if (s == null) {
            return 0;
        }
        Matcher m = ISO.matcher(s.trim());
        if (!m.matches()) {
            return 0;
        }
        SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss", Locale.US);
        String zone = m.group(3);
        f.setTimeZone(TimeZone.getTimeZone("Z".equals(zone) ? "UTC" : "GMT" + zone));
        try {
            Date d = f.parse(m.group(1));
            long ms = d == null ? 0 : d.getTime();
            String frac = m.group(2);
            if (frac != null) {
                String digits = (frac.substring(1) + "000").substring(0, 3);
                ms += Long.parseLong(digits);
            }
            return ms;
        } catch (ParseException e) {
            return 0;
        }
    }

    /** 按设备时区显示到分钟，如 2026-09-24 14:00；无法解析时原样返回。 */
    public static String formatLocal(String iso, TimeZone zone) {
        long ms = parseIsoMillis(iso);
        if (ms == 0) {
            return iso == null ? "" : iso;
        }
        SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd HH:mm", Locale.getDefault());
        f.setTimeZone(zone);
        return f.format(new Date(ms));
    }
}
