package com.blink.shop.catalog;

import android.content.Context;
import android.content.Intent;
import android.net.ConnectivityManager;
import android.net.Network;
import android.os.Bundle;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.KeyEvent;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.recyclerview.widget.LinearLayoutManager;
import androidx.recyclerview.widget.RecyclerView;
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout;

import java.util.Collections;
import java.util.List;

import com.blink.shop.R;
import com.blink.shop.account.AccountActivity;
import com.blink.shop.account.LoginActivity;
import com.blink.shop.chat.ChatActivity;
import com.blink.shop.model.Cart;
import com.blink.shop.model.Category;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Product;
import com.blink.shop.model.Session;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.CartButton;
import com.blink.shop.ui.Chips;
import com.blink.shop.ui.Pager;
import com.blink.shop.ui.StateView;

/** 首页：商品搜索、分类筛选和分页列表。 */
public final class ProductListActivity extends BaseActivity implements ProductAdapter.Listener {

    private static final String STATE_KEYWORD = "keyword";
    private static final String STATE_CATEGORY = "category";

    private EditText searchInput;
    private TextView searchClear;
    private TextView accountButton;
    private LinearLayout categoryRow;
    private LinearLayout subcategoryRow;
    private View subcategoryScroll;
    private SwipeRefreshLayout refresh;
    private RecyclerView list;
    private StateView state;
    private final ProductAdapter adapter = new ProductAdapter(this);
    private final Pager<Product> pager = new Pager<>(p -> p.productId);

