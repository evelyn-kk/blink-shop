package com.blink.shop.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import org.json.JSONArray;
import org.json.JSONObject;

/** 订单（含订单项；详情和写操作的响应带支付单）。 */
public final class Order {

    public static final String PENDING_PAYMENT = "pending_payment";
    public static final String PAID = "paid";
    public static final String SHIPPED = "shipped";
    public static final String COMPLETED = "completed";
    public static final String CANCELLED = "cancelled";

    public static final class Item {
        public final String orderItemId;
        public final String productId;
        public final String skuId;
        public final String name;
        public final String skuName;
        public final String imageUrl;
        public final String price;
        public final int quantity;
        public final String amount;
        public final String reviewId;

        Item(JSONObject o) {
            orderItemId = Json.str(o, "order_item_id");
            productId = Json.str(o, "product_id");
            skuId = Json.str(o, "sku_id");
            name = Json.str(o, "name");
            skuName = Json.str(o, "sku_name");
            imageUrl = Json.str(o, "image_url");
            price = Json.str(o, "price");
            quantity = o.optInt("quantity", 1);
            amount = Json.str(o, "amount");
            reviewId = Json.str(o, "review_id");
        }

        public boolean reviewed() {
            return !reviewId.isEmpty();
        }
    }

    public static final class Payment {
        public final String paymentId;
        public final String amount;
        public final String status;
        public final String method;
        public final String transactionNo;
        public final String paidAt;

        Payment(JSONObject o) {
            paymentId = Json.str(o, "payment_id");
            amount = Json.str(o, "amount");
            status = Json.str(o, "status");
            method = Json.str(o, "method");
            transactionNo = Json.str(o, "transaction_no");
            paidAt = Json.str(o, "paid_at");
        }
    }

    public final String orderId;
    public final String orderNo;
    public final String merchantId;
    public final String merchantName;
    public final String status;
    public final String totalAmount;
    public final String discountAmount;
    public final String payAmount;
    public final String paymentDeadlineAt;
    public final String paidAt;
    public final String shippedAt;
    public final String completedAt;
    public final String closedAt;
    public final String cancelReason;
    public final String createdAt;
    public final List<Item> items;
    /** 列表接口不带支付单，为 null。 */
    public final Payment payment;

    private Order(JSONObject o) {
        orderId = Json.str(o, "order_id");
        orderNo = Json.str(o, "order_no");
        merchantId = Json.str(o, "merchant_id");
        merchantName = Json.str(o, "merchant_name");
        status = Json.str(o, "status");
        totalAmount = Json.str(o, "total_amount");
        discountAmount = Json.str(o, "discount_amount");
        payAmount = Json.str(o, "pay_amount");
        paymentDeadlineAt = Json.str(o, "payment_deadline_at");
        paidAt = Json.str(o, "paid_at");
        shippedAt = Json.str(o, "shipped_at");
        completedAt = Json.str(o, "completed_at");
        closedAt = Json.str(o, "closed_at");
        cancelReason = Json.str(o, "cancel_reason");
        createdAt = Json.str(o, "created_at");
        List<Item> list = new ArrayList<>();
        JSONArray arr = o.optJSONArray("items");
        for (int i = 0; arr != null && i < arr.length(); i++) {
            JSONObject it = arr.optJSONObject(i);
            if (it != null) {
                list.add(new Item(it));
            }
        }
        items = Collections.unmodifiableList(list);
        JSONObject p = o.optJSONObject("payment");
        payment = p == null ? null : new Payment(p);
    }

    public static Order fromJson(JSONObject o) {
        return new Order(o == null ? new JSONObject() : o);
    }

    /** 订单状态文字（与 Web 端一致）。 */
    public static String statusLabel(String status) {
        switch (status) {
            case PENDING_PAYMENT:
                return "待支付";
            case PAID:
                return "待发货";
            case SHIPPED:
                return "已发货";
            case COMPLETED:
                return "已完成";
            case CANCELLED:
                return "已取消";
            default:
                return status;
        }
    }

    /** 全部商品件数。 */
    public int quantity() {
        int n = 0;
        for (Item it : items) {
            n += it.quantity;
        }
        return n;
    }

    /** 还有没评价的商品（已完成订单才能评价）。 */
    public boolean hasUnreviewed() {
        if (!COMPLETED.equals(status)) {
            return false;
        }
        for (Item it : items) {
            if (!it.reviewed()) {
                return true;
            }
        }
        return false;
    }
}
