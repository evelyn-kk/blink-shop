package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/net/websocket"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/speech"
)

// SpeechOptions 是语音能力：Recognizer / Synthesizer 为 nil 表示未配置。
type SpeechOptions struct {
	Recognizer  speech.Recognizer
	Synthesizer speech.Synthesizer
	// MaxSeconds 是单次语音输入的最长秒数，MaxRunes 是单次朗读的最多字数；0 用默认值。
	MaxSeconds int
	MaxRunes   int
	// IdleTimeout 是实时识别中客户端多久没有消息就断开；0 用默认值（测试可调小）。
	IdleTimeout time.Duration
	// WriteTimeout 是推送一条事件的写超时；0 用默认值（测试可调小）。
	WriteTimeout time.Duration
}

const (
	defaultSpeechSeconds = 60
	defaultTTSRunes      = 800
	defaultSpeechIdle    = 10 * time.Second
	ttsTimeout           = 20 * time.Second
	// maxAudioFrame 是一帧音频的上限（客户端按 100ms 左右分帧，约 3.2KB）。
	maxAudioFrame = 64 << 10
	// speechTail 是音频结束后等待最终结果的时间。
	speechTail = 10 * time.Second
)

var (
	ErrSpeechNotEnabled = &APIError{Status: http.StatusNotImplemented, Code: "speech_not_enabled", Message: "语音输入暂未开通，请用文字提问"}
	ErrTTSNotEnabled    = &APIError{Status: http.StatusNotImplemented, Code: "tts_not_enabled", Message: "语音朗读暂未开通"}
	ErrTTSFailed        = &APIError{Status: http.StatusBadGateway, Code: "tts_failed", Message: "语音合成服务暂时不可用，请稍后再试"}
	ErrSpeechBusy       = &APIError{Status: http.StatusConflict, Code: "speech_session_active", Message: "已有一段语音输入正在进行，请先结束它"}
)

func (o SpeechOptions) maxSeconds() int {
	if o.MaxSeconds > 0 {
		return o.MaxSeconds
	}
	return defaultSpeechSeconds
}

func (o SpeechOptions) maxRunes() int {
	if o.MaxRunes > 0 {
		return o.MaxRunes
	}
	return defaultTTSRunes
}

func (o SpeechOptions) writeTimeout() time.Duration {
	if o.WriteTimeout > 0 {
		return o.WriteTimeout
	}
	return 5 * time.Second
}

func (o SpeechOptions) idle() time.Duration {
	if o.IdleTimeout > 0 {
		return o.IdleTimeout
	}
	return defaultSpeechIdle
}

// ---------- 能力配置 ----------

type speechSTTConfig struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"`
	SampleRate int    `json:"sample_rate"`
	Encoding   string `json:"encoding"`
	MaxSeconds int    `json:"max_seconds"`
}

type speechConfigResponse struct {
	Enabled      bool            `json:"enabled"`
	Provider     string          `json:"provider"`
	Voice        string          `json:"voice"`
	MaxTextChars int             `json:"max_text_chars"`
	STT          speechSTTConfig `json:"stt"`
}

// handleSpeechConfig 返回语音能力（是否开通、供应商名、默认音色、限制），客户端据此决定是否显示语音按钮。不含任何凭据。
func (s *Server) handleSpeechConfig(w http.ResponseWriter, r *http.Request) {
	o := s.speech
	out := speechConfigResponse{MaxTextChars: o.maxRunes(),
		STT: speechSTTConfig{SampleRate: speech.SampleRate, Encoding: "pcm_s16le", MaxSeconds: o.maxSeconds()}}
	if o.Synthesizer != nil {
		out.Enabled, out.Provider, out.Voice = true, o.Synthesizer.Name(), o.Synthesizer.Voice()
	}
	if o.Recognizer != nil {
		out.STT.Enabled, out.STT.Provider = true, o.Recognizer.Name()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// ---------- 合成 ----------

type ttsRequest struct {
	Text  string `json:"text"`
	Voice string `json:"voice"`
}

// handleTTS 把文本合成音频，成功直接返回音频字节。文本由客户端先清洗（去掉 Markdown 等），这里只做长度和音色校验。
func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in ttsRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	text := strings.TrimSpace(in.Text)
	n := utf8.RuneCountInString(text)
	switch {
	case n == 0:
		writeError(w, fieldError("text", "请提供要朗读的文字"))
		return
	case n > s.speech.maxRunes():
		writeError(w, fieldError("text", "朗读内容不能超过 "+strconv.Itoa(s.speech.maxRunes())+" 字"))
		return
	case in.Voice != "" && !configcenter.ValidVoice(in.Voice):
		writeError(w, fieldError("voice", "音色名只能是字母、数字、下划线和短横线"))
		return
	}
	syn := s.speech.Synthesizer
	if syn == nil {
		writeError(w, ErrTTSNotEnabled)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ttsTimeout)
	defer cancel()
	start := time.Now()
	audio, err := syn.Synthesize(ctx, text, in.Voice)
	if err != nil {
		s.logger.WarnContext(r.Context(), "tts failed", "request_id", requestIDFromContext(r.Context()), "provider", syn.Name(), "error", speech.Redact(err))
		writeError(w, ErrTTSFailed)
		return
	}
	s.logger.InfoContext(r.Context(), "tts", "request_id", requestIDFromContext(r.Context()), "account_id", acc.AccountID, "provider", syn.Name(),
		"runes", n, "bytes", len(audio.Data), "duration_ms", time.Since(start).Milliseconds())
	h := w.Header()
	h.Set("Content-Type", audio.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(audio.Data)))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio.Data)
}

