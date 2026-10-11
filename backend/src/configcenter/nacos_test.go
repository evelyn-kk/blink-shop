package configcenter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNacos 实现 Nacos 配置 Open API 的子集（v1 cs/configs 的 GET / POST / DELETE 和登录），可以模拟宕机。
type fakeNacos struct {
	mu       sync.Mutex
	data     map[string]string
	down     atomic.Bool
	gets     atomic.Int32
	needAuth bool
	logins   atomic.Int32
	hang     atomic.Bool  // 模拟黑洞 / 丢包：读请求一直不回，直到客户端放弃
	delay    atomic.Int64 // 模拟慢 Nacos：每个读请求等这么久（纳秒）再正常回答
}

func (f *fakeNacos) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/nacos/v1/auth/login" {
			_ = r.ParseForm()
			if r.Form.Get("username") != "blink" || r.Form.Get("password") != "s3cret" {
				http.Error(w, "bad login", http.StatusForbidden)
				return
			}
			f.logins.Add(1)
			fmt.Fprint(w, `{"accessToken":"tok-1","tokenTtl":18000}`)
			return
		}
		if r.URL.Path != "/nacos/v1/cs/configs" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		if f.needAuth && r.Form.Get("accessToken") != "tok-1" {
			http.Error(w, "no token", http.StatusForbidden)
			return
		}
		key := r.Form.Get("group") + "/" + r.Form.Get("dataId")
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodGet {
			f.gets.Add(1)
			if f.hang.Load() {
				f.mu.Unlock()
				<-r.Context().Done()
				f.mu.Lock()
				return
			}
			if d := time.Duration(f.delay.Load()); d > 0 {
				f.mu.Unlock()
				select {
				case <-time.After(d):
				case <-r.Context().Done():
				}
				f.mu.Lock()
			}
		}
		switch r.Method {
		case http.MethodGet:
			v, ok := f.data[key]
			if !ok {
				http.Error(w, "config data not exist", http.StatusNotFound)
				return
			}
			fmt.Fprint(w, v)
		case http.MethodPost:
			f.data[key] = r.Form.Get("content")
			fmt.Fprint(w, "true")
		case http.MethodDelete:
			delete(f.data, key)
			fmt.Fprint(w, "true")
		}
	})
}

