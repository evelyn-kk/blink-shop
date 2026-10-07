package com.blink.shop.net;

/**
 * 按 text/event-stream 规则把逐行输入拼成事件：空行结束一个事件；多行 data 用换行连接；
 * 以冒号开头的是注释（心跳）；没有 data 的事件忽略；event 缺省为 message。
 */
public final class SseParser {

    /** 一条完整事件。 */
    public static final class Event {
        public final String event;
        public final String data;
        public final String id;

        public Event(String event, String data, String id) {
            this.event = event;
            this.data = data;
            this.id = id;
        }
    }

    public interface Sink {
        void onEvent(Event event);
    }

    private final Sink sink;
    private String event = "";
    private StringBuilder data;
    private String lastId = "";

    public SseParser(Sink sink) {
        this.sink = sink;
    }

    /** 输入一行（不含行尾换行符）。 */
    public void line(String line) {
        if (line.isEmpty()) {
            dispatch();
            return;
        }
        if (line.startsWith(":")) {
            return;
        }
        int colon = line.indexOf(':');
        String field = colon < 0 ? line : line.substring(0, colon);
        String value = colon < 0 ? "" : line.substring(colon + 1);
        if (value.startsWith(" ")) {
            value = value.substring(1);
        }
        switch (field) {
            case "event":
                event = value;
                break;
            case "data":
                if (data == null) {
                    data = new StringBuilder(value);
                } else {
                    data.append('\n').append(value);
                }
                break;
            case "id":
                if (!value.contains("\u0000")) {
                    lastId = value;
                }
                break;
            default:
                // retry 和未知字段忽略
                break;
        }
    }

    /** 流结束：规范要求丢弃最后一个没有以空行结束的事件。 */
    public void finish() {
        event = "";
        data = null;
    }

    private void dispatch() {
        if (data != null) {
            sink.onEvent(new Event(event.isEmpty() ? "message" : event, data.toString(), lastId));
        }
        event = "";
        data = null;
    }
}
