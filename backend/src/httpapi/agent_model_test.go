package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// newModelServer 创建带 mock 模型的服务；模型设置跟随动态配置（管理员接口可改）。
func newModelServer(t *testing.T, mock *llm.Mock) (*testServer, string) {
	t.Helper()
	logs := &lockedBuffer{}
	dynamic := configcenter.NewMemorySource(nil)
	resolver := configcenter.NewResolver(func(string) string { return "" }, dynamic)
	mem := memstore.New()
	if _, err := mem.ApplySeed(context.Background(), storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{
		Logger: logging.New(logs, slog.LevelDebug), Store: mem, PasswordCost: bcrypt.MinCost, AvatarDir: t.TempDir(),
		Now: func() time.Time { return testNow }, Settings: configcenter.NewHTTPSettingsProvider(resolver, false),
		Configs: configcenter.NewAdmin(resolver, dynamic), LLM: mock,
		AgentSettings: func(ctx context.Context) agent.ModelSettings {
			return ModelSettingsFrom(configcenter.AgentSettingsNow(ctx, resolver))
		},
	})
	ts := &testServer{Server: s, handler: s.Handler(), logs: logs, dynamic: dynamic, mem: mem}
	srv := httptest.NewServer(ts.handler)
	t.Cleanup(srv.Close)
	return ts, srv.URL
}

// TestAgentModelThroughHTTP：模型规划和回答经 SSE 输出；轨迹事件带模型名；管理员关掉模型后下一次运行只走规则，契约不变。
func TestAgentModelThroughHTTP(t *testing.T) {
	mock := &llm.Mock{}
	mock.Then(llm.Text(`{"intent":"product_search","query":"降噪耳机"}`),
		llm.Text(`{"type":"tool","name":"search_products","args":{"query":"降噪耳机"}}`),
		llm.Text(`{"type":"final","text":"推荐 Blink Air 降噪耳机，通勤首选。","followups":["把第一个加入购物车"]}`))
	ts, base := newModelServer(t, mock)
	// 这里按调用顺序回复，规划和第一步顺序执行；并行的情况见 TestAgentParallelFirstStepThroughHTTP
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/ai.parallel_first_step", ts.login(t, seed.AdminUsername, seed.DevPassword).Token,
		map[string]any{"value": "false", "reason": "测试"}), http.StatusOK, "")
	token := ts.login(t, seed.User2Username, seed.DevPassword).Token
	sid := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, token, map[string]any{})).SessionID
	s, _ := openStream(t, base, token, sid, msgBody("m-1", "推荐一款通勤降噪耳机"))
	start := s.next()
	events, text, blocks := collect(t, s)
	if text != "推荐 Blink Air 降噪耳机，通勤首选。" || blockOf(blocks, "product_list") == nil || len(events) == 0 {
		t.Fatalf("model answer: %q %v", text, blocks)
	}
	trace := decodeBody[runTraceResponse](t, ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+start.Data["run_id"].(string)+"/trace", token, nil))
	var stages []string
	models := map[string]string{}
	for _, it := range trace.Items {
		stages = append(stages, it.Stage+"."+it.EventType+"."+it.Status)
		if it.Model != "" {
			models[it.Stage+"."+it.EventType] = it.Model
		}
	}
	if got := strings.Join(stages, ","); got != "run.start.ok,risk.check.ok,planner.model.ok,memory.retrieval.skipped,planner.rule.ok,react.step.1.ok,tool.search_products.ok,retrieval.products.ok,rerank.products.ok,react.step.2.ok,followup.model.ok,answer.model.ok,answer.rule.ok,memory.summary.ok,run.end.completed" {
		t.Fatalf("trace: %s", got)
	}
	if models["planner.model"] != "deepseek-chat" || models["react.step.1"] != "deepseek-chat" || models["answer.model"] != "deepseek-chat" {
		t.Fatalf("model field: %v", models)
	}
	if mock.Count() != 3 {
		t.Fatalf("model calls: %d", mock.Count())
	}
	// 管理员关掉模型：下一次运行不再调用模型，回答与 7.2 的规则一致
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	for _, key := range []string{"ai.planner_enabled", "ai.agent_enabled"} {
		expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/"+key, admin, map[string]any{"value": "false", "reason": "测试"}), http.StatusOK, "")
	}
	s, _ = openStream(t, base, token, sid, msgBody("m-2", "推荐一款静音无线鼠标"))
	start = s.next()
	_, text, blocks = collect(t, s)
	if mock.Count() != 3 || !strings.Contains(text, "Blink 静音无线鼠标 M2") || blockOf(blocks, "product_list") == nil {
		t.Fatalf("rules after disable: calls=%d text=%q", mock.Count(), text)
	}
	trace = decodeBody[runTraceResponse](t, ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+start.Data["run_id"].(string)+"/trace", token, nil))
	for _, it := range trace.Items {
		if it.Model != "" || it.Stage == "react" {
			t.Fatalf("model trace while disabled: %+v", it)
		}
	}
	// 模型挂了（脚本用完 → 错误）：规划回退规则、回答回退规则，接口仍 200 正常结束
	for _, key := range []string{"ai.planner_enabled", "ai.agent_enabled"} {
		expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/"+key, admin, map[string]any{"value": "true", "reason": "测试"}), http.StatusOK, "")
	}
	s, _ = openStream(t, base, token, sid, msgBody("m-3", "推荐一款静音无线鼠标"))
	s.next()
	events, text, _ = collect(t, s)
	if !strings.Contains(text, "Blink 静音无线鼠标 M2") || events[len(events)-1].Name != EventMessageDone {
		t.Fatalf("fallback through http: %q", text)
	}
	// 工具白名单可在运行中改：推荐意图不再允许搜索商品，模型的调用被拒
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/agent.tool_policy", admin, map[string]any{"value": `{"product_search":["search_knowledge"]}`, "reason": "测试"}), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/agent.tool_policy", admin, map[string]any{"value": `{bad`, "reason": "测试"}), http.StatusBadRequest, "invalid_argument")
	mock.Script = nil
	mock.Then(llm.Text(`{"intent":"product_search","query":"鼠标"}`), llm.Text(`{"type":"tool","name":"search_products","args":{"query":"鼠标"}}`), llm.Text(`{"type":"final","text":"现在不能搜商品。"}`))
	s, _ = openStream(t, base, token, sid, msgBody("m-4", "推荐一款静音无线鼠标"))
	s.next()
	_, text, _ = collect(t, s)
	obs := mock.Requests[len(mock.Requests)-1].Messages
	if text != "现在不能搜商品。" || !strings.Contains(obs[len(obs)-1].Content, agent.CodeToolNotAllowed) {
		t.Fatalf("policy via admin: %q %s", text, obs[len(obs)-1].Content)
	}
}

