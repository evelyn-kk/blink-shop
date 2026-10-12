package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/llm"
)

// 语义断言（expect_meaning / forbid_meaning）由小模型裁判判定：给它用户问题、回答全文、结构化块摘要和一条命题，
// 只回答“回答是否表达了这条命题”。裁判只补充机器断言（商品卡、工具调用、业务写入仍是机器判定），不打分、不替代通过 / 失败。
// 提示词只在评测里用，不走 Prompt 管理；改动后要用 judge_calibration.jsonl 重新校准。

const judgePrompt = `你是电商导购回答的评审。给你：用户的问题、导购助手的回答、回答附带的结构化卡片摘要，以及一条命题。
判断回答（含卡片）是否明确表达了这条命题，或必然能从回答推出这条命题。

规则：
1. 只依据给出的回答和卡片，不用常识或商品知识替回答补全；不评价回答好不好、礼不礼貌、全不全面。
2. 意思相同、措辞不同算 yes（“卖完了”等于“没货”，“由你承担”等于“买家承担”）。
3. 回答没有涉及命题、只说了一部分、说得模棱两可、需要用户自己推断，都算 no。
4. 回答与命题相反算 no。命题里的对象（哪件商品、哪个订单）、数字和条件必须与回答一致，换成别的商品或别的条件算 no。
5. 命题要求“告诉用户 / 说明 / 确认 / 拒绝”时，回答里必须有对应的表述；只给出卡片、不置可否，算 no。

只输出一个 JSON 对象：{"verdict":"yes" 或 "no","reason":"不超过 40 字的理由"}`

const (
	judgeMaxTokens = 200
	judgeTemp      = 0.0
)

// Judge 是语义断言的裁判。并发安全：用量和耗时按调用累计。
type Judge struct {
	LLM     llm.Provider
	Model   string
	Timeout time.Duration

	mu                         sync.Mutex
	calls, errors              int
	prompt, completion, cached int
	latencies                  []time.Duration
}

// JudgeInput 是一次判定的输入。
type JudgeInput struct {
	Question, Answer, Blocks, Proposition string
}

// Verdict 是裁判的结论。
type Verdict struct {
	Yes    bool
	Reason string
}

// Check 判定一条命题；输出不是合法 JSON 或 verdict 不是 yes / no 时返回错误（不当成 no）。
func (j *Judge) Check(ctx context.Context, in JudgeInput) (Verdict, error) {
	if j.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, j.Timeout)
		defer cancel()
	}
	user := fmt.Sprintf("用户的问题：%s\n\n助手的回答：%s\n\n结构化卡片摘要：%s\n\n命题：%s", in.Question, orNone(in.Answer), orNone(in.Blocks), in.Proposition)
	start := time.Now()
	resp, err := j.LLM.Complete(ctx, llm.Request{Model: j.Model, Temperature: judgeTemp, MaxTokens: judgeMaxTokens, JSON: true,
		Messages: []llm.Message{{Role: "system", Content: judgePrompt}, {Role: "user", Content: user}}})
	j.mu.Lock()
	j.calls++
	j.latencies = append(j.latencies, time.Since(start))
	j.prompt += resp.Usage.PromptTokens
	j.completion += resp.Usage.CompletionTokens
	j.cached += resp.Usage.CachedPromptTokens
	if err != nil {
		j.errors++
	}
	j.mu.Unlock()
	if err != nil {
		return Verdict{}, err
	}
	var out struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	raw := strings.TrimSpace(resp.Content)
	if i, k := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && k > i {
		raw = raw[i : k+1]
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Verdict{}, fmt.Errorf("judge: invalid JSON: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(out.Verdict)) {
	case "yes":
		return Verdict{Yes: true, Reason: out.Reason}, nil
	case "no":
		return Verdict{Yes: false, Reason: out.Reason}, nil
	}
	return Verdict{}, fmt.Errorf("judge: verdict %q", out.Verdict)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return s
}

// write 把裁判用量写进报告（judge_ 前缀）。
func (j *Judge) write(rep *Report) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	rep.Metrics["judge_calls"] = float64(j.calls)
	rep.Metrics["judge_errors"] = float64(j.errors)
	rep.Metrics["judge_prompt_tokens"] = float64(j.prompt)
	rep.Metrics["judge_cached_prompt_tokens"] = float64(j.cached)
	rep.Metrics["judge_completion_tokens"] = float64(j.completion)
	rep.Metrics["judge_call_p50_ms"] = float64(percentile(j.latencies, 0.50).Milliseconds())
	rep.Metrics["judge_call_p95_ms"] = float64(percentile(j.latencies, 0.95).Milliseconds())
	rep.Config["judge"] = "model:" + j.Model
}

