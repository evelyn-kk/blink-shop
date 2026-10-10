package com.blink.shop.chat;

import android.Manifest;
import android.app.AlertDialog;
import android.content.ActivityNotFoundException;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Bitmap;
import android.net.Uri;
import android.os.Bundle;
import android.provider.MediaStore;
import android.provider.Settings;
import android.text.InputType;
import android.view.Gravity;
import android.view.KeyEvent;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.io.File;
import java.io.IOException;
import java.util.List;
import java.util.concurrent.Executor;

import androidx.core.content.FileProvider;
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
import com.blink.shop.model.SpeechConfig;
import com.blink.shop.net.ApiClient;
import com.blink.shop.voice.AudioRecordMic;
import com.blink.shop.voice.MediaAudioPlayer;
import com.blink.shop.voice.OkHttpSpeechConnector;
import com.blink.shop.voice.TtsController;
import com.blink.shop.voice.VoiceInputController;
import com.blink.shop.voice.VoicePrefs;
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
        ChatHistoryAdapter.Listener, AttachmentController.Listener, VoiceInputController.Listener, TtsController.Listener {

    private static final String EXTRA_PROMPT = "prompt";
    private static final String STATE_SESSION = "session_id";
    private static final String STATE_DRAFT = "draft";
    private static final String STATE_CAMERA_FILE = "camera_file";
    private static final int REQUEST_GALLERY = 41;
    private static final int REQUEST_CAMERA = 42;
    private static final int REQUEST_CAMERA_PERMISSION = 43;
    private static final int REQUEST_MIC_PERMISSION = 44;

    /** 打开聊天页；prompt 非空时预填到输入框（例如从商品页“问导购”进来）。 */
    public static Intent intent(Context c, String prompt) {
        return new Intent(c, ChatActivity.class).putExtra(EXTRA_PROMPT, prompt == null ? "" : prompt);
    }

    private ChatController controller;
    private AttachmentController attachment;
    private View attachmentBar;
    private ImageView attachmentPreview;
    private TextView attachmentStatus;
    private TextView attachmentRetry;
    /** 拍照时交给相机应用写入的临时文件（页面重建后从 savedInstanceState 恢复）。 */
    private File cameraFile;
    private VoiceInputController voice;
    private TtsController tts;
    private SpeechConfig speechConfig = SpeechConfig.DISABLED;
    private View micButton;
    private View voiceBar;
    private TextView voiceStatus;
    /** 开始语音输入时输入框里的原文（取消时还原）；voiceBase 是转写结果接在后面的前缀。 */
    private String voiceOriginal = "";
    private String voiceBase = "";
    private Async.Handle speechConfigCall;
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
        ChatBackend backend = ChatBackend.of(app.api());
        controller = new ChatController(backend, io, main);
        attachment = new AttachmentController(backend::uploadImage, io, main);
        attachment.setListener(this);
        attachmentBar = findViewById(R.id.attachment_bar);
        attachmentPreview = findViewById(R.id.attachment_preview);
        attachmentStatus = findViewById(R.id.attachment_status);
        attachmentRetry = findViewById(R.id.attachment_retry);
        findViewById(R.id.attach_button).setOnClickListener(v -> chooseImageSource());
        findViewById(R.id.attachment_remove).setOnClickListener(v -> attachment.clear());
        attachmentRetry.setOnClickListener(v -> attachment.retry());

        voice = new VoiceInputController(new OkHttpSpeechConnector(app.api()), new AudioRecordMic(), main);
        voice.setListener(this);
        tts = new TtsController(text -> {
            ApiClient.Bytes b = app.api().tts(text);
            return new TtsController.Audio(b.data, b.contentType);
        }, new MediaAudioPlayer(this), io, main);
        tts.setListener(this);
        micButton = findViewById(R.id.mic_button);
        voiceBar = findViewById(R.id.voice_bar);
        voiceStatus = findViewById(R.id.voice_status);
        micButton.setOnClickListener(v -> onMicClicked());
        findViewById(R.id.voice_cancel).setOnClickListener(v -> {
            voice.cancel();
            input.setText(voiceOriginal);
            input.setSelection(voiceOriginal.length());
        });

        adapter = new ChatAdapter(new BlockViews(this, app.images(), this), this);
        adapter.setSpeakState(new ChatAdapter.SpeakState() {
            @Override
            public boolean available() {
                return speechConfig.ttsEnabled && VoicePrefs.enabled(ChatActivity.this);
            }

            @Override
            public boolean loading(ChatTurn turn) {
                return tts.isLoading(turn.clientMessageId);
            }

            @Override
            public boolean playing(ChatTurn turn) {
                return tts.isActive(turn.clientMessageId) && !tts.isLoading(turn.clientMessageId);
            }
        });
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
            String camera = saved.getString(STATE_CAMERA_FILE, "");
            if (!camera.isEmpty()) {
                cameraFile = new File(camera);
            }
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
        out.putString(STATE_CAMERA_FILE, cameraFile == null ? "" : cameraFile.getAbsolutePath());
    }

    @Override
    protected void onStart() {
        super.onStart();
        if (controller != null) {
            controller.attach(this); // 后台期间收到的内容，回来时一次补齐
            loadSpeechConfig();
        }
    }

    @Override
    protected void onStop() {
        if (controller != null) {
            controller.detach(); // 后台继续接收，不再刷新界面
            // 离开页面：停止录音和朗读（不在后台录音）
            voice.cancel();
            tts.stop();
        }
        super.onStop();
    }

    @Override
    protected void onDestroy() {
        if (controller != null) {
            controller.dispose();
            voice.cancel();
            tts.release();
        }
        if (speechConfigCall != null) {
            speechConfigCall.cancel();
        }
        super.onDestroy();
    }

    // ---------- 语音 ----------

    /** 读取语音能力配置：决定是否显示麦克风和朗读按钮（读取失败按都没开通处理）。 */
    private void loadSpeechConfig() {
        if (speechConfigCall != null) {
            speechConfigCall.cancel();
        }
        speechConfigCall = Async.run(() -> app.api().speechConfig(), new Async.Callback<SpeechConfig>() {
            @Override
            public void onSuccess(SpeechConfig c) {
                applySpeechConfig(c);
            }

            @Override
            public void onError(ApiException e) {
                applySpeechConfig(SpeechConfig.DISABLED);
            }
        });
    }

    private void applySpeechConfig(SpeechConfig c) {
        speechConfig = c;
        tts.setMaxChars(c.maxTextChars);
        boolean on = VoicePrefs.enabled(this);
        micButton.setVisibility(on && c.sttEnabled ? View.VISIBLE : View.GONE);
        if (!on || !c.ttsEnabled) {
            tts.stop();
        }
        adapter.refreshSpeak();
    }

    private void onMicClicked() {
        if (voice.isActive()) {
            voice.finish(); // 说完了
            return;
        }
        if (controller.isBusy()) {
            toast("正在回答，请稍候或先停止");
            return;
        }
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
            startVoice();
            return;
        }
        // 第一次申请前说明语音怎么用、去哪里
        new AlertDialog.Builder(this)
                .setTitle("使用语音输入")
                .setMessage(VoicePrefs.PRIVACY)
                .setPositiveButton("继续", (d, w) -> requestPermissions(new String[]{Manifest.permission.RECORD_AUDIO}, REQUEST_MIC_PERMISSION))
                .setNegativeButton("取消", null)
                .show();
    }

    private void startVoice() {
        tts.stop(); // 录音时不朗读
        voiceOriginal = input.getText().toString();
        voiceBase = voiceOriginal;
        if (!voiceBase.isEmpty() && !voiceBase.endsWith(" ")) {
            voiceBase += " ";
        }
        voice.start();
    }

    @Override
    public void onVoiceState(VoiceInputController.State state) {
        voiceBar.setVisibility(state == VoiceInputController.State.IDLE ? View.GONE : View.VISIBLE);
        switch (state) {
            case CONNECTING:
                voiceStatus.setText("正在连接语音服务…");
                break;
            case LISTENING:
                voiceStatus.setText("正在听…说完再点一次麦克风");
                break;
            case FINISHING:
                voiceStatus.setText("正在识别…");
                break;
            default:
                voiceStatus.setText("");
        }
        micButton.setContentDescription(state == VoiceInputController.State.IDLE ? "语音输入" : "结束语音输入");
        micButton.setAlpha(state == VoiceInputController.State.LISTENING ? 0.6f : 1f);
    }

    @Override
    public void onTranscript(String text, boolean isFinal) {
        if (isFinal && text.trim().isEmpty()) {
            toast("没有听清，请再说一次");
            return;
        }
        String full = voiceBase + text;
        input.setText(full);
        input.setSelection(full.length());
    }

    @Override
    public void onVoiceError(String message) {
        toast(message);
    }

    @Override
    public void onSpeak(ChatTurn turn) {
        tts.toggle(turn.clientMessageId, turn.text());
    }

    @Override
    public void onTtsState(String key, boolean loading, boolean playing) {
        adapter.refreshSpeak();
    }

    @Override
    public void onTtsError(String message) {
        toast(message);
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
        if (attachment.isUploading()) {
            toast("图片还在上传，请稍候");
            return;
        }
        if (attachment.state() == AttachmentController.State.FAILED) {
            toast("图片没有上传成功，可以重试或移除");
            return;
        }
        List<String> files = attachment.readyIds();
        if (text.isEmpty() && files.isEmpty()) {
            toast("先说说想买什么");
            return;
        }
        if (voice.isActive()) {
            voice.cancel(); // 直接发送：以输入框里现有的文字为准
        }
        if (sendText(text, files)) {
            input.setText("");
            attachment.consumed();
        }
    }

    private boolean sendText(String text) {
        return sendText(text, java.util.Collections.emptyList());
    }

    private boolean sendText(String text, List<String> files) {
        if (controller.isBusy()) {
            toast("正在回答，请稍候或先停止");
            return false;
        }
        if (!controller.send(text, files)) {
            return false;
        }
        stickToBottom = true;
        hideKeyboard();
        return true;
    }

    // ---------- 图片附件 ----------

    private void chooseImageSource() {
        if (attachment.isUploading()) {
            toast("图片还在上传，请稍候");
            return;
        }
        new AlertDialog.Builder(this)
                .setTitle("按图片找商品")
                .setItems(new String[]{"从相册选择", "拍照"}, (d, which) -> {
                    if (which == 0) {
                        pickFromGallery();
                    } else {
                        takePhoto();
                    }
                })
                .setNegativeButton("取消", null)
                .show();
    }

    private void pickFromGallery() {
        Intent pick = new Intent(Intent.ACTION_GET_CONTENT).setType("image/*").addCategory(Intent.CATEGORY_OPENABLE);
        try {
            startActivityForResult(Intent.createChooser(pick, "选择图片"), REQUEST_GALLERY);
        } catch (ActivityNotFoundException e) {
            toast("没有可以选择图片的应用");
        }
    }

    private void takePhoto() {
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.CAMERA}, REQUEST_CAMERA_PERMISSION);
            return;
        }
        File dir = new File(getCacheDir(), "camera");
        if (!dir.isDirectory() && !dir.mkdirs()) {
            toast("无法准备拍照，请从相册选择");
            return;
        }
        cameraFile = new File(dir, "photo_" + System.currentTimeMillis() + ".jpg");
        Uri uri = FileProvider.getUriForFile(this, getPackageName() + ".files", cameraFile);
        Intent capture = new Intent(MediaStore.ACTION_IMAGE_CAPTURE).putExtra(MediaStore.EXTRA_OUTPUT, uri)
                .addFlags(Intent.FLAG_GRANT_WRITE_URI_PERMISSION | Intent.FLAG_GRANT_READ_URI_PERMISSION);
        try {
            startActivityForResult(capture, REQUEST_CAMERA);
        } catch (ActivityNotFoundException e) {
            cameraFile = null;
            toast("没有可用的相机应用，可以从相册选择");
        }
    }

    @Override
    public void onRequestPermissionsResult(int requestCode, String[] permissions, int[] results) {
        super.onRequestPermissionsResult(requestCode, permissions, results);
        if (requestCode == REQUEST_MIC_PERMISSION) {
            onMicPermission(results.length > 0 && results[0] == PackageManager.PERMISSION_GRANTED);
            return;
        }
        if (requestCode != REQUEST_CAMERA_PERMISSION) {
            return;
        }
        if (results.length > 0 && results[0] == PackageManager.PERMISSION_GRANTED) {
            takePhoto();
            return;
        }
        // 拒绝了：可以改用相册；勾了“不再询问”时只能去系统设置里打开
        boolean blocked = !shouldShowRequestPermissionRationale(Manifest.permission.CAMERA);
        AlertDialog.Builder b = new AlertDialog.Builder(this)
                .setTitle("没有相机权限")
                .setMessage(blocked ? "相机权限已被关闭。可以从相册选择图片，或到系统设置里为 Blink Shop 打开相机权限。"
                        : "拍照找同款需要相机权限。也可以从相册选择图片。")
                .setPositiveButton("从相册选择", (d, w) -> pickFromGallery())
                .setNegativeButton("取消", null);
        if (blocked) {
            b.setNeutralButton("去设置", (d, w) -> {
                try {
                    startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", getPackageName(), null)));
                } catch (ActivityNotFoundException e) {
                    toast("请到系统设置里打开相机权限");
                }
            });
        }
        b.show();
    }

    private void onMicPermission(boolean granted) {
        if (granted) {
            startVoice();
            return;
        }
        boolean blocked = !shouldShowRequestPermissionRationale(Manifest.permission.RECORD_AUDIO);
        AlertDialog.Builder b = new AlertDialog.Builder(this)
                .setTitle("没有录音权限")
                .setMessage(blocked ? "录音权限已被关闭，语音输入用不了。可以直接打字提问，或到系统设置里为 Blink Shop 打开麦克风权限。"
                        : "语音输入需要录音权限。不授权也可以直接打字提问。")
                .setPositiveButton("知道了", null);
        if (blocked) {
            b.setNeutralButton("去设置", (d, w) -> {
                try {
                    startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", getPackageName(), null)));
                } catch (ActivityNotFoundException e) {
                    toast("请到系统设置里打开麦克风权限");
                }
            });
        }
        b.show();
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == REQUEST_GALLERY) {
            if (resultCode == RESULT_OK && data != null && data.getData() != null) {
                prepareAndAttach(data.getData(), null);
            }
            return;
        }
        if (requestCode == REQUEST_CAMERA) {
            File photo = cameraFile;
            cameraFile = null;
            if (resultCode == RESULT_OK && photo != null && photo.length() > 0) {
                prepareAndAttach(Uri.fromFile(photo), photo);
            } else if (photo != null) {
                //noinspection ResultOfMethodCallIgnored
                photo.delete();
            }
        }
    }

    /** 在后台压缩图片后交给 AttachmentController 上传；拍照的临时文件处理完就删除。 */
    private void prepareAndAttach(Uri uri, File temp) {
        Async.run(() -> {
            try {
                return ChatImage.prepare(getContentResolver(), uri);
            } catch (IOException | RuntimeException e) {
                throw ApiException.local("这张图片打不开，请换一张", e);
            } finally {
                if (temp != null) {
                    //noinspection ResultOfMethodCallIgnored
                    temp.delete();
                }
            }
        }, new Async.Callback<byte[]>() {
            @Override
            public void onSuccess(byte[] jpeg) {
                attachment.attach(jpeg);
            }

            @Override
            public void onError(ApiException e) {
                attachment.fail(e.getMessage());
            }
        });
    }

    @Override
    public void onAttachmentChanged() {
        AttachmentController.State st = attachment.state();
        attachmentBar.setVisibility(st == AttachmentController.State.EMPTY ? View.GONE : View.VISIBLE);
        byte[] data = attachment.data();
        Bitmap thumb = data == null ? null : ChatImage.thumbnail(data, dp(112));
        attachmentPreview.setImageBitmap(thumb);
        attachmentPreview.setVisibility(thumb == null ? View.GONE : View.VISIBLE);
        switch (st) {
            case UPLOADING:
                attachmentStatus.setText("图片上传中…");
                break;
            case READY:
                attachmentStatus.setText("图片已就绪，发送时一起发给导购");
                break;
            case FAILED:
                attachmentStatus.setText(attachment.error());
                break;
            default:
                attachmentStatus.setText("");
        }
        attachmentRetry.setVisibility(st == AttachmentController.State.FAILED && data != null ? View.VISIBLE : View.GONE);
        input.setHint(st == AttachmentController.State.EMPTY ? "问问导购，比如“3000 以内拍照好的手机”" : "说说想找什么（可以不填）");
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