// ---------- 实时识别 ----------

// speechEvent 是发给客户端的事件：ready（可以发音频）、partial（到目前为止的整句）、final（最终结果）、
// end_of_input（服务端结束了输入，例如达到时长上限）、closed（会话结束，每个会话最后一条）、error（code + message）。
type speechEvent struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Seq        int    `json:"seq,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
	Reason     string `json:"reason,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
	MaxSeconds int    `json:"max_seconds,omitempty"`
}

// speechSessions 记录正在进行的实时识别（每个账户同时最多一个）。
type speechSessions struct {
	mu     sync.Mutex
	active map[string]bool
}

func (ss *speechSessions) acquire(account string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.active == nil {
		ss.active = map[string]bool{}
	}
	if ss.active[account] {
		return false
	}
	ss.active[account] = true
	return true
}

func (ss *speechSessions) release(account string) {
	ss.mu.Lock()
	delete(ss.active, account)
	ss.mu.Unlock()
}

// handleSpeechRealtime 把客户端的实时语音（WebSocket，二进制帧为 16kHz 单声道 16 位 PCM，文本帧为控制消息
// {"type":"start"|"end"|"cancel"}）转发给识别供应商，结果以 JSON 文本帧推回。需要登录（Authorization 头）。
func (s *Server) handleSpeechRealtime(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	if sr := r.URL.Query().Get("sample_rate"); sr != "" && sr != strconv.Itoa(speech.SampleRate) {
		writeError(w, fieldError("sample_rate", "只支持 16000Hz、单声道、16 位 PCM"))
		return
	}
	rec := s.speech.Recognizer
	if rec == nil {
		writeError(w, ErrSpeechNotEnabled)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: "需要 WebSocket 连接"})
		return
	}
	if !s.speechSessions.acquire(acc.AccountID) {
		writeError(w, ErrSpeechBusy)
		return
	}
	defer s.speechSessions.release(acc.AccountID)
	settings := settingsFromContext(r.Context())
	websocket.Server{
		// 原生 App 不带 Origin；浏览器只允许 CORS 白名单里的来源，防止跨站发起连接。
		Handshake: func(_ *websocket.Config, req *http.Request) error {
			if o := req.Header.Get("Origin"); o != "" && matchOrigin(settings.CORSAllowedOrigins, o) == "" {
				return errors.New("origin not allowed")
			}
			return nil
		},
		Handler: func(conn *websocket.Conn) { s.runSpeechSession(r, acc.AccountID, rec, conn) },
	}.ServeHTTP(w, r)
}

