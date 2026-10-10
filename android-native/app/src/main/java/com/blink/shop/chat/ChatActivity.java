package com.blink.shop.chat;

import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.text.InputType;
import android.view.Gravity;
import android.view.KeyEvent;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.util.List;
import java.util.concurrent.Executor;

import androidx.core.view.GravityCompat;
import androidx.drawerlayout.widget.DrawerLayout;
import androidx.recyclerview.widget.LinearLayoutManager;
import androidx.recyclerview.widget.RecyclerView;

import org.json.JSONObject;

import com.blink.shop.R;
import com.blink.shop.cart.CartActivity;
import com.blink.shop.catalog.ProductDetailActivity;
import com.blink.shop.catalog.ProductListActivity;
import com.blink.shop.coupon.CouponActivity;
import com.blink.shop.model.ChatSession;
import com.blink.shop.model.PageResult;
import com.blink.shop.net.ApiException;
import com.blink.shop.order.OrderDetailActivity;
import com.blink.shop.order.OrderListActivity;
import com.blink.shop.settings.SettingsActivity;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;
import com.blink.shop.ui.Chips;
import com.blink.shop.ui.StateView;

/**
 * AI 导购聊天页：历史抽屉、流式回答（步骤、Markdown 正文、结构化卡片、追问）、停止生成、失败重试。
 * 状态都在 ChatController 里；页面只负责渲染和把用户操作转给控制器。进程被回收后按会话 ID 从服务端恢复，不重复发送。
 */
