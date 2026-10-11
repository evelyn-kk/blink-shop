package httpapi

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/metrics"
)

// MetricsOptions 控制 GET /metrics 的访问：配置了 Token 时必须带 `Authorization: Bearer <Token>`；
// 没配置时 AllowLoopback 为 true（开发环境）只允许本机直连（看 TCP 来源，不看 X-Forwarded-For），否则返回 404（生产环境不开放）。
type MetricsOptions struct {
	Token         string
	AllowLoopback bool
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsAllowed(r) {
		writeError(w, ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := metrics.Default.WritePrometheus(w); err != nil {
		s.logger.WarnContext(r.Context(), "write metrics", "error", err)
	}
}

func (s *Server) metricsAllowed(r *http.Request) bool {
	if t := s.metricsOpts.Token; t != "" {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return ok && subtle.ConstantTimeCompare([]byte(got), []byte(t)) == 1
	}
	if !s.metricsOpts.AllowLoopback {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}
