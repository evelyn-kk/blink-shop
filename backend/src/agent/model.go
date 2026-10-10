package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
)

// 模型接入：小模型规划（失败回退规则）和大模型工具循环（失败、超轮次、协议错误回退规则处理器）。
// 工具白名单、参数校验、商品来源校验都在 Registry.Call 里，模型只能在白名单内选工具；最终回答经 FilterFinal 清理。

const (
	maxParseFailures  = 2
	plannerMaxTokens  = 400
	reactMaxTokens    = 1200
	plannerTemp       = 0.0
	reactTemp         = 0.1
	maxFollowups      = 3
	maxFollowupRunes  = 40
	maxObservationLen = 6000
)

// modelsFor 返回本次运行的设置；没有 LLM 时模型全部关闭。
func (r *RuleRunner) modelsFor(ctx context.Context) ModelSettings {
	st := DefaultModelSettings()
	if r.deps.Settings != nil {
		st = r.deps.Settings(ctx)
	}
	st = st.normalized()
	if r.deps.LLM == nil {
		st.PlannerEnabled, st.AgentEnabled = false, false
	}
	return st
}

// plannerSchema 校验小模型的规划输出。
var plannerSchema = object([]string{"intent"}, map[string]*Schema{
	"intent":       enum("意图", string(IntentGuide), string(IntentProductSearch), string(IntentProductCompare), string(IntentKnowledge), string(IntentCart), string(IntentCheckout), string(IntentOrder), string(IntentCoupon), string(IntentReview), string(IntentNavigation), string(IntentImageSearch), string(IntentNonGuide)),
	"action":       strLen("动作", 0, 32),
	"query":        strLen("检索词", 0, 200),
	"budget":       number("预算", 0),
	"min_price":    number("价格下限", 0),
	"brands":       strList("品牌", 5, 30),
	"exclude":      strList("排除词", 10, 30),
	"names":        strList("对比商品名", 6, 60),
	"ordinal":      integer("序号", -1, 50, nil),
	"quantity":     integer("数量", 1, 99, nil),
	"order_status": enum("订单状态", "pending_payment", "paid", "shipped", "completed", "cancelled"),
	"order_ref":    strLen("订单号", 0, 64),
	"rating":       integer("评分", 1, 5, nil),
	"content":      strLen("评价正文", 0, 500),
	"target":       enum("导航目标", TargetProducts, TargetProductDetail, TargetCart, TargetOrders, TargetOrderDetail, TargetCoupons, TargetSessions, TargetSettings),
	"reasoning":    strLen("说明", 0, 300),
})

// plan 先用规则得到基线，再（可选）用小模型覆盖意图和槽位。模型失败、超时、输出不合法都回到规则结果，并把原因写进轨迹。
func (r *RuleRunner) plan(ctx context.Context, in Input, st ModelSettings, out Output, history history) Plan {
	rule := Classify(in.Content, hasImage(in.Attachments))
	if !st.PlannerEnabled {
		return rule
	}
	start := r.deps.Now()
	meta := map[string]any{"model": st.PlannerModel, "prompt": PromptPlanner, "prompt_version": PromptVersion}
	modelPlan, usage, err := r.modelPlan(ctx, in, st, history)
	meta["prompt_tokens"], meta["completion_tokens"] = usage.PromptTokens, usage.CompletionTokens
	if err != nil {
		meta["error"] = errorClass(err)
		meta["fallback"] = "rule"
		out.Trace("planner", "model", "error", r.deps.Now().Sub(start), meta)
		r.deps.Logger.WarnContext(ctx, "model planner failed, using rules", "run_id", in.RunID, "error", err)
		return rule
	}
	merged := mergePlans(rule, modelPlan)
	meta["intent"], meta["action"], meta["rule_intent"] = string(merged.Intent), merged.Action, string(rule.Intent)
	out.Trace("planner", "model", "ok", r.deps.Now().Sub(start), meta)
	return merged
}

