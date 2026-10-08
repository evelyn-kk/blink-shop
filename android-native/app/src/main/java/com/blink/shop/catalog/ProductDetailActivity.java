package com.blink.shop.catalog;

import android.content.Context;
import android.content.Intent;
import android.graphics.Paint;
import android.graphics.Typeface;
import android.os.Build;
import android.os.Bundle;
import android.text.TextUtils;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.recyclerview.widget.LinearLayoutManager;
import androidx.recyclerview.widget.PagerSnapHelper;
import androidx.recyclerview.widget.RecyclerView;

import java.util.ArrayList;
import java.util.List;
import java.util.TimeZone;

import com.blink.shop.R;
import com.blink.shop.account.LoginActivity;
import com.blink.shop.model.Cart;
import com.blink.shop.model.Money;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Product;
import com.blink.shop.model.Review;
import com.blink.shop.model.Session;
import com.blink.shop.model.Sku;
import com.blink.shop.model.Times;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.CartButton;
import com.blink.shop.ui.Chips;
import com.blink.shop.ui.StateView;

/** 商品详情：图集、价格库存、规格选择、商品信息和评价。 */
public final class ProductDetailActivity extends BaseActivity {

    private static final String EXTRA_PRODUCT_ID = "product_id";
    private static final String EXTRA_NAME = "name";
    private static final String STATE_SKU = "sku_id";
    private static final int REVIEW_COUNT = 5;

    public static Intent intent(Context c, String productId, String name) {
        return new Intent(c, ProductDetailActivity.class).putExtra(EXTRA_PRODUCT_ID, productId).putExtra(EXTRA_NAME, name);
    }

    /** 商品和规格一起加载。 */
    private static final class Loaded {
        final Product product;
        final List<Sku> skus;

        Loaded(Product product, List<Sku> skus) {
            this.product = product;
            this.skus = skus;
        }
    }

