package com.blink.shop.data;

import java.io.UnsupportedEncodingException;
import java.net.URLEncoder;
import java.util.ArrayList;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import com.blink.shop.model.Account;
import com.blink.shop.model.Cart;
import com.blink.shop.model.Category;
import com.blink.shop.model.ChatSession;
import com.blink.shop.model.Coupon;
import com.blink.shop.model.DiscountPreview;
import com.blink.shop.model.Order;
import com.blink.shop.model.PageResult;
import com.blink.shop.model.Product;
import com.blink.shop.model.Review;
import com.blink.shop.model.Session;
import com.blink.shop.model.Sku;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.SseClient;

import okhttp3.RequestBody;

/** 用户端用到的接口。全部是阻塞调用，只在后台线程使用。 */
public final class ShopApi {

    public static final int PRODUCT_PAGE_SIZE = 20;
    static final int CHAT_SESSION_PAGE_SIZE = 20;

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

    // ---------- 购物车 ----------

    public Cart cart() throws ApiException {
        return Cart.fromJson(api.get("/cart"));
    }

    /** skuId 为空用默认规格。同一规格再次加购会累加数量。 */
    public Cart addToCart(String productId, String skuId, int quantity) throws ApiException {
        JSONObject body = obj("product_id", productId);
        if (skuId != null && !skuId.isEmpty()) {
            put(body, "sku_id", skuId);
        }
        putInt(body, "quantity", quantity);
        return Cart.fromJson(api.post("/cart/items", body));
    }

