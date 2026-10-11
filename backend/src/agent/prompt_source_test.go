package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
)

// flakyStore 让 ActivePrompt 可以出错。
type flakyStore struct {
	*memstore.Store
	fail bool
}

func (f *flakyStore) ActivePrompt(ctx context.Context, key string) (domain.AgentPrompt, error) {
	if f.fail {
		return domain.AgentPrompt{}, errors.New("db down")
	}
	return f.Store.ActivePrompt(ctx, key)
}

func TestPromptCache(t *testing.T) {
	ctx := context.Background()
	st := &flakyStore{Store: memstore.New()}
	c := NewPromptCache(st, 10*time.Second, nil)
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return now })

	// 没发布过：内置默认，版本 0
	if content, v := c.Prompt(ctx, PromptPlanner); v != 0 || content != plannerSystemPrompt {
		t.Fatalf("builtin: v%d", v)
	}
	publish := func(content string, expected int) {
		t.Helper()
		d, err := st.SavePromptDraft(ctx, domain.AgentPrompt{PromptKey: PromptPlanner, Title: "规划", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.SwitchPrompt(ctx, store.PromptSwitch{Key: PromptPlanner, Version: d.Version, ExpectedActive: expected, Action: domain.PromptActionPublish}); err != nil {
			t.Fatal(err)
		}
	}
	publish("规划 v1", 0)
	// TTL 内仍是缓存的内置；过了 TTL 读到 v1
	if _, v := c.Prompt(ctx, PromptPlanner); v != 0 {
		t.Fatalf("cached: v%d", v)
	}
	now = now.Add(11 * time.Second)
	if content, v := c.Prompt(ctx, PromptPlanner); v != 1 || content != "规划 v1" {
		t.Fatalf("after ttl: v%d %q", v, content)
	}
	// 本实例发布后 Invalidate：立即生效
	publish("规划 v2", 1)
	c.Invalidate(PromptPlanner)
	if content, v := c.Prompt(ctx, PromptPlanner); v != 2 || content != "规划 v2" {
		t.Fatalf("invalidated: v%d %q", v, content)
	}
	// 数据库出错：沿用缓存的 v2，不退回内置
	st.fail = true
	now = now.Add(11 * time.Second)
	if _, v := c.Prompt(ctx, PromptPlanner); v != 2 {
		t.Fatalf("db error: v%d", v)
	}
	// 从没读到过就出错：用内置
	if content, v := c.Prompt(ctx, PromptReact); v != 0 || content != reactSystemPrompt {
		t.Fatalf("db error, nothing cached: v%d", v)
	}
	if content, _ := c.Prompt(ctx, "agent.unknown"); content != "" {
		t.Fatal("unknown key")
	}
	// 没有配置来源：内置
	if content, v := (Deps{}).prompt(ctx, PromptRerank); v != 0 || content != rerankPrompt {
		t.Fatal("no source")
	}
}

func TestValidatePrompt(t *testing.T) {
	for _, s := range PromptSpecs() {
		if err := ValidatePrompt(s.Key, s.Builtin); err != nil {
			t.Errorf("builtin %s invalid: %v", s.Key, err)
		}
	}
	cases := map[string]string{
		"空":     "   ",
		"超长":    strings.Repeat("字", MaxPromptRunes+1),
		"缺占位符":  "你是导购",
		"占位符两次": "{{TOOLS}} 和 {{TOOLS}}",
	}
	for name, content := range cases {
		key := PromptPlanner
		if strings.Contains(name, "占位符") {
			key = PromptReact
		}
		if err := ValidatePrompt(key, content); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if ValidatePrompt("agent.nope", "x") == nil {
		t.Error("unknown key accepted")
	}
}
