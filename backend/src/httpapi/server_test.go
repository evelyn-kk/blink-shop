package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// testNow 是测试中服务端的“当前时间”，落在演示促销的有效期内。
var testNow = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

type testServer struct {
	*Server
	handler http.Handler
	logs    *lockedBuffer
	dynamic *configcenter.MemorySource
	mem     *memstore.Store     // 已写入开发种子
	objects *objectstore.Memory // 默认的对象存储；测试“未配置”时把 Server.objects 置为 nil
}

// newTestServer 用给定环境变量创建 Server；extra 在构建 handler 前注册测试路由。
func newTestServer(t *testing.T, env map[string]string, readiness []ReadinessCheck, extra func(mux *http.ServeMux)) *testServer {
	t.Helper()
	logs := &lockedBuffer{}
	dynamic := configcenter.NewMemorySource(nil)
	resolver := configcenter.NewResolver(func(k string) string { return env[k] }, dynamic)
	production := env["APP_ENV"] == "production"
	mem := memstore.New()
	if _, err := mem.ApplySeed(context.Background(), storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	objects := objectstore.NewMemory()
	s := NewServer(Options{
		Logger:       logging.New(logs, slog.LevelDebug),
		ObjectStore:  objects,
		Settings:     configcenter.NewHTTPSettingsProvider(resolver, production),
		Readiness:    readiness,
		Store:        mem,
		AvatarDir:    t.TempDir(),
		PasswordCost: bcrypt.MinCost,
		Now:          func() time.Time { return testNow },
		Configs:      configcenter.NewAdmin(resolver, dynamic),
		RiskWords:    func(ctx context.Context) []string { return configcenter.RiskBlockedWords(ctx, resolver) },
	})
	if extra != nil {
		extra(s.mux)
	}
	return &testServer{Server: s, handler: s.Handler(), logs: logs, dynamic: dynamic, mem: mem, objects: objects}
}

func (ts *testServer) do(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ts.handler.ServeHTTP(rec, r)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json; body=%s", ct, rec.Body)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v; body=%s", err, rec.Body)
	}
	if body.RequestID == "" || body.RequestID != rec.Header().Get(headerRequestID) {
		t.Errorf("request_id = %q, header = %q", body.RequestID, rec.Header().Get(headerRequestID))
	}
	return body
}

func TestHealthAndReady(t *testing.T) {
	dbDown := errors.New("dial tcp 127.0.0.1:3306: connection refused")
	tests := []struct {
		name       string
		checkErr   error
		path       string
		wantStatus int
		wantBody   string // 成功时期望的 JSON；失败时期望的 code
	}{
		{"health with db up", nil, "/api/v1/health", 200, `{"status":"ok"}`},
		{"health with db down", dbDown, "/api/v1/health", 200, `{"status":"ok"}`},
		{"ready with db up", nil, "/api/v1/ready", 200, `{"status":"ready","checks":{"mysql":"ok"}}`},
		{"ready with db down", dbDown, "/api/v1/ready", 503, "not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, nil, []ReadinessCheck{{Name: "mysql", Check: func(context.Context) error { return tt.checkErr }}}, nil)
			rec := ts.do(httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantStatus == 200 {
				if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
					t.Fatalf("body = %s, want %s", got, tt.wantBody)
				}
				return
			}
			body := decodeError(t, rec)
			if body.Code != tt.wantBody {
				t.Fatalf("code = %q, want %q", body.Code, tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), "connection refused") {
				t.Fatalf("internal error leaked to client: %s", rec.Body)
			}
			if !strings.Contains(ts.logs.String(), "connection refused") {
				t.Fatalf("readiness failure not logged: %s", ts.logs)
			}
		})
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tests := []struct {
		method, path string
		wantStatus   int
		wantCode     string
		wantAllow    string
	}{
		{http.MethodGet, "/api/v1/nope", 404, "not_found", ""},
		{http.MethodGet, "/", 404, "not_found", ""},
		{http.MethodPost, "/api/v1/health", 405, "method_not_allowed", "GET, HEAD"},
		{http.MethodDelete, "/api/v1/ready", 405, "method_not_allowed", "GET, HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := ts.do(httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if body := decodeError(t, rec); body.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, tt.wantCode)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, tt.wantAllow)
			}
		})
	}
}

