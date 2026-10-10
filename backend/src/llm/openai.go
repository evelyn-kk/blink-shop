package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	retryBaseDelay = 500 * time.Millisecond
	maxErrorBody   = 4 << 10
)

// Client 调用 OpenAI 兼容的 /chat/completions（DashScope、OpenAI、vLLM 等）。
type Client struct {
	opts Options
	http *http.Client
	// sleep 可在测试里替换，避免真的等退避时间。
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient 创建客户端；APIKey 为空时返回 nil（调用方据此知道模型未配置）。
func NewClient(opts Options) *Client {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	opts.BaseURL = strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	return &Client{opts: opts, http: &http.Client{}, sleep: sleepCtx}
}

// WithHTTPClient 替换底层 HTTP 客户端（测试用）。
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  *streamOptions  `json:"stream_options,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (c *Client) body(req Request, stream bool) ([]byte, error) {
	cr := chatRequest{Model: req.Model, Messages: req.Messages, Temperature: req.Temperature, MaxTokens: req.MaxTokens, Stream: stream}
	if stream {
		cr.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if req.JSON {
		cr.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	return json.Marshal(cr)
}

// Complete 一次性返回全文。429 / 5xx / 网络错误按 MaxRetries 指数退避重试；4xx 不重试；ctx 结束立即返回。
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	payload, err := c.body(req, false)
	if err != nil {
		return Response{}, err
	}
	var out Response
	err = c.withRetry(ctx, func(attempt int) error {
		out.Attempts = attempt
		resp, err := c.do(ctx, payload)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return err
		}
		var cr chatResponse
		if err := json.Unmarshal(raw, &cr); err != nil {
			return fmt.Errorf("llm: bad response json: %w", err)
		}
		if len(cr.Choices) == 0 {
			if cr.Error != nil {
				return fmt.Errorf("llm: %s", cr.Error.Message)
			}
			return errors.New("llm: response has no choices")
		}
		out.Content, out.FinishReason, out.Model = cr.Choices[0].Message.Content, cr.Choices[0].FinishReason, cr.Model
		if cr.Usage != nil {
			out.Usage = *cr.Usage
		}
		return nil
	})
	return out, err
}

// Stream 按 SSE 读取增量；连接建立前的失败按 Complete 的规则重试，已经开始输出后不重试（避免重复文本）。
func (c *Client) Stream(ctx context.Context, req Request, onDelta func(delta string) error) (Response, error) {
	payload, err := c.body(req, true)
	if err != nil {
		return Response{}, err
	}
	var out Response
	err = c.withRetry(ctx, func(attempt int) error {
		out.Attempts = attempt
		resp, err := c.do(ctx, payload)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var text strings.Builder
		started := false
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var cr chatResponse
			if err := json.Unmarshal([]byte(data), &cr); err != nil {
				continue // 坏帧跳过
			}
			if cr.Error != nil {
				return &RequestError{Status: 500, Body: cr.Error.Message, Retryable: !started}
			}
			if cr.Model != "" {
				out.Model = cr.Model
			}
			if cr.Usage != nil {
				out.Usage = *cr.Usage
			}
			for _, ch := range cr.Choices {
				if ch.Delta.Content != "" {
					started = true
					text.WriteString(ch.Delta.Content)
					if err := onDelta(ch.Delta.Content); err != nil {
						return noRetry{err}
					}
				}
				if ch.FinishReason != "" {
					out.FinishReason = ch.FinishReason
				}
			}
		}
		if err := scanner.Err(); err != nil {
			if started {
				return noRetry{err}
			}
			return err
		}
		out.Content = text.String()
		return nil
	})
	return out, err
}

// noRetry 包住不应重试的错误。
type noRetry struct{ error }

func (n noRetry) Unwrap() error { return n.error }

func (c *Client) withRetry(ctx context.Context, fn func(attempt int) error) error {
	var last error
	for attempt := 1; attempt <= c.opts.MaxRetries+1; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn(attempt)
		if err == nil {
			return nil
		}
		last = err
		if !retryable(err) || attempt > c.opts.MaxRetries {
			break
		}
		if err := c.sleep(ctx, retryBaseDelay*time.Duration(1<<(attempt-1))); err != nil {
			return err
		}
	}
	var nr noRetry
	if errors.As(last, &nr) {
		return nr.error
	}
	return last
}

func retryable(err error) bool {
	var nr noRetry
	if errors.As(err, &nr) {
		return false
	}
	var re *RequestError
	if errors.As(err, &re) {
		return re.Retryable
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func (c *Client) do(ctx context.Context, payload []byte) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		resp.Body.Close()
		cancel()
		return nil, &RequestError{Status: resp.StatusCode, Body: string(body), Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500}
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose 让超时 context 在响应体关闭时释放。
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	c.cancel()
	return c.ReadCloser.Close()
}