// checkMeaning 用裁判检查一条用例的 expect_meaning（必须表达）和 forbid_meaning（不能表达）。judge 为 nil 时跳过（返回跳过数）。
func checkMeaning(ctx context.Context, j *Judge, question, answer string, blocks []map[string]any, expect, forbid []string) (problems []string, skipped int, verdicts []map[string]any) {
	if len(expect)+len(forbid) == 0 {
		return nil, 0, nil
	}
	if j == nil {
		return nil, len(expect) + len(forbid), nil
	}
	summary := summarizeBlocks(blocks)
	check := func(p string, want bool) {
		v, err := j.Check(ctx, JudgeInput{Question: question, Answer: answer, Blocks: summary, Proposition: p})
		rec := map[string]any{"proposition": p, "want": want}
		switch {
		case err != nil:
			rec["error"] = err.Error()
			problems = append(problems, "裁判失败："+p)
		case v.Yes != want:
			rec["verdict"], rec["reason"] = v.Yes, v.Reason
			if want {
				problems = append(problems, "语义：回答应表达“"+p+"”（裁判："+v.Reason+"）")
			} else {
				problems = append(problems, "语义：回答不应表达“"+p+"”（裁判："+v.Reason+"）")
			}
		default:
			rec["verdict"], rec["reason"] = v.Yes, v.Reason
		}
		verdicts = append(verdicts, rec)
	}
	for _, p := range expect {
		check(p, true)
	}
	for _, p := range forbid {
		check(p, false)
	}
	return problems, 0, verdicts
}