func (r *RuleRunner) modelPlan(ctx context.Context, in Input, st ModelSettings, history history) (Plan, llm.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, st.Timeout)
	defer cancel()
	user := in.Content
	if mc := history.promptContext(); mc != "" {
		user += "\n\n（相关的历史对话，供理解指代和追问：\n" + mc + "）"
	}
	if n := len(history.Cards); n > 0 {
		user += fmt.Sprintf("\n\n（上下文：上一轮给用户看过 %d 件商品：%s）", n, cardNames(history.Cards))
	}
	if hasImage(in.Attachments) {
		user += "\n\n（用户附带了图片）"
	}
	resp, err := r.deps.LLM.Complete(ctx, llm.Request{Model: st.PlannerModel, Temperature: plannerTemp, MaxTokens: plannerMaxTokens, JSON: true,
		Messages: []llm.Message{{Role: "system", Content: plannerSystemPrompt}, {Role: "user", Content: user}}})
	if err != nil {
		return Plan{}, llm.Usage{}, err
	}
	obj, err := parseJSONObject(resp.Content)
	if err != nil {
		return Plan{}, resp.Usage, err
	}
	if err := plannerSchema.Validate(obj); err != nil {
		return Plan{}, resp.Usage, fmt.Errorf("planner output invalid: %w", err)
	}
	p := Plan{Intent: Intent(argString(obj, "intent")), Action: argString(obj, "action"), Query: argString(obj, "query"), Rule: "model",
		Exclude: argStrings(obj, "exclude"), Names: argStrings(obj, "names"), OrderStatus: domain.OrderStatus(argString(obj, "order_status")),
		OrderRef: argString(obj, "order_ref"), Content: argString(obj, "content"), Target: argString(obj, "target")}
	if f, ok := argFloat(obj, "budget"); ok && f > 0 {
		p.Budget = domain.Money(f*100 + 0.5)
	}
	if f, ok := argFloat(obj, "min_price"); ok && f > 0 {
		p.MinPrice = domain.Money(f*100 + 0.5)
	}
	p.Brands = argStrings(obj, "brands")
	p.Ordinal = argInt(obj, "ordinal", 0)
	p.Quantity = argInt(obj, "quantity", 0)
	p.Rating = argInt(obj, "rating", 0)
	return p, resp.Usage, nil
}

// mergePlans 以模型的意图和动作为准；槽位模型给了用模型的，没给用规则抽的；指代等纯规则特征沿用规则。
func mergePlans(rule, model Plan) Plan {
	out := model
	out.Rule = "model+rule"
	out.Reference = rule.Reference
	if out.Action == "" && out.Intent == rule.Intent {
		out.Action = rule.Action
	}
	if out.Query == "" {
		out.Query = rule.Query
	}
	if out.Budget == 0 {
		out.Budget = rule.Budget
	}
	if out.MinPrice == 0 {
		out.MinPrice = rule.MinPrice
	}
	if len(out.Exclude) == 0 {
		out.Exclude = rule.Exclude
	}
	if len(out.Names) == 0 {
		out.Names = rule.Names
	}
	if out.Ordinal == 0 {
		out.Ordinal = rule.Ordinal
	}
	if out.Quantity == 0 {
		out.Quantity = rule.Quantity
	}
	if out.OrderStatus == "" {
		out.OrderStatus = rule.OrderStatus
	}
	if out.OrderRef == "" {
		out.OrderRef = rule.OrderRef
	}
	if out.Rating == 0 {
		out.Rating = rule.Rating
	}
	if out.Content == "" {
		out.Content = rule.Content
	}
	if out.Target == "" {
		out.Target = rule.Target
	}
	if out.Intent == IntentGuide && out.Action == "" {
		out.Action = "help"
	}
	return out
}

// usesModelAnswer 判断意图是否走大模型工具循环：导航、非导购、图搜、打招呼只有固定回答，不需要模型。
func usesModelAnswer(p Plan) bool {
	switch p.Intent {
	case IntentNavigation, IntentNonGuide, IntentImageSearch:
		return false
	case IntentGuide:
		return p.Action != "greeting"
	}
	return true
}

// reactAction 是模型每一步的动作。
type reactAction struct {
	Type      string
	Name      string
	Args      map[string]any
	Text      string
	Followups []string
}

func parseAction(raw string) (reactAction, error) {
	obj, err := parseJSONObject(raw)
	if err != nil {
		return reactAction{}, err
	}
	a := reactAction{Type: argString(obj, "type"), Name: argString(obj, "name"), Text: argString(obj, "text")}
	switch a.Type {
	case "tool":
		if a.Name == "" {
			return a, errors.New("tool action without name")
		}
		if args, ok := obj["args"].(map[string]any); ok {
			a.Args = args
		} else if obj["args"] != nil {
			return a, errors.New("args must be an object")
		}
	case "final":
		if a.Text == "" {
			return a, errors.New("final action without text")
		}
		for _, q := range argStrings(obj, "followups") {
			if q = strings.TrimSpace(q); q != "" && utf8.RuneCountInString(q) <= maxFollowupRunes && !containsStr(a.Followups, q) && len(a.Followups) < maxFollowups {
				a.Followups = append(a.Followups, q)
			}
		}
	default:
		return a, fmt.Errorf("unknown action type %q", a.Type)
	}
	return a, nil
}

