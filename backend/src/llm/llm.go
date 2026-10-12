// Package llm 是模型调用的最小抽象：一个 OpenAI 兼容（chat/completions）的客户端和一个测试用的 mock。
// 调用方只依赖 Provider；没有配置密钥时 Provider 为 nil，导购完全走规则。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/resilience"
)

// Message 是对话里的一条消息；Role 为 system / user / assistant。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request 是一次补全请求。JSON 为 true 时要求模型输出 JSON 对象（response_format=json_object）。
type Request struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	JSON        bool
}

// Usage 是本次调用的 token 用量（服务端返回；没有时为 0）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CachedPromptTokens 是命中服务端前缀缓存的输入 token（计入 PromptTokens，按缓存价计费）：
	// DeepSeek 返回 prompt_cache_hit_tokens，OpenAI 返回 prompt_tokens_details.cached_tokens。
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
}

// UnmarshalJSON 兼容两种缓存字段。
func (u *Usage) UnmarshalJSON(b []byte) error {
	var raw struct {
		PromptTokens        int  `json:"prompt_tokens"`
		CompletionTokens    int  `json:"completion_tokens"`
		CachedPromptTokens  int  `json:"cached_prompt_tokens"`
		CacheHit            *int `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	u.PromptTokens, u.CompletionTokens, u.CachedPromptTokens = raw.PromptTokens, raw.CompletionTokens, raw.CachedPromptTokens
	switch {
	case raw.CacheHit != nil:
		u.CachedPromptTokens = *raw.CacheHit
	case raw.PromptTokensDetails != nil:
		u.CachedPromptTokens = raw.PromptTokensDetails.CachedTokens
	}
	return nil
}

// Response 是补全结果。
type Response struct {
	Content      string
	FinishReason string
	Usage        Usage
	Model        string
	// Attempts 是实际发起的请求次数（含重试）。
	Attempts int
}

// Provider 执行模型调用。实现必须尊重 ctx（超时、取消）。
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
	// Stream 逐段回调增量文本；返回的 Response.Content 是拼接后的全文。
	Stream(ctx context.Context, req Request, onDelta func(delta string) error) (Response, error)
}

// ErrNotConfigured 表示没有可用的模型（没有密钥）。
var ErrNotConfigured = errors.New("llm: not configured")

// RequestError 是服务端返回的非 2xx。Retryable 说明是否值得重试（429 / 5xx）。
type RequestError struct {
	Status    int
	Body      string
	Retryable bool
}

func (e *RequestError) Error() string {
	body := e.Body
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	return fmt.Sprintf("llm: http %d: %s", e.Status, body)
}

// Options 是 OpenAI 兼容客户端的配置。
type Options struct {
	BaseURL string
	APIKey  string
	// Timeout 是单次请求（含流式读完）的上限；0 表示 30 秒。
	Timeout time.Duration
	// MaxRetries 是 429 / 5xx / 网络错误的重试次数；0 表示不重试。
	MaxRetries int
}

// breakerProvider 在模型服务连续失败时熔断：打开期间直接返回 resilience.ErrOpen，导购立即退回规则回答，不再每次等满超时和重试。
type breakerProvider struct {
	p Provider
	b *resilience.Breaker
}

// WithBreaker 给 p 加熔断；b 为 nil 时原样返回。
func WithBreaker(p Provider, b *resilience.Breaker) Provider {
	if p == nil || b == nil {
		return p
	}
	return breakerProvider{p: p, b: b}
}

func (bp breakerProvider) Complete(ctx context.Context, req Request) (Response, error) {
	done, ok := bp.b.Start()
	if !ok {
		return Response{}, resilience.ErrOpen
	}
	resp, err := bp.p.Complete(ctx, req)
	done(err)
	return resp, err
}

func (bp breakerProvider) Stream(ctx context.Context, req Request, onDelta func(delta string) error) (Response, error) {
	done, ok := bp.b.Start()
	if !ok {
		return Response{}, resilience.ErrOpen
	}
	// 调用方回调主动中止（如输出过滤）不是模型服务的问题，不计入失败
	var aborted error
	resp, err := bp.p.Stream(ctx, req, func(d string) error {
		if e := onDelta(d); e != nil {
			aborted = e
			return e
		}
		return nil
	})
	if aborted != nil && errors.Is(err, aborted) {
		done(nil)
	} else {
		done(err)
	}
	return resp, err
}
