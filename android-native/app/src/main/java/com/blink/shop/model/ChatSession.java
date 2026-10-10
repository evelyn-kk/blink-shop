package com.blink.shop.model;

import org.json.JSONObject;

/** 导购会话（历史抽屉里的一条）。 */
public final class ChatSession {

    public final String sessionId;
    public final String title;
    public final String summary;
    public final int messageCount;
    public final String lastMessageAt;
    public final boolean pinned;

    public ChatSession(String sessionId, String title, String summary, int messageCount, String lastMessageAt, boolean pinned) {
        this.sessionId = sessionId;
        this.title = title;
        this.summary = summary;
        this.messageCount = messageCount;
        this.lastMessageAt = lastMessageAt;
        this.pinned = pinned;
    }

    public static ChatSession fromJson(JSONObject o) {
        return new ChatSession(Json.str(o, "session_id"), Json.str(o, "title"), Json.str(o, "summary"), o.optInt("message_count", 0),
                Json.str(o, "last_message_at"), o.optBoolean("pinned", false));
    }

    /** 列表里显示的标题：没有标题时用“AI 导购”。 */
    public String displayTitle() {
        return title.isEmpty() ? "AI 导购" : title;
    }
}
