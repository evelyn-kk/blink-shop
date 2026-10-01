package httpapi

import (
	"sync"
	"time"
)

// rateLimiter 是单实例内存固定窗口限流器；多实例部署时可替换为 Redis 实现。
type rateLimiter struct {
	mu        sync.Mutex
	window    time.Duration
	clients   map[string]windowState
	lastSweep time.Time
}

type windowState struct {
	count int
	start time.Time
}

func newRateLimiter(window time.Duration) *rateLimiter {
	return &rateLimiter{window: window, clients: make(map[string]windowState)}
}

// allow 在当前窗口内计数；超过 limit 时返回 false 和距离窗口重置的时间。
// limit 每次传入，便于动态配置立即生效。
func (l *rateLimiter) allow(key string, limit int, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)
	state, ok := l.clients[key]
	if !ok || now.Sub(state.start) >= l.window {
		l.clients[key] = windowState{count: 1, start: now}
		return true, 0
	}
	if state.count >= limit {
		return false, state.start.Add(l.window).Sub(now)
	}
	state.count++
	l.clients[key] = state
	return true, 0
}

// sweep 每个窗口清理一次过期的 key，防止 map 随访问者无限增长。
func (l *rateLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	for key, state := range l.clients {
		if now.Sub(state.start) >= l.window {
			delete(l.clients, key)
		}
	}
	l.lastSweep = now
}
