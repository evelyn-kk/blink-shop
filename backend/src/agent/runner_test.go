package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
)

func stages(r *recorder) string {
	var out []string
	for _, t := range r.traces {
		out = append(out, t.Stage+"."+t.Type+"."+t.Status)
	}
	return strings.Join(out, ",")
}

// TestRunShoppingLoopWithoutModel：无模型、无 AI key 的最小闭环——商品查询 → “把第一个加购物车” → 结算 → 订单查询 → 支付。
func TestRunShoppingLoopWithoutModel(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)

	r := e.run(t, seed.User2ID, sid, "推荐一款通勤降噪耳机")
	if got := stages(r); got != "risk.check.ok,memory.retrieval.skipped,planner.rule.ok,tool.search_products.ok,retrieval.products.ok,rerank.products.ok,followup.rule.ok,answer.rule.ok,memory.summary.ok" {
		t.Fatalf("trace: %s", got)
	}
	pl := r.block(BlockProductList)
	if pl == nil || productIDs(pl)[0] != "p_seed_earbuds" || !strings.Contains(r.text.String(), "Blink Air 降噪耳机") || len(r.followups) != 3 {
		t.Fatalf("search answer: blocks=%v text=%q followups=%v", r.blockTypes(), r.text.String(), r.followups)
	}
	if r.steps[0].ID != "understand" || r.steps[len(r.steps)-1].Status != StepDone {
		t.Fatalf("steps: %+v", r.steps)
	}
	if meta := r.traceOf("planner").Meta; meta["intent"] != "product_search" || meta["query"] != "通勤降噪耳机" {
		t.Fatalf("planner meta: %v", meta)
	}

	r = e.run(t, seed.User2ID, sid, "把第一个加入购物车")
	if calls := r.toolCalls(); strings.Join(calls, ",") != "search_products,add_cart_item" {
		t.Fatalf("add calls: %v", calls)
	}
	cart := e.cart(t, seed.User2ID)
	if len(cart.Items) != 1 || cart.Items[0].ProductID != "p_seed_earbuds" || cart.Items[0].Quantity != 1 {
		t.Fatalf("cart: %+v", cart.Items)
	}
	if r.block(BlockCart) == nil || r.block(BlockAction)["target"] != TargetCart || !strings.Contains(r.text.String(), "已把 Blink Air 降噪耳机 × 1 加入购物车") {
		t.Fatalf("add answer: %v %q", r.blockTypes(), r.text.String())
	}

	r = e.run(t, seed.User2ID, sid, "结算")
	if calls := r.toolCalls(); strings.Join(calls, ",") != "get_cart,checkout" {
		t.Fatalf("checkout calls: %v", calls)
	}
	orders := e.orders(t, seed.User2ID, domain.OrderPendingPayment)
	if len(orders) != 1 || orders[0].PayAmount.String() != "569.05" || len(e.cart(t, seed.User2ID).Items) != 0 {
		t.Fatalf("orders after checkout: %+v", orders)
	}
	if r.block(BlockOrderList) == nil || !strings.Contains(r.text.String(), "已为你生成 1 个订单") || !strings.Contains(r.text.String(), orders[0].OrderNo) {
		t.Fatalf("checkout answer: %v %q", r.blockTypes(), r.text.String())
	}

	r = e.run(t, seed.User2ID, sid, "我的订单")
	if r.toolCalls()[0] != "list_orders" || !strings.Contains(r.text.String(), "你有 1 个订单") || r.block(BlockOrderList) == nil {
		t.Fatalf("list answer: %v %q", r.toolCalls(), r.text.String())
	}

	r = e.run(t, seed.User2ID, sid, "支付订单")
	if calls := r.toolCalls(); strings.Join(calls, ",") != "list_orders,pay_order" {
		t.Fatalf("pay calls: %v", calls)
	}
	if paid := e.orders(t, seed.User2ID, domain.OrderPaid); len(paid) != 1 || !strings.Contains(r.text.String(), "已完成（模拟）支付") {
		t.Fatalf("pay: %+v %q", paid, r.text.String())
	}
	// 没有待支付订单了：不再调用支付工具
	r = e.run(t, seed.User2ID, sid, "支付订单")
	if calls := r.toolCalls(); strings.Join(calls, ",") != "list_orders" || !strings.Contains(r.text.String(), "没有待支付的订单") {
		t.Fatalf("pay nothing: %v %q", calls, r.text.String())
	}
}

