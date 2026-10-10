package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
)

// 每个工具覆盖五类用例：正常、缺参/非法参数、越权（别人的资源或没有用户）、状态不允许、重复请求，以及资源不存在。

func TestRegistryPolicy(t *testing.T) {
	e := newEnv(t)
	e.mustFail(t, e.tc(seed.UserID, IntentCart), "no_such_tool", nil, CodeToolNotFound)
	// 导航和非导购不能调用任何工具；只读意图不能调用写工具；购物车意图不能支付。
	e.mustFail(t, e.tc(seed.UserID, IntentNavigation), ToolGetCart, nil, CodeToolNotAllowed)
	e.mustFail(t, e.tc(seed.UserID, IntentNonGuide), ToolSearchProducts, map[string]any{"query": "耳机"}, CodeToolNotAllowed)
	e.mustFail(t, e.tc(seed.UserID, IntentProductSearch, "p_seed_earbuds"), ToolAddCartItem, map[string]any{"product_id": "p_seed_earbuds"}, CodeToolNotAllowed)
	e.mustFail(t, e.tc(seed.UserID, IntentCart), ToolPayOrder, map[string]any{"order_id": "o_seed_pending"}, CodeToolNotAllowed)
	e.mustFail(t, e.tc(seed.UserID, IntentCart), ToolCheckout, nil, CodeToolNotAllowed)
	// 没有当前用户
	e.mustFail(t, e.tc("", IntentCart), ToolGetCart, nil, CodeUnauthorized)
	// 白名单里没有写工具的意图
	for _, intent := range []Intent{IntentGuide, IntentProductSearch, IntentProductCompare, IntentKnowledge, IntentImageSearch, IntentNavigation, IntentNonGuide} {
		for _, name := range e.reg.Allowed(context.Background(), intent) {
			if e.reg.tools[name].Write {
				t.Errorf("intent %s allows write tool %s", intent, name)
			}
		}
	}
	if len(e.reg.ExportTools()) != 19 {
		t.Fatalf("tools: %d", len(e.reg.ExportTools()))
	}
}

func TestSearchProductsTool(t *testing.T) {
	e := newEnv(t)
	tc := e.tc(seed.User2ID, IntentProductSearch)
	res := e.mustOK(t, tc, ToolSearchProducts, map[string]any{"query": "通勤降噪耳机"}).(ProductSearchResult)
	if res.Relevance != RelevanceOK || len(res.Products) == 0 || res.Products[0].ProductID != "p_seed_earbuds" || res.Products[0].CategoryName != "耳机音箱" {
		t.Fatalf("earbuds: %+v", res)
	}
	// 缺参 / 非法参数
	e.mustFail(t, tc, ToolSearchProducts, nil, CodeInvalidArgument)
	e.mustFail(t, tc, ToolSearchProducts, map[string]any{"query": ""}, CodeInvalidArgument)
	e.mustFail(t, tc, ToolSearchProducts, map[string]any{"query": "耳机", "limit": float64(0)}, CodeInvalidArgument)
	e.mustFail(t, tc, ToolSearchProducts, map[string]any{"query": "耳机", "limit": float64(21)}, CodeInvalidArgument)
	e.mustFail(t, tc, ToolSearchProducts, map[string]any{"query": "耳机", "bogus": 1}, CodeInvalidArgument)
	e.mustFail(t, tc, ToolSearchProducts, map[string]any{"query": "耳机", "exclude": "x"}, CodeInvalidArgument)
	// 预算过滤：两款手机都超过 300
	res = e.mustOK(t, tc, ToolSearchProducts, map[string]any{"query": "手机", "max_price": 300.0}).(ProductSearchResult)
	if len(res.Products) != 0 || res.Filtered != 2 {
		t.Fatalf("max_price: %+v", res)
	}
	// 排除词
	res = e.mustOK(t, tc, ToolSearchProducts, map[string]any{"query": "手机", "exclude": []any{"vista"}}).(ProductSearchResult)
	if len(res.Products) != 1 || res.Products[0].ProductID != "p_seed_nova" {
		t.Fatalf("exclude: %+v", res.Products)
	}
	// 没有相关商品；下架、风控、删除的商品不出现
	for _, q := range []string{"今天天气", "便携音箱", "移动电源", "Nova 9"} {
		res = e.mustOK(t, tc, ToolSearchProducts, map[string]any{"query": q}).(ProductSearchResult)
		for _, p := range res.Products {
			if p.ProductID == "p_seed_speaker" || p.ProductID == "p_seed_powerbank" || p.ProductID == "p_seed_legacy" {
				t.Fatalf("%q returned non-visible product %s", q, p.ProductID)
			}
		}
		if q == "今天天气" && (res.Relevance != RelevanceNone || len(res.Products) != 0) {
			t.Fatalf("weather: %+v", res)
		}
	}
	// 弱命中：只有二元组部分命中（“鼠标垫”里的“鼠标”）仍返回但标为 weak 或 ok，不会是 none
	res = e.mustOK(t, tc, ToolSearchProducts, map[string]any{"query": "静音鼠标"}).(ProductSearchResult)
	if res.Relevance != RelevanceOK || res.Products[0].ProductID != "p_seed_mouse" {
		t.Fatalf("mouse: %+v", res)
	}
}