// parseJSONObject 从模型输出里取出第一个 JSON 对象（容忍前后多余文字和围栏）。
func parseJSONObject(raw string) (map[string]any, error) {
	s := strings.TrimSpace(fencePattern.ReplaceAllString(raw, "$1"))
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, errors.New("no JSON object in output")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s[start:end+1]), &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return obj, nil
}

// answerWithModel 用大模型做工具循环并生成最终回答。返回 false 表示没有完成（已写回退轨迹），由规则处理器接管。
func (s *session) answerWithModel(ctx context.Context, st ModelSettings) bool {
	r := s.r
	pending := newPendingBlocks()
	allowed := r.reg.allowedTools(ctx, s.plan.Intent)
	for _, id := range ParseProductIDs(s.in.Content) {
		s.tc.Evidence[id] = true // 用户明确写出的商品 ID
	}
	system := strings.Replace(reactSystemPrompt, "{{TOOLS}}", renderTools(allowed), 1)
	messages := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: s.reactUserPrompt()}}
	parseFailures := 0
	var lastUsage llm.Usage
	fallback := func(reason string, rounds int) bool {
		s.out.Trace("react", "fallback", "ok", 0, map[string]any{"reason": reason, "rounds": rounds, "model": st.AgentModel})
		r.deps.Logger.WarnContext(ctx, "model answer fell back to rules", "run_id", s.in.RunID, "reason", reason, "rounds", rounds)
		s.steps = 0 // 规则处理器的步骤从头编号
		return false
	}
	for round := 1; round <= st.MaxToolRounds; round++ {
		if ctx.Err() != nil {
			return false
		}
		stepID := fmt.Sprintf("model-%d", round)
		s.out.Thinking(Step{ID: stepID, Title: "思考下一步", Status: StepRunning})
		start := r.deps.Now()
		callCtx, cancel := context.WithTimeout(ctx, st.Timeout)
		resp, err := r.deps.LLM.Complete(callCtx, llm.Request{Model: st.AgentModel, Messages: messages, Temperature: reactTemp, MaxTokens: reactMaxTokens, JSON: true})
		cancel()
		s.out.Thinking(Step{ID: stepID, Title: "思考下一步", Status: StepDone})
		meta := map[string]any{"model": st.AgentModel, "prompt": PromptReact, "prompt_version": PromptVersion, "round": round,
			"prompt_chars": promptChars(messages), "prompt_hash": promptHash(messages), "prompt_tokens": resp.Usage.PromptTokens, "completion_tokens": resp.Usage.CompletionTokens}
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			meta["error"] = errorClass(err)
			s.out.Trace("react", fmt.Sprintf("step.%d", round), "error", r.deps.Now().Sub(start), meta)
			return fallback("model_error", round)
		}
		lastUsage = resp.Usage
		action, perr := parseAction(resp.Content)
		meta["raw_runes"] = utf8.RuneCountInString(resp.Content)
		if perr != nil {
			parseFailures++
			meta["action"], meta["parse_error"] = "invalid", perr.Error()
			s.out.Trace("react", fmt.Sprintf("step.%d", round), "error", r.deps.Now().Sub(start), meta)
			if parseFailures >= maxParseFailures {
				return fallback("protocol_error", round)
			}
			messages = append(messages, llm.Message{Role: "assistant", Content: truncate(resp.Content, 2000)}, llm.Message{Role: "user", Content: correctionPrompt})
			continue
		}
		parseFailures = 0
		meta["action"] = action.Type
		if action.Type == "tool" {
			meta["tool"] = action.Name
			s.out.Trace("react", fmt.Sprintf("step.%d", round), "ok", r.deps.Now().Sub(start), meta)
			obs := s.call(ctx, toolTitle(action.Name), action.Name, action.Args)
			if obs.OK {
				for _, id := range pending.absorb(action.Name, obs.Data) {
					s.tc.Evidence[id] = true
				}
			}
			messages = append(messages, llm.Message{Role: "assistant", Content: truncate(resp.Content, 2000)},
				llm.Message{Role: "user", Content: truncate(observationForModel(obs), maxObservationLen)})
			continue
		}
		// final
		s.out.Trace("react", fmt.Sprintf("step.%d", round), "ok", r.deps.Now().Sub(start), meta)
		text, removed := FilterFinal(action.Text, s.tc.Evidence)
		if strings.TrimSpace(text) == "" {
			return fallback("empty_final", round)
		}
		s.out.Thinking(Step{ID: "answer", Title: "整理回答", Status: StepDone})
		s.say(text)
		pending.emit(s)
		if len(action.Followups) > 0 {
			s.followupSource = "model"
			s.followups(action.Followups...)
		}
		s.out.Trace("answer", "model", "ok", 0, map[string]any{"model": st.AgentModel, "rounds": round, "removed_product_ids": removed,
			"prompt_tokens": lastUsage.PromptTokens, "completion_tokens": lastUsage.CompletionTokens})
		return true
	}
	return fallback("max_rounds", st.MaxToolRounds)
}

