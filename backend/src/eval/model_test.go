package eval

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
)

func mockModel(m *llm.Mock) *ModelConfig {
	return &ModelConfig{LLM: m, Settings: agent.ModelSettings{PlannerEnabled: true, AgentEnabled: true, PlannerModel: "small-m", AgentModel: "big-m",
		Timeout: 2 * time.Second, MaxToolRounds: 4}}
}

func writeCases(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cases.jsonl")
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestIntentWithModel：模型规划覆盖规则意图；模型超时回退规则并计入 planner_fallbacks；用量和明细进报告。
func TestIntentWithModel(t *testing.T) {
	m := (&llm.Mock{}).Then(
		llm.Reply{Content: `{"intent":"coupon","action":"list"}`, Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 10}},
		llm.Fail(context.DeadlineExceeded),
	)
	cases := writeCases(t,
		`{"id":"a","query":"有啥便宜的路子","expect_intent":"coupon"}`,
		`{"id":"b","query":"推荐一款降噪耳机","expect_intent":"product_search"}`)
	rep, err := RunIntent(context.Background(), IntentOptions{Cases: cases, Model: mockModel(m)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 2 || rep.Config["planner"] != "model:small-m" {
		t.Fatalf("report: passed=%d config=%v failures=%v", rep.Passed, rep.Config, rep.Failures)
	}
	if rep.Metrics["model_calls"] != 2 || rep.Metrics["model_errors"] != 1 || rep.Metrics["planner_fallbacks"] != 1 || rep.Metrics["prompt_tokens"] != 100 {
		t.Fatalf("metrics: %v", rep.Metrics)
	}
	if d := rep.Cases[1].Detail; d == nil || !slices.Contains(d["fallbacks"].([]string), "planner:timeout") {
		t.Fatalf("detail: %v", rep.Cases[1].Detail)
	}
	// 纯规则：没有模型指标，也没有明细
	rule, err := RunIntent(context.Background(), IntentOptions{Cases: cases})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rule.Metrics["model_calls"]; ok || rule.Cases[0].Detail != nil || rule.Config["planner"] != "rule" {
		t.Fatalf("rule report: %v %v", rule.Metrics, rule.Config)
	}
}

// TestAgentWithModel：模型走一次工具再给最终回答；回答里编造的商品 ID 被过滤并计数，商品卡都来自工具（幻觉挂品率 0）。
func TestAgentWithModel(t *testing.T) {
	m := (&llm.Mock{}).Then(
		llm.Text(`{"intent":"product_search","query":"降噪耳机"}`),
		llm.Text(`{"type":"tool","name":"search_products","args":{"query":"降噪耳机"}}`),
		llm.Text(`{"type":"final","text":"推荐 p_seed_fake_phone，另外 p_seed_earbuds 也不错。","followups":["要加购吗"]}`),
	)
	m.Fallback = func(llm.Request) (llm.Response, error) { return llm.Response{}, context.DeadlineExceeded }
	cases := writeCases(t, `{"id":"x","group":"recommend","query":"推荐一款通勤降噪耳机","expect_products":["p_seed_earbuds"]}`)
	rep, err := RunAgent(context.Background(), AgentOptions{Cases: cases, Model: mockModel(m)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 1 {
		t.Fatalf("failures: %v", rep.Failures)
	}
	if rep.Metrics["product_cards"] < 1 || rep.Metrics["hallucinated_card_rate"] != 0 || rep.Metrics["removed_product_ids"] != 1 || rep.Metrics["react_fallbacks"] != 0 {
		t.Fatalf("metrics: %v", rep.Metrics)
	}
	if rep.Config["runner"] != "rule+model" || rep.Config["react"] != "model:big-m" {
		t.Fatalf("config: %v", rep.Config)
	}
}

// TestCardAudit：不在在售目录、工具没返回过的商品卡都算幻觉挂品。
func TestCardAudit(t *testing.T) {
	var a cardAudit
	blocks := []map[string]any{{"type": agent.BlockProductList, "products": []map[string]any{{"product_id": "p1"}, {"product_id": "p2"}, {"product_id": "p3"}}}}
	traces := []traceRec{{stage: "tool", typ: "search_products", status: "ok", meta: map[string]any{"result": map[string]any{"product_ids": []string{"p1"}}}}}
	bad := a.check(blocks, traces, map[string]bool{"p1": true, "p2": true})
	if a.cards != 3 || a.notFromTools != 1 || a.notVisible != 1 || len(bad) != 2 {
		t.Fatalf("audit: %+v %v", a, bad)
	}
}

// 按模型拆分 token：规划用小模型、工具循环用大模型时成本分开算。
func TestTokensByModel(t *testing.T) {
	m := (&llm.Mock{}).Then(
		llm.Reply{Content: `{"intent":"product_search","query":"降噪耳机"}`, Usage: llm.Usage{PromptTokens: 100, CachedPromptTokens: 60, CompletionTokens: 10}},
		llm.Reply{Content: `{"type":"tool","name":"search_products","args":{"query":"降噪耳机"}}`, Usage: llm.Usage{PromptTokens: 1000, CachedPromptTokens: 800, CompletionTokens: 20}},
		llm.Reply{Content: `{"type":"final","text":"推荐这款耳机。"}`, Usage: llm.Usage{PromptTokens: 1500, CachedPromptTokens: 1000, CompletionTokens: 30}},
	)
	cases := writeCases(t, `{"id":"x","query":"推荐一款通勤降噪耳机"}`)
	rep, err := RunAgent(context.Background(), AgentOptions{Cases: cases, Model: mockModel(m)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Metrics["tokens_in:small-m"] != 100 || rep.Metrics["tokens_in_cached:small-m"] != 60 || rep.Metrics["tokens_in:big-m"] != 2500 ||
		rep.Metrics["tokens_in_cached:big-m"] != 1800 || rep.Metrics["tokens_out:big-m"] != 50 || rep.Metrics["cached_prompt_tokens"] != 1860 {
		t.Fatalf("metrics: %v", rep.Metrics)
	}
}
