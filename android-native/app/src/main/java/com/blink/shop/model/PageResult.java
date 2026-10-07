package com.blink.shop.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

/** 分页列表响应 {items, page, page_size, total}。 */
public final class PageResult<T> {

    public interface Parser<T> {
        T parse(JSONObject o);
    }

    public final List<T> items;
    public final int page;
    public final int pageSize;
    public final int total;

    public PageResult(List<T> items, int page, int pageSize, int total) {
        this.items = items;
        this.page = page;
        this.pageSize = pageSize;
        this.total = total;
    }

    public static <T> PageResult<T> fromJson(JSONObject o, Parser<T> parser) {
        JSONArray arr = o.optJSONArray("items");
        List<T> items = new ArrayList<>();
        if (arr != null) {
            for (int i = 0; i < arr.length(); i++) {
                JSONObject item = arr.optJSONObject(i);
                if (item != null) {
                    items.add(parser.parse(item));
                }
            }
        }
        return new PageResult<>(Collections.unmodifiableList(items), o.optInt("page", 1), o.optInt("page_size", items.size()),
                o.optInt("total", items.size()));
    }

    /** 按 total 判断是否还有下一页。 */
    public boolean hasMore() {
        return (long) page * pageSize < total;
    }
}
