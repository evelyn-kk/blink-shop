package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc, retries int) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(Options{BaseURL: srv.URL + "/v1/", APIKey: "sk-test", Timeout: 2 * time.Second, MaxRetries: retries})
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c, srv
}

func TestNotConfigured(t *testing.T) {
	if NewClient(Options{APIKey: "  "}) != nil {
		t.Fatal("empty key must yield nil client")
	}
}

func TestCompleteSendsOpenAIShapeAndParses(t *testing.T) {
	var got map[string]any
	var auth, path string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m-1","choices":[{"message":{"role":"assistant","content":"{\"intent\":\"cart\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":5}}`))
	}, 0)
	resp, err := c.Complete(context.Background(), Request{Model: "m-1", Messages: []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}, Temperature: 0.1, JSON: true, MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" || path != "/v1/chat/completions" {
		t.Fatalf("auth=%q path=%q", auth, path)
	}
	if got["model"] != "m-1" || got["temperature"] != 0.1 || got["max_tokens"] != float64(64) || got["response_format"].(map[string]any)["type"] != "json_object" || got["stream"] != nil {
		t.Fatalf("request: %v", got)
	}
	if resp.Content != `{"intent":"cart"}` || resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 5 || resp.Model != "m-1" || resp.Attempts != 1 || resp.FinishReason != "stop" {
		t.Fatalf("response: %+v", resp)
	}
}

func TestRetryOnRateLimitAndServerErrorNotOnBadRequest(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		case 2:
			w.WriteHeader(503)
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
		}
	}, 2)
	resp, err := c.Complete(context.Background(), Request{Model: "m"})
	if err != nil || resp.Content != "ok" || resp.Attempts != 3 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	// 重试用完仍失败：返回最后一次错误
	calls.Store(0)
	c2, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte("boom"))
	}, 1)
	_, err = c2.Complete(context.Background(), Request{Model: "m"})
	var re *RequestError
	if !errors.As(err, &re) || re.Status != 500 || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	// 4xx 不重试
	calls.Store(0)
	c3, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}, 3)
	_, err = c3.Complete(context.Background(), Request{Model: "m"})
	if !errors.As(err, &re) || re.Status != 400 || re.Retryable || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("error text: %v", err)
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}, 3)
	c.opts.Timeout = 150 * time.Millisecond
	start := time.Now()
	_, err := c.Complete(context.Background(), Request{Model: "m"})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout err=%v took %v", err, time.Since(start))
	}
	// 调用方的 ctx 取消：立即返回、不重试
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Complete(ctx, Request{Model: "m"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestStreamParsesDeltasAndUsage(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		if got["stream"] != true {
			t.Errorf("stream flag missing: %v", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n" +
			": keepalive\n\n" +
			"data: not-json\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n" +
			"data: [DONE]\n\n"))
	}, 0)
	var deltas []string
	resp, err := c.Stream(context.Background(), Request{Model: "m"}, func(d string) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "你好，世界" || len(deltas) != 2 || resp.Usage.CompletionTokens != 4 || resp.FinishReason != "stop" || resp.Model != "m" {
		t.Fatalf("resp=%+v deltas=%v", resp, deltas)
	}
	// 回调出错：停止并返回该错误，不重试
	var calls atomic.Int32
	c2, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\n"))
	}, 2)
	stop := errors.New("stop")
	if _, err := c2.Stream(context.Background(), Request{Model: "m"}, func(string) error { return stop }); !errors.Is(err, stop) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	// 流里的错误帧，且还没输出：按可重试错误处理
	calls.Store(0)
	c3, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"overloaded\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}, 1)
	resp, err = c3.Stream(context.Background(), Request{Model: "m"}, func(string) error { return nil })
	if err != nil || resp.Content != "ok" || resp.Attempts != 2 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestMock(t *testing.T) {
	m := (&Mock{}).Then(Text("a"), Fail(errors.New("x")), Reply{Fn: func(r Request) (Response, error) { return Response{Content: r.Model}, nil }})
	if r, err := m.Complete(context.Background(), Request{Model: "m1"}); err != nil || r.Content != "a" {
		t.Fatal(r, err)
	}
	if _, err := m.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("expected error")
	}
	var got string
	if r, err := m.Stream(context.Background(), Request{Model: "streamed-model-name"}, func(d string) error { got += d; return nil }); err != nil || got != "streamed-model-name" || r.Content != "streamed-model-name" {
		t.Fatal(r, err, got)
	}
	if _, err := m.Complete(context.Background(), Request{}); !errors.Is(err, ErrScriptExhausted) || m.Count() != 4 || m.Last().Model != "" {
		t.Fatalf("exhausted: %v %d", err, m.Count())
	}
}
