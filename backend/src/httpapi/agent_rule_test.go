package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
)

// collect 读完一个流，返回按类型分组的事件和拼接的正文。
func collect(t *testing.T, s *sseStream) (events []sseEvent, text string, blocks []map[string]any) {
	t.Helper()
	for _, ev := range s.rest() {
		events = append(events, ev)
		switch ev.Name {
		case EventTextDelta:
			text += ev.Data["delta"].(string)
		case EventBlock:
			blocks = append(blocks, ev.Data["block"].(map[string]any))
		}
	}
	if len(events) == 0 || events[len(events)-1].Name != EventMessageDone {
		t.Fatalf("stream did not end with message_done: %v", names(events))
	}
	return events, text, blocks
}

func blockOf(blocks []map[string]any, kind string) map[string]any {
	for _, b := range blocks {
		if b["type"] == kind {
			return b
		}
	}
	return nil
}

// shoppingLoop 是无模型的最小闭环：商品查询 → “把第一个加购物车” → 结算 → 订单查询 → 支付，全部经 SSE 接口完成。
func shoppingLoop(t *testing.T, ts *testServer, base string) {
	t.Helper()
	token := ts.login(t, seed.User2Username, seed.DevPassword).Token
	rec := ts.call(t, http.MethodPost, agentSessionsPath, token, map[string]any{})
	expectStatus(t, rec, http.StatusCreated, "")
	sid := decodeBody[sessionView](t, rec).SessionID
	ask := func(id, content string) (string, []map[string]any) {
		s, resp := openStream(t, base, token, sid, msgBody(id, content))
		if s == nil {
			t.Fatalf("%s: not a stream (%d)", content, resp.StatusCode)
		}
		start := s.next()
		if start.Name != EventMessageStart {
			t.Fatalf("%s: first event %s", content, start.Name)
		}
		_, text, blocks := collect(t, s)
		return text, blocks
	}

	text, blocks := ask("m-1", "推荐一款静音无线鼠标")
	pl := blockOf(blocks, "product_list")
	if pl == nil || !strings.Contains(text, "Blink 静音无线鼠标 M2") {
		t.Fatalf("search: %q %v", text, blocks)
	}
	if first := pl["products"].([]any)[0].(map[string]any); first["product_id"] != "p_seed_mouse" || first["price"] != "129.00" {
		t.Fatalf("first card: %v", first)
	}

	text, blocks = ask("m-2", "把第一个加入购物车")
	if blockOf(blocks, "cart") == nil || !strings.Contains(text, "已把 Blink 静音无线鼠标 M2 × 1 加入购物车") {
		t.Fatalf("add: %q %v", text, blocks)
	}
	cart := decodeBody[shop.Cart](t, ts.call(t, http.MethodGet, "/api/v1/cart", token, nil))
	if len(cart.Items) != 1 || cart.Items[0].ProductID != "p_seed_mouse" {
		t.Fatalf("cart via REST: %+v", cart.Items)
	}

	text, blocks = ask("m-3", "结算")
	ol := blockOf(blocks, "order_list")
	if ol == nil || !strings.Contains(text, "已为你生成 1 个订单") {
		t.Fatalf("checkout: %q %v", text, blocks)
	}
	order := ol["orders"].([]any)[0].(map[string]any)
	if order["status"] != "pending_payment" || order["pay_amount"] != "129.00" {
		t.Fatalf("order: %v", order)
	}
	if cart := decodeBody[shop.Cart](t, ts.call(t, http.MethodGet, "/api/v1/cart", token, nil)); len(cart.Items) != 0 {
		t.Fatalf("cart should be empty after checkout: %+v", cart.Items)
	}

	text, _ = ask("m-4", "我的订单")
	if !strings.Contains(text, "你有 1 个订单") || !strings.Contains(text, order["order_no"].(string)) {
		t.Fatalf("list: %q", text)
	}

	text, _ = ask("m-5", "支付订单")
	if !strings.Contains(text, "已完成（模拟）支付") {
		t.Fatalf("pay: %q", text)
	}
	got := decodeBody[shop.Order](t, ts.call(t, http.MethodGet, "/api/v1/orders/"+order["order_id"].(string), token, nil))
	if got.Status != "paid" || got.Payment == nil || got.Payment.Status != "paid" {
		t.Fatalf("order via REST: %+v", got)
	}
	// 写操作留下审计：工具名、账号和运行 ID
	for _, tool := range []string{"add_cart_item", "checkout", "pay_order"} {
		if !strings.Contains(ts.logs.String(), `"action":"agent.tool","tool":"`+tool+`","account_id":"`+seed.User2ID+`"`) {
			t.Fatalf("missing audit for %s", tool)
		}
	}
}