public final class ChatActivity extends BaseActivity implements ChatController.Listener, ChatAdapter.Listener, BlockViews.Actions,
        ChatHistoryAdapter.Listener {

    private static final String EXTRA_PROMPT = "prompt";
    private static final String STATE_SESSION = "session_id";
    private static final String STATE_DRAFT = "draft";

    /** 打开聊天页；prompt 非空时预填到输入框（例如从商品页“问导购”进来）。 */
    public static Intent intent(Context c, String prompt) {
        return new Intent(c, ChatActivity.class).putExtra(EXTRA_PROMPT, prompt == null ? "" : prompt);
    }

    private ChatController controller;
    private ChatAdapter adapter;
    private ChatHistoryAdapter historyAdapter;
    private DrawerLayout drawer;
    private RecyclerView list;
    private View welcome;
    private LinearLayout welcomeChips;
    private EditText input;
    private TextView send;
    private StateView state;
    private TextView historyEmpty;
    private EditText historySearch;
    private boolean stickToBottom = true;
    private String historyKeyword = "";
    private Async.Handle historyCall;

    @Override
    protected boolean requiresLogin() {
        return true;
    }

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        if (isFinishing()) {
            return;
        }
        setContentView(R.layout.activity_chat);
        drawer = findViewById(R.id.drawer);
        list = findViewById(R.id.turns);
        welcome = findViewById(R.id.welcome);
        welcomeChips = findViewById(R.id.welcome_chips);
        input = findViewById(R.id.input);
        send = findViewById(R.id.send_button);
        state = new StateView(findViewById(R.id.drawer));
        historyEmpty = findViewById(R.id.history_empty);
        historySearch = findViewById(R.id.history_search);

        Executor main = new android.os.Handler(android.os.Looper.getMainLooper())::post;
        Executor io = r -> Async.run(() -> {
            r.run();
            return null;
        }, new Async.Callback<Object>() {
            @Override
            public void onSuccess(Object value) {
            }

            @Override
            public void onError(ApiException e) {
            }
        });
        controller = new ChatController(ChatBackend.of(app.api()), io, main);

        adapter = new ChatAdapter(new BlockViews(this, app.images(), this), this);
        LinearLayoutManager lm = new LinearLayoutManager(this);
        lm.setStackFromEnd(true);
        list.setLayoutManager(lm);
        list.setAdapter(adapter);
        list.setItemAnimator(null);
        list.addOnScrollListener(new RecyclerView.OnScrollListener() {
            @Override
            public void onScrolled(RecyclerView rv, int dx, int dy) {
                if (dy != 0) {
                    stickToBottom = !rv.canScrollVertically(1);
                }
            }
        });

        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        findViewById(R.id.history_button).setOnClickListener(v -> openHistory());
        findViewById(R.id.new_button).setOnClickListener(v -> startNewSession());
        findViewById(R.id.history_new).setOnClickListener(v -> {
            drawer.closeDrawers();
            startNewSession();
        });
        send.setOnClickListener(v -> onSendOrStop());
        input.setOnEditorActionListener((v, actionId, event) -> {
            if (actionId == EditorInfo.IME_ACTION_SEND || (event != null && event.getKeyCode() == KeyEvent.KEYCODE_ENTER
                    && event.getAction() == KeyEvent.ACTION_DOWN && !event.isShiftPressed())) {
                onSendOrStop();
                return true;
            }
            return false;
        });
        for (String q : new String[]{"推荐一款通勤降噪耳机", "3000 以内拍照好的手机", "看看我的购物车", "有什么优惠券"}) {
            TextView chip = Chips.make(this, q, false, v -> sendText(q));
            LinearLayout.LayoutParams lp = (LinearLayout.LayoutParams) chip.getLayoutParams();
            lp.gravity = Gravity.CENTER_HORIZONTAL;
            lp.bottomMargin = dp(6);
            welcomeChips.addView(chip);
        }

        RecyclerView history = findViewById(R.id.history_list);
        historyAdapter = new ChatHistoryAdapter(this);
        history.setLayoutManager(new LinearLayoutManager(this));
        history.setAdapter(historyAdapter);
        historySearch.setOnEditorActionListener((v, actionId, event) -> {
            historyKeyword = historySearch.getText().toString().trim();
            loadHistory();
            return true;
        });
        drawer.addDrawerListener(new DrawerLayout.SimpleDrawerListener() {
            @Override
            public void onDrawerOpened(View drawerView) {
                loadHistory();
            }
        });

        String prompt = getIntent().getStringExtra(EXTRA_PROMPT);
        if (saved != null) {
            input.setText(saved.getString(STATE_DRAFT, ""));
            String sid = saved.getString(STATE_SESSION, "");
            if (!sid.isEmpty()) {
                controller.loadSession(sid);
            }
        } else if (prompt != null && !prompt.isEmpty()) {
            input.setText(prompt);
            input.setSelection(prompt.length());
        }
        controller.attach(this);
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putString(STATE_SESSION, controller.sessionId());
        out.putString(STATE_DRAFT, input.getText().toString());
    }

    @Override
    protected void onStart() {
        super.onStart();
        if (controller != null) {
            controller.attach(this); // 后台期间收到的内容，回来时一次补齐
        }
    }

    @Override
    protected void onStop() {
        if (controller != null) {
            controller.detach(); // 后台继续接收，不再刷新界面
        }
        super.onStop();
    }

    @Override
    protected void onDestroy() {
        if (controller != null) {
            controller.dispose();
        }
        super.onDestroy();
    }

    @Override
    public void onBackPressed() {
        if (drawer != null && drawer.isDrawerOpen(GravityCompat.START)) {
            drawer.closeDrawers();
            return;
        }
        super.onBackPressed();
    }

    // ---------- 发送 / 停止 ----------

    private void onSendOrStop() {
        if (controller.isStreaming()) {
            controller.stop();
            return;
        }
        String text = input.getText().toString().trim();
        if (text.isEmpty()) {
            toast("先说说想买什么");
            return;
        }
        if (sendText(text)) {
            input.setText("");
        }
    }

    private boolean sendText(String text) {
        if (controller.isBusy()) {
            toast("正在回答，请稍候或先停止");
            return false;
        }
        if (!controller.send(text)) {
            return false;
        }
        stickToBottom = true;
        hideKeyboard();
        return true;
    }

    private void startNewSession() {
        if (controller.isStreaming()) {
            controller.stop();
        }
        controller.newSession();
        input.requestFocus();
    }

    private void hideKeyboard() {
        InputMethodManager imm = (InputMethodManager) getSystemService(Context.INPUT_METHOD_SERVICE);
        if (imm != null) {
            imm.hideSoftInputFromWindow(input.getWindowToken(), 0);
        }
    }

    // ---------- 控制器回调 ----------

    @Override
    public void onChanged() {
        List<ChatTurn> turns = controller.turns();
        adapter.submit(turns);
        boolean streaming = controller.isStreaming();
        send.setText(streaming ? "停止" : "发送");
        send.setContentDescription(streaming ? "停止生成" : "发送");
        send.setEnabled(streaming || !controller.isBusy());
        send.setAlpha(send.isEnabled() ? 1f : 0.6f);
        if (controller.isLoading()) {
            state.loading("正在读取会话…");
            welcome.setVisibility(View.GONE);
        } else if (!controller.loadError().isEmpty()) {
            state.error("会话读取失败", ApiException.local(controller.loadError(), null), () -> controller.loadSession(controller.sessionId()));
            welcome.setVisibility(View.GONE);
        } else {
            state.hide();
            welcome.setVisibility(turns.isEmpty() ? View.VISIBLE : View.GONE);
        }
        if (stickToBottom && !turns.isEmpty()) {
            list.post(() -> list.scrollToPosition(adapter.getItemCount() - 1));
        }
    }

    @Override
    public void onSessionChanged(String sessionId) {
        historyAdapter.setActive(sessionId);
    }

    // ---------- 列表里的操作 ----------

    @Override
    public void onRetry(ChatTurn turn) {
        stickToBottom = true;
        if (!controller.retry(turn)) {
            toast("正在回答，请稍候");
        }
    }

    @Override
    public void onResend(ChatTurn turn) {
        stickToBottom = true;
        if (!controller.resend(turn)) {
            toast("正在回答，请稍候");
        }
    }

    @Override
    public void onFollowup(String question) {
        sendText(question);
    }

    @Override
    public void openProduct(String productId, String name) {
        if (!productId.isEmpty()) {
            startActivity(ProductDetailActivity.intent(this, productId, name));
        }
    }

    @Override
    public void openOrder(String orderId) {
        if (!orderId.isEmpty()) {
            startActivity(OrderDetailActivity.intent(this, orderId));
        }
    }

    @Override
    public void navigate(String target, JSONObject params) {
        switch (target) {
            case "cart":
                startActivity(new Intent(this, CartActivity.class));
                break;
            case "orders":
                startActivity(OrderListActivity.intent(this, ""));
                break;
            case "order_detail":
                openOrder(params.optString("order_id", ""));
                break;
            case "products":
                startActivity(new Intent(this, ProductListActivity.class).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP));
                break;
            case "product_detail":
                openProduct(params.optString("product_id", ""), params.optString("name", ""));
                break;
            case "coupons":
                startActivity(new Intent(this, CouponActivity.class));
                break;
            case "sessions":
                openHistory();
                break;
            case "settings":
                startActivity(new Intent(this, SettingsActivity.class));
                break;
            default:
                toast("当前版本暂不支持打开该页面");
        }
    }

    // ---------- 历史抽屉 ----------

    private void openHistory() {
        drawer.openDrawer(GravityCompat.START);
    }

    private void loadHistory() {
        if (historyCall != null) {
            historyCall.cancel();
        }
        String keyword = historyKeyword;
        historyCall = call(() -> app.api().chatSessions(keyword, 1), new Async.Callback<PageResult<ChatSession>>() {
            @Override
            public void onSuccess(PageResult<ChatSession> page) {
                historyAdapter.submit(page.items, controller.sessionId());
                historyEmpty.setText(keyword.isEmpty() ? "还没有历史会话" : "没有匹配的会话");
                historyEmpty.setVisibility(page.items.isEmpty() ? View.VISIBLE : View.GONE);
            }

            @Override
            public void onError(ApiException e) {
                historyEmpty.setText("读取失败：" + e.getMessage());
                historyEmpty.setVisibility(View.VISIBLE);
            }
        });
    }

    @Override
    public void onOpen(ChatSession s) {
        drawer.closeDrawers();
        if (s.sessionId.equals(controller.sessionId())) {
            return;
        }
        if (controller.isStreaming()) {
            controller.stop();
        }
        stickToBottom = true;
        controller.loadSession(s.sessionId);
    }

    @Override
    public void onMore(ChatSession s) {
        String[] items = {s.pinned ? "取消置顶" : "置顶", "重命名", "删除"};
        new AlertDialog.Builder(this).setTitle(s.displayTitle()).setItems(items, (d, which) -> {
            switch (which) {
                case 0:
                    call(() -> app.api().pinChatSession(s.sessionId, !s.pinned), refreshHistory(s.pinned ? "已取消置顶" : "已置顶"));
                    break;
                case 1:
                    rename(s);
                    break;
                default:
                    confirmDelete(s);
            }
        }).show();
    }

    private <T> Async.Callback<T> refreshHistory(String okMessage) {
        return new Async.Callback<T>() {
            @Override
            public void onSuccess(T value) {
                toast(okMessage);
                loadHistory();
            }

            @Override
            public void onError(ApiException e) {
                toast(e.getMessage());
            }
        };
    }

    private void rename(ChatSession s) {
        EditText field = new EditText(this);
        field.setInputType(InputType.TYPE_CLASS_TEXT);
        field.setText(s.displayTitle());
        field.setSelection(field.getText().length());
        field.setSingleLine(true);
        new AlertDialog.Builder(this).setTitle("重命名会话").setView(field).setNegativeButton("取消", null)
                .setPositiveButton("保存", (d, w) -> {
                    String title = field.getText().toString().trim();
                    if (title.isEmpty() || title.length() > 128) {
                        toast("标题需要 1–128 个字");
                        return;
                    }
                    call(() -> app.api().renameChatSession(s.sessionId, title), refreshHistory("已重命名"));
                }).show();
    }

    private void confirmDelete(ChatSession s) {
        new AlertDialog.Builder(this).setTitle("删除会话").setMessage("删除后不再显示“" + s.displayTitle() + "”。确定删除？")
                .setNegativeButton("取消", null).setPositiveButton("删除", (d, w) -> call(() -> {
                    app.api().deleteChatSession(s.sessionId);
                    return null;
                }, new Async.Callback<Object>() {
                    @Override
                    public void onSuccess(Object value) {
                        toast("已删除");
                        if (s.sessionId.equals(controller.sessionId())) {
                            controller.newSession();
                        }
                        loadHistory();
                    }

                    @Override
                    public void onError(ApiException e) {
                        toast(e.getMessage());
                    }
                })).show();
    }
}
