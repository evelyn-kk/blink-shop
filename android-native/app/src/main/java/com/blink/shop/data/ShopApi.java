package com.blink.shop.data;

import java.io.UnsupportedEncodingException;
import java.net.URLEncoder;
import java.util.List;

import org.json.JSONException;
import org.json.JSONObject;

import com.blink.shop.model.Account;
import com.blink.shop.model.Category;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Product;
import com.blink.shop.model.Review;
import com.blink.shop.model.Session;
import com.blink.shop.model.Sku;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiException;

import okhttp3.RequestBody;

/** 用户端用到的接口。全部是阻塞调用，只在后台线程使用。 */
public final class ShopApi {

    public static final int PRODUCT_PAGE_SIZE = 20;

    private final ApiClient api;

    public ShopApi(ApiClient api) {
        this.api = api;
    }

    public ApiClient client() {
        return api;
    }

    // ---------- 认证与账户 ----------

    public Session login(String username, String password) throws ApiException {
        return Session.fromJson(api.post("/auth/login", obj("username", username, "password", password)));
    }

    /** displayName 为空时由服务端使用用户名。 */
    public Session register(String username, String password, String displayName) throws ApiException {
        JSONObject body = obj("username", username, "password", password);
        if (displayName != null && !displayName.trim().isEmpty()) {
            put(body, "display_name", displayName.trim());
        }
        return Session.fromJson(api.post("/auth/register", body));
    }

    /** 撤销指定 token（本地先退出，再在后台通知服务端，所以 token 由调用方传入）。 */
    public void logout(String token) throws ApiException {
        api.execute(api.request("/auth/logout").header("Authorization", "Bearer " + token)
                .post(RequestBody.create("{}", ApiClient.JSON)).build());
    }

    public Account me() throws ApiException {
        return Account.fromJson(api.get("/auth/me"));
    }

    /** 参数为 null 表示不修改。 */
    public Account updateProfile(String displayName, String avatarUrl) throws ApiException {
        JSONObject body = new JSONObject();
        if (displayName != null) {
            put(body, "display_name", displayName);
        }
        if (avatarUrl != null) {
            put(body, "avatar_url", avatarUrl);
        }
        return Account.fromJson(api.patch("/account/profile", body));
    }

    /** 两个字段都提交；空串表示清空。 */
    public Account updateContact(String phone, String email) throws ApiException {
        return Account.fromJson(api.patch("/account/contact", obj("phone", phone, "email", email)));
    }

    public void deleteAccount() throws ApiException {
        api.delete("/account");
    }

    /** 上传头像，返回头像地址（再用 updateProfile 设置）。 */
    public String uploadAvatar(byte[] data, String mimeType, String filename) throws ApiException {
        JSONObject r = api.upload("/uploads/avatar", "file", filename, mimeType, data);
        String url = r.optString("url", "");
        if (url.isEmpty()) {
            throw ApiException.badResponse(null);
        }
        return url;
    }

    // ---------- 目录 ----------

    public List<Category> categories() throws ApiException {
        return Category.listFrom(api.get("/categories/tree").optJSONArray("items"));
    }

    /** keyword、categoryId 为空表示不筛选。 */
    public PageResult<Product> products(String keyword, String categoryId, int page) throws ApiException {
        StringBuilder path = new StringBuilder("/products?page=").append(page).append("&page_size=").append(PRODUCT_PAGE_SIZE);
        if (keyword != null && !keyword.trim().isEmpty()) {
            path.append("&keyword=").append(enc(keyword.trim()));
        }
        if (categoryId != null && !categoryId.isEmpty()) {
            path.append("&category_id=").append(enc(categoryId));
        }
        return PageResult.fromJson(api.get(path.toString()), Product::fromJson);
    }

    public Product product(String productId) throws ApiException {
        return Product.fromJson(api.get("/products/" + enc(productId)));
    }

    public PageResult<Sku> skus(String productId) throws ApiException {
        return PageResult.fromJson(api.get("/products/" + enc(productId) + "/skus?page_size=100"), Sku::fromJson);
    }

    public PageResult<Review> reviews(String productId, int pageSize) throws ApiException {
        return PageResult.fromJson(api.get("/products/" + enc(productId) + "/reviews?page_size=" + pageSize), Review::fromJson);
    }

    /** 检查服务地址是否可用（高级设置里的“测试连接”）。 */
    public void health() throws ApiException {
        api.get("/health");
    }

    static String enc(String s) {
        try {
            return URLEncoder.encode(s, "UTF-8").replace("+", "%20");
        } catch (UnsupportedEncodingException e) {
            throw new IllegalStateException(e);
        }
    }

    private static JSONObject obj(String... kv) {
        JSONObject o = new JSONObject();
        for (int i = 0; i + 1 < kv.length; i += 2) {
            put(o, kv[i], kv[i + 1]);
        }
        return o;
    }

    private static void put(JSONObject o, String k, String v) {
        try {
            o.put(k, v == null ? "" : v);
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
    }
}
