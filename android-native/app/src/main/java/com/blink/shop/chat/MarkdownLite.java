package com.blink.shop.chat;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * 回答正文用的 Markdown 子集解析：标题、无序/有序列表、引用、围栏代码块、段落；行内粗体、斜体、代码和链接。
 * 纯 Java、不抛异常：不认识或不完整的写法按原文显示（不会丢字），超长输入也只做线性扫描。
 * 解析结果是与 Android 无关的块列表，由 MarkdownView 转成 Spannable。
 */
public final class MarkdownLite {

    public enum Kind { PARAGRAPH, HEADING, BULLET, NUMBERED, QUOTE, CODE }

    /** 一个块：段落 / 标题（level 1–3）/ 列表项（number 为序号，0 表示无序）/ 引用 / 代码块（text 原样）。 */
    public static final class Block {
        public final Kind kind;
        public final int level;
        public final int number;
        public final List<Run> runs;
        public final String code;

        Block(Kind kind, int level, int number, List<Run> runs, String code) {
            this.kind = kind;
            this.level = level;
            this.number = number;
            this.runs = Collections.unmodifiableList(runs);
            this.code = code;
        }

        /** 块里的纯文本（无样式）。 */
        public String plain() {
            if (kind == Kind.CODE) {
                return code;
            }
            StringBuilder b = new StringBuilder();
            for (Run r : runs) {
                b.append(r.text);
            }
            return b.toString();
        }
    }

    /** 一段连续同样式的文字。url 非空表示链接。 */
    public static final class Run {
        public final String text;
        public final boolean bold;
        public final boolean italic;
        public final boolean code;
        public final String url;

        Run(String text, boolean bold, boolean italic, boolean code, String url) {
            this.text = text;
            this.bold = bold;
            this.italic = italic;
            this.code = code;
            this.url = url == null ? "" : url;
        }
    }

    private MarkdownLite() {
    }

    public static List<Block> parse(String markdown) {
        List<Block> out = new ArrayList<>();
        if (markdown == null || markdown.isEmpty()) {
            return out;
        }
        String[] lines = markdown.replace("\r\n", "\n").replace('\r', '\n').split("\n", -1);
        StringBuilder para = new StringBuilder();
        StringBuilder code = null;
        for (String raw : lines) {
            String line = raw;
            if (code != null) {
                if (line.trim().startsWith("```")) {
                    out.add(new Block(Kind.CODE, 0, 0, new ArrayList<>(), trimTrailingNewline(code.toString())));
                    code = null;
                } else {
                    code.append(line).append('\n');
                }
                continue;
            }
            String t = line.trim();
            if (t.startsWith("```")) {
                flush(out, para);
                code = new StringBuilder();
                continue;
            }
            if (t.isEmpty()) {
                flush(out, para);
                continue;
            }
            int heading = headingLevel(t);
            if (heading > 0) {
                flush(out, para);
                out.add(new Block(Kind.HEADING, heading, 0, inline(t.substring(heading).trim()), ""));
                continue;
            }
            if (t.startsWith("> ") || t.equals(">")) {
                flush(out, para);
                out.add(new Block(Kind.QUOTE, 0, 0, inline(t.length() > 1 ? t.substring(2) : ""), ""));
                continue;
            }
            if ((t.startsWith("- ") || t.startsWith("* ") || t.startsWith("• ")) && t.length() > 2) {
                flush(out, para);
                out.add(new Block(Kind.BULLET, 0, 0, inline(t.substring(2).trim()), ""));
                continue;
            }
            int num = numberedPrefix(t);
            if (num > 0) {
                flush(out, para);
                int dot = t.indexOf('.') >= 0 ? t.indexOf('.') : t.indexOf('、');
                out.add(new Block(Kind.NUMBERED, 0, num, inline(t.substring(dot + 1).trim()), ""));
                continue;
            }
            if (para.length() > 0) {
                para.append('\n');
            }
            para.append(t);
        }
        if (code != null) {
            // 没闭合的代码块：按代码显示到结尾
            out.add(new Block(Kind.CODE, 0, 0, new ArrayList<>(), trimTrailingNewline(code.toString())));
        }
        flush(out, para);
        return out;
    }