func TestCORS(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		method      string
		origin      string
		wantStatus  int
		wantAllowed string
		wantVary    bool
	}{
		{"wildcard in dev", nil, http.MethodGet, "http://any.example", 200, "*", true},
		{"no origin header", nil, http.MethodGet, "", 200, "", false},
		{"whitelisted origin", map[string]string{"CORS_ALLOWED_ORIGINS": "http://localhost:5173, https://admin.blink.example"}, http.MethodGet, "https://admin.blink.example", 200, "https://admin.blink.example", true},
		{"origin not in whitelist", map[string]string{"CORS_ALLOWED_ORIGINS": "http://localhost:5173"}, http.MethodGet, "https://evil.example", 200, "", true},
		{"preflight allowed", map[string]string{"CORS_ALLOWED_ORIGINS": "http://localhost:5173"}, http.MethodOptions, "http://localhost:5173", 204, "http://localhost:5173", true},
		{"preflight from unknown origin", map[string]string{"CORS_ALLOWED_ORIGINS": "http://localhost:5173"}, http.MethodOptions, "https://evil.example", 204, "", true},
		{"production ignores wildcard", map[string]string{"APP_ENV": "production", "CORS_ALLOWED_ORIGINS": "*,https://admin.blink.example"}, http.MethodGet, "https://evil.example", 200, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, tt.env, nil, nil)
			req := httptest.NewRequest(tt.method, "/api/v1/health", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}
			rec := ts.do(req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllowed {
				t.Fatalf("Allow-Origin = %q, want %q", got, tt.wantAllowed)
			}
			if got := rec.Header().Get("Vary") == "Origin"; got != tt.wantVary {
				t.Fatalf("Vary Origin = %v, want %v", got, tt.wantVary)
			}
			if tt.method == http.MethodOptions {
				if rec.Header().Get("Access-Control-Allow-Methods") != corsAllowMethods ||
					!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
					t.Fatalf("preflight headers missing: %v", rec.Header())
				}
				if rec.Body.Len() != 0 {
					t.Fatalf("preflight body should be empty, got %q", rec.Body)
				}
			}
		})
	}
}

func TestIPRateLimit(t *testing.T) {
	env := map[string]string{"RATE_LIMIT_IP_PER_MINUTE": "2", "TRUSTED_PROXY_CIDRS": "10.0.0.0/8"}
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		path       string
		wantStatus []int
	}{
		{"third request is limited", "203.0.113.1:5000", "", "/api/v1/nope", []int{404, 404, 429}},
		{"health is exempt", "203.0.113.2:5000", "", "/api/v1/health", []int{200, 200, 200}},
		{"embedded asset GET is exempt", "203.0.113.6:5000", "", "/api/v1/assets/catalog/products/p_seed_nova.png", []int{200, 200, 200}},
		{"avatar GET is exempt", "203.0.113.7:5000", "", "/api/v1/uploads/avatar/acct_x_0123456789abcdef.png", []int{404, 404, 404}},
		{"catalog API is not exempt", "203.0.113.8:5000", "", "/api/v1/products", []int{200, 200, 429}},
		{"preflight is exempt", "203.0.113.3:5000", "", "OPTIONS", []int{204, 204, 204}},
		{"untrusted client cannot spoof XFF", "203.0.113.4:5000", "spoof", "/api/v1/nope", []int{404, 404, 429}},
		{"trusted proxy: different clients separated", "10.0.0.5:5000", "distinct", "/api/v1/nope", []int{404, 404, 404}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, env, nil, nil)
			for i, want := range tt.wantStatus {
				method, path := http.MethodGet, tt.path
				if path == "OPTIONS" {
					method, path = http.MethodOptions, "/api/v1/nope"
				}
				req := httptest.NewRequest(method, path, nil)
				req.RemoteAddr = tt.remoteAddr
				switch tt.xff {
				case "spoof":
					req.Header.Set("X-Forwarded-For", "198.51.100."+string(rune('1'+i)))
				case "distinct":
					req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.9")
					if i == 2 {
						req.Header.Set("X-Forwarded-For", "198.51.100.8, 10.0.0.9")
					}
				}
				rec := ts.do(req)
				if rec.Code != want {
					t.Fatalf("request %d: status = %d, want %d", i+1, rec.Code, want)
				}
				if want == 429 {
					if body := decodeError(t, rec); body.Code != "rate_limited" {
						t.Fatalf("code = %q", body.Code)
					}
					if rec.Header().Get("Retry-After") == "" {
						t.Fatal("missing Retry-After")
					}
				}
			}
		})
	}
}

func TestAccountRateLimit(t *testing.T) {
	ts := newTestServer(t, map[string]string{"RATE_LIMIT_ACCOUNT_PER_MINUTE": "1"}, nil, nil)
	h := ts.withAccountRateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	send := func(account string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cart", nil)
		ctx := context.WithValue(req.Context(), ctxSettings, ts.settings.Current(req.Context()))
		ctx = context.WithValue(ctx, ctxState, &requestState{accountID: account})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code
	}
	steps := []struct {
		account string
		want    int
	}{
		{"u-1", 204},
		{"u-1", 429}, // 同一账号超限
		{"u-2", 204}, // 其他账号不受影响
		{"", 204},    // 未登录请求只走 IP 层
		{"", 204},
	}
	for i, step := range steps {
		if got := send(step.account); got != step.want {
			t.Fatalf("step %d (%q): status = %d, want %d", i+1, step.account, got, step.want)
		}
	}
}

