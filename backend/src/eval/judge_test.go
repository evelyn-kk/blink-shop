package eval

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/llm"
)

// 一致率和 kappa：4 条（人工 是是否否，裁判 是否否否）→ 一致率 0.75，期望一致 0.5，kappa 0.5；出错按与人工相反计。
func TestAgreementKappa(t *testing.T) {
	cases := []JudgeCase{{Label: "yes"}, {Label: "yes"}, {Label: "no"}, {Label: "no"}}
	po, k := agreementKappa(cases, []string{"yes", "no", "no", "no"})
	if po != 0.75 || math.Abs(k-0.5) > 1e-9 {
		t.Fatalf("po=%v kappa=%v", po, k)
	}
	if po, k := agreementKappa(cases, []string{"yes", "yes", "no", "no"}); po != 1 || k != 1 {
		t.Fatalf("perfect: %v %v", po, k)
	}
	if po, _ := agreementKappa(cases, []string{"yes", "error", "no", "error"}); po != 0.5 {
		t.Fatalf("errors count as disagreement: %v", po)
	}
}

// labelJudge 按命题查人工标签作答；flip 里的命题在第 flipRound 次被问到时答反。
func labelJudge(labels map[string]string, flip string) *Judge {
	asked := map[string]int{}
	return &Judge{Model: "judge-m", LLM: &llm.Mock{Fallback: func(req llm.Request) (llm.Response, error) {
		u := req.Messages[1].Content
		p := u[strings.LastIndex(u, "命题：")+len("命题："):]
		asked[p]++
		v := labels[p]
		if p == flip && asked[p] == 2 {
			v = map[string]string{"yes": "no", "no": "yes"}[v]
		}
		return llm.Response{Content: `{"verdict":"` + v + `","reason":"r"}`, Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 10}}, nil
	}}}
}

// 校准：三遍里第二遍有一条判反 → 第二遍一致率 0.75、稳定性 0.75，这条列为不一致样本；用量进报告。
func TestJudgeCalibration(t *testing.T) {
	cases := writeCases(t,
		`{"id":"a","question":"q","answer":"没有符合预算的手机","proposition":"P1","label":"yes","reason":"r","kind":"direct"}`,
		`{"id":"b","question":"q","answer":"推荐 Nova 12","proposition":"P2","label":"no","reason":"r","kind":"near_miss"}`,
		`{"id":"c","question":"q","answer":"不能游泳时戴","proposition":"P3","label":"yes","reason":"r","kind":"paraphrase"}`,
		`{"id":"d","question":"q","answer":"续航 30 小时","proposition":"P4","label":"no","reason":"r","kind":"omission"}`)
	j := labelJudge(map[string]string{"P1": "yes", "P2": "no", "P3": "yes", "P4": "no"}, "P2")
	rep, err := RunJudgeCalibration(context.Background(), JudgeOptions{Cases: cases, Judge: j, Repeat: 3})
	if err != nil {
		t.Fatal(err)
	}
	m := rep.Metrics
	if m["agreement_r1"] != 1 || m["agreement_r2"] != 0.75 || m["agreement_r3"] != 1 || m["stability"] != 0.75 || m["judge_calls"] != 12 || m["judge_prompt_tokens"] != 1200 {
		t.Fatalf("metrics: %v", m)
	}
	if rep.Gate != GateFailed || len(rep.Failures) != 1 || rep.Failures[0].ID != "b" {
		t.Fatalf("gate=%s failures=%v", rep.Gate, rep.Failures)
	}
	if _, err := RunJudgeCalibration(context.Background(), JudgeOptions{Cases: cases}); err == nil {
		t.Fatal("calibration without judge should fail")
	}
}

// 语义断言：没有裁判时跳过并计数；有裁判时判反记为失败；裁判输出不合法记为“裁判失败”而不是当成否。
func TestCheckMeaning(t *testing.T) {
	ctx := context.Background()
	if p, skipped, _ := checkMeaning(ctx, nil, "q", "a", nil, []string{"X"}, []string{"Y"}); len(p) != 0 || skipped != 2 {
		t.Fatalf("nil judge: %v %d", p, skipped)
	}
	j := labelJudge(map[string]string{"说了没货": "yes", "推荐了手机": "yes"}, "")
	p, _, verdicts := checkMeaning(ctx, j, "q", "a", nil, []string{"说了没货"}, []string{"推荐了手机"})
	if len(p) != 1 || !strings.Contains(p[0], "不应表达") || len(verdicts) != 2 {
		t.Fatalf("problems: %v %v", p, verdicts)
	}
	bad := &Judge{Model: "m", LLM: (&llm.Mock{}).Then(llm.Text("我觉得是的"))}
	if p, _, _ := checkMeaning(ctx, bad, "q", "a", nil, []string{"X"}, nil); len(p) != 1 || !strings.Contains(p[0], "裁判失败") {
		t.Fatalf("invalid judge output: %v", p)
	}
}

// 端到端用例带语义断言：裁判拿到问题、回答全文和块摘要；判否时用例失败，结构化断言照常检查。
func TestAgentMeaningAssertion(t *testing.T) {
	var seen string
	j := &Judge{Model: "judge-m", LLM: &llm.Mock{Fallback: func(req llm.Request) (llm.Response, error) {
		seen = req.Messages[1].Content
		return llm.Response{Content: `{"verdict":"no","reason":"没说降噪"}`}, nil
	}}}
	cases := writeCases(t, `{"id":"x","query":"推荐一款通勤降噪耳机","expect_products":["p_seed_earbuds"],"expect_meaning":["推荐了一款降噪耳机"]}`)
	rep, err := RunAgent(context.Background(), AgentOptions{Cases: cases, Judge: j})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 0 || !strings.Contains(strings.Join(rep.Failures[0].Problems, ";"), "推荐了一款降噪耳机") || rep.Metrics["judge_calls"] != 1 {
		t.Fatalf("report: %v %v", rep.Failures, rep.Metrics)
	}
	if !strings.Contains(seen, "推荐一款通勤降噪耳机") || !strings.Contains(seen, "product_list") || !strings.Contains(seen, "Blink Air 降噪耳机") {
		t.Fatalf("judge input lacks question / blocks: %s", seen)
	}
	// 没有裁判：语义断言跳过，用例按结构化断言通过
	rep, err = RunAgent(context.Background(), AgentOptions{Cases: cases})
	if err != nil || rep.Passed != 1 || rep.Metrics["meaning_skipped"] != 1 {
		t.Fatalf("no judge: %v %v", err, rep.Metrics)
	}
}
