package speech

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const dialTimeout = 10 * time.Second

// XunfeiCredentials 是讯飞开放平台的应用凭据（实时转写与在线合成共用）。
type XunfeiCredentials struct {
	AppID     string
	APIKey    string
	APISecret string
}

func (c XunfeiCredentials) ok() bool { return c.AppID != "" && c.APIKey != "" && c.APISecret != "" }

// ---------- 实时转写（RTASR） ----------

// XunfeiRecognizer 连接讯飞实时语音转写：签名 URL（HMAC-SHA1）→ WebSocket，音频以二进制帧发送，
// 结束时发 {"end":true,"sessionId":...}，结果按“稳定段累积 + 当前段”拼成整句。
type XunfeiRecognizer struct {
	Cred    XunfeiCredentials
	BaseURL string // 完整地址，如 wss://office-api-ast-dx.iflyaisol.com/ast/communicate/v1
	Path    string // 可选：拼在 BaseURL 后的路径
	Lang    string // 如 autodialect
	Now     func() time.Time
}

// NewXunfeiRecognizer 凭据不全时返回 nil。
func NewXunfeiRecognizer(r XunfeiRecognizer) *XunfeiRecognizer {
	if !r.Cred.ok() {
		return nil
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	return &r
}

func (x *XunfeiRecognizer) Name() string { return "xunfei" }

// SignedURL 生成带签名的连接地址：查询参数按键排序拼成串，用 APISecret 做 HMAC-SHA1，Base64 后作为 signature。
func (x *XunfeiRecognizer) SignedURL(uuid string) (string, error) {
	base, err := url.Parse(x.BaseURL)
	if err != nil || (base.Scheme != "wss" && base.Scheme != "ws") || base.Host == "" {
		return "", fmt.Errorf("%w: invalid realtime base url", ErrProvider)
	}
	if x.Path != "" {
		base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(x.Path, "/")
	}
	v := url.Values{}
	v.Set("appId", x.Cred.AppID)
	v.Set("accessKeyId", x.Cred.APIKey)
	v.Set("utc", x.Now().Format("2006-01-02T15:04:05-0700"))
	v.Set("audio_encode", "pcm_s16le")
	v.Set("lang", x.Lang)
	v.Set("samplerate", strconv.Itoa(SampleRate))
	if uuid != "" {
		v.Set("uuid", uuid)
	}
	mac := hmac.New(sha1.New, []byte(x.Cred.APISecret))
	_, _ = mac.Write([]byte(canonicalQuery(v)))
	v.Set("signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	base.RawQuery = v.Encode()
	return base.String(), nil
}

func (x *XunfeiRecognizer) Start(ctx context.Context, requestID string) (Stream, error) {
	u, err := x.SignedURL(requestID)
	if err != nil {
		return nil, err
	}
	conn, err := dial(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("%w: dial: %s", ErrProvider, Redact(err))
	}
	st := &xunfeiStream{conn: conn}
	// 会话 ctx 结束（客户端断开、超时）时关掉到供应商的连接，阻塞中的 Recv 随之返回
	context.AfterFunc(ctx, func() { _ = st.Close() })
	return st, nil
}

type xunfeiStream struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	sessionID string
	stable    []string
	finished  bool
	closeOnce sync.Once
}

func (s *xunfeiStream) Send(pcm []byte) error {
	if err := websocket.Message.Send(s.conn, pcm); err != nil {
		return fmt.Errorf("%w: send audio: %v", ErrProvider, err)
	}
	return nil
}

func (s *xunfeiStream) End() error {
	s.mu.Lock()
	msg := map[string]any{"end": true}
	if s.sessionID != "" {
		msg["sessionId"] = s.sessionID
	}
	s.mu.Unlock()
	if err := websocket.JSON.Send(s.conn, msg); err != nil {
		return fmt.Errorf("%w: send end: %v", ErrProvider, err)
	}
	return nil
}

func (s *xunfeiStream) Recv() (Event, error) {
	for {
		s.mu.Lock()
		done := s.finished
		s.mu.Unlock()
		if done {
			return Event{}, ErrClosed
		}
		var raw []byte
		if err := websocket.Message.Receive(s.conn, &raw); err != nil {
			return Event{}, ErrClosed
		}
		res, ok, perr := parseXunfeiResult(raw)
		if perr != nil {
			return Event{}, perr
		}
		if !ok {
			continue
		}
		s.mu.Lock()
		if res.sessionID != "" {
			s.sessionID = res.sessionID
		}
		if res.stable && res.text != "" {
			s.stable = append(s.stable, res.text)
		}
		stable := strings.Join(s.stable, "")
		if res.final {
			s.finished = true
		}
		s.mu.Unlock()
		switch {
		case res.final:
			text := stable
			if text == "" {
				text = res.text
			}
			return Event{Type: "final", Text: text}, nil
		case res.stable && stable != "":
			return Event{Type: "partial", Text: stable}, nil
		case !res.stable && (stable != "" || res.text != ""):
			return Event{Type: "partial", Text: stable + res.text}, nil
		}
	}
}

func (s *xunfeiStream) Close() error {
	s.closeOnce.Do(func() { _ = s.conn.Close() })
	return nil
}

type xunfeiResult struct {
	text, sessionID string
	stable, final   bool
}

// parseXunfeiResult 解析一条转写消息；ok 为 false 表示与结果无关（握手、心跳等）。供应商报错（code≠0）返回错误。
func parseXunfeiResult(raw []byte) (xunfeiResult, bool, error) {
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		return xunfeiResult{}, false, nil
	}
	sid := firstString(p, "sessionId", "sid", "session_id")
	if code := toInt(p["code"]); code != 0 {
		return xunfeiResult{}, false, fmt.Errorf("%w: realtime code %d: %s", ErrProvider, code, toString(p["desc"])+toString(p["message"]))
	}
	if t := toString(p["msg_type"]); t != "" && t != "result" {
		return xunfeiResult{sessionID: sid}, sid != "", nil
	}
	if t := toString(p["res_type"]); t != "" && t != "asr" {
		return xunfeiResult{sessionID: sid}, sid != "", nil
	}
	data, _ := p["data"].(map[string]any)
	cn, _ := data["cn"].(map[string]any)
	st, _ := cn["st"].(map[string]any)
	text := words(st["rt"])
	if text == "" {
		text = toString(p["text"])
	}
	final := toBool(data["ls"])
	return xunfeiResult{text: text, sessionID: sid, stable: toString(st["type"]) == "0", final: final}, text != "" || final || sid != "", nil
}

