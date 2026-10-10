package llm

import (
	"context"
	"errors"
	"sync"
)

// Mock 是测试用的 Provider：按脚本依次返回回复，并记录收到的请求。
// Script 里的一项可以是固定文本、错误或自定义函数（看请求决定回复）。
type Mock struct {
	mu       sync.Mutex
	Script   []Reply
	Requests []Request
	// Fallback 在脚本用完后调用；为 nil 时返回 ErrScriptExhausted。
	Fallback func(req Request) (Response, error)
}

// Reply 是脚本里的一项。
type Reply struct {
	Content string
	Err     error
	Fn      func(req Request) (Response, error)
	Usage   Usage
}

// ErrScriptExhausted 表示 mock 的脚本已经用完。
var ErrScriptExhausted = errors.New("llm mock: script exhausted")

// Text 构造一条固定回复。
func Text(content string) Reply { return Reply{Content: content} }

// Fail 构造一条返回错误的回复。
func Fail(err error) Reply { return Reply{Err: err} }

// Then 追加脚本。
func (m *Mock) Then(replies ...Reply) *Mock {
	m.mu.Lock()
	m.Script = append(m.Script, replies...)
	m.mu.Unlock()
	return m
}

func (m *Mock) next(req Request) (Response, error) {
	m.mu.Lock()
	m.Requests = append(m.Requests, req)
	var r Reply
	has := len(m.Script) > 0
	if has {
		r, m.Script = m.Script[0], m.Script[1:]
	}
	fallback := m.Fallback
	m.mu.Unlock()
	if !has {
		if fallback != nil {
			return fallback(req)
		}
		return Response{}, ErrScriptExhausted
	}
	if r.Fn != nil {
		return r.Fn(req)
	}
	if r.Err != nil {
		return Response{}, r.Err
	}
	return Response{Content: r.Content, FinishReason: "stop", Usage: r.Usage, Model: req.Model, Attempts: 1}, nil
}

func (m *Mock) Complete(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	return m.next(req)
}

// Stream 把回复按 8 个字一段回调。
func (m *Mock) Stream(ctx context.Context, req Request, onDelta func(delta string) error) (Response, error) {
	resp, err := m.next(req)
	if err != nil {
		return resp, err
	}
	rs := []rune(resp.Content)
	for i := 0; i < len(rs); i += 8 {
		if err := ctx.Err(); err != nil {
			return resp, err
		}
		end := i + 8
		if end > len(rs) {
			end = len(rs)
		}
		if err := onDelta(string(rs[i:end])); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

// Count 返回收到的请求数。
func (m *Mock) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Requests)
}

// Last 返回最后一次请求（没有时为空）。
func (m *Mock) Last() Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Requests) == 0 {
		return Request{}
	}
	return m.Requests[len(m.Requests)-1]
}
