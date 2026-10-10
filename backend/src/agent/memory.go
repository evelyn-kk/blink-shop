package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// 会话记忆（只在同一个账户的同一个会话里）：
//   - 历史轮次：最近 N 轮的用户消息、回答摘要和展示过的商品；
//   - 相关轮次检索：按检索词与历史轮次的重合度取最相关的几轮，交给规划和工具循环做上下文；
//   - 指代：“第一个 / 刚才那个”按最近一次展示的商品卡解析；所有展示过的商品都进入加购可信集；
//   - 追问继承：本轮没有品类锚点、只补了预算 / 品牌 / 排除 / “还有别的吗”时，沿用最近一次的商品需求；
//   - 滚动摘要：回答完成后按规则（可选小模型）更新会话摘要；用户手动改过的摘要不覆盖。
// 存储层的读取都带 account_id：别人的会话按不存在处理，读不到任何记忆。

const (
	summaryPrefix       = "用户咨询："
	maxSummaryRunes     = 500
	relevantTurnsLimit  = 3
	memoryAnswerRunes   = 120
	memoryUserTextRunes = 120
)

// memTurn 是历史里的一轮。
type memTurn struct {
	MessageID string
	UserText  string
	Answer    string
	Status    domain.RunStatus
	Cards     []ProductCard
	Plan      Plan // 对这轮用户消息重新跑一遍规则规划得到的意图和检索词
	At        time.Time
}

// history 是会话记忆：Cards 是最近一张商品卡 / 对比表里的商品（按展示顺序），Evidence 是全部出现过的商品 ID，Turns 是最近 N 轮。
type history struct {
	Cards    []ProductCard
	Evidence map[string]bool
	Turns    []memTurn
	Summary  string
	// relevant 是本轮检索到的相关轮次（loadMemory 之后由 recall 填写）。
	relevant []memTurn
}

// loadHistory 读取会话历史；当前运行（RunID）排除。读取失败按没有历史处理（不影响回答）。
func loadHistory(ctx context.Context, st store.Store, accountID, sessionID, currentRunID string) history {
	return loadMemory(ctx, st, accountID, sessionID, currentRunID, 10)
}

func loadMemory(ctx context.Context, st store.Store, accountID, sessionID, currentRunID string, window int) history {
	h := history{Evidence: map[string]bool{}}
	if accountID == "" || sessionID == "" {
		return h
	}
	if cs, err := st.GetChatSession(ctx, accountID, sessionID); err == nil {
		h.Summary = cs.Summary
	}
	turns, err := st.ListChatMessages(ctx, accountID, sessionID)
	if err != nil {
		return h
	}
	for _, t := range turns {
		if t.Run == nil || t.Run.RunID == currentRunID {
			continue
		}
		mt := memTurn{MessageID: t.Message.MessageID, UserText: t.Message.Content, Answer: t.Run.Content, Status: t.Run.Status, At: t.Message.CreatedAt}
		var cards []ProductCard
		for _, b := range t.Run.Blocks {
			kind, _ := b["type"].(string)
			if kind != BlockProductList && kind != BlockComparison {
				continue
			}
			raw, _ := json.Marshal(b["products"])
			var list []ProductCard
			if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
				cards = list
			}
		}
		if len(cards) > 0 {
			mt.Cards = cards
			h.Cards = cards
			for _, c := range cards {
				h.Evidence[c.ProductID] = true
			}
		}
		mt.Plan = Classify(mt.UserText, false)
		h.Turns = append(h.Turns, mt)
	}
	if window > 0 && len(h.Turns) > window {
		h.Turns = h.Turns[len(h.Turns)-window:]
	}
	return h
}

