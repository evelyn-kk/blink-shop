package com.blink.shop.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

/** 分类树节点。 */
public final class Category {

    public final String categoryId;
    public final String parentId;
    public final String name;
    public final List<Category> children;

    private Category(JSONObject o) {
        categoryId = Json.str(o, "category_id");
        parentId = Json.str(o, "parent_id");
        name = Json.str(o, "name");
        children = listFrom(o.optJSONArray("children"));
    }

    public static List<Category> listFrom(JSONArray arr) {
        if (arr == null || arr.length() == 0) {
            return Collections.emptyList();
        }
        List<Category> out = new ArrayList<>(arr.length());
        for (int i = 0; i < arr.length(); i++) {
            JSONObject c = arr.optJSONObject(i);
            if (c != null) {
                out.add(new Category(c));
            }
        }
        return Collections.unmodifiableList(out);
    }

    /** 在树中按 ID 找节点；找不到返回 null。 */
    public static Category find(List<Category> tree, String id) {
        if (id == null || id.isEmpty()) {
            return null;
        }
        for (Category c : tree) {
            if (c.categoryId.equals(id)) {
                return c;
            }
            Category hit = find(c.children, id);
            if (hit != null) {
                return hit;
            }
        }
        return null;
    }
}
