package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/speech"
)

const fakeSecret = "sk-speech-super-secret-9f2c"

// leakySynth 是一个会失败、错误里带着凭据和带签名 URL 的合成供应商（检查接口和日志不泄露）。
type leakySynth struct{}

func (leakySynth) Name() string  { return "xunfei" }
func (leakySynth) Voice() string { return "xiaoyan" }
func (leakySynth) Synthesize(context.Context, string, string) (speech.Audio, error) {
	return speech.Audio{}, errors.New("dial wss://tts.example.com/v2/tts?authorization=" + fakeSecret + ": refused")
}

func TestSpeechConfigAndTTS(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	merchant := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token

	// 未配置：能力配置都是关闭，TTS 501
	rec := ts.call(t, http.MethodGet, "/api/v1/speech/tts/config", user, nil)
	expectStatus(t, rec, http.StatusOK, "")
	cfg := decodeBody[speechConfigResponse](t, rec)
	if cfg.Enabled || cfg.STT.Enabled || cfg.MaxTextChars != defaultTTSRunes || cfg.STT.SampleRate != 16000 || cfg.STT.MaxSeconds != defaultSpeechSeconds {
		t.Fatalf("disabled config: %+v", cfg)
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/speech/tts", user, map[string]any{"text": "你好"}), http.StatusNotImplemented, "tts_not_enabled")
	// 只有普通用户能用
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/speech/tts/config", "", nil), http.StatusUnauthorized, "")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/speech/tts", merchant, map[string]any{"text": "你好"}), http.StatusForbidden, "")

	// mock 合成
	ts.Server.speech = SpeechOptions{Synthesizer: speech.MockSynthesizer{}, MaxRunes: 10}
	cfg = decodeBody[speechConfigResponse](t, ts.call(t, http.MethodGet, "/api/v1/speech/tts/config", user, nil))
	if !cfg.Enabled || cfg.Provider != "mock" || cfg.Voice != "mock" || cfg.MaxTextChars != 10 {
		t.Fatalf("enabled config: %+v", cfg)
	}
	rec = ts.call(t, http.MethodPost, "/api/v1/speech/tts", user, map[string]any{"text": "  你好，世界  "})
	expectStatus(t, rec, http.StatusOK, "")
	if rec.Header().Get("Content-Type") != "audio/wav" || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Body.String(), "RIFF") {
		t.Fatalf("audio: %v %q", rec.Header(), rec.Body.String()[:4])
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/speech/tts", user, map[string]any{"text": "   "}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/speech/tts", user, map[string]any{"text": strings.Repeat("字", 11)}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/speech/tts", user, map[string]any{"text": "你好", "voice": "../x"}), http.StatusBadRequest, "invalid_argument")

	// 供应商失败：502，响应和日志里都没有凭据和签名
	ts.Server.speech = SpeechOptions{Synthesizer: leakySynth{}}
	rec = ts.call(t, http.MethodPost, "/api/v1/speech/tts", user, map[string]any{"text": "你好"})
	expectStatus(t, rec, http.StatusBadGateway, "tts_failed")
	if strings.Contains(rec.Body.String(), fakeSecret) || strings.Contains(ts.logs.String(), fakeSecret) {
		t.Fatal("speech credential leaked")
	}
	if strings.Contains(decodeBody[map[string]any](t, ts.call(t, http.MethodGet, "/api/v1/speech/tts/config", user, nil))["voice"].(string), "sk-") {
		t.Fatal("config leaked")
	}
}

// fakeRecognizer 包一层 mock：记录会话是否被关闭，可以让 Start 失败。
type fakeRecognizer struct {
	speech.MockRecognizer
	fail   bool
	mu     sync.Mutex
	closed int
	starts atomic.Int32
}

func (f *fakeRecognizer) Name() string { return "mock" }

func (f *fakeRecognizer) Start(ctx context.Context, id string) (speech.Stream, error) {
	f.starts.Add(1)
	if f.fail {
		return nil, errors.New("dial wss://rtasr/x?signature=" + fakeSecret)
	}
	st, err := f.MockRecognizer.Start(ctx, id)
	return &trackedStream{Stream: st, f: f}, err
}