// summarizeBlocks 把结构化块压成给裁判看的一行摘要：块类型、标题、商品名和价格、引用的文档标题。
func summarizeBlocks(blocks []map[string]any) string {
	var parts []string
	for _, b := range blocks {
		typ, _ := b["type"].(string)
		title, _ := b["title"].(string)
		s := typ
		if title != "" {
			s += "「" + title + "」"
		}
		raw, _ := json.Marshal(b)
		var body struct {
			Products []struct {
				Name  string `json:"name"`
				Price any    `json:"price"`
			} `json:"products"`
			Citations []struct {
				DocumentTitle string `json:"document_title"`
			} `json:"citations"`
			Items []struct {
				Name     string `json:"product_name"`
				Quantity int    `json:"quantity"`
			} `json:"items"`
		}
		_ = json.Unmarshal(raw, &body)
		var names []string
		for _, p := range body.Products {
			names = append(names, fmt.Sprintf("%s（%v）", p.Name, p.Price))
		}
		for _, c := range body.Citations {
			names = append(names, c.DocumentTitle)
		}
		for _, it := range body.Items {
			names = append(names, fmt.Sprintf("%s ×%d", it.Name, it.Quantity))
		}
		if len(names) > 0 {
			s += "：" + strings.Join(names, "、")
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；")
}

// JudgeCase 是校准集的一条：问题、回答、卡片摘要、命题和人工标签。
type JudgeCase struct {
	ID          string `json:"id"`
	Question    string `json:"question"`
	Answer      string `json:"answer"`
	Blocks      string `json:"blocks"`
	Proposition string `json:"proposition"`
	Label       string `json:"label"`
	Reason      string `json:"reason"`
	Kind        string `json:"kind"`
}

// JudgeOptions 是裁判校准的输入。
type JudgeOptions struct {
	Cases   string
	Judge   *Judge
	Repeat  int // 同一批样本重复判几遍（稳定性），默认 3
	Version string
	Config  map[string]string
	// MinPass 是一致率门槛，0 表示 0.95。
	MinPass float64
}

// RunJudgeCalibration 用校准集检验裁判：每条判 Repeat 遍，指标为每遍与人工标签的一致率、Cohen's kappa、
// 重复判定完全一致的比例（稳定性）。逐条结果记三遍的结论，任何一遍与人工不同即列为不一致样本。
func RunJudgeCalibration(ctx context.Context, o JudgeOptions) (Report, error) {
	start := time.Now()
	rep := Report{Suite: "judge", Version: o.Version, StartedAt: start.UTC(), Config: map[string]string{}, Metrics: map[string]float64{}}
	for k, v := range o.Config {
		rep.Config[k] = v
	}
	if o.Judge == nil {
		return rep, errors.New("裁判校准需要模型（-judge=env）")
	}
	if o.Repeat <= 0 {
		o.Repeat = 3
	}
	rep.Config["repeat"] = fmt.Sprint(o.Repeat)
	cases, err := readJSONL[JudgeCase](o.Cases)
	if err != nil {
		return rep, err
	}
	verdicts := make([][]string, len(cases)) // [用例][轮] = yes / no / error
	for round := 0; round < o.Repeat; round++ {
		for i, c := range cases {
			v, err := o.Judge.Check(ctx, JudgeInput{Question: c.Question, Answer: c.Answer, Blocks: c.Blocks, Proposition: c.Proposition})
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return rep, ctx.Err()
				}
				verdicts[i] = append(verdicts[i], "error")
			case v.Yes:
				verdicts[i] = append(verdicts[i], "yes")
			default:
				verdicts[i] = append(verdicts[i], "no")
			}
		}
	}
	var stable int
	for r := 0; r < o.Repeat; r++ {
		var col []string
		for i := range cases {
			col = append(col, verdicts[i][r])
		}
		agree, kappa := agreementKappa(cases, col)
		rep.Metrics[fmt.Sprintf("agreement_r%d", r+1)] = round(agree)
		rep.Metrics[fmt.Sprintf("kappa_r%d", r+1)] = round(kappa)
	}
	var sumAgree, sumKappa float64
	for r := 0; r < o.Repeat; r++ {
		sumAgree += rep.Metrics[fmt.Sprintf("agreement_r%d", r+1)]
		sumKappa += rep.Metrics[fmt.Sprintf("kappa_r%d", r+1)]
	}
	rep.Metrics["agreement"] = round(sumAgree / float64(o.Repeat))
	rep.Metrics["kappa"] = round(sumKappa / float64(o.Repeat))
	for i, c := range cases {
		same := true
		for _, v := range verdicts[i][1:] {
			if v != verdicts[i][0] {
				same = false
			}
		}
		if same {
			stable++
		}
		var problems []string
		for r, v := range verdicts[i] {
			if v != c.Label {
				problems = append(problems, fmt.Sprintf("第 %d 遍判为 %s，人工 %s（%s）", r+1, v, c.Label, c.Reason))
			}
		}
		rep.recordDetail(c.Kind+"/"+c.Label, Failure{ID: c.ID, Query: c.Proposition + " ← " + c.Answer, Problems: problems, Got: verdicts[i]},
			map[string]any{"verdicts": verdicts[i], "label": c.Label})
	}
	if len(cases) > 0 {
		rep.Metrics["stability"] = round(float64(stable) / float64(len(cases)))
	}
	o.Judge.write(&rep)
	if o.MinPass == 0 {
		o.MinPass = 0.95
	}
	rep.finish(start, o.MinPass)
	// 门槛看一致率（逐条“三遍都对”比一致率更严，通过率另算）
	rep.MinPassRate = o.MinPass
	if rep.Metrics["agreement"] >= o.MinPass && rep.Metrics["kappa"] >= 0.9 && rep.Metrics["stability"] >= 0.98 {
		rep.Gate = GatePassed
	} else {
		rep.Gate = GateFailed
	}
	return rep, nil
}

// agreementKappa 计算一轮结论与人工标签的一致率和 Cohen's kappa（裁判出错按“与人工不同”计）。
func agreementKappa(cases []JudgeCase, got []string) (float64, float64) {
	n := float64(len(cases))
	if n == 0 {
		return 0, 0
	}
	var agree, humanYes, judgeYes float64
	for i, c := range cases {
		g := got[i]
		if g == "error" { // 出错当成与人工相反的结论
			if c.Label == "yes" {
				g = "no"
			} else {
				g = "yes"
			}
		}
		if g == c.Label {
			agree++
		}
		if c.Label == "yes" {
			humanYes++
		}
		if g == "yes" {
			judgeYes++
		}
	}
	po := agree / n
	pe := (humanYes/n)*(judgeYes/n) + (1-humanYes/n)*(1-judgeYes/n)
	if pe == 1 {
		return po, 1
	}
	return po, (po - pe) / (1 - pe)
}
