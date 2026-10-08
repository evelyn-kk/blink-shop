package com.blink.shop.ui;

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

import com.blink.shop.model.PageResult;

/**
 * 分页列表的状态。每次换筛选条件开启新一代（generation），旧请求的结果直接丢弃；
 * 同一时间只有一个请求；翻页时按 ID 去重（翻页期间数据变化可能出现重复）。
 */
public final class Pager<T> {

    public interface IdOf<T> {
        String id(T item);
    }

    private final IdOf<T> idOf;

    private int generation;
    private final List<T> items = new ArrayList<>();
    private final Set<String> ids = new HashSet<>();
    private int nextPage = 1;
    private boolean hasMore;
    private boolean loading;
    private boolean failed;

    public Pager(IdOf<T> idOf) {
        this.idOf = idOf;
    }

    /** 重新从第一页加载，返回本次请求的代号。 */
    public int reset() {
        generation++;
        items.clear();
        ids.clear();
        nextPage = 1;
        hasMore = false;
        loading = true;
        failed = false;
        return generation;
    }

    /** 可以加载下一页时返回代号，否则返回 -1（正在加载、没有更多、上次失败等用户点重试）。 */
    public int next() {
        if (loading || !hasMore || failed) {
            return -1;
        }
        loading = true;
        failed = false;
        return generation;
    }

    public int nextPage() {
        return nextPage;
    }

    /** 接收一页结果；不是当前代的返回 false。 */
    public boolean accept(int gen, PageResult<T> page) {
        if (gen != generation) {
            return false;
        }
        for (T p : page.items) {
            if (ids.add(idOf.id(p))) {
                items.add(p);
            }
        }
        nextPage = page.page + 1;
        hasMore = page.hasMore() && !page.items.isEmpty();
        loading = false;
        return true;
    }

    /** 请求失败；不是当前代的返回 false。失败后可以用 retryNext 重试下一页。 */
    public boolean fail(int gen) {
        if (gen != generation) {
            return false;
        }
        loading = false;
        failed = true;
        return true;
    }

    /** 翻页失败后重试。 */
    public int retryNext() {
        if (loading || !failed || items.isEmpty()) {
            return -1;
        }
        loading = true;
        failed = false;
        return generation;
    }

    public List<T> items() {
        return Collections.unmodifiableList(items);
    }

    public boolean hasMore() {
        return hasMore;
    }

    public boolean isLoading() {
        return loading;
    }

    public boolean isFailed() {
        return failed;
    }
}
