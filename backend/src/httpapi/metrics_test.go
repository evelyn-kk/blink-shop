package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/metrics"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

func (ts *testServer) scrape(remote, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = remote
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("X-Forwarded-For", "127.0.0.1") // 不能靠伪造的转发头冒充本机
	return ts.do(req)
}

// /metrics 访问控制：生产（不允许本机、没令牌）关闭；开发只允许本机 TCP 直连；配置令牌后必须带对的 Bearer。
func TestMetricsAccess(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	if rec := ts.scrape("127.0.0.1:5000", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("production without token: %d", rec.Code)
	}
	ts.metricsOpts = MetricsOptions{AllowLoopback: true}
	if rec := ts.scrape("127.0.0.1:5000", ""); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("loopback: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := ts.scrape("[::1]:5000", ""); rec.Code != http.StatusOK {
		t.Fatalf("ipv6 loopback: %d", rec.Code)
	}
	if rec := ts.scrape("192.0.2.10:5000", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("remote without token: %d", rec.Code)
	}
	ts.metricsOpts = MetricsOptions{Token: "metrics-token-0123456789abcdef", AllowLoopback: true}
	for _, auth := range []string{"", "Bearer wrong", "metrics-token-0123456789abcdef"} {
		if rec := ts.scrape("127.0.0.1:5000", auth); rec.Code != http.StatusNotFound {
			t.Fatalf("auth %q: %d", auth, rec.Code)
		}
	}
	if rec := ts.scrape("192.0.2.10:5000", "Bearer metrics-token-0123456789abcdef"); rec.Code != http.StatusOK {
		t.Fatalf("right token: %d", rec.Code)
	}
}

// 指标内容：请求按路由模式计数（不是原始路径）；导购运行、首个进展事件、结算结果和 SSE 连接数都有记录。
func TestMetricsRecorded(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	ts.metricsOpts = MetricsOptions{AllowLoopback: true}
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	route := "GET /api/v1/products/{id}"
	before := metrics.HTTPRequests.Value(route, "GET", "2xx")
	beforeNF := metrics.HTTPRequests.Value(route, "GET", "4xx")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_seed_earbuds", "", nil), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_missing", "", nil), http.StatusNotFound, "")
	if metrics.HTTPRequests.Value(route, "GET", "2xx") != before+1 || metrics.HTTPRequests.Value(route, "GET", "4xx") != beforeNF+1 {
		t.Fatal("request not counted by route pattern")
	}
	unmatched := metrics.HTTPRequests.Value("unmatched", "GET", "4xx")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/nope/12345", "", nil), http.StatusNotFound, "")
	if metrics.HTTPRequests.Value("unmatched", "GET", "4xx") != unmatched+1 {
		t.Fatal("unmatched request not counted")
	}

	runs, first := metrics.AgentRuns.Value("completed"), metrics.AgentFirstEvent.Count()
	ts.ask(t, user, "m-1", "推荐一款通勤降噪耳机")
	if metrics.AgentRuns.Value("completed") != runs+1 || metrics.AgentFirstEvent.Count() != first+1 || metrics.SSEActive.Value() != 0 {
		t.Fatalf("agent metrics: runs=%v first=%d sse=%v", metrics.AgentRuns.Value("completed")-runs, metrics.AgentFirstEvent.Count()-first, metrics.SSEActive.Value())
	}

	ok, empty := metrics.Checkouts.Value("success"), metrics.Checkouts.Value("empty_cart")
	ts.addToCart(t, user, map[string]any{"product_id": "p_seed_mouse", "sku_id": "sku_seed_mouse_gray", "quantity": 1})
	expectStatus(t, ts.checkout(t, user, "mk-1", nil), http.StatusCreated, "")
	ts.checkout(t, user, "mk-2", nil) // 购物车已空
	if metrics.Checkouts.Value("success") != ok+1 || metrics.Checkouts.Value("empty_cart") != empty+1 {
		t.Fatalf("checkout metrics: success+%v empty_cart+%v", metrics.Checkouts.Value("success")-ok, metrics.Checkouts.Value("empty_cart")-empty)
	}

	body := ts.scrape("127.0.0.1:5000", "").Body.String()
	for _, want := range []string{
		`blink_http_requests_total{route="GET /api/v1/products/{id}",method="GET",code="2xx"}`,
		`blink_http_request_duration_seconds_bucket{route="GET /api/v1/products/{id}",le="+Inf"}`,
		`blink_agent_runs_total{status="completed"}`, `# TYPE blink_agent_first_event_seconds histogram`,
		`blink_checkout_total{result="success"}`, `blink_sse_active_connections 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
	if strings.Contains(body, "p_seed_earbuds") || strings.Contains(body, "p_missing") {
		t.Fatal("raw path leaked into labels")
	}
	// SSE 流只计数，不进耗时直方图（耗时见运行耗时）
	if strings.Contains(body, `blink_http_request_duration_seconds_count{route="POST /api/v1/agent/sessions/{id}/{action}"}`) {
		t.Fatal("stream duration recorded as request latency")
	}
}

// 摘流：BeginDrain 后 /ready 立即 503，/health 和普通请求照常。
func TestReadyWhileDraining(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/ready", "", nil), http.StatusOK, "")
	ts.BeginDrain()
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/ready", "", nil), http.StatusServiceUnavailable, "not_ready")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/health", "", nil), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_seed_earbuds", "", nil), http.StatusOK, "")
}

// waitFor 等条件成立（最多 3 秒），用于异步结束的连接计数等。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
