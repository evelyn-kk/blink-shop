package com.blink.shop.net;

import okhttp3.HttpUrl;

/** 拼接接口地址；所有网络请求和图片地址都通过这里得到完整 URL。 */
public final class ApiConfig {

    private final String baseUrl;

    public ApiConfig(String baseUrl) {
        if (baseUrl == null || baseUrl.trim().isEmpty()) {
            throw new IllegalArgumentException("baseUrl must not be empty");
        }
        String trimmed = baseUrl.trim();
        while (trimmed.endsWith("/")) {
            trimmed = trimmed.substring(0, trimmed.length() - 1);
        }
        this.baseUrl = trimmed;
    }

    public String baseUrl() {
        return baseUrl;
    }

    /** 例如 url("/health") -> http://10.0.2.2:8080/api/v1/health。 */
    public String url(String path) {
        if (path == null || path.isEmpty()) {
            return baseUrl;
        }
        return path.startsWith("/") ? baseUrl + path : baseUrl + "/" + path;
    }

    /**
     * 服务端返回的图片地址（如 /api/v1/assets/...、/api/v1/uploads/avatar/...）是站内绝对路径，
     * 按接口地址的协议和主机补全；已经是 http(s) 地址的原样返回，空串返回 null。
     */
    public String resolve(String pathOrUrl) {
        if (pathOrUrl == null || pathOrUrl.trim().isEmpty()) {
            return null;
        }
        String s = pathOrUrl.trim();
        if (s.startsWith("http://") || s.startsWith("https://")) {
            return s;
        }
        HttpUrl base = HttpUrl.parse(baseUrl);
        if (base == null) {
            return null;
        }
        HttpUrl resolved = base.resolve(s.startsWith("/") ? s : "/" + s);
        return resolved == null ? null : resolved.toString();
    }

    /** 校验用户输入的接口地址：必须是 http(s)，路径以 /api/v1 结尾。不合法返回原因，合法返回 null。 */
    public static String validate(String input) {
        if (input == null || input.trim().isEmpty()) {
            return "请填写接口地址";
        }
        String s = input.trim();
        while (s.endsWith("/")) {
            s = s.substring(0, s.length() - 1);
        }
        HttpUrl url = HttpUrl.parse(s);
        if (url == null) {
            return "地址格式不正确，应以 http:// 或 https:// 开头";
        }
        if (url.query() != null || url.fragment() != null) {
            return "地址不能带查询参数";
        }
        if (!url.encodedPath().endsWith("/api/v1")) {
            return "地址应以 /api/v1 结尾，例如 http://192.168.1.10:8080/api/v1";
        }
        return null;
    }
}
