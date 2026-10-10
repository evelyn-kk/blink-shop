package com.blink.shop.chat;

import android.content.Context;
import android.graphics.Paint;
import android.graphics.Typeface;
import android.text.TextUtils;
import android.view.Gravity;
import android.view.View;
import android.widget.HorizontalScrollView;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.util.ArrayList;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

import com.blink.shop.R;
import com.blink.shop.catalog.StockLabels;
import com.blink.shop.model.Money;
import com.blink.shop.ui.ImageLoader;
import com.blink.shop.ui.OrderStatusLabel;

/**
 * 把回答里的结构化块（product_list / comparison / citation / action / cart / order_list / coupon_list / promotion_list / review_list）
 * 渲染成卡片。只渲染块里给出的数据，不做任何推断；未知类型忽略。
 */
public final class BlockViews {

    /** 卡片上的操作，由聊天页实现（跳转商品、订单、页面；点追问）。 */
    public interface Actions {
        void openProduct(String productId, String name);

        void openOrder(String orderId);

        void navigate(String target, JSONObject params);
    }

    private final Context c;
    private final ImageLoader images;
    private final Actions actions;
    private final float d;

    public BlockViews(Context c, ImageLoader images, Actions actions) {
        this.c = c;
        this.images = images;
        this.actions = actions;
        this.d = c.getResources().getDisplayMetrics().density;
    }

    /** 返回块对应的视图；不认识的类型返回 null。 */
    public View build(JSONObject block) {
        switch (block.optString("type", "")) {
            case "product_list":
                return productList(block);
            case "comparison":
                return comparison(block);
            case "citation":
                return citations(block);
            case "action":
                return action(block);
            case "cart":
                return cart(block);
            case "order_list":
                return orders(block);
            case "coupon_list":
                return coupons(block);
            case "promotion_list":
                return promotions(block);
            case "review_list":
                return reviews(block);
            default:
                return null;
        }
    }

    // ---------- 商品 ----------

    private View productList(JSONObject block) {
        LinearLayout box = column();
        String title = block.optString("title", "");
        if (!title.isEmpty()) {
            box.addView(caption(title));
        }
        JSONArray products = block.optJSONArray("products");
        for (int i = 0; products != null && i < products.length(); i++) {
            JSONObject p = products.optJSONObject(i);
            if (p != null) {
                box.addView(productCard(p, i + 1));
            }
        }
        return box;
    }

