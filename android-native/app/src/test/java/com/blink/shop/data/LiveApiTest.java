package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.junit.Assume;
import org.junit.Test;

import com.blink.shop.model.Session;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;

/**
 * 连真实 Blink API 的回归（设置 BLINK_TEST_API_BASE 时运行，如 http://127.0.0.1:28080/api/v1；
 * 会注册新账号，只能指向测试库）。
 */
public class LiveApiTest {

    private static ShopApi api() {
        String base = System.getenv("BLINK_TEST_API_BASE");
        Assume.assumeTrue("BLINK_TEST_API_BASE not set", base != null && !base.isEmpty());
        return new ShopApi(new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig(base), new FakeSession(null), () -> true));
    }

    @Test
    public void registerAndLoginWithUnicodePassword() throws Exception {
        ShopApi api = api();
        String username = "android_u" + (System.currentTimeMillis() % 1_000_000_000L);
        String password = "蓝色Blink密码2026"; // 13 个字符，21 字节
        Session reg = api.register(username, password, "");
        assertEquals(username, reg.account.username);
        Session login = api.login(username, password);
        assertTrue(!login.token.isEmpty());
        try {
            api.login(username, "蓝色Blink密码2027");
            fail("wrong password accepted");
        } catch (ApiException e) {
            assertEquals("invalid_credential", e.code());
        }
        // 超过 72 字节：服务端拒绝，客户端原样展示服务端说明
        try {
            api.register(username + "x", "密码密码密码密码密码密码密码密码密码密码密码密码密码", "");
            fail("over-long password accepted");
        } catch (ApiException e) {
            assertEquals(400, e.status());
            assertEquals("密码过长，请减少中文等多字节字符", e.getMessage());
        }
    }
}