func words(v any) string {
	var b strings.Builder
	rts, _ := v.([]any)
	for _, rt := range rts {
		m, _ := rt.(map[string]any)
		wss, _ := m["ws"].([]any)
		for _, ws := range wss {
			wm, _ := ws.(map[string]any)
			cws, _ := wm["cw"].([]any)
			for _, cw := range cws {
				cm, _ := cw.(map[string]any)
				b.WriteString(toString(cm["w"]))
			}
		}
	}
	return b.String()
}

// ---------- 在线语音合成（TTS WebAPI） ----------

// XunfeiSynthesizer 调讯飞在线语音合成：签名 URL（HMAC-SHA256，host/date/request-line）→ WebSocket，
// 发一帧请求，按帧接收 Base64 音频直到 status=2。
type XunfeiSynthesizer struct {
	Cred         XunfeiCredentials
	BaseURL      string // 如 wss://tts-api.xfyun.cn/v2/tts
	DefaultVoice string
	Encoding     string // lame（mp3）
	Now          func() time.Time
}

func NewXunfeiSynthesizer(s XunfeiSynthesizer) *XunfeiSynthesizer {
	if !s.Cred.ok() {
		return nil
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Encoding == "" {
		s.Encoding = "lame"
	}
	return &s
}

func (x *XunfeiSynthesizer) Name() string  { return "xunfei" }
func (x *XunfeiSynthesizer) Voice() string { return x.DefaultVoice }

// SignedURL 生成合成接口的鉴权地址。
func (x *XunfeiSynthesizer) SignedURL() (string, error) {
	base, err := url.Parse(x.BaseURL)
	if err != nil || (base.Scheme != "wss" && base.Scheme != "ws") || base.Host == "" {
		return "", fmt.Errorf("%w: invalid tts base url", ErrProvider)
	}
	date := x.Now().UTC().Format(http.TimeFormat)
	path := base.EscapedPath()
	if path == "" {
		path = "/v2/tts"
	}
	origin := "host: " + base.Host + "\ndate: " + date + "\nGET " + path + " HTTP/1.1"
	mac := hmac.New(sha256.New, []byte(x.Cred.APISecret))
	_, _ = mac.Write([]byte(origin))
	auth := fmt.Sprintf(`api_key="%s", algorithm="hmac-sha256", headers="host date request-line", signature="%s"`,
		x.Cred.APIKey, base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	q := base.Query()
	q.Set("authorization", base64.StdEncoding.EncodeToString([]byte(auth)))
	q.Set("date", date)
	q.Set("host", base.Host)
	base.RawQuery = q.Encode()
	return base.String(), nil
}

func (x *XunfeiSynthesizer) Synthesize(ctx context.Context, text, voice string) (Audio, error) {
	if voice == "" {
		voice = x.DefaultVoice
	}
	u, err := x.SignedURL()
	if err != nil {
		return Audio{}, err
	}
	conn, err := dial(ctx, u)
	if err != nil {
		return Audio{}, fmt.Errorf("%w: dial: %s", ErrProvider, Redact(err))
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	req := map[string]any{
		"common":   map[string]any{"app_id": x.Cred.AppID},
		"business": map[string]any{"aue": x.Encoding, "sfl": 1, "vcn": voice, "speed": 50, "volume": 50, "pitch": 50, "tte": "UTF8"},
		"data":     map[string]any{"status": 2, "text": base64.StdEncoding.EncodeToString([]byte(text))},
	}
	if err := websocket.JSON.Send(conn, req); err != nil {
		return Audio{}, fmt.Errorf("%w: send: %v", ErrProvider, err)
	}
	var audio []byte
	for {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			if ctx.Err() != nil {
				return Audio{}, ctx.Err()
			}
			return Audio{}, fmt.Errorf("%w: receive: %v", ErrProvider, err)
		}
		var f struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Audio  string `json:"audio"`
				Status int    `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return Audio{}, fmt.Errorf("%w: decode frame: %v", ErrProvider, err)
		}
		if f.Code != 0 {
			return Audio{}, fmt.Errorf("%w: tts code %d: %s", ErrProvider, f.Code, f.Message)
		}
		if f.Data.Audio != "" {
			chunk, err := base64.StdEncoding.DecodeString(f.Data.Audio)
			if err != nil {
				return Audio{}, fmt.Errorf("%w: decode audio: %v", ErrProvider, err)
			}
			audio = append(audio, chunk...)
		}
		if f.Data.Status == 2 {
			break
		}
	}
	if len(audio) == 0 {
		return Audio{}, fmt.Errorf("%w: empty audio", ErrProvider)
	}
	return Audio{Data: audio, ContentType: ContentTypeOf(x.Encoding)}, nil
}

// ---------- 公共 ----------

// dial 建立 WebSocket 连接（限时；ctx 取消时中止）。
func dial(ctx context.Context, rawURL string) (*websocket.Conn, error) {
	cfg, err := websocket.NewConfig(rawURL, "http://localhost/")
	if err != nil {
		return nil, err
	}
	cfg.Dialer = &net.Dialer{Timeout: dialTimeout}
	type result struct {
		c   *websocket.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := websocket.DialConfig(cfg)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// canonicalQuery 按键（再按值）排序拼接查询参数，用于签名。
func canonicalQuery(v url.Values) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), v[k]...)
		sort.Strings(vals)
		for _, val := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(val))
		}
	}
	return strings.Join(parts, "&")
}

func firstString(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := toString(p[k]); s != "" {
			return s
		}
	}
	return ""
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	}
	return false
}