type echoRequest struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func echoRoute(mux *http.ServeMux) {
	mux.HandleFunc("POST /test/echo", func(w http.ResponseWriter, r *http.Request) {
		var req echoRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, req)
	})
	mux.HandleFunc("POST /test/upload", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			writeError(w, mapDecodeError(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// chunkedBody 让请求没有 Content-Length，只能在读取时发现超限。
type chunkedBody struct{ io.Reader }

func TestBodyLimitAndJSONDecoding(t *testing.T) {
	env := map[string]string{"HTTP_MAX_BODY_BYTES": "64", "UPLOAD_MAX_BYTES": "256"}
	big := `{"name":"` + strings.Repeat("x", 100) + `"}`
	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		chunked     bool
		wantStatus  int
		wantCode    string
	}{
		{"valid json", "/test/echo", "application/json", `{"name":"a","count":2}`, false, 200, ""},
		{"json without content type", "/test/echo", "", `{"name":"a"}`, false, 200, ""},
		{"declared length too large", "/test/echo", "application/json", big, false, 413, "payload_too_large"},
		{"chunked body too large", "/test/echo", "application/json", big, true, 413, "payload_too_large"},
		{"malformed json", "/test/echo", "application/json", `{"name":`, false, 400, "invalid_json"},
		{"empty body", "/test/echo", "application/json", ``, false, 400, "invalid_json"},
		{"wrong field type", "/test/echo", "application/json", `{"count":"two"}`, false, 400, "invalid_argument"},
		{"trailing data", "/test/echo", "application/json", `{"name":"a"} {}`, false, 400, "invalid_json"},
		{"wrong content type", "/test/echo", "text/plain", `{"name":"a"}`, false, 415, "unsupported_media_type"},
		{"json with charset", "/test/echo", "application/json; charset=utf-8", `{"name":"a"}`, false, 200, ""},
		{"json media type is case-insensitive", "/test/echo", "Application/JSON; charset=UTF-8", `{"name":"a"}`, false, 200, ""},
		{"jsonp is not json", "/test/echo", "application/jsonp", `{"name":"a"}`, false, 415, "unsupported_media_type"},
		{"json prefix is not json", "/test/echo", "application/json-foo", `{"name":"a"}`, false, 415, "unsupported_media_type"},
		{"vendor json suffix is not json", "/test/echo", "application/vnd.api+json", `{"name":"a"}`, false, 415, "unsupported_media_type"},
		{"malformed content type", "/test/echo", "application/json;;=", `{"name":"a"}`, false, 415, "unsupported_media_type"},
		// multipart 请求体上限 = UPLOAD_MAX_BYTES（单个文件上限）+ 64KB 表单余量。
		{"multipart uses upload limit", "/test/upload", "multipart/form-data; boundary=x", strings.Repeat("y", 256+multipartOverhead), false, 204, ""},
		{"multipart over upload limit", "/test/upload", "multipart/form-data; boundary=x", strings.Repeat("y", 257+multipartOverhead), false, 413, "payload_too_large"},
		{"chunked multipart over limit", "/test/upload", "multipart/form-data; boundary=x", strings.Repeat("y", 257+multipartOverhead), true, 413, "payload_too_large"},
	}
	ts := newTestServer(t, env, nil, echoRoute)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader = strings.NewReader(tt.body)
			if tt.chunked {
				body = chunkedBody{body}
			}
			req := httptest.NewRequest(http.MethodPost, tt.path, body)
			if tt.chunked {
				req.ContentLength = -1
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := ts.do(req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantCode != "" {
				if got := decodeError(t, rec).Code; got != tt.wantCode {
					t.Fatalf("code = %q, want %q", got, tt.wantCode)
				}
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	generated := regexp.MustCompile(`^[0-9a-f]{24}$`)
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{"generated when missing", "", false},
		{"client id kept", "abc-123_X.y", true},
		{"unsafe id replaced", "bad id\nINJECT", false},
		{"too long id replaced", strings.Repeat("a", 65), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
			if tt.incoming != "" {
				req.Header.Set(headerRequestID, tt.incoming)
			}
			rec := ts.do(req)
			got := rec.Header().Get(headerRequestID)
			if tt.keep && got != tt.incoming {
				t.Fatalf("request id = %q, want %q", got, tt.incoming)
			}
			if !tt.keep && !generated.MatchString(got) {
				t.Fatalf("request id = %q, want generated hex", got)
			}
			if body := decodeError(t, rec); body.RequestID != got {
				t.Fatalf("body request_id = %q, header = %q", body.RequestID, got)
			}
		})
	}
}

func TestPanicRecovery(t *testing.T) {
	ts := newTestServer(t, nil, nil, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /test/panic", func(http.ResponseWriter, *http.Request) {
			panic("boom: secret internal state")
		})
	})
	rec := ts.do(httptest.NewRequest(http.MethodGet, "/test/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := decodeError(t, rec); body.Code != "internal_error" {
		t.Fatalf("code = %q", body.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("panic detail leaked: %s", rec.Body)
	}
	logs := ts.logs.String()
	if !strings.Contains(logs, "panic recovered") || !strings.Contains(logs, "boom") || !strings.Contains(logs, "goroutine") {
		t.Fatalf("panic not logged with stack: %s", logs)
	}
	if !strings.Contains(logs, `"status":500`) {
		t.Fatalf("access log should record 500: %s", logs)
	}
}

func TestTimeout(t *testing.T) {
	ts := newTestServer(t, map[string]string{"HTTP_REQUEST_TIMEOUT": "30ms"}, nil, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /test/slow", func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done() // 遵守 context 的 handler 超时后直接返回
		})
		mux.HandleFunc("POST /api/v1/agent/sessions/{id}/messages:stream", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := r.Context().Deadline(); ok {
				t.Error("streaming request should not have a deadline")
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})

	start := time.Now()
	rec := ts.do(httptest.NewRequest(http.MethodGet, "/test/slow", nil))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	if body := decodeError(t, rec); body.Code != "timeout" {
		t.Fatalf("code = %q", body.Code)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}

	rec = ts.do(httptest.NewRequest(http.MethodPost, "/api/v1/agent/sessions/1/messages:stream", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("stream status = %d, want 204", rec.Code)
	}
}

func TestAccessLog(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health?token=query-secret", nil)
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("User-Agent", "blink-test")
	req.Header.Set(headerRequestID, "req-1")
	req.RemoteAddr = "192.0.2.10:1234"
	ts.do(req)

	var entry map[string]any
	line := strings.TrimSpace(ts.logs.String())
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("access log is not one JSON line: %v\n%s", err, line)
	}
	want := map[string]any{
		"msg": "http request", "request_id": "req-1", "method": "GET", "path": "/api/v1/health",
		"status": float64(200), "client_ip": "192.0.2.10", "user_agent": "blink-test",
	}
	for k, v := range want {
		if entry[k] != v {
			t.Errorf("%s = %v, want %v", k, entry[k], v)
		}
	}
	for _, k := range []string{"duration_ms", "bytes", "time", "level"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("missing field %s", k)
		}
	}
	if strings.Contains(line, "query-secret") || strings.Contains(line, "header-secret") {
		t.Fatalf("secret leaked into access log: %s", line)
	}
}

