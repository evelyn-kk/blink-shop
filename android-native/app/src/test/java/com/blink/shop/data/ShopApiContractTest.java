package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.IOException;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.model.Account;
import com.blink.shop.model.Category;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Product;
import com.blink.shop.model.Review;
import com.blink.shop.model.Session;
import com.blink.shop.model.Sku;
import com.blink.shop.model.Times;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;

import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;

/** 用服务端的接口样例检查：Android 发出的请求和样例一致，样例响应能被正确解析。 */
public class ShopApiContractTest {

    private MockWebServer server;
    private FakeSession session;
    private ShopApi api;

    @Before
    public void setUp() throws IOException {
        server = new MockWebServer();
        server.start();
        session = new FakeSession(null);
        api = new ShopApi(new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig(server.url("/api/v1").toString()),
                session, () -> true));
    }

    @After
    public void tearDown() throws IOException {
        server.shutdown();
    }

    private Fixture serve(String name) throws Exception {
        Fixture f = Fixture.load(name);
        server.enqueue(f.mockResponse());
        return f;
    }

    private RecordedRequest taken(Fixture f) throws Exception {
        RecordedRequest r = server.takeRequest();
        f.assertRequest(r);
        return r;
    }

    private static void assertAccount(JSONObject want, Account got) throws Exception {
        assertEquals(want.getString("username"), got.username);
        assertEquals(want.getString("display_name"), got.displayName);
        assertEquals(want.getString("phone"), got.phone);
        assertEquals(want.getString("email"), got.email);
        assertEquals(want.getString("role"), got.role);
        assertEquals(want.getString("status"), got.status);
    }

    @Test
    public void login() throws Exception {
        Fixture f = serve("auth_login_ok");
        Session s = api.login("blink_user", "BlinkDev#2026");
        taken(f);
        assertEquals("tok_fixture", s.token);
        assertEquals(Times.parseIsoMillis("2026-10-07T00:00:00Z"), s.expiresAtMillis);
        assertAccount(f.body().getJSONObject("account"), s.account);
        assertEquals("acct_seed_user", s.account.accountId);
    }

    @Test
    public void loginInvalidCredential() throws Exception {
        Fixture f = serve("auth_login_invalid_credential");
        try {
            api.login("blink_user", "wrong-password");
            fail();
        } catch (ApiException e) {
            assertEquals("invalid_credential", e.code());
            assertEquals("账号或密码错误", e.getMessage());
        }
        taken(f);
    }

    @Test
    public void register() throws Exception {
        Fixture f = serve("auth_register_created");
        Session s = api.register("new_user", "Passw0rd!", "新用户");
        taken(f);
        assertEquals("new_user", s.account.username);
        assertEquals("新用户", s.account.displayName);
    }

    @Test
    public void registerWithoutDisplayNameOmitsField() throws Exception {
        Fixture f = serve("auth_register_username_exists");
        try {
            api.register("BLINK_USER", "Passw0rd!", "  ");
            fail();
        } catch (ApiException e) {
            assertEquals(409, e.status());
            assertEquals("username_exists", e.code());
            assertEquals("账号已存在", e.getMessage());
        }
        taken(f);
    }

    /** 服务端允许中文等多字节密码（不超过 72 字节）：客户端原样发送，不做任何转换。 */
    @Test
    public void unicodePasswordSentUnchanged() throws Exception {
        String password = "蓝色Blink密码2026";
        assertTrue(password.getBytes(java.nio.charset.StandardCharsets.UTF_8).length <= 72);
        serve("auth_register_created");
        api.register("new_user", password, "新用户");
        RecordedRequest reg = server.takeRequest();
        assertEquals(password, new JSONObject(reg.getBody().readUtf8()).getString("password"));
        serve("auth_login_ok");
        api.login("blink_user", password);
        RecordedRequest login = server.takeRequest();
        assertTrue(login.getHeader("Content-Type").contains("charset=utf-8"));
        assertEquals(password, new JSONObject(login.getBody().readUtf8()).getString("password"));
    }

    @Test
    public void meAndExpiredToken() throws Exception {
        session.token = "tok_live";
        Fixture f = serve("auth_me_ok");
        assertAccount(f.body(), api.me());
        assertEquals("Bearer tok_live", taken(f).getHeader("Authorization"));

        Fixture expired = serve("auth_token_invalid");
        try {
            api.me();
            fail();
        } catch (ApiException e) {
            assertTrue(e.isUnauthorized());
            assertEquals("登录已失效，请重新登录", e.getMessage());
        }
        server.takeRequest();
        assertEquals(java.util.Collections.singletonList("tok_live"), session.rejected);
        assertEquals("/api/v1/auth/me", expired.request.getString("path"));
    }

    @Test
    public void updateProfileAndContact() throws Exception {
        session.token = "tok_live";
        Fixture f = serve("account_profile_update_ok");
        assertAccount(f.body(), api.updateProfile("小蓝", null));
        taken(f);
        Fixture c = serve("account_contact_update_ok");
        assertAccount(c.body(), api.updateContact("13800000000", "user@example.com"));
        taken(c);
    }

    @Test
    public void categoriesTree() throws Exception {
        Fixture f = serve("categories_tree_ok");
        List<Category> tree = api.categories();
        taken(f);
        JSONArray want = f.body().getJSONArray("items");
        assertEquals(want.length(), tree.size());
        assertEquals("c_digital", tree.get(0).categoryId);
        assertEquals("手机", tree.get(0).children.get(0).name);
        assertEquals("c_digital", tree.get(0).children.get(0).parentId);
        assertEquals("c_mouse", Category.find(tree, "c_mouse").categoryId);
    }

    @Test
    public void productList() throws Exception {
        Fixture f = serve("products_list_ok");
        PageResult<Product> page = api.products("", "c_office", 1);
        taken(f);
        assertEquals(2, page.total);
        assertEquals(1, page.pageSize);
        assertTrue(page.hasMore());
        Product p = page.items.get(0);
        JSONObject want = f.body().getJSONArray("items").getJSONObject(0);
        assertEquals(want.getString("product_id"), p.productId);
        assertEquals(want.getString("sku_id"), p.skuId);
        assertEquals("129.00", p.price);
        assertEquals("159.00", p.marketPrice);
        assertEquals("Blink · Blink 数码旗舰店", p.byline());
        assertEquals(3, p.tags.size());
        assertEquals("in_stock", p.stockStatus);
        assertEquals(-1, p.stockQuantity);
    }

    @Test
    public void productSearchEncodesKeyword() throws Exception {
        Fixture f = serve("products_list_ok");
        api.products("降噪 耳机&", "", 2);
        RecordedRequest r = server.takeRequest();
        assertEquals("降噪 耳机&", r.getRequestUrl().queryParameter("keyword"));
        assertEquals("2", r.getRequestUrl().queryParameter("page"));
        assertEquals(null, r.getRequestUrl().queryParameter("category_id"));
        assertEquals("GET", f.request.getString("method"));
    }

    @Test
    public void productDetail() throws Exception {
        Fixture f = serve("product_detail_ok");
        Product p = api.product("p_seed_earbuds");
        taken(f);
        assertEquals(2, p.gallery().size());
        assertEquals(5, p.stockQuantity);
        assertEquals("low_stock", p.stockStatus);
        assertEquals("30 小时", p.attributes.get(0).display());
        assertEquals("IPX4", p.attributes.get(1).display());
        assertEquals("运动防水场景", p.notSuitableFor.get(0));
        assertEquals("入耳式主动降噪蓝牙耳机。", p.description);
    }

    @Test
    public void productNotFound() throws Exception {
        Fixture f = serve("product_not_found");
        try {
            api.product("p_seed_speaker");
            fail();
        } catch (ApiException e) {
            assertEquals("product_not_found", e.code());
            assertEquals("商品不存在或已下架", e.getMessage());
        }
        taken(f);
    }

    @Test
    public void skus() throws Exception {
        Fixture f = serve("product_skus_ok");
        PageResult<Sku> page = api.skus("p_seed_nova");
        taken(f);
        assertEquals(2, page.items.size());
        Sku first = page.items.get(0);
        assertTrue(first.isDefault);
        assertEquals("2999.00", first.price);
        assertEquals(50, first.stockQuantity);
        assertEquals("128GB / 曜石黑", first.label());
    }

    @Test
    public void reviews() throws Exception {
        Fixture f = serve("product_reviews_ok");
        PageResult<Review> page = api.reviews("p_seed_mouse", 5);
        taken(f);
        Review r = page.items.get(0);
        assertEquals("演***", r.reviewerName);
        assertEquals(5, r.rating);
        assertEquals("★★★★★", r.stars());
        assertEquals("感谢支持！", r.merchantReply);
        assertEquals(2, r.tags.size());
    }

    @Test
    public void rateLimited() throws Exception {
        Fixture f = serve("error_rate_limited");
        try {
            api.health();
            fail();
        } catch (ApiException e) {
            assertEquals(429, e.status());
            assertEquals("rate_limited", e.code());
            assertEquals("请求过于频繁，请稍后再试", e.getMessage());
        }
        assertEquals(429, f.response.getInt("status"));
    }
}
