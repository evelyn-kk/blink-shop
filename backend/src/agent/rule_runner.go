package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// RuleRunner 是导购运行器：风险词检查 → 规划意图 → 按意图白名单调用工具 → 只用工具返回的数据组织回答。
// 没有模型时规划和回答全部是规则；配置了模型（Deps.LLM）且开关打开时，用小模型规划、大模型做工具循环和最终回答，
// 任何一步失败都回到规则。工具层和安全边界（白名单、参数校验、商品来源）与模型无关。
type RuleRunner struct {
	deps Deps
	reg  *Registry
}

// NewRuleRunner 创建规则运行器。
func NewRuleRunner(deps Deps) *RuleRunner {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &RuleRunner{deps: deps, reg: NewRegistry(deps)}
}

// Registry 返回工具注册表（测试和文档导出用）。
func (r *RuleRunner) Registry() *Registry { return r.reg }

// Run 执行一次运行。任何工具失败都转成给用户的说明，不会让运行失败；ctx 取消时尽快返回。
func (r *RuleRunner) Run(ctx context.Context, in Input, out Output) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	start := r.deps.Now()
	out.Thinking(Step{ID: "understand", Title: "理解你的问题", Status: StepRunning})

	// 风险检查在规划和任何工具之前：命中就不再做别的，只给安全说明并留下轨迹与审计。
	if r.deps.Risk != nil {
		if m, hit := r.deps.Risk.Check(ctx, in.Content); hit {
			out.Trace("risk", "check", "blocked", r.deps.Now().Sub(start), map[string]any{"word": m.Word})
			r.deps.Logger.InfoContext(ctx, "audit", "action", "agent.risk_blocked", "account_id", in.AccountID, "run_id", in.RunID,
				"session_id", in.SessionID, "word", m.Word, "content_runes", utf8.RuneCountInString(in.Content))
			out.Thinking(Step{ID: "understand", Title: "理解你的问题", Status: StepDone})
			s := newSession(r, in, out, Plan{Intent: IntentNonGuide, Rule: "risk_blocked"})
			s.say("这个问题涉及平台不支持的内容，我不能继续处理。我可以帮你挑选商品、对比参数、查看购物车和订单，比如“推荐一款通勤用的降噪耳机”。")
			s.followups("推荐一款通勤降噪耳机", "看看我的购物车", "我的订单")
			s.finish(start)
			return nil
		}
		out.Trace("risk", "check", "ok", r.deps.Now().Sub(start), nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	st := r.modelsFor(ctx)
	hist := loadHistory(ctx, r.deps.Store, in.AccountID, in.SessionID, in.RunID)
	plan := r.plan(ctx, in, st, out, hist)
	out.Thinking(Step{ID: "understand", Title: "理解你的问题", Status: StepDone})
	out.Thinking(Step{ID: "plan", Title: "判断需求：" + IntentTitle(plan.Intent), Status: StepDone})
	out.Trace("planner", "rule", "ok", r.deps.Now().Sub(start), planMeta(plan))

	s := newSession(r, in, out, plan)
	s.history = hist
	for id := range s.history.Evidence {
		s.tc.Evidence[id] = true
	}
	if st.AgentEnabled && usesModelAnswer(plan) && s.answerWithModel(ctx, st) {
		s.finish(start)
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.handle(ctx); err != nil {
		return err
	}
	s.finish(start)
	return nil
}

func hasImage(atts []domain.Attachment) bool {
	for _, a := range atts {
		if strings.HasPrefix(a.MimeType, "image/") {
			return true
		}
	}
	return false
}

func planMeta(p Plan) map[string]any {
	m := toJSONMap(p)
	delete(m, "content") // 评价正文不进轨迹
	return m
}

// session 是一次运行内的状态：输出、规划、工具上下文和统计。
type session struct {
	r       *RuleRunner
	in      Input
	out     Output
	plan    Plan
	tc      *ToolContext
	history history
	runes   int
	blocks  int
	steps   int
	asked   bool // 已经向用户提问（需要补充信息），追问按提问场景生成
	done    bool // 已经生成过追问
}

func newSession(r *RuleRunner, in Input, out Output, plan Plan) *session {
	return &session{r: r, in: in, out: out, plan: plan,
		tc: &ToolContext{AccountID: in.AccountID, SessionID: in.SessionID, RunID: in.RunID, Intent: plan.Intent, Evidence: map[string]bool{}}}
}

// say 按句子流式输出一段话。
func (s *session) say(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.runes += utf8.RuneCountInString(text)
	var buf strings.Builder
	for _, r := range text {
		buf.WriteRune(r)
		if r == '。' || r == '！' || r == '？' || r == '\n' || r == '；' {
			s.out.Text(buf.String())
			buf.Reset()
		}
	}
	if buf.Len() > 0 {
		s.out.Text(buf.String())
	}
}

// sayf 格式化后输出。
func (s *session) sayf(format string, args ...any) { s.say(fmt.Sprintf(format, args...)) }

func (s *session) block(b map[string]any) {
	s.blocks++
	s.out.Block(b)
}

func (s *session) followups(qs ...string) {
	if s.done || len(qs) == 0 {
		return
	}
	s.done = true
	out := make([]string, 0, 3)
	for _, q := range qs {
		if q = strings.TrimSpace(q); q != "" && !containsStr(out, q) && len(out) < 3 {
			out = append(out, q)
		}
	}
	s.out.Followups(out)
}

// ask 向用户提问（需要补充信息才能继续），不做写操作。
func (s *session) ask(text string) {
	s.asked = true
	s.say(text)
}

func (s *session) finish(start time.Time) {
	if !s.done {
		s.followups(defaultFollowups(s.plan.Intent)...)
	}
	s.out.Trace("answer", "rule", "ok", s.r.deps.Now().Sub(start), map[string]any{"intent": string(s.plan.Intent), "action": s.plan.Action,
		"content_runes": s.runes, "blocks": s.blocks, "tool_calls": s.steps, "asked": s.asked})
}

// call 调用一个工具：先推一个思考步骤，再执行，记录轨迹；返回观察结果（OK=false 时 Message 可直接展示）。
func (s *session) call(ctx context.Context, title, tool string, args map[string]any) Observation {
	s.steps++
	stepID := fmt.Sprintf("tool-%d", s.steps)
	s.out.Thinking(Step{ID: stepID, Title: title, Status: StepRunning})
	obs := s.r.reg.Call(ctx, s.tc, tool, args)
	s.out.Thinking(Step{ID: stepID, Title: title, Status: StepDone})
	status := "ok"
	meta := map[string]any{"args": s.r.reg.Sanitize(tool, args)}
	if !obs.OK {
		status = "error"
		meta["code"], meta["message"] = obs.Code, obs.Message
		if obs.Field != "" {
			meta["field"] = obs.Field
		}
	} else {
		meta["result"] = summarize(obs.Data)
	}
	s.out.Trace("tool", tool, status, obs.Duration, meta)
	return obs
}

// summarize 给轨迹留一个简短的结果摘要（数量、ID），不写完整数据。
func summarize(data any) map[string]any {
	out := map[string]any{}
	switch d := data.(type) {
	case ProductSearchResult:
		out["count"], out["total"], out["relevance"] = len(d.Products), d.Total, d.Relevance
		ids := make([]string, 0, len(d.Products))
		for _, p := range d.Products {
			ids = append(ids, p.ProductID)
		}
		out["product_ids"] = ids
	case KnowledgeResult:
		ids := make([]string, 0, len(d.Citations))
		for _, c := range d.Citations {
			ids = append(ids, c.ChunkID)
		}
		out["chunk_ids"], out["mode"] = ids, d.Mode
	case ReviewsResult:
		out["count"], out["total"] = len(d.Reviews), d.Total
	default:
		m := toJSONMap(data)
		for _, k := range []string{"order_id", "status", "cart_item_id", "user_coupon_id", "review_id", "request_id", "total", "pay_amount"} {
			if v, ok := m[k]; ok {
				out[k] = v
			}
		}
		for _, k := range []string{"orders", "coupons", "items", "promotions"} {
			if v, ok := m[k].([]any); ok {
				out[k] = len(v)
			}
		}
	}
	return out
}

// decode 把工具返回的数据转成具体类型（工具返回的是具体结构或 map，统一经 JSON 转换）。
func decode[T any](data any) T {
	var v T
	if t, ok := data.(T); ok {
		return t
	}
	b, _ := json.Marshal(data)
	_ = json.Unmarshal(b, &v)
	return v
}

func defaultFollowups(intent Intent) []string {
	switch intent {
	case IntentCart, IntentCheckout:
		return []string{"看看我的购物车", "有什么优惠券", "去结算"}
	case IntentOrder:
		return []string{"我的订单", "待支付的订单", "推荐一款通勤降噪耳机"}
	case IntentCoupon:
		return []string{"我的优惠券", "看看购物车能优惠多少", "推荐一款手机"}
	case IntentReview:
		return []string{"Blink Nova 12 的评价怎么样", "推荐一款手机", "我的订单"}
	case IntentKnowledge:
		return []string{"七天无理由怎么退", "保修多久", "推荐一款手机"}
	default:
		return []string{"推荐一款通勤降噪耳机", "3000 以内的手机", "看看我的购物车"}
	}
}
