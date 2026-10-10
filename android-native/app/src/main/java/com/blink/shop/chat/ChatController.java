package com.blink.shop.chat;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.Executor;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import com.blink.shop.net.ApiException;
import com.blink.shop.net.SseClient;
import com.blink.shop.net.SseParser;

/**
 * 聊天页的状态和流程（纯 Java，不依赖 Android）：发送、接收事件、停止、重试、按会话恢复。
 * 所有状态只在 main 执行器上修改；阻塞的接口调用放到 io 执行器；SSE 回调先切回 main 再处理。
 * 渲染层通过 Listener 得到“有变化”的通知后读取 turns()。
 */
public final class ChatController {

    public interface Listener {
        /** 任何会影响显示的状态变化（内容、步骤、状态、加载中/出错）。 */
        void onChanged();

        /** 当前会话变了（新建、切换、服务端刚创建）；sessionId 为空表示还没有服务端会话。 */
        void onSessionChanged(String sessionId);
    }

    /** 生成 client_message_id；测试可替换为可预测的序列。 */
    public interface IdSource {
        String next();
    }

    private final ChatBackend backend;
    private final Executor io;
    private final Executor main;
    private final IdSource ids;

    private Listener listener;
    private String sessionId = "";
    private final List<ChatTurn> turns = new ArrayList<>();
    private ChatTurn active;
    private SseClient.Stream activeStream;
    /** 每次开始/停止一个流加一；旧流迟到的回调带着旧代号，一律忽略。 */
    private int generation;
    private boolean creatingSession;
    private boolean loading;
    private String loadError = "";
    private int cancelRequests;

    public ChatController(ChatBackend backend, Executor io, Executor main) {
        this(backend, io, main, () -> "a-" + UUID.randomUUID().toString().replace("-", ""));
    }

    public ChatController(ChatBackend backend, Executor io, Executor main, IdSource ids) {
        this.backend = backend;
        this.io = io;
        this.main = main;
        this.ids = ids;
    }

    // ---------- 读取 ----------

    public List<ChatTurn> turns() {
        return Collections.unmodifiableList(turns);
    }

    public String sessionId() {
        return sessionId;
    }

    public boolean isStreaming() {
        return active != null;
    }

    public boolean isBusy() {
        return active != null || creatingSession;
    }

    public boolean isLoading() {
        return loading;
    }

    public String loadError() {
        return loadError;
    }

    /** 测试用：已发出的停止请求次数。 */
    int cancelRequests() {
        return cancelRequests;
    }

    public void attach(Listener l) {
        listener = l;
        if (l != null) {
            l.onChanged();
        }
    }

    public void detach() {
        listener = null;
    }

    private void changed() {
        if (listener != null) {
            listener.onChanged();
        }
    }

    // ---------- 发送 ----------

    /** 只附图、没写文字时替用户发的问题。 */
    public static final String IMAGE_ONLY_PROMPT = "帮我找找图片里的同款";

    /** 发送一条消息。正在生成、正在创建会话或内容为空时不发送（返回 false），避免重复点击发出两条。 */
    public boolean send(String text) {
        return send(text, Collections.emptyList());
    }

    /** 发送一条带附件的消息；只有附件没有文字时用 IMAGE_ONLY_PROMPT。 */
    public boolean send(String text, List<String> attachments) {
        String content = text == null ? "" : text.trim();
        List<String> files = attachments == null ? Collections.emptyList() : attachments;
        if (content.isEmpty() && !files.isEmpty()) {
            content = IMAGE_ONLY_PROMPT;
        }
        if (content.isEmpty() || isBusy()) {
            return false;
        }
        ChatTurn turn = new ChatTurn(ids.next(), content, null, files);
        turns.add(turn);
        launch(turn);
        return true;
    }

    /** 发出一轮：还没有服务端会话时先创建（失败按传输错误处理，可重试），再开流。 */
    private void launch(ChatTurn turn) {
        if (!sessionId.isEmpty()) {
            start(turn);
            return;
        }
        creatingSession = true;
        turn.status = ChatTurn.Status.SENDING;
        turn.touch();
        changed();
        final int gen = generation;
        io.execute(() -> {
            try {
                String id = backend.createSession();
                main.execute(() -> {
                    creatingSession = false;
                    if (gen != generation || !turns.contains(turn)) {
                        return; // 期间切换/新建了会话
                    }
                    sessionId = id;
                    if (listener != null) {
                        listener.onSessionChanged(id);
                    }
                    start(turn);
                });
            } catch (ApiException e) {
                main.execute(() -> {
                    creatingSession = false;
                    if (gen != generation || !turns.contains(turn)) {
                        return;
                    }
                    fail(turn, e.getMessage(), e.code(), true);
                });
            }
        });
    }