func TestSearchKnowledgeTool(t *testing.T) {
	e := newEnv(t)
	tc := e.tc(seed.User2ID, IntentKnowledge)
	res := e.mustOK(t, tc, ToolSearchKnowledge, map[string]any{"query": "七天无理由怎么退"}).(KnowledgeResult)
	if len(res.Citations) == 0 || res.Citations[0].DocumentID != "doc_seed_after_sales" || res.Mode != "keyword" {
		t.Fatalf("after sales: %+v", res)
	}
	e.mustFail(t, tc, ToolSearchKnowledge, nil, CodeInvalidArgument)
	e.mustFail(t, tc, ToolSearchKnowledge, map[string]any{"query": "x", "limit": float64(99)}, CodeInvalidArgument)
	res = e.mustOK(t, tc, ToolSearchKnowledge, map[string]any{"query": "拍照", "product_id": "p_seed_nova"}).(KnowledgeResult)
	for _, c := range res.Citations {
		if c.ProductID != "p_seed_nova" {
			t.Fatalf("product filter: %+v", c)
		}
	}
	if len(res.Citations) == 0 {
		t.Fatal("nova citations")
	}
	// 未配置检索器
	deps := e.runner.deps
	deps.Retriever = nil
	reg := NewRegistry(deps)
	if obs := reg.Call(context.Background(), tc, ToolSearchKnowledge, map[string]any{"query": "退货"}); obs.OK || obs.Code != CodeUnavailable {
		t.Fatalf("no retriever: %+v", obs)
	}
}

