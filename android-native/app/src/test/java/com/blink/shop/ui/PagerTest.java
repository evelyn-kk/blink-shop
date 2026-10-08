package com.blink.shop.ui;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.List;

import org.json.JSONObject;
import org.junit.Test;

import com.blink.shop.model.PageResult;
import com.blink.shop.model.Product;

public class PagerTest {

    private static PageResult<Product> page(int page, int total, String... ids) throws Exception {
        List<Product> items = new ArrayList<>();
        for (String id : ids) {
            items.add(Product.fromJson(new JSONObject().put("product_id", id)));
        }
        return new PageResult<>(items, page, 2, total);
    }

    @Test
    public void pagesUntilTotalAndDedupes() throws Exception {
        Pager<Product> p = new Pager<>(x -> x.productId);
        int gen = p.reset();
        assertEquals(-1, p.next()); // 首页加载中不能翻页
        assertTrue(p.accept(gen, page(1, 5, "a", "b")));
        assertTrue(p.hasMore());
        int g2 = p.next();
        assertEquals(2, p.nextPage());
        assertEquals(-1, p.next()); // 同时只有一个请求
        assertTrue(p.accept(g2, page(2, 5, "b", "c")));
        assertEquals(3, p.items().size());
        int g3 = p.next();
        assertTrue(p.accept(g3, page(3, 5, "d")));
        assertFalse(p.hasMore());
        assertEquals(-1, p.next());
    }

    @Test
    public void staleResultsAfterFilterChangeAreDropped() throws Exception {
        Pager<Product> p = new Pager<>(x -> x.productId);
        int old = p.reset();
        int current = p.reset();
        assertFalse(p.accept(old, page(1, 2, "old")));
        assertFalse(p.fail(old));
        assertTrue(p.isLoading());
        assertTrue(p.accept(current, page(1, 1, "new")));
        assertEquals("new", p.items().get(0).productId);
    }

    @Test
    public void failedNextPageCanBeRetried() throws Exception {
        Pager<Product> p = new Pager<>(x -> x.productId);
        int gen = p.reset();
        p.accept(gen, page(1, 4, "a", "b"));
        int g = p.next();
        assertTrue(p.fail(g));
        assertTrue(p.isFailed());
        assertEquals(-1, p.next()); // 失败后不自动翻页，等用户点重试
        int retry = p.retryNext();
        assertEquals(gen, retry);
        assertEquals(2, p.nextPage());
        assertTrue(p.accept(retry, page(2, 4, "c", "d")));
        assertEquals(4, p.items().size());
    }

    @Test
    public void emptyPageStopsPaging() throws Exception {
        Pager<Product> p = new Pager<>(x -> x.productId);
        int gen = p.reset();
        p.accept(gen, page(1, 10));
        assertFalse(p.hasMore());
    }
}
