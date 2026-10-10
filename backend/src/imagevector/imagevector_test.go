package imagevector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/image/bmp"

	"github.com/evelyn-kk/blink-shop/backend/assets"
)

func seedImage(t *testing.T, slug string) image.Image {
	t.Helper()
	raw, err := fs.ReadFile(assets.FS, "catalog/products/"+slug+".png")
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestDecodeLimits(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":     nil,
		"text":      []byte("not an image"),
		"truncated": []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"),
		"too big":   make([]byte, MaxImageBytes+1),
	} {
		if _, _, err := Decode(data); !errors.Is(err, ErrInvalidImage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	tiny, _ := EncodePNG(image.NewRGBA(image.Rect(0, 0, 4, 4)))
	if _, _, err := Decode(tiny); !errors.Is(err, ErrInvalidImage) {
		t.Errorf("tiny image accepted: %v", err)
	}
	wide, _ := EncodePNG(image.NewRGBA(image.Rect(0, 0, maxSide+1, 8)))
	if _, _, err := Decode(wide); !errors.Is(err, ErrInvalidImage) {
		t.Errorf("over-wide image accepted: %v", err)
	}
	// BMP 也能解码（上传允许 BMP）
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, seedImage(t, "p_seed_mouse")); err != nil {
		t.Fatal(err)
	}
	if _, format, err := Decode(buf.Bytes()); err != nil || format != "bmp" {
		t.Errorf("bmp: %s %v", format, err)
	}
}

func TestLocalFeatures(t *testing.T) {
	ctx := context.Background()
	mouse := seedImage(t, "p_seed_mouse")
	png, _ := EncodePNG(mouse)
	v, err := Local{}.Embed(ctx, png)
	if err != nil || len(v) != (Local{}).Dim() {
		t.Fatalf("embed: %d %v", len(v), err)
	}
	if s := Cosine(v, Features(mouse)); s < 0.9999 {
		t.Fatalf("deterministic: %f", s)
	}
	// 同一张图轻度变换后仍然最像自己，和别的商品拉开距离
	cropped, _ := Transform(mouse, []string{"crop:0.85", "jpeg:60"}, 1)
	same := Cosine(Features(cropped), v)
	other := Cosine(Features(cropped), Features(seedImage(t, "p_seed_keyboard")))
	match, weak := (Local{}).Thresholds()
	if same < match || other >= same || weak >= match {
		t.Fatalf("same %.3f other %.3f thresholds %.2f/%.2f", same, other, match, weak)
	}
	// 纯色图没有主体
	solid, _ := Synthetic("solid:#ffffff", 64, 1)
	data, _ := EncodePNG(solid)
	if _, err := (Local{}).Embed(ctx, data); !errors.Is(err, ErrNoSubject) {
		t.Fatalf("solid: %v", err)
	}
	if _, err := Transform(mouse, []string{"warp:2"}, 1); err == nil {
		t.Fatal("unknown transform accepted")
	}
}

func TestDashScope(t *testing.T) {
	if NewDashScope(DashScopeOptions{APIKey: " "}) != nil {
		t.Fatal("empty key should disable")
	}
	var calls atomic.Int32
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path != "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding" || r.Header.Get("Authorization") != "Bearer sk-test" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &lastBody)
		if n == 1 {
			http.Error(w, "busy", http.StatusTooManyRequests) // 第一次限流，客户端重试一次
			return
		}
		dim := 4
		if strings.Contains(string(raw), `"dimension":3`) {
			dim = 5 // 维度不符
		}
		vec := make([]float32, dim)
		for i := range vec {
			vec[i] = float32(i + 1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"embeddings": []map[string]any{{"embedding": vec}}}})
	}))
	defer srv.Close()

	big, _ := Transform(seedImage(t, "p_seed_mouse"), []string{"scale:2.5"}, 1) // 1600 像素，发送前缩到 1024
	data, _ := EncodePNG(big)
	d := NewDashScope(DashScopeOptions{BaseURL: srv.URL + "/", APIKey: "sk-test", Model: "m", Dim: 4})
	v, err := d.Embed(context.Background(), data)
	if err != nil || len(v) != 4 || calls.Load() != 2 {
		t.Fatalf("embed: %v %v calls=%d", v, err, calls.Load())
	}
	if d.Name() != "dashscope:m:4" {
		t.Fatalf("name: %s", d.Name())
	}
	// 发送的是缩小后的 JPEG data URI
	contents := lastBody["input"].(map[string]any)["contents"].([]any)
	uri := contents[0].(map[string]any)["image"].(string)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || max(cfg.Width, cfg.Height) != dashScopeMaxSide {
		t.Fatalf("payload image: %+v %v", cfg, err)
	}
	// 维度不符报错；无法解码的图片不发请求
	if _, err := NewDashScope(DashScopeOptions{BaseURL: srv.URL, APIKey: "sk-test", Model: "m", Dim: 3}).Embed(context.Background(), data); err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("dim mismatch: %v", err)
	}
	before := calls.Load()
	if _, err := d.Embed(context.Background(), []byte("junk")); !errors.Is(err, ErrInvalidImage) || calls.Load() != before {
		t.Fatalf("junk image: %v", err)
	}
}
