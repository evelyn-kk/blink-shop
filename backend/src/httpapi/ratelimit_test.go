package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRateLimiterWindow(t *testing.T) {
	l := newRateLimiter(time.Minute)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("k", 3, t0.Add(time.Duration(i)*time.Second)); !ok {
			t.Fatalf("request %d should pass", i+1)
		}
	}
	ok, retry := l.allow("k", 3, t0.Add(20*time.Second))
	if ok {
		t.Fatal("4th request in window should be limited")
	}
	if retry != 40*time.Second {
		t.Fatalf("retry = %v, want 40s", retry)
	}
	// 限额调低立即生效
	if ok, _ := l.allow("other", 1, t0); !ok {
		t.Fatal("first request of other key should pass")
	}
	if ok, _ := l.allow("other", 1, t0.Add(time.Second)); ok {
		t.Fatal("limit 1 should block second request")
	}
	// 窗口过后重新计数
	if ok, _ := l.allow("k", 3, t0.Add(time.Minute)); !ok {
		t.Fatal("new window should pass")
	}
}

func TestRateLimiterSweepsExpiredKeys(t *testing.T) {
	l := newRateLimiter(time.Minute)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, k := range []string{"a", "b", "c"} {
		l.allow(k, 10, t0)
	}
	l.allow("d", 10, t0.Add(2*time.Minute))
	if len(l.clients) != 1 {
		t.Fatalf("expired keys not swept: %d left", len(l.clients))
	}
}

// fakeCounter 是共享计数的替身：可以让它出错，记录被调用次数。
type fakeCounter struct {
	mu     sync.Mutex
	counts map[string]int64
	fail   bool
	calls  int
}

func (f *fakeCounter) incr(_ context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		return 0, 0, errors.New("dial tcp 127.0.0.1:6379: connection refused")
	}
	if f.counts == nil {
		f.counts = map[string]int64{}
	}
	f.counts[key]++
	return f.counts[key], window / 2, nil
}

// 共享计数：多个实例用同一个计数（两个 sharedLimiter 共用一个 counter 模拟两台实例）；Redis 出错时退回进程内计数
// （照样限流，不是放开），1 秒内不再试 Redis，之后恢复；指标记后端状态和退回次数。
func TestSharedLimiterFallsBackAndRecovers(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	fc := &fakeCounter{}
	a := &sharedLimiter{name: "t_shared", window: time.Minute, counter: fc, fallback: newRateLimiter(time.Minute), logger: logger}
	b := &sharedLimiter{name: "t_shared", window: time.Minute, counter: fc, fallback: newRateLimiter(time.Minute), logger: logger}
	now := time.Unix(1_800_000_000, 0)
	allowed := 0
	for i := 0; i < 10; i++ {
		l := a
		if i%2 == 1 {
			l = b
		}
		if ok, _ := l.allow("ip:1.2.3.4", 4, now); ok {
			allowed++
		}
	}
	if allowed != 4 {
		t.Fatalf("shared window across instances allowed %d, want 4", allowed)
	}
	if ok, retry := a.allow("ip:1.2.3.4", 4, now); ok || retry != 30*time.Second {
		t.Fatalf("over limit: ok=%v retry=%v", ok, retry)
	}

	// Redis 挂了：退回进程内计数，按同样的 limit 限流
	fc.fail = true
	before := rateLimitFallback.Value("t_shared")
	allowed = 0
	for i := 0; i < 6; i++ {
		if ok, _ := a.allow("ip:5.6.7.8", 3, now); ok {
			allowed++
		}
	}
	if allowed != 3 || rateLimitFallback.Value("t_shared")-before != 6 || rateLimitBackend.Value("t_shared") != 0 {
		t.Fatalf("fallback allowed=%d fallbacks=%v backend=%v", allowed, rateLimitFallback.Value("t_shared")-before, rateLimitBackend.Value("t_shared"))
	}
	if fc.calls != 11+1 {
		t.Fatalf("redis should be skipped for 1s after an error: calls=%d", fc.calls)
	}
	if !strings.Contains(logs.String(), "falls back") || strings.Count(logs.String(), "falls back") != 1 {
		t.Fatalf("log once: %s", logs.String())
	}
	// 1 秒后恢复：再用共享计数
	fc.fail = false
	if ok, _ := a.allow("ip:9.9.9.9", 3, now.Add(1100*time.Millisecond)); !ok || rateLimitBackend.Value("t_shared") != 1 || fc.calls != 13 {
		t.Fatalf("recover: ok=%v backend=%v calls=%d", ok, rateLimitBackend.Value("t_shared"), fc.calls)
	}
	// 没配置 Redis：进程内限流，后端指标为 0
	if _, ok := newLimiter("t_mem", time.Minute, nil, logger).(*rateLimiter); !ok || rateLimitBackend.Value("t_mem") != 0 {
		t.Fatal("nil redis should give the in-process limiter")
	}
}

// 真实 Redis（BLINK_TEST_REDIS_ADDR；CI 没有 Redis 时跳过）：两个 API 实例共用 Redis，同一 IP 每分钟限 3 次，
// 交替打 6 次只有 3 次成功；Lua 脚本给窗口键设了过期。
func TestRedisRateLimitAcrossInstances(t *testing.T) {
	addr := os.Getenv("BLINK_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("BLINK_TEST_REDIS_ADDR 未设置")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { rdb.Close() })
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	env := map[string]string{"RATE_LIMIT_IP_PER_MINUTE": "3"}
	s1, s2 := newTestServer(t, env, nil, nil), newTestServer(t, env, nil, nil)
	for _, s := range []*testServer{s1, s2} {
		s.ipLimiter = newLimiter("ip", time.Minute, rdb, slog.New(slog.DiscardHandler))
	}
	ip := fmt.Sprintf("198.51.100.%d", 1+time.Now().UnixNano()%200) // 每次运行换个 IP，避免和上一次的窗口撞上
	var codes []int
	for i := 0; i < 6; i++ {
		s := s1
		if i%2 == 1 {
			s = s2
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/products", nil)
		req.RemoteAddr = ip + ":5000"
		codes = append(codes, s.do(req).Code)
	}
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 3 {
		t.Fatalf("codes across instances = %v, want exactly 3 × 200", codes)
	}
	keys, err := rdb.Keys(t.Context(), "blink_shop:rl:ip:"+ip+":*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v %v", keys, err)
	}
	if ttl := rdb.PTTL(t.Context(), keys[0]).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("window key ttl = %v", ttl)
	}
}