func newFakeNacos(t *testing.T) (*fakeNacos, *httptest.Server) {
	f := &fakeNacos{data: map[string]string{}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return f, srv
}

func TestNacosSourceTTLAndFallback(t *testing.T) {
	ctx := context.Background()
	f, srv := newFakeNacos(t)
	f.data["BLINK_SHOP/blink_shop.config.risk.blocked_words"] = "假货,违禁"
	src := NewNacosSource(NewNacosClient(NacosOptions{Addr: srv.URL, Group: "BLINK_SHOP"}), "blink_shop.config.", 10*time.Second)
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	src.SetClock(func() time.Time { return now })
	r := NewResolver(func(string) string { return "" }, src)

	if v, from := r.String(ctx, KeyRiskBlockedWords); v != "假货,违禁" || from != "dynamic" {
		t.Fatalf("first read: %q %s", v, from)
	}
	// TTL 内：用缓存，不再请求
	f.mu.Lock()
	f.data["BLINK_SHOP/blink_shop.config.risk.blocked_words"] = "假货"
	f.mu.Unlock()
	before := f.gets.Load()
	now = now.Add(9 * time.Second)
	if v := r.Get(ctx, KeyRiskBlockedWords); v != "假货,违禁" || f.gets.Load() != before {
		t.Fatalf("cached read: %q gets=%d", v, f.gets.Load()-before)
	}
	// 过了 TTL：拿到别的实例改过的新值
	now = now.Add(2 * time.Second)
	if v := r.Get(ctx, KeyRiskBlockedWords); v != "假货" {
		t.Fatalf("refreshed: %q", v)
	}
	// Nacos 宕机：沿用最后一次成功的值，TTL 内不再重试；状态里记下错误
	f.down.Store(true)
	now = now.Add(11 * time.Second)
	if v := r.Get(ctx, KeyRiskBlockedWords); v != "假货" {
		t.Fatalf("fallback: %q", v)
	}
	if _, err := src.Status(); err == nil {
		t.Fatal("status should report the failure")
	}
	before = f.gets.Load()
	now = now.Add(time.Second)
	_ = r.Get(ctx, KeyRiskBlockedWords)
	if f.gets.Load() != before {
		t.Fatal("retried within TTL while down")
	}
	// 写入失败：返回 ErrConfigCenter，本地值不变
	if err := src.Write(ctx, "risk.blocked_words", "全新"); !errors.Is(err, ErrConfigCenter) {
		t.Fatalf("write while down: %v", err)
	}
	if v := r.Get(ctx, KeyRiskBlockedWords); v != "假货" {
		t.Fatalf("value changed by a failed write: %q", v)
	}
	// 恢复后写入：本实例立即生效，Nacos 里也是新值；空值删除
	f.down.Store(false)
	if err := src.Write(ctx, "risk.blocked_words", "全新"); err != nil {
		t.Fatal(err)
	}
	if v := r.Get(ctx, KeyRiskBlockedWords); v != "全新" || f.data["BLINK_SHOP/blink_shop.config.risk.blocked_words"] != "全新" {
		t.Fatalf("after write: %q %v", v, f.data)
	}
	if err := src.Write(ctx, "risk.blocked_words", ""); err != nil {
		t.Fatal(err)
	}
	if v, from := r.String(ctx, KeyRiskBlockedWords); from != "default" || v != KeyRiskBlockedWords.Default {
		t.Fatalf("after delete: %q %s", v, from)
	}
	if _, ok := f.data["BLINK_SHOP/blink_shop.config.risk.blocked_words"]; ok {
		t.Fatal("deleted value still in nacos")
	}
}

func TestNacosSourceStartsWhenUnreachable(t *testing.T) {
	// 启动时 Nacos 就连不上：按未设置处理（环境变量 / 默认值），不阻塞、不报错
	src := NewNacosSource(NewNacosClient(NacosOptions{Addr: "127.0.0.1:1", Group: "G", Timeout: 200 * time.Millisecond}), "p.", time.Second)
	r := NewResolver(func(string) string { return "" }, src)
	if v, from := r.String(context.Background(), KeyRiskBlockedWords); from != "default" || v == "" {
		t.Fatalf("unreachable: %q %s", v, from)
	}
	if _, err := src.Status(); err == nil {
		t.Fatal("status should report the failure")
	}
	if _, ok := OpenDynamic(NacosConfig{}).(*MemorySource); !ok {
		t.Fatal("no NACOS_ADDR should use memory")
	}
}

func TestNacosLogin(t *testing.T) {
	f, srv := newFakeNacos(t)
	f.needAuth = true
	src := NewNacosSource(NewNacosClient(NacosOptions{Addr: srv.URL, Group: "G", Username: "blink", Password: "s3cret"}), "p.", time.Second)
	if err := src.Write(context.Background(), "ai.timeout", "20s"); err != nil {
		t.Fatal(err)
	}
	if err := src.Write(context.Background(), "ai.timeout", "25s"); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 1 || f.data["G/p.ai.timeout"] != "25s" {
		t.Fatalf("logins=%d data=%v", f.logins.Load(), f.data)
	}
	bad := NewNacosSource(NewNacosClient(NacosOptions{Addr: srv.URL, Group: "G", Username: "blink", Password: "wrong"}), "p.", time.Second)
	if err := bad.Write(context.Background(), "ai.timeout", "1s"); !errors.Is(err, ErrConfigCenter) || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("bad login: %v", err)
	}
}

// TestNacosIntegration 连真实的 Blink Nacos（BLINK_TEST_NACOS_ADDR，如 127.0.0.1:8848）：写入、读取、删除，用带随机后缀的 dataId 前缀，测完删除。
func TestNacosIntegration(t *testing.T) {
	addr := os.Getenv("BLINK_TEST_NACOS_ADDR")
	if addr == "" {
		t.Skip("BLINK_TEST_NACOS_ADDR not set (Nacos integration test runs locally only)")
	}
	ctx := context.Background()
	client := NewNacosClient(NacosOptions{Addr: addr, Group: "BLINK_SHOP_TEST", Timeout: 10 * time.Second}) // 全量测试并行时本机 Nacos 偶尔超过默认 3 秒
	prefix := fmt.Sprintf("blink_shop_test_%d.", time.Now().UnixNano())
	writer := NewNacosSource(client, prefix, time.Second)
	t.Cleanup(func() { _ = client.Delete(ctx, prefix+"ai.timeout") })
	if err := writer.Write(ctx, "ai.timeout", "17s"); err != nil {
		t.Fatal(err)
	}
	// 另一个“实例”：Nacos 的读是最终一致的，写入后稍等可见
	reader := NewNacosSource(client, prefix, 10*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, ok := reader.Lookup(ctx, "ai.timeout"); ok && v == "17s" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("value never visible to another instance")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := writer.Write(ctx, "ai.timeout", ""); err != nil {
		t.Fatal(err)
	}
	for deadline = time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, ok := reader.Lookup(ctx, "ai.timeout"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deleted value still visible")
		}
	}
}

