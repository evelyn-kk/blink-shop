package httpapi

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/evelyn-kk/blink-shop/backend/src/metrics"
)

// limiter 是固定窗口限流：allow 在当前窗口内给 key 计数，超过 limit 时返回 false 和距离窗口结束的时间。
// limit 每次传入，便于动态配置立即生效。
type limiter interface {
	allow(key string, limit int, now time.Time) (bool, time.Duration)
}

// rateLimiter 是单实例内存固定窗口限流器（没有配置 Redis，或 Redis 不可用时的兜底）。
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

var (
	rateLimitBackend  = metrics.Default.Gauge("blink_ratelimit_backend_redis", "限流计数是否在 Redis（1 是，0 表示没配置或已退回进程内计数）", "limiter")
	rateLimitFallback = metrics.Default.Counter("blink_ratelimit_fallback_total", "Redis 不可用、退回进程内限流的请求数", "limiter")
)

// redisWindowScript 原子地给窗口计数并在第一次计数时设过期：返回 {计数, 剩余毫秒}。
var redisWindowScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then redis.call('PEXPIRE', KEYS[1], ARGV[1]); ttl = tonumber(ARGV[1]) end
return {c, ttl}
`)

// windowCounter 在共享存储里给一个窗口 key 计数（Redis 实现；测试可替换）。
type windowCounter interface {
	incr(ctx context.Context, key string, window time.Duration) (count int64, ttl time.Duration, err error)
}

type redisCounter struct{ c redis.Scripter }

func (r redisCounter) incr(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	res, err := redisWindowScript.Run(ctx, r.c, []string{key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	return res[0], time.Duration(res[1]) * time.Millisecond, nil
}

const (
	redisLimitTimeout = 100 * time.Millisecond
	// redisRetryAfter：Redis 出错后这么久内直接用进程内计数，不让每个请求都等超时。
	redisRetryAfter = time.Second
	redisLogEvery   = 10 * time.Second
)

// sharedLimiter 用 Redis 计数，多个实例共享同一个窗口（键 blink_shop:rl:<key>:<窗口序号>，窗口按 Unix 时间对齐）。
// Redis 出错或超时时退回进程内限流（不是放开），记日志和指标，1 秒后再试 Redis。
type sharedLimiter struct {
	name     string
	window   time.Duration
	counter  windowCounter
	fallback *rateLimiter
	logger   *slog.Logger

	mu        sync.Mutex
	downUntil time.Time
	lastLog   time.Time
}

// newLimiter 按是否配置了 Redis 返回共享或进程内限流器。
func newLimiter(name string, window time.Duration, rdb redis.Scripter, logger *slog.Logger) limiter {
	if rdb == nil {
		rateLimitBackend.Set(0, name)
		return newRateLimiter(window)
	}
	rateLimitBackend.Set(1, name)
	return &sharedLimiter{name: name, window: window, counter: redisCounter{rdb}, fallback: newRateLimiter(window), logger: logger}
}

func (l *sharedLimiter) allow(key string, limit int, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	down := now.Before(l.downUntil)
	l.mu.Unlock()
	if !down {
		slot := now.UnixMilli() / l.window.Milliseconds()
		ctx, cancel := context.WithTimeout(context.Background(), redisLimitTimeout)
		count, ttl, err := l.counter.incr(ctx, "blink_shop:rl:"+key+":"+strconv.FormatInt(slot, 10), l.window)
		cancel()
		if err == nil {
			rateLimitBackend.Set(1, l.name)
			if count > int64(limit) {
				return false, ttl
			}
			return true, 0
		}
		l.markDown(now, err)
	}
	rateLimitFallback.Inc(l.name)
	return l.fallback.allow(key, limit, now)
}

func (l *sharedLimiter) markDown(now time.Time, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.downUntil = now.Add(redisRetryAfter)
	rateLimitBackend.Set(0, l.name)
	if now.Sub(l.lastLog) >= redisLogEvery {
		l.lastLog = now
		l.logger.Warn("rate limit falls back to in-process counting", "limiter", l.name, "error", err.Error())
	}
}