func TestCartTools(t *testing.T) {
	e := newEnv(t)
	me := e.tc(seed.User2ID, IntentCart, "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_missing", "p_seed_speaker")
	other := e.tc(seed.UserID, IntentCart)
	if cart := e.mustOK(t, me, ToolGetCart, nil).(shop.Cart); len(cart.Items) != 0 {
		t.Fatalf("cart should be empty: %+v", cart)
	}
	// 加购：来源不可信 / 缺参 / 非法数量
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_nova"}, CodeProductNotTrusted)
	e.mustFail(t, me, ToolAddCartItem, nil, CodeInvalidArgument)
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_earbuds", "quantity": float64(0)}, CodeInvalidArgument)
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_earbuds", "quantity": float64(100)}, CodeInvalidArgument)
	// 资源不存在 / 状态不允许
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_missing"}, "product_not_found")
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_speaker"}, "product_not_found")
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_keyboard"}, "out_of_stock")
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_earbuds", "quantity": float64(6)}, "insufficient_stock")
	e.mustFail(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_earbuds", "sku_id": "sku_nope"}, CodeInvalidArgument)
	// 正常 + 重复请求累加
	data := toJSONMap(e.mustOK(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_earbuds", "quantity": float64(2)}))
	data2 := toJSONMap(e.mustOK(t, me, ToolAddCartItem, map[string]any{"product_id": "p_seed_earbuds"}))
	if data["quantity"] != float64(2) || data2["quantity"] != float64(3) || data["cart_item_id"] != data2["cart_item_id"] {
		t.Fatalf("accumulate: %v %v", data, data2)
	}
	itemID := data["cart_item_id"].(string)
	if !strings.Contains(e.logs.String(), `"msg":"audit","action":"agent.tool","tool":"add_cart_item"`) {
		t.Fatalf("write tool must be audited: %s", e.logs.String())
	}
	// 修改：缺参 / 越权 / 超库存 / 正常
	e.mustFail(t, me, ToolUpdateCartItem, map[string]any{"cart_item_id": itemID}, CodeInvalidArgument)
	e.mustFail(t, me, ToolUpdateCartItem, map[string]any{"quantity": float64(1)}, CodeInvalidArgument)
	e.mustFail(t, other, ToolUpdateCartItem, map[string]any{"cart_item_id": itemID, "quantity": float64(1)}, "cart_item_not_found")
	e.mustFail(t, me, ToolUpdateCartItem, map[string]any{"cart_item_id": itemID, "quantity": float64(9)}, "insufficient_stock")
	e.mustFail(t, me, ToolUpdateCartItem, map[string]any{"cart_item_id": "ci_missing", "quantity": float64(1)}, "cart_item_not_found")
	cart := e.mustOK(t, me, ToolUpdateCartItem, map[string]any{"cart_item_id": itemID, "quantity": float64(1), "selected": false}).(shop.Cart)
	if cart.Items[0].Quantity != 1 || cart.Items[0].Selected || cart.Summary.SelectedCount != 0 {
		t.Fatalf("update: %+v", cart.Items[0])
	}
	// 试算：正常 / 非法券 / 太多券
	preview := e.mustOK(t, me, ToolPreviewDiscount, nil).(shop.DiscountPreview)
	if preview.PayAmount != 0 {
		t.Fatalf("nothing selected: %+v", preview)
	}
	e.mustOK(t, me, ToolUpdateCartItem, map[string]any{"cart_item_id": itemID, "selected": true})
	preview = e.mustOK(t, me, ToolPreviewDiscount, nil).(shop.DiscountPreview)
	if preview.PayAmount.String() != "569.05" { // 599 × 0.95
		t.Fatalf("preview: %+v", preview)
	}
	e.mustFail(t, me, ToolPreviewDiscount, map[string]any{"user_coupon_ids": []any{"uc_bogus"}}, "coupon_not_applicable")
	many := make([]any, 21)
	for i := range many {
		many[i] = "uc" + itoa(i)
	}
	e.mustFail(t, me, ToolPreviewDiscount, map[string]any{"user_coupon_ids": many}, CodeInvalidArgument)
	// 删除：越权 / 正常 / 重复
	e.mustFail(t, other, ToolDeleteCartItem, map[string]any{"cart_item_id": itemID}, "cart_item_not_found")
	e.mustFail(t, me, ToolDeleteCartItem, nil, CodeInvalidArgument)
	if cart := e.mustOK(t, me, ToolDeleteCartItem, map[string]any{"cart_item_id": itemID}).(shop.Cart); len(cart.Items) != 0 {
		t.Fatalf("delete: %+v", cart)
	}
	e.mustFail(t, me, ToolDeleteCartItem, map[string]any{"cart_item_id": itemID}, "cart_item_not_found")
}

func TestCheckoutAndOrderTools(t *testing.T) {
	e := newEnv(t)
	cart := e.tc(seed.User2ID, IntentCart, "p_seed_lamp", "p_seed_mouse")
	co := e.tc(seed.User2ID, IntentCheckout)
	od := e.tc(seed.User2ID, IntentOrder)
	odOther := e.tc(seed.UserID, IntentOrder)

	e.mustFail(t, co, ToolCheckout, nil, "empty_cart")
	e.mustFail(t, co, ToolCheckout, map[string]any{"user_coupon_ids": "x"}, CodeInvalidArgument)
	e.mustOK(t, cart, ToolAddCartItem, map[string]any{"product_id": "p_seed_lamp"})
	res := e.mustOK(t, co, ToolCheckout, nil).(shop.CheckoutResult)
	if res.Replayed || len(res.Orders) != 1 || res.Orders[0].Status != domain.OrderPendingPayment || res.Orders[0].PayAmount.String() != "229.00" {
		t.Fatalf("checkout: %+v", res)
	}
	// 同一次运行重复调用：幂等重放，不会再下单
	again := e.mustOK(t, co, ToolCheckout, nil).(shop.CheckoutResult)
	if !again.Replayed || again.Orders[0].OrderID != res.Orders[0].OrderID {
		t.Fatalf("replay: %+v", again)
	}
	// 新的运行：购物车已清空
	co2 := e.tc(seed.User2ID, IntentCheckout)
	co2.RunID = "run_second"
	e.mustFail(t, co2, ToolCheckout, nil, "empty_cart")
	orderID := res.Orders[0].OrderID

	// 订单查询：正常 / 非法状态 / 越权 / 不存在
	list := toJSONMap(e.mustOK(t, od, ToolListOrders, nil))
	if list["total"] != float64(1) {
		t.Fatalf("list: %v", list)
	}
	e.mustFail(t, od, ToolListOrders, map[string]any{"status": "bogus"}, CodeInvalidArgument)
	e.mustFail(t, od, ToolListOrders, map[string]any{"limit": float64(0)}, CodeInvalidArgument)
	if got := e.mustOK(t, od, ToolGetOrder, map[string]any{"order_id": orderID}).(shop.Order); got.Payment == nil || got.Payment.Status != domain.PaymentPending {
		t.Fatalf("get: %+v", got)
	}
	e.mustFail(t, od, ToolGetOrder, nil, CodeInvalidArgument)
	e.mustFail(t, od, ToolGetOrder, map[string]any{"order_id": "o_missing"}, "order_not_found")
	e.mustFail(t, odOther, ToolGetOrder, map[string]any{"order_id": orderID}, "order_not_found")

	// 支付：越权 / 非法方式 / 正常 / 重复（状态不允许）
	e.mustFail(t, odOther, ToolPayOrder, map[string]any{"order_id": orderID}, "order_not_found")
	e.mustFail(t, od, ToolPayOrder, map[string]any{"order_id": orderID, "method": "cash"}, CodeInvalidArgument)
	e.mustFail(t, od, ToolPayOrder, nil, CodeInvalidArgument)
	paid := e.mustOK(t, od, ToolPayOrder, map[string]any{"order_id": orderID}).(shop.Order)
	if paid.Status != domain.OrderPaid || paid.Payment.Method != "mock_balance" {
		t.Fatalf("pay: %+v", paid)
	}
	e.mustFail(t, od, ToolPayOrder, map[string]any{"order_id": orderID}, "order_status_conflict")
	e.mustFail(t, od, ToolCancelOrder, map[string]any{"order_id": orderID}, "order_status_conflict")
	e.mustFail(t, od, ToolConfirmReceipt, map[string]any{"order_id": orderID}, "order_status_conflict")
	// 演示用户的待支付订单早已过期：支付时顺带关闭
	e.mustFail(t, odOther, ToolPayOrder, map[string]any{"order_id": "o_seed_pending"}, "order_expired")

	// 取消：另一个订单
	e.mustOK(t, cart, ToolAddCartItem, map[string]any{"product_id": "p_seed_mouse"})
	res = e.mustOK(t, co2, ToolCheckout, nil).(shop.CheckoutResult)
	second := res.Orders[0].OrderID
	e.mustFail(t, od, ToolCancelOrder, map[string]any{"order_id": second, "reason": strings.Repeat("长", 201)}, CodeInvalidArgument)
	e.mustFail(t, odOther, ToolCancelOrder, map[string]any{"order_id": second}, "order_not_found")
	if got := e.mustOK(t, od, ToolCancelOrder, map[string]any{"order_id": second, "reason": "不想要了"}).(shop.Order); got.Status != domain.OrderCancelled || got.CancelReason != "不想要了" {
		t.Fatalf("cancel: %+v", got)
	}
	e.mustFail(t, od, ToolCancelOrder, map[string]any{"order_id": second}, "order_status_conflict")
	e.mustFail(t, od, ToolCancelOrder, map[string]any{"order_id": "o_missing"}, "order_not_found")

	// 确认收货：演示用户的已发货订单
	e.mustFail(t, od, ToolConfirmReceipt, map[string]any{"order_id": "o_seed_shipped"}, "order_not_found")
	e.mustFail(t, odOther, ToolConfirmReceipt, map[string]any{"order_id": "o_seed_paid"}, "order_status_conflict")
	e.mustFail(t, odOther, ToolConfirmReceipt, nil, CodeInvalidArgument)
	if got := e.mustOK(t, odOther, ToolConfirmReceipt, map[string]any{"order_id": "o_seed_shipped"}).(shop.Order); got.Status != domain.OrderCompleted {
		t.Fatalf("confirm: %+v", got)
	}
	e.mustFail(t, odOther, ToolConfirmReceipt, map[string]any{"order_id": "o_seed_shipped"}, "order_status_conflict")
}

func TestCouponAndPromotionTools(t *testing.T) {
	e := newEnv(t)
	tc := e.tc(seed.UserID, IntentCoupon)
	list := toJSONMap(e.mustOK(t, tc, ToolListCoupons, nil))
	coupons := list["coupons"].([]any)
	if len(coupons) != 2 {
		t.Fatalf("coupons: %v", list)
	}
	canClaim := map[string]bool{}
	for _, c := range coupons {
		m := c.(map[string]any)
		canClaim[m["coupon_id"].(string)] = m["can_claim"].(bool)
	}
	if canClaim["coupon_seed_platform"] || !canClaim["coupon_seed_digital"] {
		t.Fatalf("can_claim: %v", canClaim)
	}
	e.mustFail(t, tc, ToolListCoupons, map[string]any{"limit": float64(0)}, CodeInvalidArgument)
	// 领券：缺参 / 不存在 / 正常 / 重复（上限）
	e.mustFail(t, tc, ToolClaimCoupon, nil, CodeInvalidArgument)
	e.mustFail(t, tc, ToolClaimCoupon, map[string]any{"coupon_id": "coupon_missing"}, "coupon_not_found")
	if got := e.mustOK(t, tc, ToolClaimCoupon, map[string]any{"coupon_id": "coupon_seed_digital"}).(shop.UserCoupon); got.Status != domain.UserCouponUnused {
		t.Fatalf("claim: %+v", got)
	}
	e.mustFail(t, tc, ToolClaimCoupon, map[string]any{"coupon_id": "coupon_seed_digital"}, "coupon_limit_reached")
	e.mustFail(t, tc, ToolClaimCoupon, map[string]any{"coupon_id": "coupon_seed_platform"}, "coupon_limit_reached")
	mine := toJSONMap(e.mustOK(t, tc, ToolListUserCoupons, map[string]any{"status": "unused"}))
	if mine["total"] != float64(1) {
		t.Fatalf("mine: %v", mine)
	}
	e.mustFail(t, tc, ToolListUserCoupons, map[string]any{"status": "bogus"}, CodeInvalidArgument)
	// 促销：全部 / 按商品 / 不存在
	promos := toJSONMap(e.mustOK(t, tc, ToolListPromotions, nil))
	if promos["total"] != float64(3) {
		t.Fatalf("promotions: %v", promos)
	}
	promos = toJSONMap(e.mustOK(t, tc, ToolListPromotions, map[string]any{"product_id": "p_seed_lamp"}))
	if promos["total"] != float64(1) {
		t.Fatalf("lamp promotions: %v", promos)
	}
	e.mustFail(t, tc, ToolListPromotions, map[string]any{"product_id": "p_missing"}, "product_not_found")
	e.mustFail(t, tc, ToolListPromotions, map[string]any{"limit": float64(99)}, CodeInvalidArgument)
}

func TestReviewTools(t *testing.T) {
	e := newEnv(t)
	me := e.tc(seed.UserID, IntentReview)
	other := e.tc(seed.User2ID, IntentReview)
	res := e.mustOK(t, me, ToolListReviews, map[string]any{"product_id": "p_seed_mouse"}).(ReviewsResult)
	if res.Total != 1 || res.Average != 5 || res.Reviews[0].ReviewerName != "演**" || res.Reviews[0].MerchantReply == "" {
		t.Fatalf("reviews: %+v", res)
	}
	e.mustFail(t, me, ToolListReviews, nil, CodeInvalidArgument)
	e.mustFail(t, me, ToolListReviews, map[string]any{"product_id": "p_missing"}, "product_not_found")
	e.mustFail(t, me, ToolListReviews, map[string]any{"product_id": "p_seed_speaker"}, "product_not_found")
	// 发表评价：缺参 / 越权 / 状态不允许 / 不存在 / 正常 / 重复
	base := map[string]any{"order_id": "o_seed_completed", "order_item_id": "o_seed_completed_item2", "rating": float64(4), "content": "还能用"}
	e.mustFail(t, me, ToolCreateReview, map[string]any{"order_id": "o_seed_completed"}, CodeInvalidArgument)
	e.mustFail(t, me, ToolCreateReview, with(base, "rating", float64(6)), CodeInvalidArgument)
	e.mustFail(t, me, ToolCreateReview, with(base, "content", ""), CodeInvalidArgument)
	e.mustFail(t, me, ToolCreateReview, with(base, "tags", []any{"a", "b", "c", "d", "e", "f"}), CodeInvalidArgument)
	e.mustFail(t, other, ToolCreateReview, base, "order_not_found")
	e.mustFail(t, me, ToolCreateReview, with(with(base, "order_id", "o_seed_shipped"), "order_item_id", "o_seed_shipped_item1"), "order_not_completed")
	e.mustFail(t, me, ToolCreateReview, with(base, "order_item_id", "o_seed_completed_item9"), "order_item_not_found")
	e.mustFail(t, me, ToolCreateReview, with(base, "order_id", "o_missing"), "order_not_found")
	e.mustFail(t, me, ToolCreateReview, with(base, "order_item_id", "o_seed_completed_item1"), "review_exists")
	if got := e.mustOK(t, me, ToolCreateReview, base).(shop.Review); got.Rating != 4 || got.ProductID != "p_seed_legacy" {
		t.Fatalf("create: %+v", got)
	}
	e.mustFail(t, me, ToolCreateReview, base, "review_exists")
}

// TestPrivateArgsRedacted（REV-018）：评价正文、标签和取消原因不进审计日志；轨迹用的 Sanitize 同样只留长度。
func TestPrivateArgsRedacted(t *testing.T) {
	e := newEnv(t)
	me := e.tc(seed.UserID, IntentReview)
	content, tag := "联系我 138-0000-0000，住朝阳区某小区 3 号楼", "私密标签X"
	args := map[string]any{"order_id": "o_seed_completed", "order_item_id": "o_seed_completed_item2", "rating": float64(5), "content": content, "tags": []any{tag}}
	got := e.mustOK(t, me, ToolCreateReview, args).(shop.Review)
	if got.Content != content || len(got.Tags) != 1 || got.Tags[0] != tag {
		t.Fatalf("review itself must keep the text: %+v", got)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"tool":"create_review"`) || strings.Contains(logs, content) || strings.Contains(logs, tag) || strings.Contains(logs, "138-0000") {
		t.Fatalf("audit leaked review text: %s", logs)
	}
	if !strings.Contains(logs, fmt.Sprintf(`"content":{"redacted":true,"runes":%d}`, utf8.RuneCountInString(content))) || !strings.Contains(logs, `"tags":{"items":1,"redacted":true}`) || !strings.Contains(logs, `"order_item_id":"o_seed_completed_item2"`) || !strings.Contains(logs, `"rating":5`) {
		t.Fatalf("audit should keep ids, rating and lengths: %s", logs)
	}
	san := toJSONMap(e.reg.Sanitize(ToolCreateReview, args))
	if c, _ := san["content"].(map[string]any); c["redacted"] != true || c["runes"] != float64(utf8.RuneCountInString(content)) || san["rating"] != float64(5) {
		t.Fatalf("sanitize: %v", san)
	}
	reason := "搬家了不要了，电话 139-0000-0000"
	san = toJSONMap(e.reg.Sanitize(ToolCancelOrder, map[string]any{"order_id": "o_1", "reason": reason}))
	if r, _ := san["reason"].(map[string]any); r["redacted"] != true || strings.Contains(fmt.Sprint(san), "139") {
		t.Fatalf("cancel reason: %v", san)
	}
	// 未标记的参数仍按通用规则处理；未知工具只做通用处理
	san = e.reg.Sanitize(ToolSearchProducts, map[string]any{"query": "耳机", "exclude": []any{"a"}})
	if san["query"] != "耳机" || san["exclude"] != "[1 items]" {
		t.Fatalf("generic: %v", san)
	}
	if san = e.reg.Sanitize("nope", map[string]any{"x": strings.Repeat("长", 100)}); utf8.RuneCountInString(san["x"].(string)) != 81 {
		t.Fatalf("unknown tool: %v", san)
	}
	for _, tool := range e.reg.Tools() {
		for _, p := range tool.Private {
			if _, ok := tool.Schema.Properties[p]; !ok {
				t.Fatalf("%s: private field %s not in schema", tool.Name, p)
			}
		}
	}
}

func with(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}

// TestToolInternalErrorNotLeaked：存储层错误只记日志，观察里是统一的说明。
func TestToolInternalErrorNotLeaked(t *testing.T) {
	e := newEnv(t)
	deps := e.runner.deps
	deps.Store = failingStore{Store: e.mem}
	deps.Shop = shop.New(deps.Store, deps.Now, 0)
	logs := &strings.Builder{}
	deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	reg := NewRegistry(deps)
	obs := reg.Call(context.Background(), e.tc(seed.UserID, IntentCart), ToolGetCart, nil)
	if obs.OK || obs.Code != CodeInternal || strings.Contains(obs.Message, "secret") {
		t.Fatalf("internal: %+v", obs)
	}
	if !strings.Contains(logs.String(), "secret dsn failure") {
		t.Fatalf("error not logged: %s", logs.String())
	}
}
