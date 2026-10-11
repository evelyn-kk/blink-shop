// Package resilience 是外部依赖的熔断器：连续失败达到阈值后打开一段时间，期间调用直接失败（调用方走降级），
// 冷却结束后只放一个探测请求，成功则恢复，失败则再打开一个冷却期。
package resilience

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/metrics"
)

// ErrOpen 表示熔断器打开，调用没有发出。
var ErrOpen = errors.New("circuit breaker open")

// Breaker 是一个依赖的熔断器。零值不可用，用 NewBreaker。
type Breaker struct {
	name      string
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	open      bool
	probing   bool
}

// NewBreaker 创建熔断器：threshold 次连续失败后打开 cooldown。name 是依赖名（指标标签）。
func NewBreaker(name string, threshold int, cooldown time.Duration) *Breaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	metrics.BreakerOpen.Set(0, name)
	return &Breaker{name: name, threshold: threshold, cooldown: cooldown, now: time.Now}
}

// SetClock 替换时钟（测试用）。
func (b *Breaker) SetClock(now func() time.Time) { b.now = now }

// Allow 判断这次能否发出调用；返回 false 时调用方应直接降级（并计 breaker_open）。
// 冷却期结束后的第一次调用作为探测放行，探测结束（Record）前其他调用仍被拒绝。
func (b *Breaker) Allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if b.probing || b.now().Before(b.openUntil) {
		metrics.External.Inc(b.name, "breaker_open")
		return false
	}
	b.probing = true
	return true
}

// Record 记录一次放行调用的结果。调用方主动取消（context.Canceled）不计入：那不是依赖的问题。
func (b *Breaker) Record(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	probe := b.probing
	b.probing = false
	if err != nil && errors.Is(err, context.Canceled) {
		return
	}
	if err == nil {
		b.failures, b.open = 0, false
		metrics.BreakerOpen.Set(0, b.name)
		return
	}
	b.failures++
	if probe || b.failures >= b.threshold {
		b.open, b.openUntil = true, b.now().Add(b.cooldown)
		metrics.BreakerOpen.Set(1, b.name)
	}
}

// Open 返回熔断器当前是否打开（状态展示用）。
func (b *Breaker) Open() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}