func TestClientIP(t *testing.T) {
	trusted, _ := configcenter.NewHTTPSettingsProvider(configcenter.NewResolver(func(k string) string {
		return map[string]string{"TRUSTED_PROXY_CIDRS": "10.0.0.0/8, 192.168.1.1"}[k]
	}, nil), false).Current(context.Background()), 0
	trustAll := trusted
	trustAll.TrustAllProxies = true

	tests := []struct {
		name     string
		settings configcenter.HTTPSettings
		remote   string
		xff      string
		want     string
	}{
		{"direct client", trusted, "203.0.113.9:1", "", "203.0.113.9"},
		{"untrusted remote ignores xff", trusted, "203.0.113.9:1", "1.2.3.4", "203.0.113.9"},
		{"trusted proxy uses xff", trusted, "10.1.1.1:1", "198.51.100.1", "198.51.100.1"},
		{"skip trusted hops from right", trusted, "10.1.1.1:1", "198.51.100.1, 10.2.2.2, 192.168.1.1", "198.51.100.1"},
		{"client cannot prepend fake ip", trusted, "10.1.1.1:1", "6.6.6.6, 198.51.100.1", "198.51.100.1"},
		{"garbage xff stops walk", trusted, "10.1.1.1:1", "not-an-ip", "10.1.1.1"},
		{"trust all uses leftmost", trustAll, "203.0.113.9:1", "198.51.100.1, 203.0.113.50", "198.51.100.1"},
		{"ipv6 remote", trusted, "[2001:db8::1]:443", "", "2001:db8::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(req, tt.settings); got != tt.want {
				t.Fatalf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDynamicSettingsApplyWithoutRestart(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	req := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		r.Header.Set("Origin", "https://admin.blink.example")
		return ts.do(r)
	}
	if got := req().Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("default Allow-Origin = %q, want *", got)
	}
	ts.dynamic.Set(configcenter.KeyCORSAllowedOrigins.Name, "https://other.example")
	if got := req().Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("after dynamic update Allow-Origin = %q, want empty", got)
	}
}

// lockedBuffer 是并发安全的日志缓冲：WebSocket 等被接管的连接在测试读取日志时可能还在写访问日志。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
