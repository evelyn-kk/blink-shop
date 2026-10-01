// Package httpapi 负责 HTTP 路由、中间件和 JSON 输入输出。
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
)

// SettingsProvider 返回当前生效的 HTTP 配置；由 configcenter.HTTPSettingsProvider 实现。
type SettingsProvider interface {
	Current(ctx context.Context) configcenter.HTTPSettings
}

// ReadinessCheck 是 /ready 检查的一项依赖，Check 返回 nil 表示可用。
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// Options 是 Server 的全部依赖。
type Options struct {
	Logger    *slog.Logger
	Settings  SettingsProvider
	Readiness []ReadinessCheck
}

type Server struct {
	logger         *slog.Logger
	settings       SettingsProvider
	readiness      []ReadinessCheck
	mux            *http.ServeMux
	ipLimiter      *rateLimiter
	accountLimiter *rateLimiter
}

const readinessTimeout = 2 * time.Second

func NewServer(opts Options) *Server {
	s := &Server{
		logger:         opts.Logger,
		settings:       opts.Settings,
		readiness:      opts.Readiness,
		mux:            http.NewServeMux(),
		ipLimiter:      newRateLimiter(time.Minute),
		accountLimiter: newRateLimiter(time.Minute),
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/ready", s.handleReady)
}

// Handler 返回带完整中间件链的 handler。顺序（外 → 内）：
// request id/配置 → 访问日志 → panic 恢复 → CORS → IP 限流 → 请求体限制 → 超时 → [认证，1.2] → 账号限流 → 路由。
func (s *Server) Handler() http.Handler {
	var h http.Handler = http.HandlerFunc(s.dispatch)
	h = s.withAccountRateLimit(h)
	h = s.withTimeout(h)
	h = s.withBodyLimit(h)
	h = s.withIPRateLimit(h)
	h = s.withCORS(h)
	h = s.withRecover(h)
	h = s.withAccessLog(h)
	h = s.withRequestContext(h)
	return h
}

// dispatch 交给 ServeMux 路由；未命中的路径和方法统一输出 JSON 错误，而不是 ServeMux 默认的纯文本。
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if h, pattern := s.mux.Handler(r); pattern == "" {
		probe := &statusProbe{header: http.Header{}}
		h.ServeHTTP(probe, r)
		if probe.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", probe.header.Get("Allow"))
			writeError(w, ErrMethodNotAllow)
			return
		}
		writeError(w, ErrNotFound)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// statusProbe 只记录 ServeMux 内置 404/405 handler 写出的状态码和 Allow 头。
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header         { return p.header }
func (p *statusProbe) Write(b []byte) (int, error) { return len(b), nil }
func (p *statusProbe) WriteHeader(status int)      { p.status = status }

type healthResponse struct {
	Status string `json:"status"`
}

// handleHealth 只表示进程存活，不检查任何依赖。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// handleReady 依次检查依赖（当前为 MySQL）；任一不可用返回 503。具体错误只写日志。
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	checks := make(map[string]string, len(s.readiness))
	for _, c := range s.readiness {
		if err := c.Check(ctx); err != nil {
			s.logger.WarnContext(r.Context(), "readiness check failed",
				"request_id", requestIDFromContext(r.Context()), "check", c.Name, "error", err)
			writeError(w, &APIError{http.StatusServiceUnavailable, ErrNotReady.Code, "依赖 " + c.Name + " 不可用"})
			return
		}
		checks[c.Name] = "ok"
	}
	writeJSON(w, http.StatusOK, readyResponse{Status: "ready", Checks: checks})
}
