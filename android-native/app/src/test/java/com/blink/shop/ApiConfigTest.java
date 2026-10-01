package com.blink.shop;

import static org.junit.Assert.assertEquals;
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
}