    private View productCard(JSONObject p, int index) {
        LinearLayout card = new LinearLayout(c);
        card.setOrientation(LinearLayout.HORIZONTAL);
        card.setBackgroundResource(R.drawable.bg_card_outline);
        card.setPadding(dp(10), dp(10), dp(10), dp(10));
        card.setLayoutParams(marginTop(6));
        card.setMinimumHeight(dp(48));
        ImageView image = new ImageView(c);
        image.setLayoutParams(new LinearLayout.LayoutParams(dp(72), dp(72)));
        image.setBackgroundResource(R.drawable.bg_image);
        image.setScaleType(ImageView.ScaleType.CENTER_CROP);
        image.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        if (images != null) {
            images.load(image, p.optString("image_url", ""), dp(72));
        }
        card.addView(image);
        LinearLayout text = column();
        LinearLayout.LayoutParams tl = new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1);
        tl.setMarginStart(dp(10));
        text.setLayoutParams(tl);
        String name = p.optString("name", "");
        text.addView(label(index + ". " + name, R.color.text_primary, 15, true, 2));
        String byline = TextUtils.join(" · ", nonEmpty(p.optString("brand", ""), p.optString("merchant_name", "")));
        if (!byline.isEmpty()) {
            text.addView(label(byline, R.color.text_secondary, 12, false, 1));
        }
        List<String> points = ChatTurn.strings(p.optJSONArray("selling_points"));
        String pts = points.isEmpty() ? p.optString("recommend_reason", "") : TextUtils.join(" · ", points);
        if (!pts.isEmpty()) {
            text.addView(label(pts, R.color.text_secondary, 12, false, 2));
        }
        LinearLayout priceRow = new LinearLayout(c);
        priceRow.setOrientation(LinearLayout.HORIZONTAL);
        priceRow.setGravity(Gravity.BOTTOM);
        priceRow.setLayoutParams(marginTop(4));
        TextView price = label(Money.format(p.optString("price", "")), R.color.price, 16, true, 1);
        priceRow.addView(price);
        String market = p.optString("market_price", "");
        if (Money.greater(market, p.optString("price", ""))) {
            TextView mp = label(Money.format(market), R.color.text_hint, 12, false, 1);
            mp.setPaintFlags(mp.getPaintFlags() | Paint.STRIKE_THRU_TEXT_FLAG);
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT);
            lp.setMarginStart(dp(6));
            mp.setLayoutParams(lp);
            mp.setContentDescription("原价 " + Money.format(market));
            priceRow.addView(mp);
        }
        View spacer = new View(c);
        priceRow.addView(spacer, new LinearLayout.LayoutParams(0, 1, 1));
        TextView stock = new TextView(c);
        stock.setTextSize(12);
        StockLabels.apply(stock, p.optString("stock_status", ""));
        priceRow.addView(stock);
        text.addView(priceRow);
        card.addView(text);
        String productId = p.optString("product_id", "");
        card.setContentDescription("商品：" + name + "，" + Money.format(p.optString("price", "")) + "，点击查看详情");
        card.setOnClickListener(v -> actions.openProduct(productId, name));
        card.setForeground(c.getDrawable(android.R.drawable.list_selector_background));
        return card;
    }

    // ---------- 对比 ----------

    private View comparison(JSONObject block) {
        JSONArray products = block.optJSONArray("products");
        JSONArray rows = block.optJSONArray("rows");
        LinearLayout box = column();
        box.addView(caption("对比"));
        HorizontalScrollView scroll = new HorizontalScrollView(c);
        scroll.setHorizontalScrollBarEnabled(false);
        LinearLayout table = column();
        table.setBackgroundResource(R.drawable.bg_card_outline);
        int n = products == null ? 0 : products.length();
        LinearLayout head = row();
        head.addView(cell("", true, true));
        for (int i = 0; i < n; i++) {
            JSONObject p = products.optJSONObject(i);
            TextView cellView = cell(p == null ? "" : p.optString("name", ""), true, false);
            String pid = p == null ? "" : p.optString("product_id", "");
            String name = p == null ? "" : p.optString("name", "");
            cellView.setTextColor(c.getColor(R.color.brand));
            cellView.setOnClickListener(v -> actions.openProduct(pid, name));
            cellView.setContentDescription(name + "，点击查看详情");
            head.addView(cellView);
        }
        table.addView(head);
        for (int r = 0; rows != null && r < rows.length(); r++) {
            JSONObject rowObj = rows.optJSONObject(r);
            if (rowObj == null) {
                continue;
            }
            LinearLayout line = row();
            if (r % 2 == 0) {
                line.setBackgroundColor(c.getColor(R.color.page_bg));
            }
            line.addView(cell(rowObj.optString("label", ""), true, true));
            JSONArray values = rowObj.optJSONArray("values");
            for (int i = 0; i < n; i++) {
                line.addView(cell(values == null || i >= values.length() ? "—" : values.optString(i, "—"), false, false));
            }
            table.addView(line);
        }
        scroll.addView(table);
        box.addView(scroll);
        return box;
    }

    private TextView cell(String text, boolean bold, boolean labelColumn) {
        TextView t = new TextView(c);
        t.setText(text);
        t.setTextSize(13);
        t.setTypeface(null, bold ? Typeface.BOLD : Typeface.NORMAL);
        t.setTextColor(c.getColor(labelColumn ? R.color.text_secondary : R.color.text_primary));
        t.setPadding(dp(8), dp(8), dp(8), dp(8));
        t.setMaxWidth(dp(160));
        t.setMinWidth(dp(labelColumn ? 64 : 112));
        return t;
    }

    // ---------- 引用 ----------

    private View citations(JSONObject block) {
        LinearLayout box = column();
        box.addView(caption("参考资料"));
        JSONArray cits = block.optJSONArray("citations");
        for (int i = 0; cits != null && i < cits.length(); i++) {
            JSONObject ci = cits.optJSONObject(i);
            if (ci == null) {
                continue;
            }
            LinearLayout card = column();
            card.setBackgroundResource(R.drawable.bg_card_outline);
            card.setPadding(dp(10), dp(8), dp(10), dp(8));
            card.setLayoutParams(marginTop(6));
            String doc = ci.optString("document_title", "");
            String title = ci.optString("title", "");
            card.addView(label(TextUtils.join(" · ", nonEmpty(doc, title)), R.color.text_primary, 13, true, 2));
            card.addView(label(ci.optString("snippet", ""), R.color.text_secondary, 13, false, 6));
            box.addView(card);
        }
        return box;
    }

    // ---------- 动作 ----------

    private View action(JSONObject block) {
        TextView button = new TextView(c);
        button.setText(block.optString("label", "打开"));
        button.setTextSize(14);
        button.setTextColor(c.getColor(R.color.brand));
        button.setBackgroundResource(R.drawable.bg_secondary_button);
        button.setGravity(Gravity.CENTER);
        button.setMinHeight(dp(44));
        button.setMinWidth(dp(120));
        button.setPadding(dp(16), 0, dp(16), 0);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dp(8);
        button.setLayoutParams(lp);
        String target = block.optString("target", "");
        JSONObject params = block.optJSONObject("params");
        button.setOnClickListener(v -> actions.navigate(target, params == null ? new JSONObject() : params));
        return button;
    }

    // ---------- 购物车 ----------

    private View cart(JSONObject block) {
        LinearLayout box = column();
        box.setBackgroundResource(R.drawable.bg_card_outline);
        box.setPadding(dp(10), dp(8), dp(10), dp(8));
        box.setLayoutParams(marginTop(6));
        JSONArray items = block.optJSONArray("items");
        JSONObject summary = block.optJSONObject("summary");
        box.addView(caption("购物车"));
        if (items == null || items.length() == 0) {
            box.addView(label("购物车是空的", R.color.text_secondary, 13, false, 1));
        }
        for (int i = 0; items != null && i < items.length(); i++) {
            JSONObject it = items.optJSONObject(i);
            if (it == null) {
                continue;
            }
            String line = it.optString("product_name", "") + " × " + it.optInt("quantity", 0) + "　" + Money.format(it.optString("pay_amount", ""));
            if (!it.optBoolean("available", true)) {
                line += "（" + it.optString("unavailable_reason", "不可购买") + "）";
            } else if (!it.optBoolean("selected", true)) {
                line += "（未选中）";
            }
            TextView t = label(line, R.color.text_primary, 13, false, 2);
            String pid = it.optString("product_id", "");
            String name = it.optString("product_name", "");
            t.setOnClickListener(v -> actions.openProduct(pid, name));
            box.addView(t);
        }
        if (summary != null) {
            box.addView(label("已选 " + summary.optInt("selected_count", 0) + " 件，优惠 " + Money.format(summary.optString("discount_amount", "0"))
                    + "，应付 " + Money.format(summary.optString("pay_amount", "0")), R.color.price, 14, true, 2));
        }
        JSONArray hints = block.optJSONArray("hints");
        for (int i = 0; hints != null && i < hints.length(); i++) {
            JSONObject h = hints.optJSONObject(i);
            if (h != null) {
                box.addView(label("再买 " + Money.format(h.optString("shortfall", "")) + " 可参加“" + h.optString("name", "") + "”", R.color.warning_text, 12, false, 2));
            }
        }
        return box;
    }

    // ---------- 订单 ----------

    private View orders(JSONObject block) {
        LinearLayout box = column();
        JSONArray orders = block.optJSONArray("orders");
        for (int i = 0; orders != null && i < orders.length(); i++) {
            JSONObject o = orders.optJSONObject(i);
            if (o == null) {
                continue;
            }
            LinearLayout card = column();
            card.setBackgroundResource(R.drawable.bg_card_outline);
            card.setPadding(dp(10), dp(8), dp(10), dp(8));
            card.setLayoutParams(marginTop(6));
            LinearLayout top = row();
            TextView no = label("订单 " + o.optString("order_no", ""), R.color.text_primary, 13, true, 1);
            no.setLayoutParams(new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1));
            top.addView(no);
            TextView status = new TextView(c);
            status.setTextSize(12);
            OrderStatusLabel.apply(status, o.optString("status", ""));
            top.addView(status);
            card.addView(top);
            JSONArray items = o.optJSONArray("items");
            List<String> names = new ArrayList<>();
            for (int j = 0; items != null && j < items.length(); j++) {
                JSONObject it = items.optJSONObject(j);
                if (it != null) {
                    names.add(it.optString("name", "") + " × " + it.optInt("quantity", 0));
                }
            }
            card.addView(label(TextUtils.join("、", names), R.color.text_secondary, 13, false, 3));
            card.addView(label(o.optString("merchant_name", "") + "　实付 " + Money.format(o.optString("pay_amount", "")), R.color.price, 13, true, 1));
            String orderId = o.optString("order_id", "");
            card.setOnClickListener(v -> actions.openOrder(orderId));
            card.setContentDescription("订单 " + o.optString("order_no", "") + "，点击查看详情");
            box.addView(card);
        }
        return box;
    }

    // ---------- 券 / 活动 / 评价 ----------

    private View coupons(JSONObject block) {
        LinearLayout box = column();
        JSONArray available = block.optJSONArray("available");
        JSONArray mine = block.optJSONArray("mine");
        if (available != null && available.length() > 0) {
            box.addView(caption("可领的券"));
            for (int i = 0; i < available.length(); i++) {
                JSONObject cp = available.optJSONObject(i);
                if (cp == null) {
                    continue;
                }
                String state = cp.optBoolean("can_claim", false) ? "可领取" : "已领过";
                box.addView(line(cp.optString("name", ""), cp.optString("description", "") + " · " + state));
            }
        }
        if (mine != null && mine.length() > 0) {
            box.addView(caption("我的券"));
            for (int i = 0; i < mine.length(); i++) {
                JSONObject uc = mine.optJSONObject(i);
                JSONObject cp = uc == null ? null : uc.optJSONObject("coupon");
                if (cp != null) {
                    box.addView(line(cp.optString("name", ""), cp.optString("description", "")));
                }
            }
        }
        if (box.getChildCount() == 0) {
            box.addView(label("暂无优惠券", R.color.text_secondary, 13, false, 1));
        }
        return box;
    }

    private View promotions(JSONObject block) {
        LinearLayout box = column();
        JSONArray items = block.optJSONArray("promotions");
        if (items == null || items.length() == 0) {
            box.addView(label("暂无进行中的活动", R.color.text_secondary, 13, false, 1));
            return box;
        }
        box.addView(caption("进行中的活动"));
        for (int i = 0; i < items.length(); i++) {
            JSONObject p = items.optJSONObject(i);
            if (p != null) {
                box.addView(line(p.optString("name", ""), p.optString("description", "")));
            }
        }
        return box;
    }

    private View reviews(JSONObject block) {
        LinearLayout box = column();
        box.setBackgroundResource(R.drawable.bg_card_outline);
        box.setPadding(dp(10), dp(8), dp(10), dp(8));
        box.setLayoutParams(marginTop(6));
        int total = block.optInt("total", 0);
        String name = block.optString("product_name", "");
        box.addView(label(name + "：" + total + " 条评价" + (total > 0 ? "，平均 " + block.optDouble("average_rating", 0) + " 分" : ""),
                R.color.text_primary, 13, true, 2));
        JSONArray items = block.optJSONArray("reviews");
        for (int i = 0; items != null && i < items.length(); i++) {
            JSONObject r = items.optJSONObject(i);
            if (r == null) {
                continue;
            }
            int rating = r.optInt("rating", 0);
            StringBuilder stars = new StringBuilder();
            for (int s = 0; s < 5; s++) {
                stars.append(s < rating ? '★' : '☆');
            }
            TextView t = label(r.optString("reviewer_name", "") + " " + stars + "  " + r.optString("content", ""), R.color.text_secondary, 13, false, 4);
            t.setContentDescription(r.optString("reviewer_name", "") + " 评 " + rating + " 星：" + r.optString("content", ""));
            box.addView(t);
            String reply = r.optString("merchant_reply", "");
            if (!reply.isEmpty()) {
                box.addView(label("商家回复：" + reply, R.color.text_hint, 12, false, 3));
            }
        }
        String pid = block.optString("product_id", "");
        box.setOnClickListener(v -> actions.openProduct(pid, name));
        return box;
    }

    // ---------- 小部件 ----------

    private LinearLayout column() {
        LinearLayout l = new LinearLayout(c);
        l.setOrientation(LinearLayout.VERTICAL);
        l.setLayoutParams(new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT));
        return l;
    }

    private LinearLayout row() {
        LinearLayout l = new LinearLayout(c);
        l.setOrientation(LinearLayout.HORIZONTAL);
        l.setGravity(Gravity.CENTER_VERTICAL);
        return l;
    }

    private TextView caption(String text) {
        TextView t = label(text, R.color.text_secondary, 12, true, 1);
        t.setLayoutParams(marginTop(8));
        return t;
    }

    private View line(String title, String detail) {
        LinearLayout l = column();
        l.setBackgroundResource(R.drawable.bg_card_outline);
        l.setPadding(dp(10), dp(8), dp(10), dp(8));
        l.setLayoutParams(marginTop(6));
        l.addView(label(title, R.color.text_primary, 13, true, 2));
        if (!detail.isEmpty()) {
            l.addView(label(detail, R.color.text_secondary, 12, false, 3));
        }
        return l;
    }

    private TextView label(String text, int colorRes, float sp, boolean bold, int maxLines) {
        TextView t = new TextView(c);
        t.setText(text);
        t.setTextSize(sp);
        t.setTextColor(c.getColor(colorRes));
        t.setTypeface(null, bold ? Typeface.BOLD : Typeface.NORMAL);
        t.setMaxLines(maxLines);
        t.setEllipsize(TextUtils.TruncateAt.END);
        return t;
    }

    private LinearLayout.LayoutParams marginTop(int dps) {
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dp(dps);
        return lp;
    }

    private static List<String> nonEmpty(String... values) {
        List<String> out = new ArrayList<>();
        for (String v : values) {
            if (v != null && !v.isEmpty()) {
                out.add(v);
            }
        }
        return out;
    }

    private int dp(int v) {
        return Math.round(v * d);
    }
}