// “把第一个加购物车”但这一轮之前没给用户看过任何商品：不猜，不写，提问。
func TestAddFirstWithoutVisibleCards(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "把第一个加入购物车")
	if len(r.toolCalls()) != 0 || !strings.Contains(r.text.String(), "我还没有给你推荐过商品") || len(e.cart(t, seed.User2ID).Items) != 0 {
		t.Fatalf("calls=%v text=%q", r.toolCalls(), r.text.String())
	}
	if meta := r.traceOf("answer").Meta; meta["asked"] != true {
		t.Fatalf("answer meta: %v", meta)
	}
	// 看过商品但说“第三个”，只有两个：同样不写
	e.run(t, seed.User2ID, sid, "推荐一款手机")
	r = e.run(t, seed.User2ID, sid, "把第三个加入购物车")
	if containsStr(r.toolCalls(), ToolAddCartItem) || !strings.Contains(r.text.String(), "没有第 3 个") {
		t.Fatalf("calls=%v text=%q", r.toolCalls(), r.text.String())
	}
	// 指代不清（看过两个，说“那个”）：列出来问
	r = e.run(t, seed.User2ID, sid, "把那个加购")
	if containsStr(r.toolCalls(), ToolAddCartItem) || r.block(BlockProductList) == nil || !strings.Contains(r.text.String(), "你指的是哪一个") {
		t.Fatalf("calls=%v text=%q", r.toolCalls(), r.text.String())
	}
	// “第二个”明确：加的是上一张卡的第二件（手机按价格排序：Nova 2999、Vista 4299）
	r = e.run(t, seed.User2ID, sid, "把第二个加入购物车")
	cart := e.cart(t, seed.User2ID)
	if len(cart.Items) != 1 || cart.Items[0].ProductID != "p_seed_vista" {
		t.Fatalf("cart: %+v text=%q", cart.Items, r.text.String())
	}
}

func TestAddByName(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	// 名称唯一且带数量
	r := e.run(t, seed.User2ID, sid, "把 Blink 静音无线鼠标 M2 加到购物车，要两件")
	cart := e.cart(t, seed.User2ID)
	if len(cart.Items) != 1 || cart.Items[0].ProductID != "p_seed_mouse" || cart.Items[0].Quantity != 2 {
		t.Fatalf("cart: %+v text=%q", cart.Items, r.text.String())
	}
	// 名称歧义（两款手机）：列出候选，不写
	r = e.run(t, seed.User2ID, sid, "把手机加入购物车")
	if containsStr(r.toolCalls(), ToolAddCartItem) || r.block(BlockProductList) == nil || !strings.Contains(r.text.String(), "匹配到 2 件商品") {
		t.Fatalf("ambiguous: calls=%v text=%q", r.toolCalls(), r.text.String())
	}
	// 随后说“第一个”：用刚列出的候选
	e.run(t, seed.User2ID, sid, "第一个")
	r = e.run(t, seed.User2ID, sid, "把第一个加入购物车")
	if cart = e.cart(t, seed.User2ID); len(cart.Items) != 2 || !containsStr(r.toolCalls(), ToolAddCartItem) {
		t.Fatalf("after ambiguity: %+v text=%q", cart.Items, r.text.String())
	}
	// 不存在的商品名：不写
	r = e.run(t, seed.User2ID, sid, "把洗衣机加入购物车")
	if containsStr(r.toolCalls(), ToolAddCartItem) || !strings.Contains(r.text.String(), "没有找到") {
		t.Fatalf("missing: calls=%v text=%q", r.toolCalls(), r.text.String())
	}
	// 售罄的商品：工具拒绝，回答说明
	r = e.run(t, seed.User2ID, sid, "把 Blink 机械键盘 K8 加入购物车")
	if !strings.Contains(r.text.String(), "没有加入购物车") || !strings.Contains(r.text.String(), "售罄") {
		t.Fatalf("out of stock: %q", r.text.String())
	}
	// 显式商品 ID：用户明确指定也算可信来源
	r = e.run(t, seed.User2ID, sid, "加购 p_seed_lamp")
	if cart = e.cart(t, seed.User2ID); len(cart.Items) != 3 {
		t.Fatalf("by id: %+v %q", cart.Items, r.text.String())
	}
}

