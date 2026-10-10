package com.blink.shop.voice;

import java.util.regex.Pattern;

/**
 * 朗读前的文本清洗：导购回答是 Markdown，直接朗读会念出符号和链接。去掉代码块、图片、链接地址、标题 / 列表 / 引用标记、
 * 强调符号、表格竖线、HTML 标签、商品 ID 和网址，合并空白；超长时在句末截断（不超过 maxChars）。纯 Java。
 */
public final class TtsText {

    private static final Pattern FENCE = Pattern.compile("(?s)```.*?(```|$)");
    private static final Pattern IMAGE = Pattern.compile("!\\[([^\\]]*)]\\([^)]*\\)");
    private static final Pattern LINK = Pattern.compile("\\[([^\\]]+)]\\([^)]*\\)");
    private static final Pattern URL = Pattern.compile("https?://\\S+");
    private static final Pattern HTML = Pattern.compile("<[^>\\n]{1,100}>");
    private static final Pattern PRODUCT_ID = Pattern.compile("\\b(?:p|sku|o|file|run|s)_[A-Za-z0-9_]{4,}\\b");
    private static final Pattern LINE_MARK = Pattern.compile("(?m)^\\s{0,3}(?:#{1,6}\\s+|>\\s?|[-*+]\\s+|\\d+[.)]\\s+)");
    private static final Pattern TABLE_RULE = Pattern.compile("(?m)^\\s*\\|?\\s*:?-{2,}.*$");
    private static final Pattern EMPHASIS = Pattern.compile("(\\*\\*|__|\\*|_|~~|`)");
    private static final Pattern SPACES = Pattern.compile("[ \\t\\x0B\\f\\r]+");
    private static final Pattern BLANK_LINES = Pattern.compile("\\n{2,}");

    private TtsText() {
    }

    public static String clean(String markdown, int maxChars) {
        if (markdown == null) {
            return "";
        }
        String s = markdown;
        s = FENCE.matcher(s).replaceAll("（代码略）");
        s = IMAGE.matcher(s).replaceAll("$1");
        s = LINK.matcher(s).replaceAll("$1");
        s = URL.matcher(s).replaceAll("");
        s = HTML.matcher(s).replaceAll("");
        s = PRODUCT_ID.matcher(s).replaceAll("");
        s = TABLE_RULE.matcher(s).replaceAll("");
        s = LINE_MARK.matcher(s).replaceAll("");
        s = s.replace('|', '，');
        s = EMPHASIS.matcher(s).replaceAll("");
        s = s.replace("（）", "").replace("()", "");
        s = SPACES.matcher(s).replaceAll(" ");
        s = BLANK_LINES.matcher(s).replaceAll("\n").trim();
        return truncate(s, maxChars);
    }

    /** 超过 maxChars 个字时，在 maxChars 以内最后一个句末标点处截断；找不到就直接截断。 */
    static String truncate(String s, int maxChars) {
        if (maxChars <= 0 || s.codePointCount(0, s.length()) <= maxChars) {
            return s;
        }
        int end = s.offsetByCodePoints(0, maxChars);
        String head = s.substring(0, end);
        int cut = -1;
        for (int i = head.length() - 1; i >= head.length() / 2; i--) {
            char c = head.charAt(i);
            if (c == '。' || c == '！' || c == '？' || c == '；' || c == '\n' || c == '.' || c == '!' || c == '?') {
                cut = i + 1;
                break;
            }
        }
        return (cut > 0 ? head.substring(0, cut) : head).trim();
    }
}