// recall 按检索词重合度找出与本轮最相关的历史轮次（最多 3 轮，越近越优先）。
func (h *history) recall(content string) []memTurn {
	terms := QueryTerms(content)
	type scored struct {
		t     memTurn
		score float64
		i     int
	}
	var list []scored
	for i, t := range h.Turns {
		text := strings.ToLower(t.UserText + " " + t.Answer)
		var sc float64
		for _, term := range terms {
			sc += coverage(strings.ToLower(term), text)
		}
		if sc > 0 {
			list = append(list, scored{t, sc, i})
		}
	}
	sort.SliceStable(list, func(a, b int) bool {
		if list[a].score != list[b].score {
			return list[a].score > list[b].score
		}
		return list[a].i > list[b].i
	})
	var out []memTurn
	for i, s := range list {
		if i >= relevantTurnsLimit {
			break
		}
		out = append(out, s.t)
	}
	h.relevant = out
	return out
}

// lastProductNeed 返回最近一次商品需求的检索词（product_search / product_compare 且有检索词），没有返回空。
func (h *history) lastProductNeed() (string, Plan) {
	for i := len(h.Turns) - 1; i >= 0; i-- {
		p := h.Turns[i].Plan
		if (p.Intent == IntentProductSearch || p.Intent == IntentProductCompare) && p.Query != "" && hasProductAnchor(h.Turns[i].UserText) {
			return p.Query, p
		}
	}
	return "", Plan{}
}

// strongFollowUp 是明确在接着上文找商品的说法；weakFollowUp（“那…呢”）只在本轮已判为商品需求时才算。
var (
	strongFollowUp = []string{"还有", "别的", "其他", "其它", "换一", "换个", "便宜", "贵一点", "再推荐", "再来", "再找", "更好", "预算", "以内", "以下", "以上", "不要", "除了"}
	weakFollowUp   = []string{"那", "呢"}
	followUpWords  = append(append([]string{}, strongFollowUp...), weakFollowUp...)
)

// hasProductAnchor 判断一句话有没有自己的商品锚点（品类词或型号）。
func hasProductAnchor(text string) bool {
	return containsAny(strings.ToLower(text), catalogWords...)
}

// inherit 处理追问：本轮是商品需求但没有品类锚点，且像在补充条件时，沿用最近一次的商品需求检索词。
func (h *history) inherit(p Plan, content string) (Plan, bool) {
	if p.Intent != IntentProductSearch && !(p.Intent == IntentGuide && p.Action == "fallback") {
		return p, false
	}
	if hasProductAnchor(stripExclusions(content)) { // “不要 Nova”里的 Nova 是排除项，不是锚点
		return p, false
	}
	lower := strings.ToLower(content)
	constrained := p.Budget > 0 || p.MinPrice > 0 || len(p.Exclude) > 0 || len(p.Brands) > 0
	followUp := containsAny(lower, strongFollowUp...) || (p.Intent == IntentProductSearch && containsAny(lower, weakFollowUp...))
	if !constrained && !followUp {
		return p, false
	}
	prevQuery, _ := h.lastProductNeed()
	if prevQuery == "" {
		return p, false
	}
	terms := strings.Fields(prevQuery)
	for _, t := range strings.Fields(p.Query) {
		if !containsStr(terms, t) && !containsAny(t, followUpWords...) {
			terms = append(terms, t)
		}
	}
	p.Query = strings.Join(terms, " ")
	p.Intent, p.Action, p.Inherited = IntentProductSearch, "", true
	p.Rule += "+memory"
	return p, true
}

