package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// PromptSpec 是一个可在后台管理的 Prompt：内置默认内容（版本 0）和必须保留的占位符。
type PromptSpec struct {
	Key          string
	Title        string
	Description  string
	Builtin      string
	Placeholders []string
}

// MaxPromptRunes 是一个 Prompt 的最大字数。
const MaxPromptRunes = 20000

var promptSpecs = []PromptSpec{
	{Key: PromptPlanner, Title: "意图规划", Description: "小模型把一句话分到一个意图并抽取槽位，只输出 JSON", Builtin: plannerSystemPrompt},
	{Key: PromptReact, Title: "工具循环", Description: "大模型在工具循环里选择工具、给出最终回答；{{TOOLS}} 替换为当前意图允许的工具和参数", Builtin: reactSystemPrompt,
		Placeholders: []string{"{{TOOLS}}"}},
	{Key: PromptRerank, Title: "商品重排", Description: "小模型在规则候选里调整商品顺序（ai.rerank_enabled 开启时）", Builtin: rerankPrompt},
	{Key: PromptSummary, Title: "会话摘要", Description: "小模型一句话概括会话（ai.summary_enabled 开启时）", Builtin: summaryPromptText},
}

// PromptSpecs 返回全部可管理的 Prompt（顺序固定）。
func PromptSpecs() []PromptSpec { return append([]PromptSpec(nil), promptSpecs...) }

// LookupPromptSpec 按 key 查找。
func LookupPromptSpec(key string) (PromptSpec, bool) {
	for _, s := range promptSpecs {
		if s.Key == key {
			return s, true
		}
	}
	return PromptSpec{}, false
}

// ValidatePrompt 校验草稿内容：非空、不超长、保留必需的占位符。
func ValidatePrompt(key, content string) error {
	spec, ok := LookupPromptSpec(key)
	if !ok {
		return fmt.Errorf("没有这个 Prompt：%s", key)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("Prompt 内容不能为空")
	}
	if n := utf8.RuneCountInString(content); n > MaxPromptRunes {
		return fmt.Errorf("Prompt 最多 %d 字（当前 %d 字）", MaxPromptRunes, n)
	}
	for _, ph := range spec.Placeholders {
		if strings.Count(content, ph) != 1 {
			return fmt.Errorf("必须保留且只出现一次占位符 %s", ph)
		}
	}
	return nil
}

// PromptSource 返回 key 当前生效的 Prompt 内容和版本（0 表示内置默认）。
type PromptSource interface {
	Prompt(ctx context.Context, key string) (content string, version int)
}

// PromptCache 从 Store 读取已发布的 Prompt，缓存 TTL：新发布的版本最迟 TTL 后在所有实例生效，本实例发布时调用 Invalidate 立即生效。
// 没有发布过的 key 用内置默认（版本 0）；读取出错时沿用缓存（从没读到过则用内置），并记日志。
type PromptCache struct {
	store  store.Store
	ttl    time.Duration
	now    func() time.Time
	logger *slog.Logger

	mu      sync.Mutex
	entries map[string]promptEntry
}

type promptEntry struct {
	content string
	version int
	at      time.Time
}

func NewPromptCache(st store.Store, ttl time.Duration, logger *slog.Logger) *PromptCache {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &PromptCache{store: st, ttl: ttl, now: time.Now, logger: logger, entries: map[string]promptEntry{}}
}

// SetClock 替换时钟（测试 TTL 用）。
func (c *PromptCache) SetClock(now func() time.Time) { c.now = now }

func (c *PromptCache) Prompt(ctx context.Context, key string) (string, int) {
	spec, ok := LookupPromptSpec(key)
	if !ok {
		return "", 0
	}
	c.mu.Lock()
	e, cached := c.entries[key]
	c.mu.Unlock()
	if cached && c.now().Sub(e.at) < c.ttl {
		return e.content, e.version
	}
	p, err := c.store.ActivePrompt(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		e = promptEntry{content: spec.Builtin, version: 0, at: c.now()}
	case err != nil:
		c.logger.WarnContext(ctx, "read active prompt failed, using cached", "prompt", key, "error", err)
		if !cached {
			e = promptEntry{content: spec.Builtin, version: 0}
		}
		e.at = c.now() // TTL 内不再重试
	default:
		e = promptEntry{content: p.Content, version: p.Version, at: c.now()}
	}
	c.mu.Lock()
	c.entries[key] = e
	c.mu.Unlock()
	return e.content, e.version
}

// Invalidate 清掉 key 的缓存（本实例发布 / 回滚后调用，下一次运行立即读到新版本）。
func (c *PromptCache) Invalidate(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// prompt 读取 key 的当前内容和版本；没有配置来源时用内置默认（版本 0）。
func (d Deps) prompt(ctx context.Context, key string) (string, int) {
	if d.Prompts != nil {
		if content, version := d.Prompts.Prompt(ctx, key); content != "" {
			return content, version
		}
	}
	spec, _ := LookupPromptSpec(key)
	return spec.Builtin, 0
}