func (f *fakeRecognizer) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

type trackedStream struct {
	speech.Stream
	f    *fakeRecognizer
	once sync.Once
}

func (s *trackedStream) Close() error {
	s.once.Do(func() { s.f.mu.Lock(); s.f.closed++; s.f.mu.Unlock() })
	return s.Stream.Close()
}

type speechClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func dialSpeech(t *testing.T, base, token, origin string) (*speechClient, error) {
	t.Helper()
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(base, "http")+"/api/v1/speech/realtime?sample_rate=16000", "http://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	if origin == "" {
		cfg.Header = http.Header{}
	} else {
		cfg.Origin, _ = cfg.Origin.Parse(origin)
	}
	cfg.Header.Set("Authorization", "Bearer "+token)
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &speechClient{t: t, conn: conn}, nil
}

// mustDial 连接实时识别；上一个会话刚结束时服务端可能还没释放占用（409），稍等重试。
func mustDial(t *testing.T, base, token string) *speechClient {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := dialSpeech(t, base, token, "")
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *speechClient) next() speechEvent {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ev speechEvent
	if err := websocket.JSON.Receive(c.conn, &ev); err != nil {
		c.t.Fatalf("receive: %v", err)
	}
	return ev
}

func (c *speechClient) audio(seconds float64) {
	c.t.Helper()
	n := int(seconds * speech.BytesPerSecond)
	for sent := 0; sent < n; sent += 3200 {
		if err := websocket.Message.Send(c.conn, make([]byte, min(3200, n-sent))); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *speechClient) control(typ string) {
	c.t.Helper()
	if err := websocket.Message.Send(c.conn, `{"type":"`+typ+`"}`); err != nil {
		c.t.Fatal(err)
	}
}

// events 读事件直到 closed（含），返回 type:text 列表。
func (c *speechClient) events() []string {
	c.t.Helper()
	var out []string
	for {
		ev := c.next()
		item := ev.Type
		if ev.Text != "" || ev.Code != "" || ev.Reason != "" {
			item += ":" + ev.Text + ev.Code + ev.Reason
		}
		out = append(out, item)
		if ev.Type == "closed" {
			return out
		}
	}
}

func TestSpeechRealtime(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	srv := httptest.NewServer(ts.handler)
	defer srv.Close()
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	user2 := ts.login(t, seed.User2Username, seed.DevPassword).Token

	// 未配置：501；采样率不对：400；不是 WebSocket：400；未登录：401
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/speech/realtime", user, nil), http.StatusNotImplemented, "speech_not_enabled")
	fake := &fakeRecognizer{MockRecognizer: speech.MockRecognizer{Text: "降噪耳机"}}
	ts.Server.speech = SpeechOptions{Recognizer: fake, MaxSeconds: 3, IdleTimeout: 300 * time.Millisecond}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/speech/realtime?sample_rate=8000", user, nil), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/speech/realtime", user, nil), http.StatusBadRequest, "invalid_argument")
	if _, err := dialSpeech(t, srv.URL, "bad-token", ""); err == nil {
		t.Fatal("unauthenticated websocket accepted")
	}

	// 正常一轮：ready → 音频 → partial → end → final → closed
	c := mustDial(t, srv.URL, user)
	if ev := c.next(); ev.Type != "ready" || ev.SampleRate != 16000 || ev.MaxSeconds != 3 {
		t.Fatalf("ready: %+v", ev)
	}
	c.audio(1)
	c.control("end")
	if got := strings.Join(c.events(), ","); got != "partial:降,partial:降噪,final:降噪耳机,closed" {
		t.Fatalf("session: %s", got)
	}

	// 达到时长上限：服务端结束输入，再给最终结果
	c = mustDial(t, srv.URL, user)
	c.next()
	c.audio(3.5)
	got := c.events()
	if !contains(got, "end_of_input:max_duration") || got[len(got)-2] != "final:降噪耳机" {
		t.Fatalf("max duration: %v", got)
	}

	// 空闲超时
	c = mustDial(t, srv.URL, user)
	c.next()
	if got := c.events(); !contains(got, "error:idle_timeout") {
		t.Fatalf("idle: %v", got)
	}

	// 取消：不给结果
	c = mustDial(t, srv.URL, user)
	c.next()
	c.audio(0.5)
	c.control("cancel")
	if got := c.events(); contains(got, "final:降噪耳机") {
		t.Fatalf("cancel: %v", got)
	}

	// 帧太大
	c = mustDial(t, srv.URL, user)
	c.next()
	_ = websocket.Message.Send(c.conn, make([]byte, maxAudioFrame+1))
	if got := c.events(); !contains(got, "error:frame_too_large") {
		t.Fatalf("frame: %v", got)
	}

	// 同一账户同时只能一个会话；别的账户不受影响
	c = mustDial(t, srv.URL, user)
	c.next()
	if _, err := dialSpeech(t, srv.URL, user, ""); err == nil {
		t.Fatal("second concurrent session accepted")
	}
	c2 := mustDial(t, srv.URL, user2)
	c2.next()
	// 客户端直接断开：供应商连接被关闭，账户可以再开新会话
	before := fake.closedCount()
	_ = c.conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for fake.closedCount() == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fake.closedCount() == before {
		t.Fatal("provider stream not closed after client disconnect")
	}
	mustDial(t, srv.URL, user).next()
	_ = c2.conn.Close()

	// 浏览器来源不在白名单：握手被拒
	ts.dynamic.Set("http.cors.allowed_origins", "https://admin.blink.example")
	if _, err := dialSpeech(t, srv.URL, user2, "https://evil.example"); err == nil {
		t.Fatal("cross-site websocket accepted")
	}
	ts.dynamic.Set("http.cors.allowed_origins", "*") // 测试客户端总带 Origin，恢复后再继续

	// 供应商连不上：error 事件，日志不带签名
	ts.Server.speech = SpeechOptions{Recognizer: &fakeRecognizer{fail: true}}
	c = mustDial(t, srv.URL, user2)
	if got := c.events(); !contains(got, "error:speech_unavailable") {
		t.Fatalf("provider down: %v", got)
	}
	if strings.Contains(ts.logs.String(), fakeSecret) {
		t.Fatal("signature leaked into logs")
	}
}

