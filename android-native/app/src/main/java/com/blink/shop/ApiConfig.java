package com.blink.shop;

/** 拼接接口地址；所有网络请求都应通过这里得到完整 URL。 */
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
}
