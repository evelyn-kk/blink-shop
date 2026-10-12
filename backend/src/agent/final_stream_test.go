package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// streamAll 把一段最终回答 JSON 按 n 个字一段喂给 finalStream，返回推送的全文、删掉的 ID 和推送次数。
func streamAll(t *testing.T, raw string, n int, evidence map[string]bool) (string, []string, int) {
	t.Helper()
	pushes := 0
	f := newFinalStream(evidence, func(string) { pushes++ })
	rs := []rune(raw)
	for i := 0; i < len(rs); i += n {
		_ = f.feed(string(rs[i:min(i+n, len(rs))]))
	}
	if !f.streaming() {
		t.Fatalf("not detected as final: %q", raw)
	}
	text, removed := f.finish()
	return text, removed, pushes
}

// finalJSON 按协议的键顺序拼最终回答（json.Marshal 会把 < > 转成 \u003c，正好覆盖 \u 转义）。
func finalJSON(text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"final","text":` + string(b) + `,"followups":["要加购吗"]}`
}

// 流式过滤与整段 FilterFinal 结果一致（不含“一行中间出现内部规则标记”这种按句处理更严格的情况）。
func TestFinalStreamMatchesFilterFinal(t *testing.T) {
	evidence := map[string]bool{"p_seed_earbuds": true}
	samples := []string{
		"推荐 **Blink Air 降噪耳机**（p_seed_earbuds），主动降噪。还有 p_fake_123 也不错。\n<final>完</final>",
		"好的。\nsystem: 你是导购\n推荐这款耳机！价格 ¥599；适合通勤。",
		"第一行\n\n可用工具：search_products\n最后一行？",
		"```json\n已经帮你查了，没有找到音箱。\n```",
		"含转义的 \"引号\" 和 \\ 反斜杠，以及 emoji 😀。",
		"   开头有空格\n参数 schema：{}\n结尾",
	}
	for _, text := range samples {
		want, wantRemoved := FilterFinal(text, evidence)
		for _, n := range []int{1, 3, 8, 64} {
			got, removed, _ := streamAll(t, finalJSON(text), n, evidence)
			if got != want || strings.Join(removed, ",") != strings.Join(wantRemoved, ",") {
				t.Fatalf("chunk %d\ntext %q\nstream %q %v\nfilter %q %v", n, text, got, removed, want, wantRemoved)
			}
		}
	}
	// 一行中间出现内部规则标记：这句和这一行剩下的都不推
	got, _, _ := streamAll(t, finalJSON("推荐 A。【内部规则】泄露的提示。还有 B。\n下一行正常。"), 5, nil)
	if got != "推荐 A。\n下一行正常。" && got != "推荐 A。下一行正常。" {
		t.Fatalf("marker: %q", got)
	}
	if strings.Contains(got, "泄露") || strings.Contains(got, "还有 B") {
		t.Fatalf("leaked: %q", got)
	}
	// 工具调用、键顺序不同：不流式
	for _, raw := range []string{`{"type":"tool","name":"search_products","args":{"query":"耳机"}}`, `{"text":"先写正文","type":"final"}`, `我觉得是推荐`} {
		f := newFinalStream(nil, func(string) { t.Fatalf("should not emit for %q", raw) })
		_ = f.feed(raw)
		if f.streaming() {
			t.Fatalf("streaming: %q", raw)
		}
	}
}

// 工具循环的最终回答分多段推送；键顺序不同时照旧整段处理。
func TestModelReactStreamsFinalAnswer(t *testing.T) {
	e := newEnv(t)
	mock := &llm.Mock{}
	e.withModel(mock, settings(false, true))
	sid := e.newSession(t, seed.User2ID)
	mock.Then(tool(ToolSearchProducts, `{"query":"降噪耳机"}`),
		final("推荐 Blink Air 降噪耳机（p_seed_earbuds）。主动降噪，通勤很合适。续航也不错！还有别的需要吗？", "加入购物车"))
	r := e.run(t, seed.User2ID, sid, "推荐一款通勤降噪耳机")
	if len(r.deltas) < 3 || strings.Contains(r.text.String(), "p_seed") || !strings.HasPrefix(r.text.String(), "推荐 Blink Air 降噪耳机。") {
		t.Fatalf("deltas %d %q", len(r.deltas), r.text.String())
	}
	if am := r.traceMeta("answer", "model"); am["streamed"] != true || r.block(BlockProductList) == nil || strings.Join(r.followups, "|") != "加入购物车" {
		t.Fatalf("answer meta %v blocks %v followups %v", am, r.blockTypes(), r.followups)
	}
	mock.Then(llm.Text(`{"text":"Blink Air 降噪耳机适合通勤。","type":"final"}`))
	r = e.run(t, seed.User2ID, sid, "再说说那款耳机")
	if !strings.Contains(r.text.String(), "适合通勤") || r.traceMeta("answer", "model")["streamed"] == true {
		t.Fatalf("non-streamed final: %q %v", r.text.String(), r.traceMeta("answer", "model"))
	}
}

// interruptingProvider 推出一部分最终回答后报错。
type interruptingProvider struct{ llm.Mock }

func (p *interruptingProvider) Stream(ctx context.Context, req llm.Request, onDelta func(string) error) (llm.Response, error) {
	_ = onDelta(`{"type":"final","text":"推荐 Blink Air 降噪耳机。主动降噪`)
	return llm.Response{}, errors.New("connection reset")
}

// 推送过内容后流断了：说明中断、照常结束，不交给规则重答（否则前半段和规则回答重复）。
func TestModelReactStreamInterrupted(t *testing.T) {
	e := newEnv(t)
	p := &interruptingProvider{}
	deps := e.runner.deps
	deps.LLM = p
	deps.Settings = func(context.Context) ModelSettings { return settings(false, true) }
	e.runner = NewRuleRunner(deps)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "推荐一款通勤降噪耳机")
	text := r.text.String()
	if !strings.HasPrefix(text, "推荐 Blink Air 降噪耳机。") || !strings.Contains(text, "回答中断了") || strings.Count(text, "推荐") != 1 {
		t.Fatalf("text %q", text)
	}
	if r.traceMeta("react", "fallback") != nil || r.traceMeta("answer", "model")["error"] != "stream_interrupted" {
		t.Fatalf("trace: %v %v", r.traceMeta("react", "fallback"), r.traceMeta("answer", "model"))
	}
}