    /** 已提交的搜索词（输入框里未提交的不算）。 */
    private String keyword = "";
    private String categoryId = "";
    private List<Category> tree = Collections.emptyList();
    private boolean categoriesLoading;
    private ApiException lastError;
    private ConnectivityManager.NetworkCallback networkCallback;
    private int apiVersion;
    private CartButton cartButton;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_product_list);
        searchInput = findViewById(R.id.search_input);
        searchClear = findViewById(R.id.search_clear);
        accountButton = findViewById(R.id.account_button);
        categoryRow = findViewById(R.id.category_row);
        subcategoryRow = findViewById(R.id.subcategory_row);
        subcategoryScroll = findViewById(R.id.subcategory_scroll);
        refresh = findViewById(R.id.refresh);
        list = findViewById(R.id.product_list);
        state = new StateView(findViewById(android.R.id.content));

        if (savedInstanceState != null) {
            keyword = savedInstanceState.getString(STATE_KEYWORD, "");
            categoryId = savedInstanceState.getString(STATE_CATEGORY, "");
            searchInput.setText(keyword);
        }

        apiVersion = app.apiVersion();
        adapter.setImages(app.images(), dp(96));
        LinearLayoutManager lm = new LinearLayoutManager(this);
        list.setLayoutManager(lm);
        list.setAdapter(adapter);
        list.addOnScrollListener(new RecyclerView.OnScrollListener() {
            @Override
            public void onScrolled(RecyclerView rv, int dx, int dy) {
                if (dy > 0 && lm.findLastVisibleItemPosition() >= adapter.getItemCount() - 3) {
                    loadMore();
                }
            }
        });
        refresh.setColorSchemeColors(getColor(R.color.brand));
        refresh.setOnRefreshListener(() -> {
            if (tree.isEmpty()) {
                loadCategories();
            }
            reload(false);
        });

        searchInput.setOnEditorActionListener((v, actionId, event) -> {
            boolean enter = event != null && event.getKeyCode() == KeyEvent.KEYCODE_ENTER && event.getAction() == KeyEvent.ACTION_DOWN;
            if (actionId == EditorInfo.IME_ACTION_SEARCH || enter) {
                submitSearch();
                return true;
            }
            return false;
        });
        searchInput.addTextChangedListener(new TextWatcher() {
            @Override
            public void beforeTextChanged(CharSequence s, int start, int count, int after) {
            }

            @Override
            public void onTextChanged(CharSequence s, int start, int before, int count) {
                searchClear.setVisibility(s.length() > 0 ? View.VISIBLE : View.GONE);
            }

            @Override
            public void afterTextChanged(Editable s) {
            }
        });
        searchClear.setVisibility(keyword.isEmpty() ? View.GONE : View.VISIBLE);
        searchClear.setOnClickListener(v -> {
            searchInput.setText("");
            if (!keyword.isEmpty()) {
                keyword = "";
                reload(true);
            }
        });
        findViewById(R.id.search_button).setOnClickListener(v -> submitSearch());
        accountButton.setOnClickListener(v -> openAccount());
        // AI 导购需要登录；未登录时聊天页会转到登录页
        findViewById(R.id.chat_button).setOnClickListener(v -> startActivity(ChatActivity.intent(this, "")));
        cartButton = new CartButton(this, findViewById(R.id.cart_button));

        renderAccountButton(app.sessions().current());
        renderCategories();
        loadCategories();
        reload(true);
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putString(STATE_KEYWORD, keyword);
        out.putString(STATE_CATEGORY, categoryId);
    }

    @Override
    protected void onStart() {
        super.onStart();
        watchNetwork();
        if (apiVersion != app.apiVersion()) {
            // 在设置里换了服务地址：旧数据和图片地址都作废
            apiVersion = app.apiVersion();
            adapter.setImages(app.images(), dp(96));
            tree = Collections.emptyList();
            categoryId = "";
            renderCategories();
            loadCategories();
            reload(true);
        }
    }

    @Override
    protected void onStop() {
        unwatchNetwork();
        super.onStop();
    }

    @Override
    protected void onSessionUpdated(Session session) {
        renderAccountButton(session);
        if (cartButton != null) {
            cartButton.refresh();
        }
    }

    @Override
    public void onAddToCart(Product p) {
        if (!app.sessions().isLoggedIn()) {
            toast("请先登录");
            startActivity(new Intent(this, LoginActivity.class));
            return;
        }
        call(() -> app.api().addToCart(p.productId, p.skuId, 1), new Async.Callback<Cart>() {
            @Override
            public void onSuccess(Cart cart) {
                toast("已加入购物车");
                cartButton.show(cart.totalQuantity());
            }

            @Override
            public void onError(ApiException e) {
                toast(e.getMessage());
            }
        });
    }

    // ---------- 加载 ----------

    /** 从第一页重新加载。showLoading 为 false 时用下拉刷新的转圈代替整页加载。 */
    private void reload(boolean showLoading) {
        int gen = pager.reset();
        lastError = null;
        if (showLoading) {
            adapter.setItems(Collections.emptyList(), ProductAdapter.Footer.NONE);
            state.loading("正在加载商品…");
        }
        String kw = keyword;
        String cat = categoryId;
        call(() -> app.api().products(kw, cat, 1), new Async.Callback<PageResult<Product>>() {
            @Override
            public void onSuccess(PageResult<Product> page) {
                if (!pager.accept(gen, page)) {
                    return;
                }
                refresh.setRefreshing(false);
                list.scrollToPosition(0);
                render();
            }

            @Override
            public void onError(ApiException e) {
                if (!pager.fail(gen)) {
                    return;
                }
                refresh.setRefreshing(false);
                lastError = e;
                adapter.setItems(Collections.emptyList(), ProductAdapter.Footer.NONE);
                state.error("商品加载失败", e, () -> reload(true));
            }
        });
    }

    private void loadMore() {
        int gen = pager.next();
        if (gen < 0) {
            return;
        }
        fetchNext(gen);
    }

    @Override
    public void onRetryMore() {
        int gen = pager.retryNext();
        if (gen >= 0) {
            fetchNext(gen);
        }
    }

    private void fetchNext(int gen) {
        render();
        String kw = keyword;
        String cat = categoryId;
        int page = pager.nextPage();
        call(() -> app.api().products(kw, cat, page), new Async.Callback<PageResult<Product>>() {
            @Override
            public void onSuccess(PageResult<Product> result) {
                if (pager.accept(gen, result)) {
                    render();
                }
            }

            @Override
            public void onError(ApiException e) {
                if (pager.fail(gen)) {
                    lastError = e;
                    toast(e.getMessage());
                    render();
                }
            }
        });
    }

    private void render() {
        List<Product> items = pager.items();
        if (items.isEmpty() && !pager.isLoading()) {
            adapter.setItems(items, ProductAdapter.Footer.NONE);
            if (keyword.isEmpty() && categoryId.isEmpty()) {
                state.empty("暂无商品", "稍后再来看看");
            } else {
                state.empty("没有找到相关商品", "换个关键词或分类试试");
            }
            return;
        }
        state.hide();
        ProductAdapter.Footer footer;
        if (pager.isLoading()) {
            footer = ProductAdapter.Footer.LOADING;
        } else if (pager.isFailed()) {
            footer = ProductAdapter.Footer.ERROR;
        } else if (!pager.hasMore() && items.size() > 3) {
            footer = ProductAdapter.Footer.END;
        } else {
            footer = ProductAdapter.Footer.NONE;
        }
        adapter.setItems(items, footer);
    }

    private void submitSearch() {
        String kw = searchInput.getText().toString().trim();
        hideKeyboard();
        searchInput.clearFocus();
        if (kw.equals(keyword) && lastError == null) {
            return;
        }
        keyword = kw;
        reload(true);
    }

    // ---------- 分类 ----------

    private void loadCategories() {
        if (categoriesLoading) {
            return;
        }
        categoriesLoading = true;
        call(() -> app.api().categories(), new Async.Callback<List<Category>>() {
            @Override
            public void onSuccess(List<Category> value) {
                categoriesLoading = false;
                tree = value;
                renderCategories();
            }

            @Override
            public void onError(ApiException e) {
                // 分类失败不挡住商品列表；下拉刷新或网络恢复时再试
                categoriesLoading = false;
            }
        });
    }

    private void renderCategories() {
        categoryRow.removeAllViews();
        Category selected = Category.find(tree, categoryId);
        Category top = selected == null ? null : (selected.parentId.isEmpty() ? selected : Category.find(tree, selected.parentId));
        categoryRow.addView(Chips.make(this, "全部", top == null, v -> selectCategory("")));
        for (Category c : tree) {
            categoryRow.addView(Chips.make(this, c.name, top != null && top.categoryId.equals(c.categoryId), v -> selectCategory(c.categoryId)));
        }
        subcategoryRow.removeAllViews();
        if (top != null && !top.children.isEmpty()) {
            subcategoryScroll.setVisibility(View.VISIBLE);
            subcategoryRow.addView(Chips.make(this, "全部" + top.name, selected == top, v -> selectCategory(top.categoryId)));
            for (Category c : top.children) {
                subcategoryRow.addView(Chips.make(this, c.name, selected == c, v -> selectCategory(c.categoryId)));
            }
        } else {
            subcategoryScroll.setVisibility(View.GONE);
        }
    }

    private void selectCategory(String id) {
        if (id.equals(categoryId)) {
            return;
        }
        categoryId = id;
        renderCategories();
        reload(true);
    }

    // ---------- 账户 ----------

    private void renderAccountButton(Session s) {
        if (s == null) {
            accountButton.setText(R.string.login);
            accountButton.setContentDescription("登录或注册");
        } else {
            accountButton.setText(s.account.name());
            accountButton.setContentDescription("我的账户：" + s.account.name());
        }
    }

    private void openAccount() {
        Class<?> target = app.sessions().isLoggedIn() ? AccountActivity.class : LoginActivity.class;
        startActivity(new Intent(this, target));
    }

    @Override
    public void onProductClick(Product p) {
        startActivity(ProductDetailActivity.intent(this, p.productId, p.name));
    }

    // ---------- 网络恢复 ----------

    /** 因网络失败停在错误页时，网络恢复后自动重试。 */
    private void watchNetwork() {
        ConnectivityManager cm = (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
        if (cm == null || networkCallback != null) {
            return;
        }
        networkCallback = new ConnectivityManager.NetworkCallback() {
            @Override
            public void onAvailable(Network network) {
                runOnUiThread(() -> {
                    if (isFinishing() || lastError == null || !lastError.isNetwork()) {
                        return;
                    }
                    if (tree.isEmpty()) {
                        loadCategories();
                    }
                    if (state.isShowingError()) {
                        reload(true);
                    } else if (pager.isFailed()) {
                        onRetryMore();
                    }
                });
            }
        };
        try {
            cm.registerDefaultNetworkCallback(networkCallback);
        } catch (RuntimeException e) {
            networkCallback = null;
        }
    }

    private void unwatchNetwork() {
        ConnectivityManager cm = (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
        if (cm != null && networkCallback != null) {
            try {
                cm.unregisterNetworkCallback(networkCallback);
            } catch (RuntimeException ignored) {
                // 已注销
            }
        }
        networkCallback = null;
    }

    private void hideKeyboard() {
        InputMethodManager imm = (InputMethodManager) getSystemService(Context.INPUT_METHOD_SERVICE);
        if (imm != null) {
            imm.hideSoftInputFromWindow(searchInput.getWindowToken(), 0);
        }
    }
}