// 黑洞 Nacos：完整 Load 只等一次请求超时（熔断后其余配置项直接用默认值），而不是“配置项数 × 超时”；
// 熔断期间每个刷新间隔只放一个探测请求；Nacos 恢复后下一个刷新周期重新读到值。
func TestNacosBlackholeDoesNotStackTimeouts(t *testing.T) {
	ctx := context.Background()
	f, srv := newFakeNacos(t)
	f.data["G/p.risk.blocked_words"] = "假货,刷单"
	f.hang.Store(true)
	const timeout = 300 * time.Millisecond
	src := NewNacosSource(NewNacosClient(NacosOptions{Addr: srv.URL, Group: "G", Timeout: timeout}), "p.", 10*time.Second)
	var mu sync.Mutex
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	src.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	r := NewResolver(func(string) string { return "" }, src)

	start := time.Now()
	if _, err := Load(ctx, r); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*timeout {
		t.Fatalf("Load took %s with a blackholed Nacos (%d keys), want about one timeout", elapsed, len(Settings()))
	}
	if src.Fetches() != 1 || f.gets.Load() != 1 {
		t.Fatalf("fetches=%d gets=%d, want 1", src.Fetches(), f.gets.Load())
	}
	if r.Get(ctx, KeyRequestTimeout) != "30s" || r.Get(ctx, KeyRiskBlockedWords) != KeyRiskBlockedWords.Default {
		t.Fatal("unreachable Nacos should fall back to defaults")
	}
	if _, err := src.Status(); err == nil {
		t.Fatal("status should report the failure")
	}

	// 过了一个刷新间隔：并发的读取里只有一个去探测，其他直接用缓存 / 默认值
	advance(11 * time.Second)
	var wg sync.WaitGroup
	for _, k := range Settings() {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.Get(ctx, k.Key) }()
	}
	wg.Wait()
	if src.Fetches() != 2 {
		t.Fatalf("fetches=%d, want exactly one probe", src.Fetches())
	}

	// Nacos 恢复：下一个刷新周期读到值，熔断解除，其他配置项也恢复按项读取
	f.hang.Store(false)
	advance(11 * time.Second)
	if v := r.Get(ctx, KeyRiskBlockedWords); v != "假货,刷单" {
		t.Fatalf("after recovery: %q", v)
	}
	if _, err := src.Status(); err != nil {
		t.Fatalf("status after recovery: %v", err)
	}
	before := src.Fetches()
	_ = r.Get(ctx, KeyAITimeout)
	if src.Fetches() != before+1 {
		t.Fatal("other keys should be fetched again after recovery")
	}
}

// 慢但能回答的 Nacos：调用方的截止时间（启动总预算）对整个 Load 生效，用完后剩下的配置项直接用默认值。
func TestNacosLoadRespectsCallerBudget(t *testing.T) {
	f, srv := newFakeNacos(t)
	f.delay.Store(int64(150 * time.Millisecond))
	src := NewNacosSource(NewNacosClient(NacosOptions{Addr: srv.URL, Group: "G", Timeout: 3 * time.Second}), "p.", 10*time.Second)
	r := NewResolver(func(string) string { return "" }, src)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Load(ctx, r); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Load took %s, want it bounded by the 500ms budget", elapsed)
	}
	if n := src.Fetches(); n > 5 {
		t.Fatalf("fetches=%d after the budget ran out", n)
	}
}

// 调用方主动取消（客户端断开）不算 Nacos 失败，不触发熔断。
func TestNacosCallerCancelIsNotAFailure(t *testing.T) {
	f, srv := newFakeNacos(t)
	f.hang.Store(true)
	src := NewNacosSource(NewNacosClient(NacosOptions{Addr: srv.URL, Group: "G", Timeout: 3 * time.Second}), "p.", 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, ok := src.Lookup(ctx, "ai.timeout"); ok {
		t.Fatal("cancelled lookup should be unset")
	}
	if _, err := src.Status(); err != nil {
		t.Fatalf("cancel counted as failure: %v", err)
	}
	f.hang.Store(false)
	f.mu.Lock()
	f.data["G/p.ai.timeout"] = "20s"
	f.mu.Unlock()
	if v, ok := src.Lookup(context.Background(), "ai.timeout"); !ok || v != "20s" {
		t.Fatalf("next lookup should hit Nacos: %q %v", v, ok)
	}
}