// promptContext 把相关轮次压成给模型看的上下文（用户原话 + 回答开头），不含商品 ID 以外的内部数据。
func (h *history) promptContext() string {
	if len(h.relevant) == 0 && h.Summary == "" {
		return ""
	}
	var b strings.Builder
	if h.Summary != "" {
		b.WriteString("会话摘要：")
		b.WriteString(truncate(h.Summary, 200))
		b.WriteString("\n")
	}
	for _, t := range h.relevant {
		b.WriteString("- 用户：")
		b.WriteString(truncate(t.UserText, memoryUserTextRunes))
		if t.Answer != "" {
			b.WriteString(" ／ 回答：")
			b.WriteString(truncate(strings.ReplaceAll(t.Answer, "\n", " "), memoryAnswerRunes))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// ---------- 滚动摘要 ----------

// ruleSummary 按规则生成摘要：最近几轮商品 / 服务需求的主题（意图 + 检索词），去重后按时间先后连接，最多 500 字（超长时保留最近的）。
func ruleSummary(turns []memTurn, current Plan, currentText string) string {
	var topics []string
	add := func(p Plan, text string) {
		topic := IntentTitle(p.Intent)
		switch {
		case p.Query != "":
			topic += "（" + p.Query + "）"
		case p.Intent == IntentGuide || p.Intent == IntentNonGuide:
			topic = truncate(strings.TrimSpace(text), 20)
		}
		if topic != "" && (len(topics) == 0 || topics[len(topics)-1] != topic) {
			topics = append(topics, topic)
		}
	}
	for _, t := range turns {
		add(t.Plan, t.UserText)
	}
	add(current, currentText)
	out := summaryPrefix + strings.Join(topics, "；")
	for utf8.RuneCountInString(out) > maxSummaryRunes && len(topics) > 1 {
		topics = topics[1:]
		out = summaryPrefix + "…；" + strings.Join(topics, "；")
	}
	if utf8.RuneCountInString(out) > maxSummaryRunes {
		out = string([]rune(out)[:maxSummaryRunes-1]) + "…"
	}
	return out
}

const summaryPromptText = `你是导购会话摘要器。根据最近的对话，用一句不超过 120 字的中文概括用户的购物需求和进展（想买什么、预算、已加购或下单的情况）。
只输出 JSON：{"summary":"..."}。不要编造对话里没有的信息，不要包含手机号、地址等个人信息。`

// updateSummary 在回答完成后更新会话摘要。只覆盖空摘要或自动生成的摘要（以“用户咨询：”开头）；用户手动改过的保留。
// 返回写入的摘要和方式（rule / model），没有写入时为空。
func (r *RuleRunner) updateSummary(ctx context.Context, st ModelSettings, in Input, plan Plan, h history, answer string) (string, string) {
	if h.Summary != "" && !strings.HasPrefix(h.Summary, summaryPrefix) {
		return "", ""
	}
	summary, mode := ruleSummary(h.Turns, plan, in.Content), "rule"
	if st.SummaryEnabled && r.deps.LLM != nil {
		if s, err := r.modelSummary(ctx, st, h, in.Content, answer); err == nil && s != "" {
			summary, mode = summaryPrefix+s, "model"
		}
	}
	summary = RedactPII(summary)
	if summary == h.Summary {
		return "", ""
	}
	if _, err := r.deps.Store.UpdateChatSession(ctx, in.AccountID, in.SessionID, func(cs *domain.ChatSession) error {
		if cs.Summary != "" && !strings.HasPrefix(cs.Summary, summaryPrefix) {
			return errUserSummary // 期间被用户改过
		}
		cs.Summary = summary
		return nil
	}); err != nil {
		return "", ""
	}
	return summary, mode
}

type summaryError string

func (e summaryError) Error() string { return string(e) }

const errUserSummary = summaryError("summary edited by user")

func (r *RuleRunner) modelSummary(ctx context.Context, st ModelSettings, h history, content, answer string) (string, error) {
	var b strings.Builder
	start := len(h.Turns) - 4
	if start < 0 {
		start = 0
	}
	for _, t := range h.Turns[start:] {
		b.WriteString("用户：" + truncate(t.UserText, 120) + "\n回答：" + truncate(t.Answer, 160) + "\n")
	}
	b.WriteString("用户：" + truncate(content, 120) + "\n回答：" + truncate(answer, 160))
	cctx, cancel := context.WithTimeout(ctx, st.Timeout)
	defer cancel()
	resp, err := r.deps.LLM.Complete(cctx, llm.Request{Model: st.PlannerModel, JSON: true, Temperature: 0, MaxTokens: 200,
		Messages: []llm.Message{{Role: "system", Content: summaryPromptText}, {Role: "user", Content: b.String()}}})
	if err != nil {
		return "", err
	}
	obj, err := parseJSONObject(resp.Content)
	if err != nil {
		return "", err
	}
	return truncate(strings.TrimSpace(argString(obj, "summary")), 200), nil
}
