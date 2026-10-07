package com.blink.shop.account;

import static org.junit.Assert.assertEquals;

import org.junit.Test;

public class MaskingTest {

    @Test
    public void masksPhone() {
        assertEquals("138****1234", Masking.phone("13800001234"));
        assertEquals("123456", Masking.phone("123456"));
        assertEquals("", Masking.phone(null));
    }

    @Test
    public void masksEmail() {
        assertEquals("u***@example.com", Masking.email("user@example.com"));
        assertEquals("@x", Masking.email("@x"));
        assertEquals("plain", Masking.email("plain"));
    }
}