// 客户端心跳：最后一次推送之后过了写超时，客户端再发 ping，服务端自动回 pong 不能失败、会话不能被当成空闲中断
// （OkHttp 每 15 秒 ping 一次；修复前残留的过期写超时让 pong 写失败，会话在 15 秒处被误判为 idle_timeout）。
func TestSpeechRealtimeSurvivesPingAfterWriteTimeout(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	srv := httptest.NewServer(ts.handler)
	defer srv.Close()
	ts.Server.speech = SpeechOptions{Recognizer: speech.MockRecognizer{Text: "好"}, IdleTimeout: 2 * time.Second, WriteTimeout: 50 * time.Millisecond}
	c := mustDial(t, srv.URL, ts.login(t, seed.UserUsername, seed.DevPassword).Token)
	c.next()
	c.audio(0.6)
	if ev := c.next(); ev.Type != "partial" { // 最后一次推送
		t.Fatalf("partial: %+v", ev)
	}
	for i := 0; i < 5; i++ { // 继续发音频、穿插 ping，跨过写超时
		time.Sleep(100 * time.Millisecond)
		c.conn.PayloadType = websocket.PingFrame
		if _, err := c.conn.Write(nil); err != nil {
			t.Fatal(err)
		}
		c.conn.PayloadType = websocket.BinaryFrame
		c.audio(0.1)
	}
	c.control("end")
	if got := strings.Join(c.events(), ","); got != "final:好,closed" {
		t.Fatalf("after ping: %s", got)
	}
	// 会话日志在 closed 之后写
	for end := time.Now().Add(2 * time.Second); !strings.Contains(ts.logs.String(), `"outcome":"final"`); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("session not completed normally")
		}
	}
}
