package com.blink.shop.order;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.Gravity;
import android.view.View;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;

import com.blink.shop.R;
import com.blink.shop.data.Drafts;
import com.blink.shop.model.Order;
import com.blink.shop.model.Session;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Busy;

/** 评价一件已完成订单里的商品。输入随时存为本地草稿（App 被杀后再进来还在），提交成功后清除。 */
public final class ReviewActivity extends BaseActivity {

    private static final String EXTRA_ORDER_ID = "order_id";
    private static final String EXTRA_ITEM_ID = "item_id";
    private static final String EXTRA_NAME = "name";
    private static final String EXTRA_SKU = "sku";
    private static final String EXTRA_IMAGE = "image";
    private static final String STATE_RATING = "rating";
    private static final String[] RATING_TEXT = {"", "很差", "较差", "一般", "满意", "非常满意"};

    public static Intent intent(Context c, String orderId, Order.Item item) {
        return new Intent(c, ReviewActivity.class).putExtra(EXTRA_ORDER_ID, orderId).putExtra(EXTRA_ITEM_ID, item.orderItemId)
                .putExtra(EXTRA_NAME, item.name).putExtra(EXTRA_SKU, item.skuName).putExtra(EXTRA_IMAGE, item.imageUrl);
    }

    private String orderId;
    private String itemId;
    private String accountId;
    private int rating = 5;
    private EditText content;
    private EditText tags;
    private TextView error;
    private Busy busy;
    private boolean submitting;
    private boolean done;

    @Override
    protected boolean requiresLogin() {
        return true;
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (isFinishing()) {
            return;
        }
        orderId = getIntent().getStringExtra(EXTRA_ORDER_ID);
        itemId = getIntent().getStringExtra(EXTRA_ITEM_ID);
        Session s = app.sessions().current();
        if (orderId == null || itemId == null || s == null) {
            finish();
            return;
        }
        accountId = s.account.accountId;
        setContentView(R.layout.activity_review);
        content = findViewById(R.id.review_content);
        tags = findViewById(R.id.review_tags);
        error = findViewById(R.id.review_error);
        busy = new Busy(findViewById(android.R.id.content));
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        ((TextView) findViewById(R.id.review_product)).setText(getIntent().getStringExtra(EXTRA_NAME) + "\n"
                + getIntent().getStringExtra(EXTRA_SKU));
        app.images().load(findViewById(R.id.review_image), getIntent().getStringExtra(EXTRA_IMAGE), dp(64));

        Drafts.Review draft = app.drafts().review(accountId, itemId);
        if (savedInstanceState != null) {
            rating = savedInstanceState.getInt(STATE_RATING, 5);
        } else if (draft != null) {
            rating = draft.rating;
            content.setText(draft.content);
            tags.setText(draft.tags);
            toast("已恢复上次未提交的内容");
        }
        TextWatcher save = new TextWatcher() {
            @Override
            public void beforeTextChanged(CharSequence s, int start, int count, int after) {
            }

            @Override
            public void onTextChanged(CharSequence s, int start, int before, int count) {
            }

            @Override
            public void afterTextChanged(Editable s) {
                updateCount();
                saveDraft();
            }
        };
        content.addTextChangedListener(save);
        tags.addTextChangedListener(save);
        findViewById(R.id.review_submit).setOnClickListener(v -> submit());
        renderStars();
        updateCount();
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putInt(STATE_RATING, rating);
    }

    @Override
    protected void onPause() {
        saveDraft();
        super.onPause();
    }

    private void renderStars() {
        LinearLayout row = findViewById(R.id.star_row);
        row.removeAllViews();
        for (int i = 1; i <= 5; i++) {
            int value = i;
            TextView star = new TextView(this);
            star.setText(i <= rating ? "★" : "☆");
            star.setTextSize(30);
            star.setTextColor(0xFFF5A623);
            star.setGravity(Gravity.CENTER);
            star.setContentDescription(i + " 分" + (i == rating ? "，已选中" : ""));
            star.setOnClickListener(v -> {
                rating = value;
                renderStars();
                saveDraft();
            });
            row.addView(star, new LinearLayout.LayoutParams(dp(48), dp(48)));
        }
        ((TextView) findViewById(R.id.rating_label)).setText(rating + " 分 · " + RATING_TEXT[rating]);
    }

    private void updateCount() {
        ((TextView) findViewById(R.id.content_count)).setText(content.getText().length() + "/500");
    }

    private void saveDraft() {
        if (done || content == null) {
            return;
        }
        String c = content.getText().toString();
        String t = tags.getText().toString();
        if (c.trim().isEmpty() && t.trim().isEmpty() && rating == 5) {
            app.drafts().clearReview(accountId, itemId);
        } else {
            app.drafts().saveReview(accountId, itemId, new Drafts.Review(rating, c, t));
        }
    }

    /** 标签输入按空格、逗号、顿号拆开，去掉空的和重复的；数量和长度规则由服务端检查。 */
    static List<String> splitTags(String raw) {
        LinkedHashSet<String> out = new LinkedHashSet<>();
        for (String part : raw.split("[\\s,，、;；]+")) {
            String t = part.trim();
            if (!t.isEmpty()) {
                out.add(t);
            }
        }
        return new ArrayList<>(out);
    }

    private void submit() {
        if (submitting) {
            return;
        }
        String c = content.getText().toString().trim();
        if (c.isEmpty()) {
            showError("请填写评价内容");
            content.requestFocus();
            return;
        }
        List<String> tagList = splitTags(tags.getText().toString());
        int r = rating;
        submitting = true;
        error.setVisibility(View.GONE);
        busy.show("正在提交评价…");
        call(() -> app.api().review(orderId, itemId, r, c, tagList), new Async.Callback<String>() {
            @Override
            public void onSuccess(String reviewId) {
                finishDone("评价已提交");
            }

            @Override
            public void onError(ApiException e) {
                submitting = false;
                busy.hide();
                if ("review_exists".equals(e.code())) {
                    finishDone("这件商品已经评价过了");
                    return;
                }
                showError(e.getMessage());
                if ("content".equals(e.field())) {
                    content.requestFocus();
                } else if ("tags".equals(e.field())) {
                    tags.requestFocus();
                }
            }
        });
    }

    private void finishDone(String message) {
        done = true;
        app.drafts().clearReview(accountId, itemId);
        busy.hide();
        toast(message);
        finish();
    }

    private void showError(String message) {
        error.setText(message);
        error.setVisibility(View.VISIBLE);
    }
}
