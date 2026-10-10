package com.blink.shop.voice;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class TtsTextTest {

    @Test
    public void stripsMarkdown() {
        String md = "## 推荐\n\n- **Blink Air 降噪耳机**（¥599）：通勤首选\n- 详情见 [商品页](https://shop.example/p/p_seed_earbuds)\n"
                + "> 注意：不防水\n\n```json\n{\"a\":1}\n```\n| 型号 | 价格 |\n| --- | --- |\n| Air | 599 |\n![图](https://x/y.png)<br>p_seed_earbuds";
        String out = TtsText.clean(md, 800);
        assertFalse(out, out.contains("**") || out.contains("#") || out.contains("http") || out.contains("p_seed") || out.contains("```")
                || out.contains("<br>") || out.contains("---"));
        assertTrue(out, out.contains("Blink Air 降噪耳机（¥599）：通勤首选"));
        assertTrue(out, out.contains("详情见 商品页"));
        assertTrue(out, out.contains("注意：不防水"));
        assertTrue(out, out.contains("（代码略）"));
    }

    @Test
    public void emptyAndBlank() {
        assertEquals("", TtsText.clean(null, 10));
        assertEquals("", TtsText.clean("  \n```\ncode only", 10).replace("（代码略）", ""));
        assertEquals("", TtsText.clean("https://x.example/a p_seed_mouse", 10));
    }

    @Test
    public void truncatesAtSentenceEnd() {
        String s = "第一句话很短。第二句话稍微长一点点。第三句";
        assertEquals("第一句话很短。第二句话稍微长一点点。", TtsText.clean(s, 20));
        // 前半段没有句号：直接截断
        assertEquals("一二三四五", TtsText.clean("一二三四五六七八九十", 5));
        assertEquals(s, TtsText.clean(s, 100));
    }
}
