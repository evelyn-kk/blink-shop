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
	epoch     uint64 // 每次打开加一：打开之前就发出的调用，结果不再影响打开后的状态
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

// Start 判断这次能否发出调用。ok 为 false 时调用方应直接降级（已计 breaker_open）；ok 为 true 时调用结束后必须调用一次 done(err)。
// 冷却期结束后的第一次调用作为探测放行，探测结束前其他调用仍被拒绝。
func (b *Breaker) Start() (done func(err error), ok bool) {
	if b == nil {
		return func(error) {}, true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	probe := false
	if b.open {
		if b.probing || b.now().Before(b.openUntil) {
			metrics.External.Inc(b.name, "breaker_open")
			return nil, false
		}
		b.probing, probe = true, true
	}
	epoch := b.epoch
	var once sync.Once
	return func(err error) { once.Do(func() { b.finish(epoch, probe, err) }) }, true
}

// finish 记录一次放行调用的结果：
//   - 探测结束（不论结果）都清掉 probing，取消了也一样，下一次调用可以重新探测；
//   - 调用方主动取消（context.Canceled）不计入：那不是依赖的问题；
//   - 打开之前发出、打开之后才结束的调用（epoch 不同）不影响状态：不能让一个旧的成功把刚打开的熔断器关掉，
//     也不能让它清掉正在进行的探测；
//   - 成功关闭熔断器；失败累计，达到阈值或探测失败时打开（重新计冷却期）。
func (b *Breaker) finish(epoch uint64, probe bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe {
		b.probing = false
	}
	if err != nil && errors.Is(err, context.Canceled) {
		return
	}
	if !probe && epoch != b.epoch {
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
		b.epoch++
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