// 规划和第一步并行（默认打开）：规划的意图与规则一致时采用第一步，工具在规划回来后执行，回答和顺序执行时一样；
// 规划给出不同意图时第一步被丢掉，按规划的意图重走（只用新白名单里的工具）。模型调用按请求内容回复（并行时顺序不固定）。
func TestAgentParallelFirstStepThroughHTTP(t *testing.T) {
	var planIntent atomic.Value
	planIntent.Store(`{"intent":"product_search","query":"降噪耳机"}`)
	mock := &llm.Mock{Fallback: func(req llm.Request) (llm.Response, error) {
		all := ""
		for _, m := range req.Messages {
			all += m.Content
		}
		switch {
		case !strings.Contains(all, "search_products") && !strings.Contains(all, "list_orders"): // 规划（提示里没有工具清单）
			time.Sleep(30 * time.Millisecond) // 规划比第一步慢，第一步的结果要等规划回来才能用
			return llm.Response{Content: planIntent.Load().(string), Model: req.Model}, nil
		case len(req.Messages) == 2 && strings.Contains(all, "list_orders"): // 订单白名单
			return llm.Response{Content: `{"type":"tool","name":"list_orders","args":{}}`, Model: req.Model}, nil
		case len(req.Messages) == 2:
			return llm.Response{Content: `{"type":"tool","name":"search_products","args":{"query":"降噪耳机"}}`, Model: req.Model}, nil
		}
		return llm.Response{Content: `{"type":"final","text":"推荐 Blink Air 降噪耳机，通勤首选。"}`, Model: req.Model}, nil
	}}
	ts, base := newModelServer(t, mock)
	token := ts.login(t, seed.User2Username, seed.DevPassword).Token
	sid := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, token, map[string]any{})).SessionID
	traceOf := func(runID string) string {
		var stages []string
		for _, it := range decodeBody[runTraceResponse](t, ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", token, nil)).Items {
			stages = append(stages, it.Stage+"."+it.EventType+"."+it.Status)
		}
		return strings.Join(stages, ",")
	}
	s, _ := openStream(t, base, token, sid, msgBody("p-1", "推荐一款通勤降噪耳机"))
	start := s.next()
	_, text, blocks := collect(t, s)
	if text != "推荐 Blink Air 降噪耳机，通勤首选。" || blockOf(blocks, "product_list") == nil || mock.Count() != 3 {
		t.Fatalf("adopted: %q calls=%d", text, mock.Count())
	}
	if got := traceOf(start.Data["run_id"].(string)); !strings.Contains(got, "planner.model.ok,react.parallel.adopted,memory.retrieval.skipped,planner.rule.ok,react.step.1.ok,tool.search_products.ok") {
		t.Fatalf("adopted trace: %s", got)
	}
	// 规划说是查订单：第一步（搜商品）被丢掉，按订单白名单重走，搜商品的调用不会执行
	planIntent.Store(`{"intent":"order","action":"list"}`)
	s, _ = openStream(t, base, token, sid, msgBody("p-2", "推荐一款通勤降噪耳机"))
	start = s.next()
	collect(t, s)
	got := traceOf(start.Data["run_id"].(string))
	if !strings.Contains(got, "react.parallel.discarded") || strings.Contains(got, "tool.search_products") || !strings.Contains(got, "tool.list_orders.ok") {
		t.Fatalf("discarded trace: %s", got)
	}
}