// reactUserPrompt 把本轮内容、规划结果和上下文里可见的商品交给模型。
func (s *session) reactUserPrompt() string {
	var b strings.Builder
	b.WriteString("用户说：")
	b.WriteString(s.in.Content)
	planJSON, _ := json.Marshal(planMeta(s.plan))
	b.WriteString("\n\n规划结果（供参考）：")
	b.Write(planJSON)
	if mc := s.history.promptContext(); mc != "" {
		b.WriteString("\n\n相关的历史对话：\n")
		b.WriteString(mc)
	}
	if len(s.history.Cards) > 0 {
		b.WriteString("\n\n上一轮给用户看过的商品（按顺序，可直接加购）：")
		for i, c := range s.history.Cards {
			fmt.Fprintf(&b, "\n%d. %s（product_id=%s，%s）", i+1, c.Name, c.ProductID, yuan(c.Price))
		}
	}
	if hasImage(s.in.Attachments) {
		b.WriteString("\n\n（用户附带了图片，但图片搜索还没有开放；按文字回答并说明。）")
	}
	return b.String()
}

// allowedTools 返回意图当前允许的工具对象（按名字排序）。
func (r *Registry) allowedTools(ctx context.Context, intent Intent) []*Tool {
	var out []*Tool
	for _, name := range r.Allowed(ctx, intent) {
		if t, ok := r.tools[name]; ok {
			out = append(out, t)
		}
	}
	return out
}

func toolTitle(name string) string {
	switch name {
	case ToolSearchProducts:
		return "搜索商品"
	case ToolSearchKnowledge:
		return "查找资料"
	case ToolGetCart:
		return "查看购物车"
	case ToolAddCartItem:
		return "加入购物车"
	case ToolUpdateCartItem:
		return "修改购物车"
	case ToolDeleteCartItem:
		return "删除购物车项"
	case ToolPreviewDiscount:
		return "试算优惠"
	case ToolCheckout:
		return "提交订单"
	case ToolListOrders, ToolGetOrder:
		return "查询订单"
	case ToolPayOrder:
		return "支付订单"
	case ToolCancelOrder:
		return "取消订单"
	case ToolConfirmReceipt:
		return "确认收货"
	case ToolListCoupons, ToolListUserCoupons:
		return "查看优惠券"
	case ToolClaimCoupon:
		return "领取优惠券"
	case ToolListPromotions:
		return "查看促销活动"
	case ToolListReviews:
		return "查看评价"
	case ToolCreateReview:
		return "发表评价"
	}
	return "调用 " + name
}

func cardNames(cards []ProductCard) string {
	names := make([]string, 0, len(cards))
	for _, c := range cards {
		names = append(names, c.Name)
	}
	return strings.Join(names, "、")
}

func truncate(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes]) + "…"
}

func promptChars(messages []llm.Message) int {
	n := 0
	for _, m := range messages {
		n += utf8.RuneCountInString(m.Content)
	}
	return n
}

// promptHash 只记提示的摘要，不记原文（原文可能含用户内容）。
func promptHash(messages []llm.Message) string {
	h := sha256.New()
	for _, m := range messages {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// errorClass 把模型错误归类（不记原始错误文本，避免带出密钥或地址）。
func errorClass(err error) string {
	var re *llm.RequestError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.As(err, &re):
		return fmt.Sprintf("http_%d", re.Status)
	case strings.Contains(err.Error(), "invalid"), strings.Contains(err.Error(), "JSON"):
		return "invalid_output"
	}
	return "error"
}
