package com.blink.shop.chat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.List;

import org.junit.Test;

public class MarkdownLiteTest {

    private static String kinds(List<MarkdownLite.Block> blocks) {
        StringBuilder b = new StringBuilder();
        for (MarkdownLite.Block x : blocks) {
            if (b.length() > 0) {
                b.append(',');
            }
            b.append(x.kind.name().toLowerCase());
            if (x.level > 0) {
                b.append(x.level);
            }
            if (x.number > 0) {
                b.append('#').append(x.number);
            }
        }
        return b.toString();
    }

    @Test
    public void parsesBlocksAndInline() {
        List<MarkdownLite.Block> blocks = MarkdownLite.parse(
                "# 推荐\n\n按“通勤降噪耳机”找到 **1 件**在售商品，优先推荐 *Blink Air*（`¥599`）。\n- 主动降噪\n- 30 小时续航\n1. 第一步\n2. 第二步\n> 注意：不支持游泳佩戴\n```\ncode here\n```\n[详情](https://example.com/p)");
        assertEquals("heading1,paragraph,bullet,bullet,numbered#1,numbered#2,quote,code,paragraph", kinds(blocks));
        MarkdownLite.Block p = blocks.get(1);
        assertEquals("按“通勤降噪耳机”找到 1 件在售商品，优先推荐 Blink Air（¥599）。", p.plain());
        assertTrue(p.runs.get(1).bold);
        assertEquals("1 件", p.runs.get(1).text);
        assertTrue(p.runs.get(3).italic);
        assertEquals("Blink Air", p.runs.get(3).text);
        assertTrue(p.runs.get(5).code);
        assertEquals("¥599", p.runs.get(5).text);
        assertEquals("code here", blocks.get(7).code);
        MarkdownLite.Block link = blocks.get(8);
        assertEquals("https://example.com/p", link.runs.get(0).url);
        assertEquals("详情", link.runs.get(0).text);
    }

    @Test
    public void malformedInputNeverThrowsAndKeepsText() {
        String[] inputs = {
                "**没有闭合的粗体", "*单个星号 * 到处 * 都是*", "`反引号没闭合", "[坏链接](javascript:alert(1))", "[没有括号](", "](", "```\n没闭合的代码块",
                "#没有空格的井号", "####### 七级标题", "- ", "1.没有空格", "99999. 大序号", "<script>alert(1)</script>", "**", "***", "``", "\r\n\r\n",
                "混合 **粗体 *斜体** 交错*", "", "   ", "\u0000￿ 控制字符", new String(new char[20000]).replace('\0', '字'),
        };
        for (String in : inputs) {
            List<MarkdownLite.Block> blocks = MarkdownLite.parse(in);
            String plain = MarkdownLite.plainText(in);
            // 非空输入里的可见文字不能丢（标记符号除外）
            for (String word : new String[]{"没有闭合的粗体", "反引号没闭合", "坏链接", "没闭合的代码块", "七级标题", "没有空格", "大序号", "alert(1)"}) {
                if (in.contains(word)) {
                    assertTrue(in + " lost " + word, plain.contains(word));
                }
            }
            assertTrue(blocks.size() >= 0);
        }
        // 非 http(s) 链接按原文显示，不产生可点击链接
        for (MarkdownLite.Block b : MarkdownLite.parse("[坏链接](javascript:alert(1))")) {
            for (MarkdownLite.Run r : b.runs) {
                assertEquals("", r.url);
            }
        }
        // 20000 字的段落只有一个块
        assertEquals(1, MarkdownLite.parse(new String(new char[20000]).replace('\0', '字')).size());
        assertEquals("", MarkdownLite.plainText(null));
    }

    @Test
    public void unclosedBoldStaysLiteral() {
        List<MarkdownLite.Block> blocks = MarkdownLite.parse("价格 **很低，真的");
        assertEquals(1, blocks.size());
        assertEquals("价格 **很低，真的", blocks.get(0).plain());
        for (MarkdownLite.Run r : blocks.get(0).runs) {
            assertFalse(r.bold);
        }
    }

    @Test
    public void streamingPrefixParsesIncrementally() {
        // 流式输出时任意前缀都能解析，拼完等于全文
        String full = "优先推荐 **Blink Air 降噪耳机**（¥599）：通勤降噪性价比高。\n- 主动降噪\n- 30 小时续航";
        for (int i = 0; i <= full.length(); i++) {
            String prefix = full.substring(0, i);
            String plain = MarkdownLite.plainText(prefix);
            assertTrue(prefix, plain.length() <= prefix.length());
        }
        assertEquals("优先推荐 Blink Air 降噪耳机（¥599）：通勤降噪性价比高。\n主动降噪\n30 小时续航", MarkdownLite.plainText(full));
    }
}
