package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
)

const headerRequestID = "X-Request-ID"

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxSettings
	ctxState
	ctxAccount // 认证中间件写入的 domain.Account
)

// requestState 由最外层中间件创建，内层（认证）写入、外层（访问日志）读取。
type requestState struct {
	accountID string
}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

func settingsFromContext(ctx context.Context) configcenter.HTTPSettings {
	s, _ := ctx.Value(ctxSettings).(configcenter.HTTPSettings)
	return s
}

func accountIDFromContext(ctx context.Context) (string, bool) {
	st, _ := ctx.Value(ctxState).(*requestState)
	if st == nil || st.accountID == "" {
		return "", false
	}
	return st.accountID, true
}

// 客户端传入的 request id 只接受安全字符，避免日志注入。
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,64}$`)

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:]) // Go 1.24 起 crypto/rand.Read 不会返回错误
	return hex.EncodeToString(b[:])
}

// withRequestContext 分配 request id，并把本次请求生效的 HTTP 配置放进 context，后续中间件共用同一份。
func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(headerRequestID, id)
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		ctx = context.WithValue(ctx, ctxSettings, s.settings.Current(ctx))
		ctx = context.WithValue(ctx, ctxState, &requestState{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withAccessLog 每个请求结束后写一条结构化访问日志。字段说明见 backend/README.md。
func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.Status() >= 500 {
			level = slog.LevelError
		}
		attrs := []slog.Attr{
			slog.String("request_id", requestIDFromContext(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.Status()),
			slog.Int64("bytes", rec.bytes),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("client_ip", clientIP(r, settingsFromContext(r.Context()))),
			slog.String("user_agent", r.UserAgent()),
		}
		if id, ok := accountIDFromContext(r.Context()); ok {
			attrs = append(attrs, slog.String("account_id", id))
		}
		s.logger.LogAttrs(r.Context(), level, "http request", attrs...)
	})
}

// withRecover 捕获 handler panic，记录堆栈并返回 500 JSON，不把堆栈暴露给客户端。
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if p == http.ErrAbortHandler {
				panic(p)
			}
			s.logger.ErrorContext(r.Context(), "panic recovered",
				"request_id", requestIDFromContext(r.Context()),
				"panic", fmt.Sprint(p),
				"stack", string(debug.Stack()),
			)
			if !headerWritten(w) {
				writeError(w, ErrInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const (
	corsAllowMethods = "GET, POST, PATCH, DELETE, OPTIONS"
	corsAllowHeaders = "Content-Type, Accept, Authorization, X-Request-ID"
)

// withCORS 按白名单回写 Access-Control-Allow-Origin；OPTIONS 预检直接 204。
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		h := w.Header()
		if origin != "" {
			h.Add("Vary", "Origin")
			if allowed := matchOrigin(settingsFromContext(r.Context()).CORSAllowedOrigins, origin); allowed != "" {
				h.Set("Access-Control-Allow-Origin", allowed)
				h.Set("Access-Control-Expose-Headers", headerRequestID)
			}
		}
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", corsAllowMethods)
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func matchOrigin(allowed []string, origin string) string {
	for _, item := range allowed {
		if item == "*" {
			return "*"
		}
		if strings.EqualFold(strings.TrimRight(item, "/"), origin) {
			return origin
		}
	}
	return ""
}

// 健康检查不参与限流，避免探针被误伤。
func rateLimitExempt(r *http.Request) bool {
	return r.URL.Path == "/api/v1/health" || r.URL.Path == "/api/v1/ready"
}

// withIPRateLimit 是认证前的限流层：按客户端 IP 计数。
func (s *Server) withIPRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		settings := settingsFromContext(r.Context())
		if !rateLimitExempt(r) {
			if ok, retry := s.ipLimiter.allow("ip:"+clientIP(r, settings), settings.RateLimitIPPerMin, time.Now()); !ok {
				writeRateLimited(w, retry)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withAccountRateLimit 是认证后的限流层：已登录请求按账号计数，未登录请求只受 IP 层约束。
func (s *Server) withAccountRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := accountIDFromContext(r.Context()); ok && !rateLimitExempt(r) {
			limit := settingsFromContext(r.Context()).RateLimitAccountPerMin
			if allowed, retry := s.accountLimiter.allow("account:"+id, limit, time.Now()); !allowed {
				writeRateLimited(w, retry)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	setRetryAfter(w, retryAfter)
	writeError(w, ErrRateLimited)
}

func setRetryAfter(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

// withBodyLimit 限制请求体：multipart 上传用 UploadMaxBytes，其余用 MaxBodyBytes。
// 声明的 Content-Length 超限时直接 413；未声明长度的请求在读取超限时由 decodeJSON 等返回 413。
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			settings := settingsFromContext(r.Context())
			limit := settings.MaxBodyBytes
			if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType == "multipart/form-data" {
				limit = settings.UploadMaxBytes
			}
			if r.ContentLength > limit {
				writeError(w, ErrPayloadTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// 流式接口（SSE、实时语音）由自身控制生命周期，不套用统一请求超时。
func isStreamingRequest(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, ":stream") || r.URL.Path == "/api/v1/speech/realtime"
}

// withTimeout 给请求 context 设置截止时间。handler 因超时返回且尚未写响应时，统一返回 504。
// 不使用 http.TimeoutHandler：它会缓冲整个响应，无法支持流式输出。
func (s *Server) withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStreamingRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), settingsFromContext(r.Context()).RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !headerWritten(w) {
			writeError(w, ErrTimeout)
		}
	})
}

// clientIP 返回客户端 IP。只有直连方是可信代理时才读取 X-Forwarded-For，
// 并从右往左跳过可信代理，取第一个不可信地址，防止客户端伪造。
func clientIP(r *http.Request, settings configcenter.HTTPSettings) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	remoteIP := net.ParseIP(remote)
	if remoteIP == nil || !(settings.TrustAllProxies || inNets(remoteIP, settings.TrustedProxies)) {
		return remote
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	client := remote
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			break
		}
		client = ip.String()
		if !settings.TrustAllProxies && !inNets(ip, settings.TrustedProxies) {
			break
		}
	}
	return client
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// responseRecorder 记录状态码和字节数，同时保留 Flush 等能力（通过 Unwrap 交给 http.ResponseController）。
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *responseRecorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Status 返回已写出的状态码；handler 什么都没写时按 200 计。
func (r *responseRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (r *responseRecorder) wroteHeader() bool { return r.status != 0 }

func headerWritten(w http.ResponseWriter) bool {
	for {
		switch v := w.(type) {
		case *responseRecorder:
			return v.wroteHeader()
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return false
		}
	}
}
