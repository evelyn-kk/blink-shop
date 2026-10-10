package imagevector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
)

// DashScopeOptions 是 DashScope 多模态 Embedding（POST {base}/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding）的配置。
type DashScopeOptions struct {
	BaseURL string
	APIKey  string
	Model   string
	Dim     int
	Timeout time.Duration
	// Match / Weak 是相似度阈值；0 表示用默认值。不同模型的分布不同，换模型后应先跑图片评测再调。
	Match, Weak float64
}

// DashScope 调用多模态 Embedding。图片先在本地解码校验、缩到长边不超过 1024 并转成 JPEG，再以 data URI 发送：
// 不把原始字节、链接或 EXIF 发给外部服务。
type DashScope struct {
	opts   DashScopeOptions
	client *http.Client
}

const (
	dashScopeMaxSide = 1024
	defaultMatch     = 0.75
	defaultWeak      = 0.6
)

// NewDashScope 创建客户端；APIKey 为空时返回 nil。
func NewDashScope(o DashScopeOptions) *DashScope {
	if strings.TrimSpace(o.APIKey) == "" {
		return nil
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Match <= 0 {
		o.Match = defaultMatch
	}
	if o.Weak <= 0 || o.Weak > o.Match {
		o.Weak = defaultWeak
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	return &DashScope{opts: o, client: &http.Client{Timeout: o.Timeout,
		// 不跟随重定向：密钥只发给配置的地址
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (d *DashScope) Dim() int { return d.opts.Dim }
func (d *DashScope) Name() string {
	return "dashscope:" + d.opts.Model + ":" + strconv.Itoa(d.opts.Dim)
}
func (d *DashScope) Thresholds() (float64, float64) { return d.opts.Match, d.opts.Weak }

func (d *DashScope) Embed(ctx context.Context, data []byte) ([]float32, error) {
	img, _, err := Decode(data)
	if err != nil {
		return nil, err
	}
	payload, err := jpegDataURI(img)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"model":      d.opts.Model,
		"input":      map[string]any{"contents": []map[string]string{{"image": payload}}},
		"parameters": map[string]any{"dimension": d.opts.Dim, "output_type": "dense"},
	})
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		vec, retry, err := d.once(ctx, body)
		if err == nil {
			return vec, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, lastErr
}

func (d *DashScope) once(ctx context.Context, body []byte) ([]float32, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.opts.BaseURL+"/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.opts.APIKey)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode/100 != 2 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("image embedding: status %d: %s", resp.StatusCode, snippet(raw))
	}
	var out struct {
		Output struct {
			Embeddings []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"embeddings"`
		} `json:"output"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, fmt.Errorf("image embedding: decode: %w", err)
	}
	if out.Code != "" {
		return nil, false, fmt.Errorf("image embedding: %s: %s", out.Code, out.Message)
	}
	if len(out.Output.Embeddings) == 0 {
		return nil, false, errors.New("image embedding: empty result")
	}
	vec := out.Output.Embeddings[0].Embedding
	if len(vec) != d.opts.Dim {
		return nil, false, fmt.Errorf("image embedding: dimension %d, want %d", len(vec), d.opts.Dim)
	}
	return normalize(vec), false, nil
}

func jpegDataURI(img image.Image) (string, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if s := max(w, h); s > dashScopeMaxSide {
		w, h = max(1, w*dashScopeMaxSide/s), max(1, h*dashScopeMaxSide/s)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
