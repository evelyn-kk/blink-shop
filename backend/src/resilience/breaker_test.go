package resilience

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var boom = errors.New("boom")

func newTestBreaker(t *testing.T, threshold int) (*Breaker, func(time.Duration)) {
	t.Helper()
	var mu sync.Mutex
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	b := NewBreaker("test_"+t.Name(), threshold, 10*time.Second)
	b.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	return b, func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
}

// call 发起一次调用并立即以 err 结束；返回是否放行。
func call(b *Breaker, err error) bool {
	done, ok := b.Start()
	if ok {
		done(err)
	}
	return ok
}

func TestBreaker(t *testing.T) {
	b, advance := newTestBreaker(t, 3)
	// 偶发失败不打开；成功清零
	call(b, boom)
	call(b, boom)
	call(b, nil)
	call(b, boom)
	call(b, boom)
	if b.Open() || !call(b, context.Canceled) || b.Open() {
		t.Fatal("should still be closed; cancel must not count")
	}
	call(b, boom)
	if !b.Open() || call(b, nil) {
		t.Fatal("should open after 3 consecutive failures")
	}
	// 冷却期内拒绝；过后只放一个探测
	advance(11 * time.Second)
	probe, ok := b.Start()
	if !ok {
		t.Fatal("probe should be allowed")
	}
	if _, ok := b.Start(); ok {
		t.Fatal("only one probe at a time")
	}
	// 探测失败：再打开一个冷却期
	probe(boom)
	if call(b, nil) {
		t.Fatal("failed probe should reopen")
	}
	advance(11 * time.Second)
	if !call(b, nil) || b.Open() || !call(b, nil) {
		t.Fatal("successful probe should close")
	}
	var nilBreaker *Breaker
	if done, ok := nilBreaker.Start(); !ok {
		t.Fatal("nil breaker allows everything")
	} else {
		done(boom)
	}
}

// REV-024：探测被取消（客户端断开 / 上游取消）后不能卡住——下一次调用可以重新探测，熔断器保持打开，取消不计入失败。
func TestBreakerCancelledProbe(t *testing.T) {
	b, advance := newTestBreaker(t, 1)
	call(b, boom)
	advance(11 * time.Second)
	probe, ok := b.Start()
	if !ok {
		t.Fatal("probe should be allowed")
	}
	probe(context.Canceled)
	if !b.Open() {
		t.Fatal("cancelled probe must not close the breaker")
	}
	next, ok := b.Start()
	if !ok {
		t.Fatal("after a cancelled probe a new probe must be allowed")
	}
	next(nil)
	if b.Open() {
		t.Fatal("successful probe should close")
	}
	// done 只生效一次
	probe(nil)
	probe(boom)
	if b.Open() {
		t.Fatal("repeated done changed state")
	}
}

// 打开之前就发出的旧调用在打开后才结束：既不能清掉正在进行的探测（放出第二个探测），也不能用旧的成功把熔断器关掉。
func TestBreakerIgnoresCallsFromBeforeOpening(t *testing.T) {
	b, advance := newTestBreaker(t, 1)
	slowOK, ok := b.Start() // 熔断前发出、还没结束
	if !ok {
		t.Fatal("closed breaker should allow")
	}
	slowFail, _ := b.Start()
	call(b, boom) // 打开
	advance(11 * time.Second)
	probe, ok := b.Start()
	if !ok {
		t.Fatal("probe should be allowed")
	}
	slowOK(nil)
	if !b.Open() {
		t.Fatal("stale success closed the breaker")
	}
	if _, ok := b.Start(); ok {
		t.Fatal("stale success cleared the in-flight probe")
	}
	slowFail(boom)
	if _, ok := b.Start(); ok {
		t.Fatal("stale failure cleared the in-flight probe")
	}
	probe(nil)
	if b.Open() {
		t.Fatal("probe success should close")
	}
}

func TestBreakerConcurrentProbe(t *testing.T) {
	b, advance := newTestBreaker(t, 1)
	call(b, boom)
	advance(2 * time.Second)
	advance(10 * time.Second)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := b.Start(); ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("allowed %d probes, want 1", allowed.Load())
	}
}
