// Package vector 是向量检索的基础设施：Embedding 客户端（OpenAI 兼容 /embeddings）和 Milvus（REST v2）上的知识分块、商品两个索引。
// 都是可选的：没有配置 Embedding 或 Milvus 时上层只用关键词检索。
package vector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Embedder 把文本转成向量。同一个 Embedder 的维度固定。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
	// Name 用于评测报告的配置指纹（提供方 + 模型 + 维度）。
	Name() string
}

// EmbedOptions 是 OpenAI 兼容 Embedding 服务的配置。
type EmbedOptions struct {
	BaseURL string
	APIKey  string
	Model   string
	Dim     int
	Timeout time.Duration
	// BatchSize 是一次请求最多的文本数（DashScope 限 10）；0 表示 10。
	BatchSize int
}

// OpenAIEmbedder 调用 POST {base}/embeddings。
type OpenAIEmbedder struct {
	opts EmbedOptions
	http *http.Client
}

// NewOpenAIEmbedder 创建客户端；APIKey 或 Model 为空、维度不合法时返回 nil（表示未配置）。
func NewOpenAIEmbedder(opts EmbedOptions) *OpenAIEmbedder {
	if strings.TrimSpace(opts.APIKey) == "" || strings.TrimSpace(opts.Model) == "" || opts.Dim <= 0 {
		return nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 10
	}
	opts.BaseURL = strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	return &OpenAIEmbedder{opts: opts, http: &http.Client{}}
}

func (e *OpenAIEmbedder) Dim() int { return e.opts.Dim }
func (e *OpenAIEmbedder) Name() string {
	return fmt.Sprintf("openai-compatible:%s:%d", e.opts.Model, e.opts.Dim)
}

// WithHTTPClient 替换底层 HTTP 客户端（测试用）。
func (e *OpenAIEmbedder) WithHTTPClient(h *http.Client) *OpenAIEmbedder {
	e.http = h
	return e
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.opts.BatchSize {
		end := min(start+e.opts.BatchSize, len(texts))
		vecs, err := e.batch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *OpenAIEmbedder) batch(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": e.opts.Model, "input": texts, "dimensions": e.opts.Dim, "encoding_format": "float"})
	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.opts.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.opts.APIKey)
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		msg := string(raw)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("embedding: http %d: %s", resp.StatusCode, msg)
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("embedding: bad response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embedding: got %d vectors for %d texts", len(parsed.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(texts) || len(d.Embedding) != e.opts.Dim {
			return nil, fmt.Errorf("embedding: vector %d has dim %d, want %d", d.Index, len(d.Embedding), e.opts.Dim)
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("embedding: missing vector %d", i)
		}
	}
	return out, nil
}

// HashEmbedder 是不依赖外部服务的确定性向量：把汉字二元组和字母数字词散列到固定维度并归一化。
// 只反映字面重合，没有语义能力；用于测试和本地演示 Milvus 链路，不要在生产使用。
type HashEmbedder struct{ D int }

func (h HashEmbedder) Dim() int {
	if h.D <= 0 {
		return 64
	}
	return h.D
}

func (h HashEmbedder) Name() string { return fmt.Sprintf("hash:%d", h.Dim()) }

func (h HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, h.Dim())
		for _, tok := range hashTokens(t) {
			sum := sha256.Sum256([]byte(tok))
			idx := binary.BigEndian.Uint32(sum[:4]) % uint32(h.Dim())
			sign := float32(1)
			if sum[4]&1 == 1 {
				sign = -1
			}
			v[idx] += sign
		}
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		if norm == 0 {
			v[0] = 1
			norm = 1
		}
		n := float32(math.Sqrt(norm))
		for j := range v {
			v[j] /= n
		}
		out[i] = v
	}
	return out, nil
}

func hashTokens(text string) []string {
	var toks []string
	var han []rune
	var word []rune
	flushHan := func() {
		for i := 0; i+1 < len(han); i++ {
			toks = append(toks, string(han[i:i+2]))
		}
		if len(han) == 1 {
			toks = append(toks, string(han))
		}
		han = nil
	}
	flushWord := func() {
		if len(word) > 0 {
			toks = append(toks, strings.ToLower(string(word)))
		}
		word = nil
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, r)
		default:
			flushHan()
			flushWord()
		}
	}
	flushHan()
	flushWord()
	return toks
}

// ErrDimMismatch 表示已有集合的向量维度和当前 Embedding 不一致（换了模型），需要删掉集合重建。
var ErrDimMismatch = errors.New("vector: collection dimension does not match embedder")
