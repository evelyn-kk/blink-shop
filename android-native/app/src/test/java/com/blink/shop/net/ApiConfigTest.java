package com.blink.shop.net;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertThrows;

import org.junit.Test;

public class ApiConfigTest {

    @Test
    public void trimsTrailingSlashes() {
        ApiConfig config = new ApiConfig("http://10.0.2.2:8080/api/v1//");
        assertEquals("http://10.0.2.2:8080/api/v1", config.baseUrl());
    }

    @Test
    public void joinsPathWithSingleSlash() {
        ApiConfig config = new ApiConfig("http://10.0.2.2:8080/api/v1/");
        assertEquals("http://10.0.2.2:8080/api/v1/health", config.url("/health"));
        assertEquals("http://10.0.2.2:8080/api/v1/health", config.url("health"));
    }

    @Test
    public void rejectsEmptyBaseUrl() {
        assertThrows(IllegalArgumentException.class, () -> new ApiConfig("  "));
    }

    @Test
    public void resolvesAssetPathsAgainstHost() {
        ApiConfig config = new ApiConfig("http://10.0.2.2:8080/api/v1");
        assertEquals("http://10.0.2.2:8080/api/v1/assets/catalog/products/p.png",
                config.resolve("/api/v1/assets/catalog/products/p.png"));
        assertEquals("https://cdn.example.com/a.png", config.resolve("https://cdn.example.com/a.png"));
        assertEquals("http://10.0.2.2:8080/x.png", config.resolve("x.png"));
        assertNull(config.resolve(""));
        assertNull(config.resolve(null));
    }

    @Test
    public void validatesUserInput() {
        assertNull(ApiConfig.validate("http://192.168.1.10:8080/api/v1"));
        assertNull(ApiConfig.validate(" https://shop.example.com/api/v1/ "));
        assertNotNull(ApiConfig.validate(""));
        assertNotNull(ApiConfig.validate("192.168.1.10:8080/api/v1"));
        assertNotNull(ApiConfig.validate("ftp://h/api/v1"));
        assertNotNull(ApiConfig.validate("http://h:8080"));
        assertNotNull(ApiConfig.validate("http://h/api/v1?x=1"));
    }
}
