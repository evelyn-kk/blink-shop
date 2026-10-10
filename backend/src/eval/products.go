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
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/risk"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
)

// ProductCase 是一条商品搜索用例，经导购运行器端到端执行（每条用例一个新会话）。History 是先发的几句话（多轮用例）。
//   - expect_top：推荐卡（“为你找到的商品”）的第一件；expect_ids：都必须在推荐卡里；absent：不能出现在推荐卡里；
//   - expect_none：不能给出推荐卡（没找到，或只给“相近 / 不太合适”的参考）；expect_text：回答里必须包含的文字。
type ProductCase struct {
	ID         string   `json:"id"`
	History    []string `json:"history"`
	Query      string   `json:"query"`
	ExpectTop  string   `json:"expect_top"`
	ExpectIDs  []string `json:"expect_ids"`
	Absent     []string `json:"absent"`
	ExpectNone bool     `json:"expect_none"`
	ExpectText []string `json:"expect_text"`
	Tags       []string `json:"tags"`
}

// ProductOptions 是商品搜索评测的输入。
type ProductOptions struct {
	Cases        string
	ProductIndex rag.ProductIndex
	Version      string
	Config       map[string]string
	Now          func() time.Time
}

// RunProducts 跑商品搜索用例。指标：通过率、top1 准确率、无结果正确率（不推荐不相关商品）、负向约束正确率（排除词 / 价格 / 冲突）。
func RunProducts(ctx context.Context, o ProductOptions) (Report, error) {
	start := time.Now()
	rep := Report{Suite: "products", Version: o.Version, StartedAt: start.UTC(), Config: map[string]string{"recall": "keyword", "rerank": "rule"}, Metrics: map[string]float64{}}
	for k, v := range o.Config {
		rep.Config[k] = v
	}
	if o.ProductIndex != nil {
		rep.Config["recall"] = "keyword+vector"
	}
	cases, err := readJSONL[ProductCase](o.Cases)
	if err != nil {
		return rep, err
	}
	st, err := newSeededStore(ctx)
	if err != nil {
		return rep, err
	}
	now := o.Now
	if now == nil {
		// 落在演示促销有效期内的固定时间，结果可复现
		fixed := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
		now = func() time.Time { return fixed }
	}
	runner := agent.NewRuleRunner(agent.Deps{Store: st, Shop: shop.New(st, now, 0), Retriever: rag.NewRetriever(st, nil, nil),
		Risk: risk.Static("违法", "违禁", "假货", "绕过风控"), ProductIndex: o.ProductIndex, Now: now})
	topCases, topOK, noneCases, noneOK, negCases, negOK := 0, 0, 0, 0, 0, 0
	for i, c := range cases {
		account := seed.User2ID
		cs, err := st.CreateChatSession(ctx, domain.ChatSession{AccountID: account, Title: "评测"})
		if err != nil {
			return rep, err
		}
		var last *capture
		for j, msg := range append(append([]string{}, c.History...), c.Query) {
			last, err = runOnce(ctx, st, runner, account, cs.SessionID, fmt.Sprintf("eval-%d-%d", i, j), msg)
			if err != nil {
				return rep, fmt.Errorf("%s: %w", c.ID, err)
			}
		}
		rec := last.recommended()
		var problems []string
		if c.ExpectTop != "" {
			topCases++
			if len(rec) > 0 && rec[0] == c.ExpectTop {
				topOK++
			} else {
				problems = append(problems, "第一件应是 "+c.ExpectTop)
			}
		}
		for _, id := range c.ExpectIDs {
			if !slices.Contains(rec, id) {
				problems = append(problems, id+" 应在推荐里")
			}
		}
		negative := c.ExpectNone || len(c.Absent) > 0
		if negative {
			negCases++
		}
		negFail := false
		for _, id := range c.Absent {
			if slices.Contains(rec, id) {
				problems = append(problems, id+" 不应被推荐")
				negFail = true
			}
		}
		if c.ExpectNone {
			noneCases++
			if len(rec) == 0 {
				noneOK++
			} else {
				problems = append(problems, "不应给出推荐")
				negFail = true
			}
		}
		if negative && !negFail {
			negOK++
		}
		for _, want := range c.ExpectText {
			if !strings.Contains(last.text.String(), want) {
				problems = append(problems, "回答应包含“"+want+"”")
			}
		}
		rep.Total++
		if len(problems) == 0 {
			rep.Passed++
		} else {
			rep.Failures = append(rep.Failures, Failure{ID: c.ID, Query: c.Query, Problems: problems, Got: rec})
		}
	}
	if topCases > 0 {
		rep.Metrics["top1_accuracy"] = round(float64(topOK) / float64(topCases))
	}
	if noneCases > 0 {
		rep.Metrics["no_recommendation_accuracy"] = round(float64(noneOK) / float64(noneCases))
	}
	if negCases > 0 {
		rep.Metrics["negative_constraint_accuracy"] = round(float64(negOK) / float64(negCases))
	}
	rep.finish(start)
	return rep, nil
}

// capture 收集一次运行的输出。
type capture struct {
	mu     sync.Mutex
	text   strings.Builder
	blocks []map[string]any
	follow []string
}

func (c *capture) Thinking(agent.Step) {}
func (c *capture) Text(d string)       { c.mu.Lock(); c.text.WriteString(d); c.mu.Unlock() }
func (c *capture) Block(b map[string]any) {
	c.mu.Lock()
	c.blocks = append(c.blocks, b)
	c.mu.Unlock()
}
func (c *capture) Followups(q []string)                                        { c.mu.Lock(); c.follow = q; c.mu.Unlock() }
func (c *capture) Trace(string, string, string, time.Duration, map[string]any) {}

// recommended 返回推荐卡（标题为“为你找到的商品”）里的商品 ID；“相近 / 不太合适 / 匹配到的”等参考卡不算推荐。
func (c *capture) recommended() []string {
	var out []string
	for _, b := range c.blocks {
		if b["type"] != agent.BlockProductList || b["title"] != "为你找到的商品" {
			continue
		}
		raw, _ := json.Marshal(b["products"])
		var list []struct {
			ProductID string `json:"product_id"`
		}
		_ = json.Unmarshal(raw, &list)
		for _, p := range list {
			out = append(out, p.ProductID)
		}
	}
	return out
}

func runOnce(ctx context.Context, st *memstore.Store, runner *agent.RuleRunner, account, session, clientID, content string) (*capture, error) {
	started, err := st.StartAgentRun(ctx, domain.UserMessage{AccountID: account, SessionID: session, ClientMessageID: clientID, Content: content}, func(*domain.ChatSession) {})
	if err != nil {
		return nil, err
	}
	c := &capture{}
	if err := runner.Run(ctx, agent.Input{AccountID: account, SessionID: session, RunID: started.Run.RunID, TraceID: started.Run.TraceID, Content: content}, c); err != nil {
		return nil, err
	}
	_, err = st.UpdateAgentRun(ctx, account, started.Run.RunID, func(r *domain.AgentRun) error {
		r.Status, r.Content, r.Blocks, r.Followups = domain.RunCompleted, c.text.String(), c.blocks, c.follow
		return nil
	})
	return c, err
}