    private String productId;
    private StateView state;
    private Product product;
    private List<Sku> skus = new ArrayList<>();
    private String selectedSkuId = "";
    private CartButton cartButton;
    private boolean adding;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_product_detail);
        productId = getIntent().getStringExtra(EXTRA_PRODUCT_ID);
        if (productId == null || productId.isEmpty()) {
            finish();
            return;
        }
        if (savedInstanceState != null) {
            selectedSkuId = savedInstanceState.getString(STATE_SKU, "");
        }
        String name = getIntent().getStringExtra(EXTRA_NAME);
        if (name != null && !name.isEmpty()) {
            ((TextView) findViewById(R.id.top_title)).setText(name);
        }
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        TextView market = findViewById(R.id.detail_market_price);
        market.setPaintFlags(market.getPaintFlags() | Paint.STRIKE_THRU_TEXT_FLAG);
        state = new StateView(findViewById(android.R.id.content));
        cartButton = new CartButton(this, findViewById(R.id.cart_button));
        findViewById(R.id.add_to_cart).setOnClickListener(v -> addToCart());
        load();
    }

    @Override
    protected void onSessionUpdated(Session session) {
        if (cartButton != null) {
            cartButton.refresh();
        }
    }

    /** 未登录先去登录（登录后回到本页再点一次）；数量规则和库存以服务端为准。 */
    private void addToCart() {
        if (adding || product == null) {
            return;
        }
        if (!app.sessions().isLoggedIn()) {
            toast("请先登录");
            startActivity(new Intent(this, LoginActivity.class));
            return;
        }
        adding = true;
        Button b = findViewById(R.id.add_to_cart);
        b.setEnabled(false);
        b.setText("加入中…");
        String skuId = selectedSkuId;
        call(() -> app.api().addToCart(productId, skuId, 1), new Async.Callback<Cart>() {
            @Override
            public void onSuccess(Cart cart) {
                adding = false;
                cartButton.show(cart.totalQuantity());
                toast("已加入购物车");
                renderSkus();
            }

            @Override
            public void onError(ApiException e) {
                adding = false;
                toast(e.getMessage());
                renderSkus();
            }
        });
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putString(STATE_SKU, selectedSkuId);
    }

    private void load() {
        state.loading("正在加载商品…");
        call(() -> {
            Product p = app.api().product(productId);
            PageResult<Sku> page = app.api().skus(productId);
            return new Loaded(p, page.items);
        }, new Async.Callback<Loaded>() {
            @Override
            public void onSuccess(Loaded value) {
                product = value.product;
                skus = new ArrayList<>(value.skus);
                state.hide();
                render();
                loadReviews();
            }

            @Override
            public void onError(ApiException e) {
                if ("product_not_found".equals(e.code()) || e.status() == 404) {
                    state.empty("商品不存在或已下架", "去看看其他商品吧");
                } else {
                    state.error("商品加载失败", e, ProductDetailActivity.this::load);
                }
            }
        });
    }

    private void render() {
        Product p = product;
        ((TextView) findViewById(R.id.top_title)).setText(p.name);
        ((TextView) findViewById(R.id.detail_name)).setText(p.name);
        setTextOrHide(R.id.detail_byline, p.byline());
        setTextOrHide(R.id.detail_tags, p.tags.isEmpty() ? "" : "# " + TextUtils.join("   # ", p.tags));
        setTextOrHide(R.id.detail_reason, p.recommendReason.isEmpty() ? "" : "推荐理由：" + p.recommendReason);
        renderGallery(p.gallery());
        if (selectedSkuId.isEmpty() || findSku(selectedSkuId) == null) {
            selectedSkuId = defaultSkuId();
        }
        renderSkus();
        renderSections(p);
    }

    // ---------- 图集 ----------

    private void renderGallery(List<String> urls) {
        RecyclerView gallery = findViewById(R.id.gallery);
        TextView indicator = findViewById(R.id.gallery_indicator);
        int height = Math.min(getResources().getDisplayMetrics().widthPixels, dp(320));
        ViewGroup.LayoutParams lp = gallery.getLayoutParams();
        lp.height = height;
        gallery.setLayoutParams(lp);
        LinearLayoutManager lm = new LinearLayoutManager(this, LinearLayoutManager.HORIZONTAL, false);
        gallery.setLayoutManager(lm);
        if (gallery.getOnFlingListener() == null) {
            new PagerSnapHelper().attachToRecyclerView(gallery);
        }
        gallery.setAdapter(new RecyclerView.Adapter<RecyclerView.ViewHolder>() {
            @Override
            public RecyclerView.ViewHolder onCreateViewHolder(ViewGroup parent, int viewType) {
                ImageView iv = new ImageView(parent.getContext());
                iv.setLayoutParams(new RecyclerView.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
                iv.setScaleType(ImageView.ScaleType.FIT_CENTER);
                return new RecyclerView.ViewHolder(iv) {
                };
            }

            @Override
            public void onBindViewHolder(RecyclerView.ViewHolder holder, int position) {
                ImageView iv = (ImageView) holder.itemView;
                iv.setContentDescription(getString(R.string.product_image) + " " + (position + 1) + "/" + urls.size());
                app.images().load(iv, urls.get(position), height);
            }

            @Override
            public int getItemCount() {
                return urls.size();
            }
        });
        indicator.setVisibility(urls.size() > 1 ? View.VISIBLE : View.GONE);
        indicator.setText(urls.isEmpty() ? "" : "1/" + urls.size());
        gallery.clearOnScrollListeners();
        gallery.addOnScrollListener(new RecyclerView.OnScrollListener() {
            @Override
            public void onScrollStateChanged(RecyclerView rv, int newState) {
                int pos = lm.findFirstCompletelyVisibleItemPosition();
                if (pos >= 0) {
                    indicator.setText((pos + 1) + "/" + urls.size());
                }
            }
        });
    }

    // ---------- 规格 ----------

    private String defaultSkuId() {
        for (Sku s : skus) {
            if (s.isDefault) {
                return s.skuId;
            }
        }
        return skus.isEmpty() ? product.skuId : skus.get(0).skuId;
    }

    private Sku findSku(String id) {
        for (Sku s : skus) {
            if (s.skuId.equals(id)) {
                return s;
            }
        }
        return null;
    }

    private void renderSkus() {
        Sku selected = findSku(selectedSkuId);
        LinearLayout row = findViewById(R.id.sku_row);
        row.removeAllViews();
        // 只有一个规格时不必选择，只显示规格信息
        findViewById(R.id.sku_card).setVisibility(skus.isEmpty() ? View.GONE : View.VISIBLE);
        row.setVisibility(skus.size() > 1 ? View.VISIBLE : View.GONE);
        for (Sku s : skus) {
            row.addView(Chips.make(this, s.label(), s == selected, v -> {
                selectedSkuId = s.skuId;
                renderSkus();
            }));
        }
        String price = selected != null ? selected.price : product.price;
        String stockStatus = selected != null ? selected.stockStatus : product.stockStatus;
        TextView priceView = findViewById(R.id.detail_price);
        priceView.setText(Money.format(price));
        TextView market = findViewById(R.id.detail_market_price);
        boolean showMarket = Money.greater(product.marketPrice, price);
        market.setText(showMarket ? Money.format(product.marketPrice) : "");
        market.setContentDescription(showMarket ? "原价 " + Money.format(product.marketPrice) : null);
        StockLabels.apply(findViewById(R.id.detail_stock), stockStatus);
        findViewById(R.id.bottom_bar).setVisibility(View.VISIBLE);
        Button add = findViewById(R.id.add_to_cart);
        boolean soldOut = selected != null ? selected.stockQuantity <= 0 : "out_of_stock".equals(product.stockStatus);
        add.setEnabled(!soldOut && !adding);
        add.setText(adding ? "加入中…" : (soldOut ? "暂时缺货" : "加入购物车"));
        TextView info = findViewById(R.id.sku_info);
        if (selected == null) {
            info.setVisibility(View.GONE);
        } else {
            info.setVisibility(View.VISIBLE);
            String stockText = selected.stockQuantity > 0 ? "库存 " + selected.stockQuantity + " 件" : "暂时缺货";
            info.setText("已选：" + selected.skuName + " · " + Money.format(selected.price) + " · " + stockText);
        }
    }

    // ---------- 商品信息 ----------

    private void renderSections(Product p) {
        LinearLayout sections = findViewById(R.id.sections);
        sections.removeAllViews();
        addListSection(sections, "商品卖点", p.sellingPoints);
        if (!p.attributes.isEmpty()) {
            List<String> lines = new ArrayList<>();
            for (Product.Attribute a : p.attributes) {
                lines.add(a.key + "：" + a.display());
            }
            addListSection(sections, "商品参数", lines);
        }
        addListSection(sections, "适合", p.suitableFor);
        addListSection(sections, "不适合", p.notSuitableFor);
        addListSection(sections, "注意事项", p.riskNotes);
        if (!p.description.isEmpty()) {
            LinearLayout card = newCard(sections, "商品介绍");
            card.addView(bodyText(p.description));
        }
    }

    private void addListSection(LinearLayout parent, String title, List<String> lines) {
        if (lines.isEmpty()) {
            return;
        }
        LinearLayout card = newCard(parent, title);
        for (String line : lines) {
            card.addView(bodyText("• " + line));
        }
    }

    private LinearLayout newCard(LinearLayout parent, String title) {
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.VERTICAL);
        card.setBackgroundResource(R.drawable.bg_card);
        card.setPadding(dp(16), dp(16), dp(16), dp(16));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dp(12);
        card.setLayoutParams(lp);
        TextView t = new TextView(this);
        t.setText(title);
        t.setTextSize(16);
        t.setTextColor(getColor(R.color.text_primary));
        t.setTypeface(null, Typeface.BOLD);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            t.setAccessibilityHeading(true);
        }
        t.setPadding(0, 0, 0, dp(6));
        card.addView(t);
        parent.addView(card);
        return card;
    }

    private TextView bodyText(String s) {
        TextView v = new TextView(this);
        v.setText(s);
        v.setTextSize(14);
        v.setTextColor(getColor(R.color.text_primary));
        v.setLineSpacing(dp(2), 1f);
        v.setPadding(0, dp(3), 0, dp(3));
        return v;
    }

    // ---------- 评价 ----------

    private void loadReviews() {
        LinearLayout listView = findViewById(R.id.review_list);
        listView.removeAllViews();
        listView.addView(hintText("正在加载评价…", null));
        call(() -> app.api().reviews(productId, REVIEW_COUNT), new Async.Callback<PageResult<Review>>() {
            @Override
            public void onSuccess(PageResult<Review> page) {
                renderReviews(page);
            }

            @Override
            public void onError(ApiException e) {
                listView.removeAllViews();
                listView.addView(hintText("评价加载失败：" + e.getMessage() + "（点击重试）", v -> loadReviews()));
            }
        });
    }

    private void renderReviews(PageResult<Review> page) {
        ((TextView) findViewById(R.id.review_title)).setText(page.total > 0 ? "用户评价（" + page.total + "）" : "用户评价");
        LinearLayout listView = findViewById(R.id.review_list);
        listView.removeAllViews();
        if (page.items.isEmpty()) {
            listView.addView(hintText("暂无评价", null));
            return;
        }
        TimeZone zone = TimeZone.getDefault();
        for (Review r : page.items) {
            LinearLayout item = new LinearLayout(this);
            item.setOrientation(LinearLayout.VERTICAL);
            item.setPadding(0, dp(8), 0, dp(8));
            TextView head = new TextView(this);
            head.setText(r.reviewerName + "  " + r.stars());
            head.setContentDescription(r.reviewerName + "，" + r.rating + " 分");
            head.setTextColor(getColor(R.color.text_primary));
            head.setTextSize(14);
            item.addView(head);
            if (!r.content.isEmpty()) {
                item.addView(bodyText(r.content));
            }
            if (!r.tags.isEmpty()) {
                TextView tags = bodyText(TextUtils.join("  ", r.tags));
                tags.setTextColor(getColor(R.color.brand));
                item.addView(tags);
            }
            if (!r.merchantReply.isEmpty()) {
                TextView reply = bodyText("商家回复：" + r.merchantReply);
                reply.setBackgroundResource(R.drawable.bg_tag);
                reply.setPadding(dp(8), dp(6), dp(8), dp(6));
                item.addView(reply);
            }
            TextView time = new TextView(this);
            time.setText(Times.formatLocal(r.createdAt, zone));
            time.setTextSize(12);
            time.setTextColor(getColor(R.color.text_hint));
            item.addView(time);
            listView.addView(item);
        }
    }

    private TextView hintText(String s, View.OnClickListener onClick) {
        TextView v = bodyText(s);
        v.setTextColor(getColor(R.color.text_secondary));
        v.setMinHeight(onClick == null ? 0 : dp(48));
        v.setGravity(Gravity.CENTER_VERTICAL);
        v.setOnClickListener(onClick);
        return v;
    }

    private void setTextOrHide(int id, String text) {
        TextView v = findViewById(id);
        v.setText(text);
        v.setVisibility(text.isEmpty() ? View.GONE : View.VISIBLE);
    }
}
