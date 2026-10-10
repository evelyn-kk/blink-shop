package speech

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DoubaoSynthesizer 调豆包（火山引擎）语音合成 HTTP 接口：POST JSON，鉴权头 `Bearer;<token>`，返回 Base64 音频。
type DoubaoSynthesizer struct {
	AppID        string
	Token        string
	Cluster      string
	BaseURL      string // 如 https://openspeech.bytedance.com/api/v1/tts
	DefaultVoice string
	Encoding     string // mp3
	UID          string
	client       *http.Client
}

func NewDoubaoSynthesizer(d DoubaoSynthesizer) *DoubaoSynthesizer {
	if d.AppID == "" || d.Token == "" {
		return nil
	}
	if d.Encoding == "" {
		d.Encoding = "mp3"
	}
	if d.UID == "" {
		d.UID = "blink-shop"
	}
	// 不跟随重定向：令牌只发给配置的地址
	d.client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &d
}

func (d *DoubaoSynthesizer) Name() string  { return "doubao" }
func (d *DoubaoSynthesizer) Voice() string { return d.DefaultVoice }

func (d *DoubaoSynthesizer) Synthesize(ctx context.Context, text, voice string) (Audio, error) {
	if voice == "" {
		voice = d.DefaultVoice
	}
	if u, err := url.Parse(d.BaseURL); err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return Audio{}, fmt.Errorf("%w: invalid tts base url", ErrProvider)
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	body, _ := json.Marshal(map[string]any{
		"app":   map[string]any{"appid": d.AppID, "token": d.Token, "cluster": d.Cluster},
		"user":  map[string]any{"uid": d.UID},
		"audio": map[string]any{"voice_type": voice, "encoding": d.Encoding, "speed_ratio": 1.0, "volume_ratio": 1.0, "pitch_ratio": 1.0},
		"request": map[string]any{"reqid": hex.EncodeToString(id[:]), "text": text, "text_type": "plain", "operation": "query",
			"with_frontend": 1, "frontend_type": "unitTson"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL, bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer;"+d.Token)
	resp, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Audio{}, ctx.Err()
		}
		return Audio{}, fmt.Errorf("%w: %s", ErrProvider, Redact(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return Audio{}, fmt.Errorf("%w: read: %v", ErrProvider, err)
	}
	if resp.StatusCode/100 != 2 {
		return Audio{}, fmt.Errorf("%w: http %d", ErrProvider, resp.StatusCode)
	}
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Audio{}, fmt.Errorf("%w: decode: %v", ErrProvider, err)
	}
	if r.Code != 3000 && r.Code != 0 {
		return Audio{}, fmt.Errorf("%w: code %d: %s", ErrProvider, r.Code, r.Message)
	}
	audio, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil || len(audio) == 0 {
		return Audio{}, fmt.Errorf("%w: empty audio", ErrProvider)
	}
	return Audio{Data: audio, ContentType: ContentTypeOf(d.Encoding)}, nil
}