func (s *Server) runSpeechSession(r *http.Request, accountID string, rec speech.Recognizer, conn *websocket.Conn) {
	defer conn.Close()
	conn.MaxPayloadBytes = maxAudioFrame
	o := s.speech
	maxBytes := o.maxSeconds() * speech.BytesPerSecond
	reqID := requestIDFromContext(r.Context())
	// 会话总时限：最长输入 + 等待结果；客户端断开或时限到都会取消供应商连接
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Duration(o.maxSeconds())*time.Second+speechTail)
	defer cancel()
	start := time.Now()

	var writeMu sync.Mutex
	send := func(ev speechEvent) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(o.writeTimeout()))
		err := websocket.JSON.Send(conn, ev)
		// 发完清掉写超时：x/net 在读取时自动回 pong（客户端心跳），残留的过期写超时会让 pong 失败、整个会话中断
		_ = conn.SetWriteDeadline(time.Time{})
		return err
	}
	fail := func(code, msg string) { _ = send(speechEvent{Type: "error", Code: code, Message: msg}) }

	stream, err := rec.Start(ctx, reqID)
	if err != nil {
		s.logger.WarnContext(ctx, "speech provider start failed", "request_id", reqID, "provider", rec.Name(), "error", speech.Redact(err))
		fail("speech_unavailable", "语音识别服务暂时不可用，请稍后再试或用文字提问")
		_ = send(speechEvent{Type: "closed"})
		return
	}
	defer stream.Close()
	if err := send(speechEvent{Type: "ready", SampleRate: speech.SampleRate, MaxSeconds: o.maxSeconds()}); err != nil {
		return
	}

	// 结果转发：供应商的 partial / final 依次推给客户端；final 之后结束会话
	results := make(chan struct{})
	var finalRunes atomic.Int64
	go func() {
		defer close(results)
		seq := 0
		for {
			ev, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, speech.ErrClosed) && ctx.Err() == nil {
					s.logger.WarnContext(ctx, "speech provider result failed", "request_id", reqID, "error", speech.Redact(err))
					fail("speech_failed", "语音识别出错了，请重试")
				}
				return
			}
			seq++
			if err := send(speechEvent{Type: ev.Type, Text: ev.Text, Seq: seq}); err != nil {
				return
			}
			if ev.Type == "final" {
				finalRunes.Store(int64(utf8.RuneCountInString(ev.Text)))
				return
			}
		}
	}()

	received, ended, outcome := 0, false, "final"
	readError := ""             // 读客户端出错时的错误（不含音频和转写内容），排查用
	var lastAudio time.Duration // 最后一帧音频到达的时间（相对会话开始），排查空闲超时用
	inputs := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		for {
			_ = conn.SetReadDeadline(time.Now().Add(o.idle()))
			var msg []byte
			if err := websocket.Message.Receive(conn, &msg); err != nil {
				readErr <- err
				return
			}
			select {
			case inputs <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	endInput := func(reason string) {
		if ended {
			return
		}
		ended = true
		if reason != "" {
			_ = send(speechEvent{Type: "end_of_input", Reason: reason})
		}
		if err := stream.End(); err != nil && !errors.Is(err, speech.ErrClosed) {
			s.logger.WarnContext(ctx, "speech provider end failed", "request_id", reqID, "error", speech.Redact(err))
		}
	}
loop:
	for {
		select {
		case <-results:
			break loop
		case <-ctx.Done():
			outcome = "timeout"
			fail("speech_timeout", "语音输入超时了，请重试")
			break loop
		case err := <-readErr:
			readError = err.Error()
			if ended {
				// 客户端在等结果时断开 / 不再发消息：等结果转发结束（或会话时限）
				select {
				case <-results:
				case <-ctx.Done():
				}
				if ctx.Err() != nil {
					outcome = "timeout"
				}
				break loop
			}
			if errors.Is(err, websocket.ErrFrameTooLarge) {
				outcome = "frame_too_large"
				fail("frame_too_large", "单帧音频太大（最多 64KB）")
			} else if isReadTimeout(err) {
				outcome = "idle"
				fail("idle_timeout", "很久没有收到声音，已停止语音输入")
			} else {
				outcome = "client_closed"
			}
			break loop
		case msg := <-inputs:
			if ctrl, ok := parseSpeechControl(msg); ok {
				switch ctrl {
				case "end":
					endInput("")
				case "cancel":
					outcome = "cancelled"
					break loop
				}
				continue
			}
			if ended || len(msg) == 0 {
				continue
			}
			if received+len(msg) > maxBytes {
				msg = msg[:maxBytes-received]
			}
			received += len(msg)
			lastAudio = time.Since(start)
			if len(msg) > 0 {
				if err := stream.Send(msg); err != nil {
					s.logger.WarnContext(ctx, "speech provider send failed", "request_id", reqID, "error", speech.Redact(err))
					outcome = "provider_error"
					fail("speech_failed", "语音识别出错了，请重试")
					break loop
				}
			}
			if received >= maxBytes {
				endInput("max_duration")
			}
		}
	}
	cancel()
	_ = send(speechEvent{Type: "closed"})
	s.logger.InfoContext(r.Context(), "speech session", "request_id", reqID, "account_id", accountID, "provider", rec.Name(), "outcome", outcome,
		"audio_bytes", received, "audio_ms", received*1000/speech.BytesPerSecond, "last_audio_ms", lastAudio.Milliseconds(), "read_error", readError, "result_runes", finalRunes.Load(), "duration_ms", time.Since(start).Milliseconds())
}

// parseSpeechControl 识别控制消息：短小的 JSON 对象且带 type 字段（音频帧不会是合法 JSON 对象）。
func parseSpeechControl(msg []byte) (string, bool) {
	if len(msg) == 0 || len(msg) > 256 || msg[0] != '{' {
		return "", false
	}
	var c struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg, &c) != nil || c.Type == "" {
		return "", false
	}
	return c.Type, true
}

// isReadTimeout：读客户端超时（空闲）。写超时等其他错误不算空闲。
func isReadTimeout(err error) bool {
	var ne *net.OpError
	return errors.As(err, &ne) && ne.Op == "read" && ne.Timeout()
}
