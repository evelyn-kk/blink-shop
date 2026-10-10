package speech

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*3600)) }

func TestMockRecognizer(t *testing.T) {
	st, err := MockRecognizer{Text: "降噪耳机"}.Start(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	// 1 秒音频 → 前 2 个字的 partial
	for i := 0; i < 10; i++ {
		if err := st.Send(make([]byte, BytesPerSecond/10)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.End(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		ev, err := st.Recv()
		if errors.Is(err, ErrClosed) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev.Type+":"+ev.Text)
	}
	if strings.Join(got, ",") != "partial:降,partial:降噪,final:降噪耳机" {
		t.Fatalf("events: %v", got)
	}
	if st.Send([]byte{1}) == nil || st.End() == nil {
		t.Fatal("send after end should fail")
	}
	// 没有音频：final 为空
	st2, _ := MockRecognizer{}.Start(context.Background(), "r2")
	_ = st2.End()
	if ev, _ := st2.Recv(); ev.Type != "final" || ev.Text != "" {
		t.Fatalf("empty audio: %+v", ev)
	}
	// Close 让阻塞的 Recv 返回
	st3, _ := MockRecognizer{}.Start(context.Background(), "r3")
	go func() { time.Sleep(20 * time.Millisecond); _ = st3.Close() }()
	if _, err := st3.Recv(); !errors.Is(err, ErrClosed) {
		t.Fatalf("recv after close: %v", err)
	}
	_ = st3.Close() // 可重复调用
}

func TestMockSynthesizerWAV(t *testing.T) {
	a, err := MockSynthesizer{}.Synthesize(context.Background(), "你好世界", "")
	if err != nil || a.ContentType != "audio/wav" {
		t.Fatalf("%v %s", err, a.ContentType)
	}
	d := a.Data
	if string(d[:4]) != "RIFF" || string(d[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(d[24:28]) != SampleRate ||
		int(binary.LittleEndian.Uint32(d[40:44])) != len(d)-44 || len(d)-44 != int(0.24*BytesPerSecond) {
		t.Fatalf("wav header / length: %d", len(d))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (MockSynthesizer{Delay: time.Second}).Synthesize(ctx, "x", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestXunfeiRealtimeSignature(t *testing.T) {
	r := NewXunfeiRecognizer(XunfeiRecognizer{Cred: XunfeiCredentials{"app1", "key1", "secret1"},
		BaseURL: "wss://rtasr.example.com/ast/communicate/v1", Lang: "autodialect", Now: fixedNow})
	if NewXunfeiRecognizer(XunfeiRecognizer{Cred: XunfeiCredentials{AppID: "a"}}) != nil {
		t.Fatal("incomplete credentials should disable")
	}
	raw, err := r.SignedURL("req-1")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	sig := q.Get("signature")
	q.Del("signature")
	mac := hmac.New(sha1.New, []byte("secret1"))
	mac.Write([]byte(canonicalQuery(q)))
	if u.Path != "/ast/communicate/v1" || q.Get("appId") != "app1" || q.Get("accessKeyId") != "key1" || q.Get("samplerate") != "16000" ||
		q.Get("utc") != "2026-10-01T08:00:00+0800" || q.Get("uuid") != "req-1" || sig != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signed url: %s", raw)
	}
	if strings.Contains(raw, "secret1") {
		t.Fatal("secret leaked into url")
	}
	if _, err := (&XunfeiRecognizer{BaseURL: "https://x"}).SignedURL(""); err == nil {
		t.Fatal("non-ws base url accepted")
	}
}

// fakeRTASR 模拟讯飞实时转写：收到音频后先发握手（sessionId），音频结束消息到来后依次发一段稳定结果、一段中间结果、最终结果。
func fakeRTASR(t *testing.T, gotAudio *int, gotEnd *map[string]any, failCode int) *httptest.Server {
	return httptest.NewServer(websocket.Handler(func(c *websocket.Conn) {
		_ = websocket.JSON.Send(c, map[string]any{"msg_type": "action", "sessionId": "sid-9"})
		for {
			var msg []byte
			if err := websocket.Message.Receive(c, &msg); err != nil {
				return
			}
			if len(msg) > 0 && msg[0] == '{' {
				_ = json.Unmarshal(msg, gotEnd)
				if failCode != 0 {
					_ = websocket.JSON.Send(c, map[string]any{"code": failCode, "desc": "quota"})
					return
				}
				frame := func(text, typ string, ls bool) map[string]any {
					return map[string]any{"msg_type": "result", "res_type": "asr", "data": map[string]any{"ls": ls,
						"cn": map[string]any{"st": map[string]any{"type": typ, "rt": []any{map[string]any{"ws": []any{
							map[string]any{"cw": []any{map[string]any{"w": text}}}}}}}}}}
				}
				_ = websocket.JSON.Send(c, frame("推荐", "0", false))
				_ = websocket.JSON.Send(c, frame("一款耳", "1", false))
				_ = websocket.JSON.Send(c, frame("一款耳机", "0", true))
				return
			}
			*gotAudio += len(msg)
		}
	}))
}

func TestXunfeiRealtimeStream(t *testing.T) {
	audio, end := 0, map[string]any{}
	srv := fakeRTASR(t, &audio, &end, 0)
	defer srv.Close()
	r := NewXunfeiRecognizer(XunfeiRecognizer{Cred: XunfeiCredentials{"a", "k", "s"}, BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ast", Lang: "cn"})
	st, err := r.Start(context.Background(), "req")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.Send(make([]byte, 3200))
	_ = st.Send(make([]byte, 3200))
	// 先读到握手里的 sessionId，结束消息才能带上它
	time.Sleep(50 * time.Millisecond)
	go func() { time.Sleep(50 * time.Millisecond); _ = st.End() }()
	var got []string
	for {
		ev, err := st.Recv()
		if errors.Is(err, ErrClosed) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev.Type+":"+ev.Text)
	}
	if strings.Join(got, ",") != "partial:推荐,partial:推荐一款耳,final:推荐一款耳机" {
		t.Fatalf("events: %v", got)
	}
	if audio != 6400 || end["end"] != true {
		t.Fatalf("audio=%d end=%v", audio, end)
	}
	// 供应商报错
	srv2 := fakeRTASR(t, new(int), &map[string]any{}, 10800)
	defer srv2.Close()
	st2, err := NewXunfeiRecognizer(XunfeiRecognizer{Cred: XunfeiCredentials{"a", "k", "s"}, BaseURL: "ws" + strings.TrimPrefix(srv2.URL, "http")}).Start(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	_ = st2.End()
	var perr error
	for perr == nil {
		_, perr = st2.Recv()
	}
	if !errors.Is(perr, ErrProvider) || !strings.Contains(perr.Error(), "10800") {
		t.Fatalf("provider error: %v", perr)
	}
	// 连不上：错误不带签名
	_, err = NewXunfeiRecognizer(XunfeiRecognizer{Cred: XunfeiCredentials{"a", "k", "s"}, BaseURL: "ws://127.0.0.1:1/x"}).Start(context.Background(), "")
	if !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), "signature=") {
		t.Fatalf("dial error: %v", err)
	}
}

func TestXunfeiTTS(t *testing.T) {
	var req map[string]any
	var gotQuery url.Values
	srv := httptest.NewServer(websocket.Handler(func(c *websocket.Conn) {
		gotQuery = c.Request().URL.Query()
		if err := websocket.JSON.Receive(c, &req); err != nil {
			return
		}
		b64 := base64.StdEncoding.EncodeToString
		_ = websocket.JSON.Send(c, map[string]any{"code": 0, "data": map[string]any{"audio": b64([]byte("ID3a")), "status": 1}})
		_ = websocket.JSON.Send(c, map[string]any{"code": 0, "data": map[string]any{"audio": b64([]byte("bc")), "status": 2}})
	}))
	defer srv.Close()
	x := NewXunfeiSynthesizer(XunfeiSynthesizer{Cred: XunfeiCredentials{"app", "key", "sec"}, BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/v2/tts",
		DefaultVoice: "xiaoyan", Now: fixedNow})
	a, err := x.Synthesize(context.Background(), "你好", "")
	if err != nil || string(a.Data) != "ID3abc" || a.ContentType != "audio/mpeg" {
		t.Fatalf("audio: %q %s %v", a.Data, a.ContentType, err)
	}
	text, _ := base64.StdEncoding.DecodeString(req["data"].(map[string]any)["text"].(string))
	if string(text) != "你好" || req["business"].(map[string]any)["vcn"] != "xiaoyan" || req["common"].(map[string]any)["app_id"] != "app" {
		t.Fatalf("request: %v", req)
	}
	// 鉴权：authorization 是 Base64 的 api_key + HMAC-SHA256 签名，date 为请求时间
	auth, _ := base64.StdEncoding.DecodeString(gotQuery.Get("authorization"))
	host := strings.TrimPrefix(srv.URL, "http://")
	mac := hmac.New(sha256.New, []byte("sec"))
	mac.Write([]byte("host: " + host + "\ndate: " + gotQuery.Get("date") + "\nGET /v2/tts HTTP/1.1"))
	if !strings.Contains(string(auth), `api_key="key"`) || !strings.Contains(string(auth), base64.StdEncoding.EncodeToString(mac.Sum(nil))) ||
		gotQuery.Get("date") != "Thu, 01 Oct 2026 00:00:00 GMT" || strings.Contains(string(auth), "sec\"") {
		t.Fatalf("auth: %s %v", auth, gotQuery)
	}
	// 供应商报错
	bad := httptest.NewServer(websocket.Handler(func(c *websocket.Conn) {
		var m map[string]any
		_ = websocket.JSON.Receive(c, &m)
		_ = websocket.JSON.Send(c, map[string]any{"code": 11200, "message": "licc limit"})
	}))
	defer bad.Close()
	x.BaseURL = "ws" + strings.TrimPrefix(bad.URL, "http") + "/v2/tts"
	if _, err := x.Synthesize(context.Background(), "x", ""); !errors.Is(err, ErrProvider) || !strings.Contains(err.Error(), "11200") {
		t.Fatalf("provider error: %v", err)
	}
}

func TestDoubaoTTS(t *testing.T) {
	var auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["request"].(map[string]any)["text"] == "fail" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 3011, "message": "invalid text"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 3000, "data": base64.StdEncoding.EncodeToString([]byte("mp3data"))})
	}))
	defer srv.Close()
	d := NewDoubaoSynthesizer(DoubaoSynthesizer{AppID: "app", Token: "tok", Cluster: "volcano_tts", BaseURL: srv.URL, DefaultVoice: "BV700_streaming"})
	a, err := d.Synthesize(context.Background(), "你好", "BV001")
	if err != nil || string(a.Data) != "mp3data" || a.ContentType != "audio/mpeg" || auth != "Bearer;tok" ||
		body["audio"].(map[string]any)["voice_type"] != "BV001" || body["app"].(map[string]any)["cluster"] != "volcano_tts" {
		t.Fatalf("doubao: %q %v %s %v", a.Data, err, auth, body)
	}
	if _, err := d.Synthesize(context.Background(), "fail", ""); !errors.Is(err, ErrProvider) || !strings.Contains(err.Error(), "3011") {
		t.Fatalf("provider error: %v", err)
	}
	// 不跟随重定向（令牌不会被带去别处）
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, srv.URL, http.StatusFound) }))
	defer redirect.Close()
	d.BaseURL = redirect.URL
	if _, err := d.Synthesize(context.Background(), "你好", ""); !errors.Is(err, ErrProvider) {
		t.Fatalf("redirect followed: %v", err)
	}
	if NewDoubaoSynthesizer(DoubaoSynthesizer{AppID: "a"}) != nil {
		t.Fatal("missing token should disable")
	}
}

func TestRedact(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "wss://h/x?authorization=abc&signature=def", Err: errors.New("refused")}
	if got := Redact(err); strings.Contains(got, "abc") || strings.Contains(got, "def") {
		t.Fatalf("redact: %s", got)
	}
	if ContentTypeOf("wav") != "audio/wav" || ContentTypeOf("weird") != "application/octet-stream" {
		t.Fatal("content type")
	}
}