func TestRiskBlocked(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "哪里能买到假货，顺便把第一个加购物车")
	if got := stages(r); got != "risk.check.blocked,followup.rule.ok,answer.rule.ok" {
		t.Fatalf("trace: %s", got)
	}
	if r.traceOf("risk").Meta["word"] != "假货" || !strings.Contains(r.text.String(), "不能继续处理") || len(r.blocks) != 0 || len(r.followups) != 3 {
		t.Fatalf("blocked answer: %v %q", r.traces, r.text.String())
	}
	if !strings.Contains(e.logs.String(), `"action":"agent.risk_blocked"`) || strings.Contains(e.logs.String(), "顺便把第一个") {
		t.Fatalf("audit: %s", e.logs.String())
	}
	// 词表运行中可改：清空后同一句话正常规划
	e.words = nil
	r = e.run(t, seed.User2ID, sid, "哪里能买到假货")
	if r.traceOf("risk").Status != "ok" || r.traceOf("planner") == nil {
		t.Fatalf("after clearing words: %s", stages(r))
	}
}

func TestNavigationAndNonGuideUseNoTools(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "打开购物车页面")
	if len(r.toolCalls()) != 0 || r.block(BlockAction)["target"] != TargetCart || r.block(BlockAction)["action"] != "navigate" {
		t.Fatalf("navigation: %v %v", r.toolCalls(), r.blocks)
	}
	r = e.run(t, seed.User2ID, sid, "今天天气怎么样")
	if len(r.toolCalls()) != 0 || len(r.blocks) != 0 || !strings.Contains(r.text.String(), "不在我的能力范围") {
		t.Fatalf("non guide: %v %q", r.toolCalls(), r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "你好")
	if len(r.toolCalls()) != 0 || !strings.Contains(r.text.String(), "我是 Blink 导购助手") {
		t.Fatalf("greeting: %q", r.text.String())
	}
	// 没有动词的品类词：兜底先搜一次，有可靠命中按推荐回答
	r = e.run(t, seed.User2ID, sid, "通勤降噪耳机")
	if r.block(BlockProductList) == nil || r.traceOf("answer").Meta["intent"] != "product_search" {
		t.Fatalf("fallback search: %v %v", r.blockTypes(), r.traceOf("answer").Meta)
	}
	// 兜底搜不到：按非导购说明边界，不虚构商品
	r = e.run(t, seed.User2ID, sid, "嗯嗯好的呢")
	if r.block(BlockProductList) != nil || !strings.Contains(r.text.String(), "我暂时没有理解") {
		t.Fatalf("fallback miss: %v %q", r.blockTypes(), r.text.String())
	}
	// 图片附件走图搜；没有配置图片搜索时如实说明（图搜的完整用例见 image_search_test.go）
	r = e.run(t, seed.User2ID, sid, "看看这张图", domain.Attachment{FileID: "f1", MimeType: "image/png"})
	if calls := r.toolCalls(); len(calls) != 1 || calls[0] != ToolSearchImage || !strings.Contains(r.text.String(), "图片搜索暂时不可用") {
		t.Fatalf("image: %v %q", r.toolCalls(), r.text.String())
	}
}

