package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ModelConfig 让评测用模型跑规划、工具循环、重排和摘要（与线上同一个运行器）；nil 表示纯规则（默认，CI 用）。
type ModelConfig struct {
	LLM      llm.Provider
	Settings agent.ModelSettings
}

// describe 把模型开关和模型名写进报告配置（进入配置指纹）。
func (m *ModelConfig) describe(cfg map[string]string) {
	if m == nil {
		return
	}
	s := m.Settings
	onOff := func(b bool, model string) string {
		if !b {
			return "off"
		}
		return "model:" + model
	}
	cfg["planner"] = onOff(s.PlannerEnabled, s.PlannerModel)
	cfg["react"] = onOff(s.AgentEnabled, s.AgentModel)
	cfg["rerank"] = onOff(s.RerankEnabled, s.PlannerModel)
	cfg["summary"] = onOff(s.SummaryEnabled, s.PlannerModel)
	cfg["max_tool_rounds"] = fmt.Sprint(s.MaxToolRounds)
	cfg["llm_timeout"] = s.Timeout.String()
}

func (m *ModelConfig) deps(d agent.Deps, mt *meter) agent.Deps {
	if m == nil {
		return d
	}
	s := m.Settings
	d.LLM = mt
	d.Settings = func(context.Context) agent.ModelSettings { return s }
	return d
}

// meter 包住 Provider，统计一条用例里的模型调用次数、token、耗时和错误。
type meter struct {
	inner                                         llm.Provider
	mu                                            sync.Mutex
	calls, errors, promptTokens, completionTokens int
	latencies                                     []time.Duration
	models                                        map[string]int
}

func newMeter(p llm.Provider) *meter {
	if p == nil {
		return nil
	}
	return &meter{inner: p, models: map[string]int{}}
}

func (m *meter) record(req llm.Request, resp llm.Response, err error, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.models[req.Model]++
	m.latencies = append(m.latencies, d)
	m.promptTokens += resp.Usage.PromptTokens
	m.completionTokens += resp.Usage.CompletionTokens
	if err != nil {
		m.errors++
	}
}

func (m *meter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	start := time.Now()
	resp, err := m.inner.Complete(ctx, req)
	m.record(req, resp, err, time.Since(start))
	return resp, err
}

func (m *meter) Stream(ctx context.Context, req llm.Request, onDelta func(string) error) (llm.Response, error) {
	start := time.Now()
	resp, err := m.inner.Stream(ctx, req, onDelta)
	m.record(req, resp, err, time.Since(start))
	return resp, err
}

// modelStats 汇总整套用例的模型用量与回退，写进报告指标。
type modelStats struct {
	cases                             int
	calls, errors, prompt, completion int
	callLatency, caseLatency          []time.Duration
	plannerFallbacks, reactFallbacks  int
	reactFallbackReasons              map[string]int
	removedProductIDs                 int
	plannerModelOK, plannerModelRuns  int
	models                            map[string]int
}

