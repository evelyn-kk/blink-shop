package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/risk"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// testNow 落在演示促销和券的有效期内。
var testNow = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

// env 是测试环境：写入开发种子的内存 Store、业务层、工具注册表和规则运行器。
type env struct {
	mem    *memstore.Store
	shop   *shop.Service
	reg    *Registry
	runner *RuleRunner
	logs   *bytes.Buffer
	words  []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	mem := memstore.New()
	if _, err := mem.ApplySeed(context.Background(), storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	e := &env{mem: mem, logs: &bytes.Buffer{}, words: []string{"违法", "违禁", "假货", "绕过风控"}}
	now := func() time.Time { return testNow }
	e.shop = shop.New(mem, now, 0)
	logger := logging.New(e.logs, slog.LevelDebug)
	deps := Deps{Store: mem, Shop: e.shop, Retriever: rag.NewRetriever(mem, nil, logger),
		Risk: risk.WordList{Words: func(context.Context) []string { return e.words }}, Logger: logger, Now: now}
	e.runner = NewRuleRunner(deps)
	e.reg = e.runner.Registry()
	return e
}

// tc 构造工具上下文；evidence 是本轮可信的商品 ID。
func (e *env) tc(account string, intent Intent, evidence ...string) *ToolContext {
	tc := &ToolContext{AccountID: account, SessionID: "s_test", RunID: "run_test_" + account, Intent: intent, Evidence: map[string]bool{}}
	for _, id := range evidence {
		tc.Evidence[id] = true
	}
	return tc
}

func (e *env) call(t *testing.T, tc *ToolContext, tool string, args map[string]any) Observation {
	t.Helper()
	return e.reg.Call(context.Background(), tc, tool, args)
}

// mustOK 断言调用成功并返回数据。
func (e *env) mustOK(t *testing.T, tc *ToolContext, tool string, args map[string]any) any {
	t.Helper()
	obs := e.call(t, tc, tool, args)
	if !obs.OK {
		t.Fatalf("%s %v: %s %s (%s)", tool, args, obs.Code, obs.Message, obs.Field)
	}
	return obs.Data
}

// mustFail 断言调用被拒绝且错误码一致。
func (e *env) mustFail(t *testing.T, tc *ToolContext, tool string, args map[string]any, code string) Observation {
	t.Helper()
	obs := e.call(t, tc, tool, args)
	if obs.OK || obs.Code != code {
		t.Fatalf("%s %v: got ok=%v code=%q msg=%q, want %q", tool, args, obs.OK, obs.Code, obs.Message, code)
	}
	return obs
}

// newSession 创建一个会话并返回 ID。
func (e *env) newSession(t *testing.T, account string) string {
	t.Helper()
	cs, err := e.mem.CreateChatSession(context.Background(), domain.ChatSession{AccountID: account, Title: "测试"})
	if err != nil {
		t.Fatal(err)
	}
	return cs.SessionID
}

// recorder 实现 Output，记录全部事件。
type recorder struct {
	mu        sync.Mutex
	steps     []Step
	text      strings.Builder
	deltas    []string
	blocks    []map[string]any
	followups []string
	traces    []trace
}

type trace struct {
	Stage, Type, Status string
	Meta                map[string]any
}

func (r *recorder) Thinking(s Step) { r.mu.Lock(); r.steps = append(r.steps, s); r.mu.Unlock() }
func (r *recorder) Text(d string) {
	r.mu.Lock()
	r.text.WriteString(d)
	r.deltas = append(r.deltas, d)
	r.mu.Unlock()
}
func (r *recorder) Block(b map[string]any) {
	r.mu.Lock()
	r.blocks = append(r.blocks, b)
	r.mu.Unlock()
}
func (r *recorder) Followups(q []string) { r.mu.Lock(); r.followups = q; r.mu.Unlock() }
func (r *recorder) Trace(stage, typ, status string, _ time.Duration, meta map[string]any) {
	r.mu.Lock()
	r.traces = append(r.traces, trace{stage, typ, status, meta})
	r.mu.Unlock()
}

func (r *recorder) blockTypes() []string {
	var out []string
	for _, b := range r.blocks {
		out = append(out, b["type"].(string))
	}
	return out
}

func (r *recorder) block(kind string) map[string]any {
	for _, b := range r.blocks {
		if b["type"] == kind {
			return b
		}
	}
	return nil
}

// toolCalls 返回轨迹里按顺序的工具名（stage=tool）。
func (r *recorder) toolCalls() []string {
	var out []string
	for _, t := range r.traces {
		if t.Stage == "tool" {
			out = append(out, t.Type)
		}
	}
	return out
}

func (r *recorder) traceOf(stage string) *trace {
	for i := range r.traces {
		if r.traces[i].Stage == stage {
			return &r.traces[i]
		}
	}
	return nil
}

// run 以 account 在 session 里发一条消息，跑完整个运行。
func (e *env) run(t *testing.T, account, session, content string, atts ...domain.Attachment) *recorder {
	t.Helper()
	ctx := context.Background()
	started, err := e.mem.StartAgentRun(ctx, domain.UserMessage{AccountID: account, SessionID: session, ClientMessageID: domain.NewID("cm"), Content: content,
		Attachments: atts}, func(*domain.ChatSession) {})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	if err := e.runner.Run(ctx, Input{AccountID: account, SessionID: session, RunID: started.Run.RunID, TraceID: started.Run.TraceID, Content: content, Attachments: atts}, rec); err != nil {
		t.Fatalf("run %q: %v", content, err)
	}
	// 像 httpapi 一样把结果写回运行，后续轮次才能看到上一张商品卡。
	if _, err := e.mem.UpdateAgentRun(ctx, account, started.Run.RunID, func(r *domain.AgentRun) error {
		r.Status, r.Content, r.Blocks, r.Followups = domain.RunCompleted, rec.text.String(), rec.blocks, rec.followups
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rec
}

// cart 读取账户当前购物车。
func (e *env) cart(t *testing.T, account string) shop.Cart {
	t.Helper()
	c, err := e.shop.Cart(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) orders(t *testing.T, account string, status domain.OrderStatus) []shop.Order {
	t.Helper()
	items, _, err := e.shop.ListOrders(context.Background(), store.OrderQuery{AccountID: account, Status: status, Page: store.Page{Page: 1, PageSize: 50}})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func productIDs(b map[string]any) []string {
	var out []string
	items, _ := b["products"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		out = append(out, m["product_id"].(string))
	}
	return out
}

var _ = seed.UserID
