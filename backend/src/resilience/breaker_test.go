package resilience

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBreaker(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	b := NewBreaker("test_dep", 3, 10*time.Second)
	b.SetClock(func() time.Time { return now })
	boom := errors.New("boom")
	// 偶发失败不打开；成功清零
	b.Record(boom)
	b.Record(boom)
	b.Record(nil)
	b.Record(boom)
	b.Record(boom)
	if !b.Allow() || b.Open() {
		t.Fatal("should still be closed")
	}
	// 主动取消不计入
	b.Record(context.Canceled)
	if b.Open() {
		t.Fatal("cancel counted as failure")
	}
	b.Record(boom)
	if !b.Open() || b.Allow() {
		t.Fatal("should open after 3 consecutive failures")
	}
	// 冷却期内拒绝；过后只放一个探测
	now = now.Add(11 * time.Second)
	if !b.Allow() {
		t.Fatal("probe should be allowed")
	}
	if b.Allow() {
		t.Fatal("only one probe at a time")
	}
	// 探测失败：再打开一个冷却期
	b.Record(boom)
	if b.Allow() {
		t.Fatal("failed probe should reopen")
	}
	now = now.Add(11 * time.Second)
	if !b.Allow() {
		t.Fatal("second probe")
	}
	b.Record(nil)
	if b.Open() || !b.Allow() || !b.Allow() {
		t.Fatal("successful probe should close")
	}
	var nilBreaker *Breaker
	if !nilBreaker.Allow() {
		t.Fatal("nil breaker allows everything")
	}
	nilBreaker.Record(boom)
}

func TestBreakerConcurrentProbe(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	b := NewBreaker("test_dep2", 1, time.Second)
	b.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	b.Record(errors.New("boom"))
	mu.Lock()
	now = now.Add(2 * time.Second)
	mu.Unlock()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Allow() {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("allowed %d probes, want 1", allowed.Load())
	}
}
