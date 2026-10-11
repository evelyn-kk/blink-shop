package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestWritePrometheus(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("t_requests_total", "请求数", "route", "code")
	h := r.Histogram("t_duration_seconds", "耗时", []float64{0.1, 1}, "route")
	g := r.Gauge("t_active", "活跃数")
	r.GaugeFunc("t_pool_open", "连接数", func() float64 { return 1 })
	r.GaugeFunc("t_pool_open", "连接数", func() float64 { return 3 }) // 同名替换，不重复输出
	c.Inc("GET /a", "2xx")
	c.Add(2, "GET /a", "2xx")
	c.Inc(`GET /"b"`, "5xx")
	h.Observe(0.05, "GET /a")
	h.Observe(0.1, "GET /a") // 恰好等于上界算在该桶（le 是 ≤）
	h.Observe(3, "GET /a")
	g.Add(2)
	g.Add(-1)
	var b strings.Builder
	if err := r.WritePrometheus(&b); err != nil {
		t.Fatal(err)
	}
	want := `# HELP t_requests_total 请求数
# TYPE t_requests_total counter
t_requests_total{route="GET /\"b\"",code="5xx"} 1
t_requests_total{route="GET /a",code="2xx"} 3
# HELP t_duration_seconds 耗时
# TYPE t_duration_seconds histogram
t_duration_seconds_bucket{route="GET /a",le="0.1"} 2
t_duration_seconds_bucket{route="GET /a",le="1"} 2
t_duration_seconds_bucket{route="GET /a",le="+Inf"} 3
t_duration_seconds_sum{route="GET /a"} 3.15
t_duration_seconds_count{route="GET /a"} 3
# HELP t_active 活跃数
# TYPE t_active gauge
t_active 1
# HELP t_pool_open 连接数
# TYPE t_pool_open gauge
t_pool_open 3
`
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestConcurrentWrites(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("t_total", "x", "k")
	h := r.Histogram("t_h", "x", nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				c.Inc(fmt.Sprint(i % 3))
				h.Observe(0.2)
			}
			_ = r.WritePrometheus(&strings.Builder{})
		}()
	}
	wg.Wait()
	if total := c.Value("0") + c.Value("1") + c.Value("2"); total != 5000 || h.Count() != 5000 {
		t.Fatalf("total=%v count=%d", total, h.Count())
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }

func TestResultOf(t *testing.T) {
	for err, want := range map[error]string{
		nil: "ok", context.Canceled: "canceled", fmt.Errorf("x: %w", context.DeadlineExceeded): "timeout",
		timeoutErr{}: "timeout", errors.New("boom"): "error",
	} {
		if got := ResultOf(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}

func TestMismatchedRegistrationPanics(t *testing.T) {
	r := NewRegistry()
	r.Counter("t_x", "x", "a")
	defer func() {
		if recover() == nil {
			t.Fatal("want panic")
		}
	}()
	r.Gauge("t_x", "x", "a")
}
