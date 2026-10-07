package com.blink.shop.net;

import java.io.IOException;
import java.io.InterruptedIOException;
import java.net.SocketTimeoutException;

import org.json.JSONException;
import org.json.JSONObject;

/**
 * 接口调用失败。getMessage() 总是可以直接展示给用户的中文说明：
 * 服务端错误用响应里的 message，网络错误按原因给出提示，不暴露 Java 异常文本。
 */
public final class ApiException extends Exception {

    public enum Kind {
        /** 设备没有网络。 */
        OFFLINE,
        /** 连不上服务器：地址错误、服务没启动、域名解析失败等。 */
        UNREACHABLE,
        TIMEOUT,
        /** 服务器返回了非 2xx。 */
        HTTP,
        /** 2xx 但响应不是预期的 JSON。 */
        BAD_RESPONSE,
        CANCELED,
        /** 本机处理失败（如读取图片），消息由调用方给出。 */
        LOCAL,
    }

    public static final String MSG_OFFLINE = "网络未连接，请检查网络后重试";
    public static final String MSG_UNREACHABLE = "无法连接服务器，请检查网络或服务地址";
    public static final String MSG_TIMEOUT = "请求超时，请稍后重试";
    public static final String MSG_BAD_RESPONSE = "服务器返回的数据无法识别，请稍后重试";

    private final Kind kind;
    private final int status;
    private final String code;
    private final String field;
    private final String requestId;

    private ApiException(Kind kind, int status, String code, String message, String field, String requestId, Throwable cause) {
        super(message, cause);
        this.kind = kind;
        this.status = status;
        this.code = code == null ? "" : code;
        this.field = field == null ? "" : field;
        this.requestId = requestId == null ? "" : requestId;
    }

    public Kind kind() {
        return kind;
    }

    /** HTTP 状态码；非 HTTP 错误为 0。 */
    public int status() {
        return status;
    }

    /** 服务端错误码，如 invalid_credential；非 HTTP 错误为空串。 */
    public String code() {
        return code;
    }

    /** 校验失败的请求字段，如 username；没有时为空串。 */
    public String field() {
        return field;
    }

    public String requestId() {
        return requestId;
    }

    public boolean isUnauthorized() {
        return kind == Kind.HTTP && status == 401;
    }

    /** 网络层面的失败（可重试），区别于服务端明确拒绝。 */
    public boolean isNetwork() {
        return kind == Kind.OFFLINE || kind == Kind.UNREACHABLE || kind == Kind.TIMEOUT;
    }

    public static ApiException offline() {
        return new ApiException(Kind.OFFLINE, 0, null, MSG_OFFLINE, null, null, null);
    }

    public static ApiException canceled() {
        return new ApiException(Kind.CANCELED, 0, null, "已取消", null, null, null);
    }

    public static ApiException local(String message, Throwable cause) {
        return new ApiException(Kind.LOCAL, 0, null, message, null, null, cause);
    }

    public static ApiException badResponse(Throwable cause) {
        return new ApiException(Kind.BAD_RESPONSE, 0, null, MSG_BAD_RESPONSE, null, null, cause);
    }

    /** 把 OkHttp 抛出的 IOException 归类。canceled 表示调用方主动取消。 */
    public static ApiException fromIOException(IOException e, boolean canceled) {
        if (canceled) {
            return new ApiException(Kind.CANCELED, 0, null, "已取消", null, null, e);
        }
        if (e instanceof SocketTimeoutException
                || (e instanceof InterruptedIOException && "timeout".equals(e.getMessage()))) {
            return new ApiException(Kind.TIMEOUT, 0, null, MSG_TIMEOUT, null, null, e);
        }
        // 域名解析失败、连接被拒、TLS 握手失败、连接中断等都归为“连不上服务器”。
        return new ApiException(Kind.UNREACHABLE, 0, null, MSG_UNREACHABLE, null, null, e);
    }

    /** 按统一错误体 {code, message, field, request_id} 解析非 2xx 响应；不是 JSON 时按状态码给出说明。 */
    public static ApiException fromHttp(int status, String body, String requestIdHeader) {
        String code = null;
        String message = null;
        String field = null;
        String requestId = requestIdHeader;
        if (body != null && !body.trim().isEmpty()) {
            try {
                JSONObject json = new JSONObject(body);
                code = json.optString("code", null);
                message = json.optString("message", null);
                field = json.optString("field", null);
                String rid = json.optString("request_id", null);
                if (rid != null && !rid.isEmpty()) {
                    requestId = rid;
                }
            } catch (JSONException ignored) {
                // 非 JSON（如网关错误页）：按状态码给出说明
            }
        }
        if (message == null || message.trim().isEmpty()) {
            message = defaultMessage(status);
        }
        return new ApiException(Kind.HTTP, status, code, message, field, requestId, null);
    }

    static String defaultMessage(int status) {
        switch (status) {
            case 401:
                return "请先登录";
            case 403:
                return "没有权限执行此操作";
            case 404:
                return "内容不存在";
            case 413:
                return "上传的内容太大";
            case 429:
                return "请求过于频繁，请稍后再试";
            default:
                if (status >= 500) {
                    return "服务暂时不可用，请稍后重试（" + status + "）";
                }
                return "请求失败（" + status + "）";
        }
    }
}