// add 记一条用例：m 为 nil（纯规则）时只记耗时；traces 是这条用例所有轮次的轨迹。返回写进逐条结果的明细。
func (s *modelStats) add(m *meter, caseLatency time.Duration, traces []traceRec) map[string]any {
	s.cases++
	s.caseLatency = append(s.caseLatency, caseLatency)
	if s.reactFallbackReasons == nil {
		s.reactFallbackReasons = map[string]int{}
		s.models = map[string]int{}
	}
	detail := map[string]any{"latency_ms": caseLatency.Milliseconds()}
	var reasons []string
	for _, tr := range traces {
		switch {
		case tr.stage == "planner" && tr.typ == "model":
			s.plannerModelRuns++
			if tr.status == "error" {
				s.plannerFallbacks++
				reasons = append(reasons, "planner:"+fmt.Sprint(tr.meta["error"]))
			} else {
				s.plannerModelOK++
			}
		case tr.stage == "react" && tr.typ == "fallback":
			s.reactFallbacks++
			r := fmt.Sprint(tr.meta["reason"])
			s.reactFallbackReasons[r]++
			reasons = append(reasons, "react:"+r)
		case tr.stage == "answer" && tr.typ == "model":
			if ids, ok := tr.meta["removed_product_ids"].([]string); ok {
				s.removedProductIDs += len(ids)
				if len(ids) > 0 {
					detail["removed_product_ids"] = ids
				}
			}
		}
	}
	if len(reasons) > 0 {
		detail["fallbacks"] = reasons
	}
	if m == nil {
		return detail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s.calls += m.calls
	s.errors += m.errors
	s.prompt += m.promptTokens
	s.completion += m.completionTokens
	s.callLatency = append(s.callLatency, m.latencies...)
	for k, v := range m.models {
		s.models[k] += v
	}
	detail["model_calls"], detail["prompt_tokens"], detail["completion_tokens"], detail["model_errors"] = m.calls, m.promptTokens, m.completionTokens, m.errors
	return detail
}

// write 把汇总写进报告指标；withModel 为 false 时只写耗时。
func (s *modelStats) write(rep *Report, withModel bool) {
	rep.Metrics["case_latency_p50_ms"] = float64(percentile(s.caseLatency, 0.50).Milliseconds())
	rep.Metrics["case_latency_p95_ms"] = float64(percentile(s.caseLatency, 0.95).Milliseconds())
	if !withModel || s.cases == 0 {
		return
	}
	n := float64(s.cases)
	rep.Metrics["model_calls"] = float64(s.calls)
	rep.Metrics["model_errors"] = float64(s.errors)
	rep.Metrics["model_calls_per_case"] = round(float64(s.calls) / n)
	rep.Metrics["prompt_tokens"] = float64(s.prompt)
	rep.Metrics["completion_tokens"] = float64(s.completion)
	rep.Metrics["tokens_per_case"] = round(float64(s.prompt+s.completion) / n)
	rep.Metrics["model_call_p50_ms"] = float64(percentile(s.callLatency, 0.50).Milliseconds())
	rep.Metrics["model_call_p95_ms"] = float64(percentile(s.callLatency, 0.95).Milliseconds())
	rep.Metrics["planner_model_runs"] = float64(s.plannerModelRuns)
	rep.Metrics["planner_fallbacks"] = float64(s.plannerFallbacks)
	rep.Metrics["react_fallbacks"] = float64(s.reactFallbacks)
	for r, c := range s.reactFallbackReasons {
		rep.Metrics["react_fallback_"+r] = float64(c)
	}
	rep.Metrics["removed_product_ids"] = float64(s.removedProductIDs)
	models := make([]string, 0, len(s.models))
	for k, v := range s.models {
		models = append(models, fmt.Sprintf("%s=%d", k, v))
	}
	slices.Sort(models)
	rep.Config["model_calls_by_model"] = strings.Join(models, ",")
}

// percentile 用最近秩法取分位数；空切片为 0。
func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	idx := int(float64(len(s))*p+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// cardAudit 检查商品卡是否都“有出处”：存在于在售目录、并由本段对话里的工具返回过。用于统计幻觉挂品。
type cardAudit struct {
	cards, notVisible, notFromTools int
}

// check 审计最后一轮的商品卡；traces 是整段对话的轨迹（多轮用例里之前轮次的搜索结果也算出处）。返回有问题的卡。
func (a *cardAudit) check(blocks []map[string]any, traces []traceRec, visible map[string]bool) []string {
	fromTools := map[string]bool{}
	for _, tr := range traces {
		if tr.stage != "tool" || tr.status != "ok" {
			continue
		}
		res, _ := tr.meta["result"].(map[string]any)
		if ids, ok := res["product_ids"].([]string); ok {
			for _, id := range ids {
				fromTools[id] = true
			}
		}
	}
	var bad []string
	seen := map[string]bool{}
	for _, b := range blocks {
		t, _ := b["type"].(string)
		if t != agent.BlockProductList && t != agent.BlockComparison {
			continue
		}
		for _, id := range cardIDs(b) {
			if seen[id] {
				continue
			}
			seen[id] = true
			a.cards++
			if !visible[id] {
				a.notVisible++
				bad = append(bad, id+"(不在在售目录)")
			} else if !fromTools[id] {
				a.notFromTools++
				bad = append(bad, id+"(工具未返回)")
			}
		}
	}
	return bad
}

func (a *cardAudit) write(rep *Report) {
	rep.Metrics["product_cards"] = float64(a.cards)
	rep.Metrics["cards_not_visible"] = float64(a.notVisible)
	rep.Metrics["cards_not_from_tools"] = float64(a.notFromTools)
	rate := 0.0
	if a.cards > 0 {
		rate = round(float64(a.notVisible+a.notFromTools) / float64(a.cards))
	}
	rep.Metrics["hallucinated_card_rate"] = rate
}

// cardIDs 取商品卡 / 对比表里的商品 ID。
func cardIDs(b map[string]any) []string {
	var list []struct {
		ProductID string `json:"product_id"`
	}
	_ = json.Unmarshal(mustJSON(b["products"]), &list)
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.ProductID)
	}
	return out
}

// visibleProducts 返回演示数据里在售（用户可见）的商品 ID。
func visibleProducts(ctx context.Context, st store.Store) (map[string]bool, error) {
	items, _, err := st.SearchVisibleProducts(ctx, store.ProductSearch{Page: store.Page{Page: 1, PageSize: 100}})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(items))
	for _, it := range items {
		out[it.ProductID] = true
	}
	return out, nil
}