    private void start(ChatTurn turn) {
        generation++;
        final int gen = generation;
        active = turn;
        turn.status = ChatTurn.Status.SENDING;
        turn.touch();
        changed();
        activeStream = backend.stream(sessionId, turn.clientMessageId, turn.userText, turn.attachments, new SseClient.Listener() {
            @Override
            public void onEvent(SseParser.Event event) {
                main.execute(() -> handleEvent(gen, turn, event));
            }

            @Override
            public void onClosed() {
                main.execute(() -> handleClosed(gen, turn));
            }

            @Override
            public void onError(ApiException error) {
                main.execute(() -> handleError(gen, turn, error));
            }
        });
    }

    private boolean stale(int gen, ChatTurn turn) {
        return gen != generation || turn != active;
    }

    private void handleEvent(int gen, ChatTurn turn, SseParser.Event ev) {
        if (stale(gen, turn)) {
            return;
        }
        JSONObject data;
        try {
            data = ev.data.isEmpty() ? new JSONObject() : new JSONObject(ev.data);
        } catch (JSONException e) {
            return; // 坏掉的一帧：跳过，不中断整个流
        }
        String runId = data.optString("run_id", "");
        if ("message_start".equals(ev.event)) {
            if (!turn.runId.isEmpty() && !turn.runId.equals(runId)) {
                return; // 同一连接里出现另一个运行的开始：不接受
            }
            turn.runId = runId;
            turn.replayed = data.optBoolean("replayed", false);
            if (turn.replayed) {
                // 服务端重放保存的回答：从头来，不和本地已有内容拼接
                turn.text.setLength(0);
                turn.blocks.clear();
                turn.steps.clear();
                turn.followups = Collections.emptyList();
            }
            turn.status = ChatTurn.Status.STREAMING;
            turn.touch();
            changed();
            return;
        }
        // 带 run_id 的事件必须属于当前运行（按 run id 去重）
        if (!runId.isEmpty() && !turn.runId.isEmpty() && !runId.equals(turn.runId)) {
            return;
        }
        switch (ev.event) {
            case "thinking": {
                JSONObject step = data.optJSONObject("step");
                if (step != null) {
                    turn.step(step.optString("id", ""), step.optString("title", ""), "done".equals(step.optString("status", "")));
                    changed();
                }
                break;
            }
            case "text_delta":
                turn.text.append(data.optString("delta", ""));
                turn.touch();
                changed();
                break;
            case "block": {
                JSONObject block = data.optJSONObject("block");
                if (block != null) {
                    turn.blocks.add(block);
                    turn.touch();
                    changed();
                }
                break;
            }
            case "followups":
                turn.followups = ChatTurn.strings(data.optJSONArray("questions"));
                turn.touch();
                changed();
                break;
            case "message_done":
                turn.status = ChatTurn.Status.DONE;
                finish(turn);
                break;
            case "error": {
                String code = data.optString("code", "");
                String message = data.optString("message", "");
                if ("cancelled".equals(code)) {
                    turn.status = ChatTurn.Status.CANCELLED;
                    turn.errorCode = code;
                    turn.errorMessage = message.isEmpty() ? "已停止生成" : message;
                    finish(turn);
                } else {
                    // 服务端明确失败：重试要重新提问（同一个 ID 只会重放这个失败）
                    fail(turn, message.isEmpty() ? ChatTurn.failureMessage(code) : message, code, false);
                }
                break;
            }
            default:
                // 未知事件：忽略，保持兼容
        }
    }

    private void handleClosed(int gen, ChatTurn turn) {
        if (stale(gen, turn)) {
            return;
        }
        // 服务端承诺以 message_done 或 error 结束；提前关闭按连接中断处理，重试复用同一条消息（服务端会重放或等它结束）
        fail(turn, "连接中断，请重试", "disconnected", true);
    }

    private void handleError(int gen, ChatTurn turn, ApiException e) {
        if (stale(gen, turn)) {
            return;
        }
        fail(turn, e.getMessage(), e.code(), true);
    }

    private void fail(ChatTurn turn, String message, String code, boolean retrySameId) {
        turn.status = ChatTurn.Status.ERROR;
        turn.errorMessage = message == null || message.isEmpty() ? "出了点问题，请重试" : message;
        turn.errorCode = code == null ? "" : code;
        turn.failedBeforeStart = retrySameId;
        finish(turn);
    }

    private void finish(ChatTurn turn) {
        if (turn == active) {
            active = null;
            activeStream = null;
        }
        turn.touch();
        changed();
    }

    // ---------- 停止 / 重试 ----------

    /** 停止当前生成：断开连接并通知服务端取消（已生成的内容保留）。没有进行中的生成时什么都不做。 */
    public void stop() {
        ChatTurn turn = active;
        if (turn == null) {
            return;
        }
        generation++; // 之后迟到的事件全部忽略
        SseClient.Stream stream = activeStream;
        active = null;
        activeStream = null;
        if (stream != null) {
            stream.cancel();
        }
        turn.status = ChatTurn.Status.CANCELLED;
        turn.errorCode = "cancelled";
        turn.errorMessage = "已停止生成";
        turn.touch();
        changed();
        String runId = turn.runId;
        if (!runId.isEmpty()) {
            cancelRequests++;
            io.execute(() -> {
                try {
                    backend.cancelRun(runId);
                } catch (ApiException ignored) {
                    // 已结束（409）或网络问题：断开连接本身已让服务端把运行记为取消
                }
            });
        }
    }