    private static void flush(List<Block> out, StringBuilder para) {
        if (para.length() > 0) {
            out.add(new Block(Kind.PARAGRAPH, 0, 0, inline(para.toString()), ""));
            para.setLength(0);
        }
    }

    private static String trimTrailingNewline(String s) {
        return s.endsWith("\n") ? s.substring(0, s.length() - 1) : s;
    }

    private static int headingLevel(String t) {
        int n = 0;
        while (n < t.length() && n < 3 && t.charAt(n) == '#') {
            n++;
        }
        return n > 0 && n < t.length() && t.charAt(n) == ' ' ? n : 0;
    }

    /** “1. ” / “12、” 开头的有序列表项，返回序号；不是返回 0。 */
    private static int numberedPrefix(String t) {
        int i = 0;
        while (i < t.length() && i < 3 && Character.isDigit(t.charAt(i))) {
            i++;
        }
        if (i == 0 || i >= t.length()) {
            return 0;
        }
        char c = t.charAt(i);
        boolean ok = (c == '.' && i + 1 < t.length() && t.charAt(i + 1) == ' ') || c == '、';
        return ok ? Integer.parseInt(t.substring(0, i)) : 0;
    }

    /** 行内解析：`code`、**bold**、*italic*、[text](url)。没有配对的标记按原文输出。 */
    static List<Run> inline(String s) {
        List<Run> runs = new ArrayList<>();
        StringBuilder buf = new StringBuilder();
        boolean bold = false;
        boolean italic = false;
        int i = 0;
        while (i < s.length()) {
            char c = s.charAt(i);
            if (c == '`') {
                int end = s.indexOf('`', i + 1);
                if (end > i + 1) {
                    push(runs, buf, bold, italic);
                    runs.add(new Run(s.substring(i + 1, end), false, false, true, ""));
                    i = end + 1;
                    continue;
                }
            } else if (c == '*' && i + 1 < s.length() && s.charAt(i + 1) == '*') {
                if (bold || s.indexOf("**", i + 2) > i + 2) {
                    push(runs, buf, bold, italic);
                    bold = !bold;
                    i += 2;
                    continue;
                }
            } else if (c == '*' && !(i + 1 < s.length() && s.charAt(i + 1) == ' ')) {
                int end = s.indexOf('*', i + 1);
                if (italic || (end > i + 1 && s.charAt(end - 1) != ' ')) {
                    push(runs, buf, bold, italic);
                    italic = !italic;
                    i += 1;
                    continue;
                }
            } else if (c == '[') {
                int close = s.indexOf("](", i + 1);
                int end = close > 0 ? s.indexOf(')', close + 2) : -1;
                if (close > i + 1 && end > close) {
                    String url = s.substring(close + 2, end).trim();
                    if (url.startsWith("http://") || url.startsWith("https://")) {
                        push(runs, buf, bold, italic);
                        runs.add(new Run(s.substring(i + 1, close), bold, italic, false, url));
                        i = end + 1;
                        continue;
                    }
                }
            }
            buf.append(c);
            i++;
        }
        push(runs, buf, bold, italic);
        return runs;
    }

    private static void push(List<Run> runs, StringBuilder buf, boolean bold, boolean italic) {
        if (buf.length() > 0) {
            runs.add(new Run(buf.toString(), bold, italic, false, ""));
            buf.setLength(0);
        }
    }

    /** 去掉所有标记后的纯文本（无障碍朗读、测试用）。 */
    public static String plainText(String markdown) {
        StringBuilder b = new StringBuilder();
        for (Block block : parse(markdown)) {
            if (b.length() > 0) {
                b.append('\n');
            }
            b.append(block.plain());
        }
        return b.toString();
    }
}