func TestKnowledgeAnswersOnlyFromCitations(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "七天无理由怎么退")
	cit := r.block(BlockCitation)
	if cit == nil || !strings.Contains(r.text.String(), "《Blink 数码售后政策》") || !strings.Contains(r.text.String(), "签收后 7 天内") {
		t.Fatalf("knowledge: %v %q", r.blockTypes(), r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "怎么开发票")
	if r.block(BlockCitation) != nil || !strings.Contains(r.text.String(), "没有找到") || !strings.Contains(r.text.String(), "不能凭空回答") {
		t.Fatalf("no citation: %v %q", r.blockTypes(), r.text.String())
	}
	// 预算 + 排除 + 相近商品
	r = e.run(t, seed.User2ID, sid, "3000 以内拍照好的手机，不要 Vista")
	pl := r.block(BlockProductList)
	if pl == nil || strings.Join(productIDs(pl), ",") != "p_seed_nova" || !strings.Contains(r.text.String(), "¥3000 以内") || !strings.Contains(r.text.String(), "排除“vista”") {
		t.Fatalf("budget/exclude: %v %q", r.blockTypes(), r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "200 以内的手机")
	if r.block(BlockProductList) != nil || !strings.Contains(r.text.String(), "因为价格、品牌或排除条件没有列出") {
		t.Fatalf("over budget: %v %q", r.blockTypes(), r.text.String())
	}
}

func TestCompare(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "对比 Blink Nova 12 和 Blink Vista Pro")
	cmp := r.block(BlockComparison)
	if cmp == nil || len(cmp["products"].([]any)) != 2 || !strings.Contains(r.text.String(), "Blink Nova 12 更便宜") {
		t.Fatalf("compare: %v %q", r.blockTypes(), r.text.String())
	}
	rows := cmp["rows"].([]any)
	if rows[0].(map[string]any)["label"] != "价格" || rows[0].(map[string]any)["values"].([]any)[0] != "¥2999" {
		t.Fatalf("rows: %v", rows[0])
	}
	// 对比表里的商品可以直接加购
	e.run(t, seed.User2ID, sid, "把第二个加入购物车")
	if cart := e.cart(t, seed.User2ID); len(cart.Items) != 1 || cart.Items[0].ProductID != "p_seed_vista" {
		t.Fatalf("add from comparison: %+v", cart.Items)
	}
	// 对比对象不够：提问
	r = e.run(t, seed.User2ID, sid, "对比一下洗衣机和冰箱")
	if r.block(BlockComparison) != nil || !strings.Contains(r.text.String(), "要对比哪几款商品") {
		t.Fatalf("compare ask: %v %q", r.blockTypes(), r.text.String())
	}
}