// TestAgentShoppingLoopHTTP：默认运行器（无模型）经真实 HTTP + SSE 走通闭环（内存 Store）。
func TestAgentShoppingLoopHTTP(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	srv := newHTTPServer(t, ts)
	shoppingLoop(t, ts, srv)
}

// TestAgentShoppingLoopMySQL：同一闭环在真实 MySQL 上，会话历史里的商品卡（“第一个”）也经数据库读回。
func TestAgentShoppingLoopMySQL(t *testing.T) {
	ts, _ := newMySQLTestServer(t, time.Now)
	srv := newHTTPServer(t, ts)
	shoppingLoop(t, ts, srv)
}

// TestAgentGuardsHTTP：没有上文时“把第一个加购物车”不写购物车；风险词在规划前拦截；导航不调用工具。
func TestAgentGuardsHTTP(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	base := newHTTPServer(t, ts)
	token := ts.login(t, seed.User2Username, seed.DevPassword).Token
	sid := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, token, map[string]any{})).SessionID
	stream := func(id, content string) (string, []map[string]any, string) {
		s, _ := openStream(t, base, token, sid, msgBody(id, content))
		start := s.next()
		_, text, blocks := collect(t, s)
		return text, blocks, start.Data["run_id"].(string)
	}
	text, blocks, runID := stream("g-1", "把第一个加入购物车")
	if len(blocks) != 0 || !strings.Contains(text, "我还没有给你推荐过商品") {
		t.Fatalf("no context add: %q %v", text, blocks)
	}
	if cart := decodeBody[shop.Cart](t, ts.call(t, http.MethodGet, "/api/v1/cart", token, nil)); len(cart.Items) != 0 {
		t.Fatalf("cart written without a target: %+v", cart.Items)
	}
	trace := decodeBody[runTraceResponse](t, ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", token, nil))
	for _, it := range trace.Items {
		if it.Stage == "tool" {
			t.Fatalf("tool called without a target: %+v", it)
		}
	}

	text, blocks, runID = stream("g-2", "有没有假货")
	if len(blocks) != 0 || !strings.Contains(text, "不能继续处理") {
		t.Fatalf("risk: %q %v", text, blocks)
	}
	trace = decodeBody[runTraceResponse](t, ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", token, nil))
	var stages []string
	for _, it := range trace.Items {
		stages = append(stages, it.Stage+"."+it.EventType+"."+it.Status)
	}
	if got := strings.Join(stages, ","); got != "run.start.ok,risk.check.blocked,followup.rule.ok,answer.rule.ok,run.end.completed" {
		t.Fatalf("risk trace: %s", got)
	}
	if !strings.Contains(ts.logs.String(), `"action":"agent.risk_blocked"`) {
		t.Fatal("risk hit not audited")
	}
	// 管理员改了风险词后立即生效：加入“鼠标”，推荐鼠标被拦截
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/risk.blocked_words", admin, map[string]any{"value": "假货,鼠标", "reason": "测试"}), http.StatusOK, "")
	text, _, _ = stream("g-3", "推荐一款静音无线鼠标")
	if !strings.Contains(text, "不能继续处理") {
		t.Fatalf("dynamic words: %q", text)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/risk.blocked_words", admin, map[string]any{"value": "", "reason": "恢复"}), http.StatusOK, "")

	text, blocks, runID = stream("g-4", "打开购物车页面")
	if a := blockOf(blocks, "action"); a == nil || a["target"] != "cart" {
		t.Fatalf("navigation: %q %v", text, blocks)
	}
	trace = decodeBody[runTraceResponse](t, ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", token, nil))
	for _, it := range trace.Items {
		if it.Stage == "tool" {
			t.Fatalf("navigation called a tool: %+v", it)
		}
	}
}

func newHTTPServer(t *testing.T, ts *testServer) string {
	t.Helper()
	srv := httptest.NewServer(ts.handler)
	t.Cleanup(srv.Close)
	return srv.URL
}