    /**
     * 重试失败的一轮：传输类失败复用同一个 client_message_id（服务端幂等，重放或继续等待），
     * 服务端明确失败则用新的 ID 重新提问。正在生成时不允许。
     */
    public boolean retry(ChatTurn turn) {
        if (isBusy() || !turns.contains(turn) || turn.status != ChatTurn.Status.ERROR) {
            return false;
        }
        if (turn.failedBeforeStart) {
            reset(turn);
            launch(turn);
            return true;
        }
        ChatTurn fresh = new ChatTurn(ids.next(), turn.userText, null, turn.attachments);
        turns.set(turns.indexOf(turn), fresh);
        launch(fresh);
        return true;
    }

    /** 对已停止或已失败的一轮重新提问（新的 client_message_id）。 */
    public boolean resend(ChatTurn turn) {
        if (isBusy() || !turns.contains(turn) || turn.isActive()) {
            return false;
        }
        ChatTurn fresh = new ChatTurn(ids.next(), turn.userText, null, turn.attachments);
        turns.set(turns.indexOf(turn), fresh);
        launch(fresh);
        return true;
    }

    private static void reset(ChatTurn turn) {
        turn.text.setLength(0);
        turn.steps.clear();
        turn.blocks.clear();
        turn.followups = Collections.emptyList();
        turn.errorMessage = "";
        turn.errorCode = "";
        turn.replayed = false;
        turn.runId = "";
    }

    // ---------- 会话 ----------

    /** 开始一个新会话：服务端会话等第一条消息发送时再创建（没有消息的会话不会出现在历史里）。 */
    public void newSession() {
        abandonActive();
        turns.clear();
        sessionId = "";
        loadError = "";
        loading = false;
        changed();
        if (listener != null) {
            listener.onSessionChanged("");
        }
    }

    /** 加载（或页面重建后恢复）一个会话：读详情重建每一轮；最后一轮还在运行时重新连接同一条消息，等服务端重放结果，不重复发送。 */
    public void loadSession(String id) {
        abandonActive();
        turns.clear();
        sessionId = id;
        loading = true;
        loadError = "";
        changed();
        if (listener != null) {
            listener.onSessionChanged(id);
        }
        final int gen = generation;
        io.execute(() -> {
            try {
                JSONObject detail = backend.sessionDetail(id);
                main.execute(() -> {
                    if (gen != generation || !id.equals(sessionId)) {
                        return;
                    }
                    loading = false;
                    applyDetail(detail);
                });
            } catch (ApiException e) {
                main.execute(() -> {
                    if (gen != generation || !id.equals(sessionId)) {
                        return;
                    }
                    loading = false;
                    loadError = e.getMessage();
                    changed();
                });
            }
        });
    }

    private void applyDetail(JSONObject detail) {
        turns.clear();
        JSONArray messages = detail.optJSONArray("messages");
        ChatTurn resume = null;
        if (messages != null) {
            for (int i = 0; i < messages.length(); i++) {
                JSONObject m = messages.optJSONObject(i);
                if (m == null) {
                    continue;
                }
                ChatTurn turn = new ChatTurn(m.optString("client_message_id", ids.next()), m.optString("content", ""), m.optString("created_at", ""),
                        fileIds(m.optJSONArray("attachments")));
                JSONObject run = m.optJSONObject("run");
                if (run != null) {
                    turn.applyRun(run);
                } else {
                    turn.status = ChatTurn.Status.ERROR;
                    turn.errorMessage = "这条消息没有得到回答";
                    turn.failedBeforeStart = true;
                }
                turns.add(turn);
                resume = turn.status == ChatTurn.Status.STREAMING && i == messages.length() - 1 ? turn : null;
            }
        }
        changed();
        if (resume != null) {
            // 上个页面没收完的运行：用同一个 client_message_id 重新连接，服务端等它结束后重放，不会再跑一次
            start(resume);
        }
    }

    private static List<String> fileIds(JSONArray arr) {
        List<String> out = new ArrayList<>();
        if (arr == null) {
            return out;
        }
        for (int i = 0; i < arr.length(); i++) {
            JSONObject a = arr.optJSONObject(i);
            String id = a == null ? "" : a.optString("file_id", "");
            if (!id.isEmpty()) {
                out.add(id);
            }
        }
        return out;
    }

    private void abandonActive() {
        generation++;
        SseClient.Stream stream = activeStream;
        active = null;
        activeStream = null;
        if (stream != null) {
            stream.cancel();
        }
        creatingSession = false;
    }

    /** 页面销毁：断开本地连接（服务端会把进行中的运行记为取消）。 */
    public void dispose() {
        abandonActive();
        listener = null;
    }
}
