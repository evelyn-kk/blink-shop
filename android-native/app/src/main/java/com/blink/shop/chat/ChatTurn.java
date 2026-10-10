package com.blink.shop.chat;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import org.json.JSONArray;
import org.json.JSONObject;

/**
 * 聊天里的一轮：用户的一条消息和导购的回答。纯 Java，不依赖 Android。
 * 字段只由 ChatController 在主线程修改；渲染层只读。
 */
public final class ChatTurn {

    public enum Status {
        /** 已提交，还没收到 message_start。 */
        SENDING,
        /** 收到 message_start，正在接收内容。 */
        STREAMING,
        DONE,
        /** 服务端报错、连接中断或本地错误；errorMessage 说明原因。 */
        ERROR,
        /** 用户停止，或服务端重放的已取消运行。 */
        CANCELLED
    }

    /** 思考步骤（thinking 事件）；同一 id 以后到的状态为准。 */
    public static final class Step {
        public final String id;
        public final String title;
        public final boolean done;

        Step(String id, String title, boolean done) {
            this.id = id;
            this.title = title;
            this.done = done;
        }
    }

    public final String clientMessageId;
    public final String userText;
    /** 服务端消息时间（历史回放时有），本地发送时为空。 */
    public final String createdAt;

    Status status = Status.SENDING;
    String runId = "";
    boolean replayed;
    final StringBuilder text = new StringBuilder();
    final LinkedHashMap<String, Step> steps = new LinkedHashMap<>();
    final List<JSONObject> blocks = new ArrayList<>();
    List<String> followups = Collections.emptyList();
    String errorMessage = "";
    String errorCode = "";
    /** 失败发生在收到 message_start 之前（传输问题）：重试可以复用同一个 client_message_id。 */
    boolean failedBeforeStart;
    /** 每次内容变化加一，渲染层据此判断是否需要重绘。 */
    int revision;

    ChatTurn(String clientMessageId, String userText, String createdAt) {
        this.clientMessageId = clientMessageId;
        this.userText = userText;
        this.createdAt = createdAt == null ? "" : createdAt;
    }

    public Status status() {
        return status;
    }

    public String runId() {
        return runId;
    }

    public boolean replayed() {
        return replayed;
    }

    public String text() {
        return text.toString();
    }

    public List<Step> steps() {
        return new ArrayList<>(steps.values());
    }

    public List<JSONObject> blocks() {
        return Collections.unmodifiableList(blocks);
    }

    public List<String> followups() {
        return followups;
    }

    public String errorMessage() {
        return errorMessage;
    }

    public String errorCode() {
        return errorCode;
    }

    public int revision() {
        return revision;
    }

    public boolean isActive() {
        return status == Status.SENDING || status == Status.STREAMING;
    }

    /** 失败后可以重试（取消的用“重新提问”）。 */
    public boolean canRetry() {
        return status == Status.ERROR;
    }

    void touch() {
        revision++;
    }

    void step(String id, String title, boolean done) {
        if (id.isEmpty()) {
            return;
        }
        Step old = steps.get(id);
        steps.put(id, new Step(id, title.isEmpty() && old != null ? old.title : title, done));
        touch();
    }

    /** 把会话详情里保存的运行结果填进来（历史回放）。 */
    void applyRun(JSONObject run) {
        runId = run.optString("run_id", "");
        text.setLength(0);
        text.append(run.optString("content", ""));
        blocks.clear();
        JSONArray bs = run.optJSONArray("blocks");
        if (bs != null) {
            for (int i = 0; i < bs.length(); i++) {
                JSONObject b = bs.optJSONObject(i);
                if (b != null) {
                    blocks.add(b);
                }
            }
        }
        followups = strings(run.optJSONArray("followups"));
        String st = run.optString("status", "");
        errorCode = run.optString("error_code", "");
        switch (st) {
            case "completed":
                status = Status.DONE;
                break;
            case "cancelled":
                status = Status.CANCELLED;
                errorMessage = "已停止生成";
                break;
            case "failed":
                status = Status.ERROR;
                errorMessage = failureMessage(errorCode);
                break;
            default:
                // queued / running：上一个页面还没收完，由控制器重新连接同一条消息等它结束
                status = Status.STREAMING;
        }
        touch();
    }

    static String failureMessage(String code) {
        switch (code) {
            case "run_timeout":
                return "回答超时，请稍后重试";
            case "server_shutdown":
            case "interrupted":
                return "服务重启中断了这次回答，请重新发送";
            default:
                return "这次回答没有完成，请稍后重试";
        }
    }

    static List<String> strings(JSONArray arr) {
        if (arr == null || arr.length() == 0) {
            return Collections.emptyList();
        }
        List<String> out = new ArrayList<>(arr.length());
        for (int i = 0; i < arr.length(); i++) {
            String s = arr.optString(i, "");
            if (!s.isEmpty()) {
                out.add(s);
            }
        }
        return Collections.unmodifiableList(out);
    }

    /** 渲染层用：块里的字符串字段，缺省为空串。 */
    public static String field(JSONObject o, String key) {
        return o == null || o.isNull(key) ? "" : o.optString(key, "");
    }

    public static Map<String, Step> noSteps() {
        return Collections.emptyMap();
    }
}