    /** quantity 为 null 表示不改数量，selected 为 null 表示不改选中状态。 */
    public Cart updateCartItem(String cartItemId, Integer quantity, Boolean selected) throws ApiException {
        JSONObject body = new JSONObject();
        try {
            if (quantity != null) {
                body.put("quantity", quantity.intValue());
            }
            if (selected != null) {
                body.put("selected", selected.booleanValue());
            }
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
        return Cart.fromJson(api.patch("/cart/items/" + enc(cartItemId), body));
    }

    public Cart deleteCartItem(String cartItemId) throws ApiException {
        return Cart.fromJson(api.delete("/cart/items/" + enc(cartItemId)));
    }

    /** couponIds 为 null 时服务端自动选最优券；空列表表示不用券。 */
    public DiscountPreview discountPreview(List<String> couponIds) throws ApiException {
        String path = "/cart/discount-preview";
        if (couponIds != null) {
            path += "?user_coupon_ids=" + enc(joinIds(couponIds));
        }
        return DiscountPreview.fromJson(api.get(path));
    }

    // ---------- 优惠券 ----------

    public PageResult<Coupon> availableCoupons() throws ApiException {
        return PageResult.fromJson(api.get("/coupons/available?page_size=100"), Coupon::fromJson);
    }

    /** status 为空表示全部。 */
    public PageResult<Coupon.Mine> myCoupons(String status) throws ApiException {
        String path = "/coupons/mine?page_size=100";
        if (status != null && !status.isEmpty()) {
            path += "&status=" + enc(status);
        }
        return PageResult.fromJson(api.get(path), Coupon.Mine::fromJson);
    }

    public Coupon.Mine claimCoupon(String couponId) throws ApiException {
        return Coupon.Mine.fromJson(api.post("/coupons/" + enc(couponId) + ":claim", new JSONObject()));
    }

    // ---------- 订单 ----------

    /** 下单结果：按店铺拆成的订单。replayed 表示这个幂等键之前已经下过单，返回的是那次的订单。 */
    public static final class CheckoutResult {
        public final List<Order> orders;
        public final boolean replayed;

        CheckoutResult(List<Order> orders, boolean replayed) {
            this.orders = orders;
            this.replayed = replayed;
        }
    }

    /**
     * 结算下单。idempotencyKey 在确认页生成并保存，重试（含 App 被杀后重进）用同一个键，不会重复下单。
     * couponIds 为 null 时自动选最优券；expectedPayAmount 是确认页显示的金额，与重新计算不一致时 409 price_changed。
     */
    public CheckoutResult checkout(String idempotencyKey, List<String> couponIds, String expectedPayAmount) throws ApiException {
        JSONObject body = obj("idempotency_key", idempotencyKey, "expected_pay_amount", expectedPayAmount);
        if (couponIds != null) {
            try {
                body.put("user_coupon_ids", new JSONArray(couponIds));
            } catch (JSONException e) {
                throw new IllegalStateException(e);
            }
        }
        JSONObject r = api.post("/orders:checkout", body);
        List<Order> orders = new ArrayList<>(PageResult.fromJson(r, Order::fromJson).items);
        return new CheckoutResult(orders, r.optBoolean("replayed", false));
    }

    /** status 为空表示全部。 */
    public PageResult<Order> orders(String status, int page) throws ApiException {
        String path = "/orders?page=" + page + "&page_size=" + PRODUCT_PAGE_SIZE;
        if (status != null && !status.isEmpty()) {
            path += "&status=" + enc(status);
        }
        return PageResult.fromJson(api.get(path), Order::fromJson);
    }

    public Order order(String orderId) throws ApiException {
        return Order.fromJson(api.get("/orders/" + enc(orderId)));
    }

    /** 模拟支付。method 为 mock_alipay / mock_wechat / mock_balance。 */
    public Order pay(String orderId, String method) throws ApiException {
        return Order.fromJson(api.post("/orders/" + enc(orderId) + ":pay", obj("method", method)).optJSONObject("order"));
    }

    /** reason 为空时服务端记为“用户取消”。 */
    public Order cancelOrder(String orderId, String reason) throws ApiException {
        JSONObject body = new JSONObject();
        if (reason != null && !reason.trim().isEmpty()) {
            put(body, "reason", reason.trim());
        }
        return Order.fromJson(api.post("/orders/" + enc(orderId) + ":cancel", body));
    }

    public Order confirmReceipt(String orderId) throws ApiException {
        return Order.fromJson(api.post("/orders/" + enc(orderId) + ":confirm-receipt", new JSONObject()));
    }

    /** 评价已完成订单里的一件商品，返回评价 ID。 */
    public String review(String orderId, String orderItemId, int rating, String content, List<String> tags) throws ApiException {
        JSONObject body = obj("content", content);
        try {
            body.put("rating", rating);
            body.put("tags", new JSONArray(tags));
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
        return api.post("/orders/" + enc(orderId) + "/items/" + enc(orderItemId) + ":review", body).optString("review_id", "");
    }

    // ---------- 导购 ----------

    /** 会话列表：置顶在前、按最近消息倒序；keyword 为空表示全部。只列有消息的会话。 */
    public PageResult<ChatSession> chatSessions(String keyword, int page) throws ApiException {
        StringBuilder path = new StringBuilder("/agent/sessions?page=").append(page).append("&page_size=").append(CHAT_SESSION_PAGE_SIZE);
        if (keyword != null && !keyword.trim().isEmpty()) {
            path.append("&keyword=").append(enc(keyword.trim()));
        }
        return PageResult.fromJson(api.get(path.toString()), ChatSession::fromJson);
    }

    /** 会话详情：{session, messages:[{message_id, client_message_id, content, attachments, created_at, run}]}。 */
    public JSONObject chatSessionDetail(String sessionId) throws ApiException {
        return api.get("/agent/sessions/" + enc(sessionId));
    }

    public ChatSession renameChatSession(String sessionId, String title) throws ApiException {
        return ChatSession.fromJson(api.patch("/agent/sessions/" + enc(sessionId), obj("title", title)));
    }

    /** 显式设置置顶状态（不是切换）。 */
    public ChatSession pinChatSession(String sessionId, boolean pinned) throws ApiException {
        JSONObject body = new JSONObject();
        try {
            body.put("pinned", pinned);
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
        return ChatSession.fromJson(api.post("/agent/sessions/" + enc(sessionId) + ":pin", body));
    }

    public void deleteChatSession(String sessionId) throws ApiException {
        api.delete("/agent/sessions/" + enc(sessionId));
    }

    /** 停止一次运行。已经结束的运行服务端返回 409 run_finished；已取消的幂等。 */
    public void cancelAgentRun(String runId) throws ApiException {
        api.post("/agent/runs/" + enc(runId) + ":cancel", new JSONObject());
    }

    /** 新建导购会话，返回 session_id。 */
    public String createChatSession() throws ApiException {
        String id = api.post("/agent/sessions", new JSONObject()).optString("session_id", "");
        if (id.isEmpty()) {
            throw ApiException.badResponse(null);
        }
        return id;
    }

    /**
     * 向会话发送一条消息并流式接收回答（SSE）。clientMessageId 由调用方生成并在重试时复用：
     * 服务端对同一个 ID 不会再运行，而是重放已有的回答。取消返回的 Stream 会断开连接，服务端把这次运行记为已取消。
     */
    /** 发一条导购消息并接收流式回答；attachments 是先用 uploadFile 上传得到的 file_id（只能是本人上传的文件）。 */
    public SseClient.Stream streamAgentMessage(String sessionId, String clientMessageId, String content, List<String> attachments,
            SseClient.Listener listener) {
        JSONObject body = obj("client_message_id", clientMessageId, "content", content);
        if (attachments != null && !attachments.isEmpty()) {
            JSONArray list = new JSONArray();
            for (String id : attachments) {
                list.put(obj("file_id", id));
            }
            try {
                body.put("attachments", list);
            } catch (JSONException e) {
                throw new IllegalStateException(e);
            }
        }
        return new SseClient(api).post("/agent/sessions/" + enc(sessionId) + "/messages:stream", body, listener);
    }

    /** 上传私有文件（聊天附图），返回 file_id。类型由服务端按内容判断。 */
    public String uploadFile(byte[] data, String mimeType, String filename) throws ApiException {
        JSONObject r = api.upload("/files", "file", filename, mimeType, data);
        JSONObject file = r.optJSONObject("file");
        String id = file == null ? "" : file.optString("file_id", "");
        if (id.isEmpty()) {
            throw ApiException.badResponse(null);
        }
        return id;
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

    private static String joinIds(List<String> ids) {
        StringBuilder b = new StringBuilder();
        for (String id : ids) {
            if (b.length() > 0) {
                b.append(',');
            }
            b.append(id);
        }
        return b.toString();
    }

    private static void putInt(JSONObject o, String k, int v) {
        try {
            o.put(k, v);
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
    }

    private static void put(JSONObject o, String k, String v) {
        try {
            o.put(k, v == null ? "" : v);
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
    }
}