func TestCartModifyAndCheckoutGuards(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	// 空车结算：不调用 checkout
	r := e.run(t, seed.User2ID, sid, "结算")
	if strings.Join(r.toolCalls(), ",") != "get_cart" || r.block(BlockAction)["target"] != TargetCart || !strings.Contains(r.text.String(), "购物车是空的") {
		t.Fatalf("empty checkout: %v %q", r.toolCalls(), r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "看看我的购物车")
	if !strings.Contains(r.text.String(), "还是空的") || r.block(BlockCart) == nil {
		t.Fatalf("empty cart: %q", r.text.String())
	}
	e.run(t, seed.User2ID, sid, "加购 p_seed_mouse")
	e.run(t, seed.User2ID, sid, "加购 p_seed_earbuds")
	r = e.run(t, seed.User2ID, sid, "购物车里有什么")
	if !strings.Contains(r.text.String(), "购物车里有 2 种商品") || !strings.Contains(r.text.String(), "再买 ¥") || r.block(BlockCart) == nil {
		t.Fatalf("cart view: %q", r.text.String())
	}
	// 改数量：超库存被业务层拒绝
	r = e.run(t, seed.User2ID, sid, "把购物车里的耳机改成 9 件")
	if !strings.Contains(r.text.String(), "没有修改数量") || e.cart(t, seed.User2ID).Items[1].Quantity != 1 {
		t.Fatalf("over stock: %q", r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "把购物车里的耳机改成 2 件")
	if !strings.Contains(r.text.String(), "已把 Blink Air 降噪耳机 改为 2 件") {
		t.Fatalf("update: %q", r.text.String())
	}
	// 取消选中 / 删除 / 歧义
	r = e.run(t, seed.User2ID, sid, "取消选中购物车里的鼠标")
	if it := itemOf(e.cart(t, seed.User2ID), "p_seed_mouse"); it.Selected || !strings.Contains(r.text.String(), "已取消选中") {
		t.Fatalf("unselect: %+v %q", it, r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "删掉购物车里的鼠标")
	if c := e.cart(t, seed.User2ID); len(c.Items) != 1 || c.Items[0].ProductID != "p_seed_earbuds" || !strings.Contains(r.text.String(), "已从购物车删除 Blink 静音无线鼠标 M2") {
		t.Fatalf("remove: %+v %q", c.Items, r.text.String())
	}
	// 只剩一件时“第一个”“那个”都明确
	e.run(t, seed.User2ID, sid, "加购 p_seed_mouse")
	r = e.run(t, seed.User2ID, sid, "删掉购物车里的第一个")
	if c := e.cart(t, seed.User2ID); len(c.Items) != 1 || !strings.Contains(r.text.String(), "已从购物车删除") {
		t.Fatalf("remove first: %+v %q", c.Items, r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "删掉购物车里的洗衣机")
	if len(e.cart(t, seed.User2ID).Items) != 1 || !strings.Contains(r.text.String(), "没有找到") {
		t.Fatalf("remove missing: %q", r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "清空购物车")
	if len(e.cart(t, seed.User2ID).Items) != 0 || !strings.Contains(r.text.String(), "删除了 1 种商品") {
		t.Fatalf("clear: %q", r.text.String())
	}
}

func TestOrderTargetsMustBeUnambiguous(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	e.run(t, seed.User2ID, sid, "加购 p_seed_lamp")
	e.run(t, seed.User2ID, sid, "结算")
	e.run(t, seed.User2ID, sid, "加购 p_seed_mouse")
	e.run(t, seed.User2ID, sid, "结算")
	pending := e.orders(t, seed.User2ID, domain.OrderPendingPayment)
	if len(pending) != 2 {
		t.Fatalf("pending: %d", len(pending))
	}
	// 两个待支付订单，“支付订单”不猜
	r := e.run(t, seed.User2ID, sid, "支付订单")
	if containsStr(r.toolCalls(), ToolPayOrder) || !strings.Contains(r.text.String(), "要支付哪一个") || r.block(BlockOrderList) == nil {
		t.Fatalf("ambiguous pay: %v %q", r.toolCalls(), r.text.String())
	}
	// 按订单号
	r = e.run(t, seed.User2ID, sid, "支付订单 "+pending[1].OrderNo)
	if !containsStr(r.toolCalls(), ToolPayOrder) || e.orders(t, seed.User2ID, domain.OrderPaid)[0].OrderID != pending[1].OrderID {
		t.Fatalf("pay by no: %v %q", r.toolCalls(), r.text.String())
	}
	// 只剩一个：直接取消
	r = e.run(t, seed.User2ID, sid, "取消订单")
	if !containsStr(r.toolCalls(), ToolCancelOrder) || len(e.orders(t, seed.User2ID, domain.OrderCancelled)) != 1 || !strings.Contains(r.text.String(), "已取消") {
		t.Fatalf("cancel: %v %q", r.toolCalls(), r.text.String())
	}
	// 别人的订单号：当前用户查不到
	r = e.run(t, seed.User2ID, sid, "支付订单 BS2026092010001")
	if containsStr(r.toolCalls(), ToolPayOrder) || !strings.Contains(r.text.String(), "没有找到订单号") {
		t.Fatalf("foreign order: %v %q", r.toolCalls(), r.text.String())
	}
	// 演示用户：确认收货只作用于自己的已发货订单
	sid2 := e.newSession(t, seed.UserID)
	r = e.run(t, seed.UserID, sid2, "确认收货")
	if strings.Join(r.toolCalls(), ",") != "list_orders,confirm_receipt" || !strings.Contains(r.text.String(), "已确认收货") {
		t.Fatalf("confirm: %v %q", r.toolCalls(), r.text.String())
	}
	r = e.run(t, seed.UserID, sid2, "待支付的订单")
	if containsStr(r.toolCalls(), ToolPayOrder) || !strings.Contains(r.text.String(), "待支付的订单") {
		t.Fatalf("status list: %v %q", r.toolCalls(), r.text.String())
	}
}

func TestCouponAndReviewFlows(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "有什么优惠券")
	if r.block(BlockCouponList) == nil || !strings.Contains(r.text.String(), "现在有 1 张券可以领") || !strings.Contains(r.text.String(), "你手里还有 1 张未使用的券") {
		t.Fatalf("coupons: %v %q", r.blockTypes(), r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "帮我领券")
	if !strings.Contains(r.text.String(), "已帮你领到 1 张券：Blink 数码满 500 减 50") {
		t.Fatalf("claim: %q", r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "帮我领券")
	if containsStr(r.toolCalls(), ToolClaimCoupon) || !strings.Contains(r.text.String(), "没有可以领的券") {
		t.Fatalf("claim again: %v %q", r.toolCalls(), r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "有什么促销活动")
	if r.block(BlockPromotions) == nil || !strings.Contains(r.text.String(), "进行中的活动有 3 个") {
		t.Fatalf("promotions: %q", r.text.String())
	}

	sid2 := e.newSession(t, seed.UserID)
	r = e.run(t, seed.UserID, sid2, "Blink 静音无线鼠标 M2 的评价怎么样")
	if r.block(BlockReviewList) == nil || !strings.Contains(r.text.String(), "有 1 条评价，平均 5.0 分") {
		t.Fatalf("reviews: %v %q", r.blockTypes(), r.text.String())
	}
	// 发表评价：缺评分先问；目标是已完成订单里唯一没评价的商品
	r = e.run(t, seed.UserID, sid2, "我想评价一下 Nova 9")
	if containsStr(r.toolCalls(), ToolCreateReview) || !strings.Contains(r.text.String(), "打几星") {
		t.Fatalf("ask rating: %v %q", r.toolCalls(), r.text.String())
	}
	e.logs.Reset()
	r = e.run(t, seed.UserID, sid2, "给 Nova 9 打四星，评价：老机器还能用，联系我 138-0000-0000")
	if !containsStr(r.toolCalls(), ToolCreateReview) || !strings.Contains(r.text.String(), "已为 Blink Nova 9（停产） 发布 4 星评价") {
		t.Fatalf("create review: %v %q", r.toolCalls(), r.text.String())
	}
	// REV-018：评价正文（少于 80 字）不进任何轨迹元数据，也不进日志；轨迹仍保留订单项 ID、评分和正文长度
	for _, tr := range r.traces {
		if raw := fmt.Sprint(tr.Meta); strings.Contains(raw, "老机器") || strings.Contains(raw, "138-0000") {
			t.Fatalf("trace %s.%s leaks review text: %v", tr.Stage, tr.Type, tr.Meta)
		}
		if tr.Stage == "tool" && tr.Type == ToolCreateReview {
			args := tr.Meta["args"].(map[string]any)
			if c, _ := args["content"].(map[string]any); c["redacted"] != true || c["runes"] != utf8.RuneCountInString("老机器还能用，联系我 138-0000-0000") || args["rating"] != 4 || args["order_item_id"] != "o_seed_completed_item2" {
				t.Fatalf("create_review trace args: %v", args)
			}
		}
	}
	if logs := e.logs.String(); strings.Contains(logs, "老机器") || strings.Contains(logs, "138-0000") || !strings.Contains(logs, `"tool":"create_review"`) {
		t.Fatalf("logs leak review text: %s", logs)
	}
	r = e.run(t, seed.UserID, sid2, "给 Nova 9 打四星，评价：再评一次")
	if containsStr(r.toolCalls(), ToolCreateReview) || !strings.Contains(r.text.String(), "没有找到还能评价的商品") {
		t.Fatalf("review again: %v %q", r.toolCalls(), r.text.String())
	}
	// 没有订单的用户
	r = e.run(t, seed.User2ID, sid, "给鼠标打五星，评价：好")
	if containsStr(r.toolCalls(), ToolCreateReview) || !strings.Contains(r.text.String(), "还没有已完成的订单") {
		t.Fatalf("no orders: %q", r.text.String())
	}
}

func itemOf(c shop.Cart, productID string) shop.CartItem {
	for _, it := range c.Items {
		if it.ProductID == productID {
			return it
		}
	}
	return shop.CartItem{}
}

func TestRunCancelled(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &recorder{}
	if err := e.runner.Run(ctx, Input{AccountID: seed.User2ID, SessionID: "s", RunID: "r", Content: "推荐耳机"}, rec); err == nil || len(rec.deltas) != 0 {
		t.Fatalf("cancelled run: err=%v deltas=%v", err, rec.deltas)
	}
}

// 文本按句子流式输出，拼起来等于完整回答。
func TestStreamingChunks(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "推荐一款通勤降噪耳机")
	if len(r.deltas) < 3 || strings.Join(r.deltas, "") != r.text.String() {
		t.Fatalf("deltas: %d", len(r.deltas))
	}
}
