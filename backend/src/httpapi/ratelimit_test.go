package httpapi

import (
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
