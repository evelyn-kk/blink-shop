// Command loadtest 对运行中的 Blink API 做压测，输出 JSON + Markdown 报告：
//   - catalog：匿名并发浏览商品列表、详情、SKU、评价、分类树，统计 P50 / P95 / P99、错误率和吞吐；
//   - search：只打带关键词的商品搜索，20 个关键词轮流（品类、品牌、型号、数字、多词、查不到的词），按关键词分别统计；
//   - api：登录用户并发读购物车、订单、会话列表；
//   - sse：N 个用户同时发起导购流式对话（同时保持 N 条 SSE 连接），统计首个进展事件时间、整段耗时和成功率；
//   - checkout：M 个用户各买 1 件库存充足的商品并发结算（成功率）；K 个用户抢库存只有 S 件的商品（只能成功 S 单、库存不为负）；
//     同一用户用两个幂等键同时结算同一购物车（只能成功一次）。
//
// 只用于本地或预发环境：会注册账号、下单，结束后不清理（用一次性的库）。目标环境要放宽限流（见 quality/README）。
//
//	go run ./cmd/loadtest -base http://127.0.0.1:8080 -duration 30s -concurrency 32 -sse 200 -checkout 100
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

type options struct {
	base        string
	duration    time.Duration
	concurrency int
	sse         int
	checkout    int
	contention  int
	out         string
	scenarios   map[string]bool
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("loadtest", flag.ContinueOnError)
	var o options
	fs.StringVar(&o.base, "base", "http://127.0.0.1:8080", "API 地址（不含 /api/v1）")
	fs.DurationVar(&o.duration, "duration", 20*time.Second, "catalog / api 场景各跑多久")
	fs.IntVar(&o.concurrency, "concurrency", 32, "catalog / api 场景的并发数")
	fs.IntVar(&o.sse, "sse", 100, "同时进行的导购 SSE 连接数")
	fs.IntVar(&o.checkout, "checkout", 50, "并发结算的用户数（库存充足的商品）")
	fs.IntVar(&o.contention, "contention", 20, "抢购库存紧张商品的用户数")
	fs.StringVar(&o.out, "out", "", "报告目录，默认 ../quality/reports/loadtest-<时间>")
	scen := fs.String("scenarios", "catalog,api,sse,checkout", "要跑的场景")
	if err := fs.Parse(args); err != nil {
		return err
	}
	o.base = strings.TrimRight(o.base, "/")
	o.scenarios = map[string]bool{}
	for _, s := range strings.Split(*scen, ",") {
		o.scenarios[strings.TrimSpace(s)] = true
	}
	if o.out == "" {
		o.out = filepath.Join("..", "quality", "reports", "loadtest-"+time.Now().Format("20060102-150405"))
	}
	c := &client{base: o.base + "/api/v1", http: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{
		MaxIdleConns: 1024, MaxIdleConnsPerHost: 1024, IdleConnTimeout: 90 * time.Second}}}
	if _, err := c.do(ctx, http.MethodGet, "/ready", "", nil, nil); err != nil {
		return fmt.Errorf("API 未就绪：%w", err)
	}
	rep := report{Base: o.base, StartedAt: time.Now().UTC(), Params: map[string]any{"duration": o.duration.String(), "concurrency": o.concurrency,
		"sse": o.sse, "checkout": o.checkout, "contention": o.contention}}
	if o.scenarios["catalog"] {
		rep.Scenarios = append(rep.Scenarios, catalog(ctx, c, o))
	}
	if o.scenarios["search"] {
		rep.Scenarios = append(rep.Scenarios, search(ctx, c, o))
	}
	if o.scenarios["api"] {
		s, err := userAPI(ctx, c, o)
		if err != nil {
			return err
		}
		rep.Scenarios = append(rep.Scenarios, s)
	}
	if o.scenarios["sse"] {
		s, err := sse(ctx, c, o)
		if err != nil {
			return err
		}
		rep.Scenarios = append(rep.Scenarios, s)
	}
	if o.scenarios["checkout"] {
		s, err := checkout(ctx, c, o)
		if err != nil {
			return err
		}
		rep.Scenarios = append(rep.Scenarios, s...)
	}
	rep.DurationMS = time.Since(rep.StartedAt).Milliseconds()
	if err := rep.write(o.out); err != nil {
		return err
	}
	for _, s := range rep.Scenarios {
		fmt.Fprintf(out, "%-22s %s\n", s.Name, s.line())
	}
	fmt.Fprintln(out, "报告：", o.out)
	var failed []string
	for _, s := range rep.Scenarios {
		if !s.Pass {
			failed = append(failed, s.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("未达标：%v", failed)
	}
	return nil
}

// ---------- HTTP ----------

type client struct {
	base string
	http *http.Client
}

type apiError struct {
	Status int
	Code   string
}

func (e *apiError) Error() string { return fmt.Sprintf("http %d %s", e.Status, e.Code) }

func (c *client) do(ctx context.Context, method, path, token string, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &e)
		return resp.StatusCode, &apiError{Status: resp.StatusCode, Code: e.Code}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

var userSeq atomic.Int64

// register 注册一个一次性账号，返回 token。
func (c *client) register(ctx context.Context, prefix string) (string, error) {
	name := fmt.Sprintf("lt_%s_%d_%d", prefix, time.Now().UnixNano()%1e9, userSeq.Add(1))
	var out struct {
		Token string `json:"token"`
	}
	_, err := c.do(ctx, http.MethodPost, "/auth/register", "", map[string]any{"username": name, "password": "Load-Test#2026", "display_name": "压测"}, &out)
	return out.Token, err
}

func (c *client) users(ctx context.Context, prefix string, n int) ([]string, error) {
	out := make([]string, n)
	var wg sync.WaitGroup
	var firstErr atomic.Value
	sem := make(chan struct{}, 16)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tok, err := c.register(ctx, prefix)
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
			}
			out[i] = tok
		}()
	}
	wg.Wait()
	if err, _ := firstErr.Load().(error); err != nil {
		return nil, fmt.Errorf("注册压测账号：%w", err)
	}
	return out, nil
}

// ---------- 统计 ----------

type latencies struct {
	mu   sync.Mutex
	ms   []float64
	errs map[string]int
}

func (l *latencies) add(d time.Duration, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		if l.errs == nil {
			l.errs = map[string]int{}
		}
		l.errs[errKey(err)]++
		return
	}
	l.ms = append(l.ms, float64(d.Microseconds())/1000)
}

func errKey(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return fmt.Sprintf("%d %s", ae.Status, ae.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "transport"
}

type stats struct {
	Count     int            `json:"count"`
	Errors    map[string]int `json:"errors"`
	ErrorRate float64        `json:"error_rate"`
	P50       float64        `json:"p50_ms"`
	P95       float64        `json:"p95_ms"`
	P99       float64        `json:"p99_ms"`
	Max       float64        `json:"max_ms"`
}

func (l *latencies) stats() stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := stats{Count: len(l.ms), Errors: map[string]int{}}
	n := len(l.ms)
	for k, v := range l.errs {
		s.Errors[k] = v
		n += v
	}
	if n > 0 {
		s.ErrorRate = round(float64(n-len(l.ms)) / float64(n))
	}
	if len(l.ms) == 0 {
		return s
	}
	xs := append([]float64(nil), l.ms...)
	sort.Float64s(xs)
	q := func(p float64) float64 { return round(xs[min(len(xs)-1, int(math.Ceil(p*float64(len(xs))))-1)]) }
	s.P50, s.P95, s.P99, s.Max = q(0.5), q(0.95), q(0.99), round(xs[len(xs)-1])
	return s
}

func round(f float64) float64 { return math.Round(f*100) / 100 }

// ---------- 场景 ----------

type scenario struct {
	Name    string           `json:"name"`
	Target  string           `json:"target"`
	Pass    bool             `json:"pass"`
	RPS     float64          `json:"rps,omitempty"`
	Stats   *stats           `json:"stats,omitempty"`
	Extra   map[string]any   `json:"extra,omitempty"`
	ByRoute map[string]stats `json:"by_route,omitempty"`
}

func (s scenario) line() string {
	mark := "达标"
	if !s.Pass {
		mark = "未达标"
	}
	var parts []string
	if s.Stats != nil {
		parts = append(parts, fmt.Sprintf("n=%d p50=%.1fms p95=%.1fms p99=%.1fms err=%.2f%%", s.Stats.Count, s.Stats.P50, s.Stats.P95, s.Stats.P99, s.Stats.ErrorRate*100))
	}
	if s.RPS > 0 {
		parts = append(parts, fmt.Sprintf("rps=%.0f", s.RPS))
	}
	keys := make([]string, 0, len(s.Extra))
	for k := range s.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, s.Extra[k]))
	}
	return fmt.Sprintf("[%s] %s（目标：%s）", mark, strings.Join(parts, " "), s.Target)
}

// loop 在 duration 内用 concurrency 个 worker 反复执行 fn，按 fn 返回的路由名分别统计。
func loop(ctx context.Context, o options, fn func(ctx context.Context, worker, i int) (string, time.Duration, error)) (*latencies, map[string]*latencies, float64) {
	all := &latencies{}
	by := map[string]*latencies{}
	var byMu sync.Mutex
	ctx, cancel := context.WithTimeout(ctx, o.duration)
	defer cancel()
	var wg sync.WaitGroup
	var total atomic.Int64
	start := time.Now()
	for w := range o.concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				route, d, err := fn(ctx, w, i)
				if ctx.Err() != nil && err != nil {
					return // 到时间被取消的那一次不算
				}
				total.Add(1)
				all.add(d, err)
				byMu.Lock()
				l := by[route]
				if l == nil {
					l = &latencies{}
					by[route] = l
				}
				byMu.Unlock()
				l.add(d, err)
			}
		}()
	}
	wg.Wait()
	return all, by, round(float64(total.Load()) / time.Since(start).Seconds())
}

func timed(f func() error) (time.Duration, error) {
	start := time.Now()
	err := f()
	return time.Since(start), err
}

var productIDs = []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}

func catalog(ctx context.Context, c *client, o options) scenario {
	paths := []struct{ route, path string }{
		{"products", "/products?page_size=12"},
		{"products_search", "/products?keyword=%E8%80%B3%E6%9C%BA&page_size=12"},
		{"product_detail", ""},
		{"product_skus", ""},
		{"product_reviews", ""},
		{"categories", "/categories/tree"},
	}
	all, by, rps := loop(ctx, o, func(ctx context.Context, w, i int) (string, time.Duration, error) {
		p := paths[(w+i)%len(paths)]
		path := p.path
		id := productIDs[(w*7+i)%len(productIDs)]
		switch p.route {
		case "product_detail":
			path = "/products/" + id
		case "product_skus":
			path = "/products/" + id + "/skus"
		case "product_reviews":
			path = "/products/" + id + "/reviews"
		}
		d, err := timed(func() error { _, err := c.do(ctx, http.MethodGet, path, "", nil, nil); return err })
		return p.route, d, err
	})
	s := all.stats()
	sc := scenario{Name: "catalog", Target: "P95 < 300ms，错误率 < 0.1%", Stats: &s, RPS: rps, ByRoute: map[string]stats{}}
	for k, l := range by {
		sc.ByRoute[k] = l.stats()
	}
	sc.Pass = s.P95 < 300 && s.ErrorRate < 0.001 && s.Count > 0
	return sc
}

// searchKeywords 是 search 场景轮流使用的关键词：品类、品牌、型号、两字词、数字、多词组合、只命中少数商品的词和查不到的词。
var searchKeywords = []string{"耳机", "降噪耳机", "手机", "台灯", "机械键盘", "鼠标", "Blink", "Orion", "K8", "Nova 12", "静音",
	"护眼", "通勤 降噪", "拍照手机", "蓝牙音箱", "热插拔", "5000mAh", "游戏鼠标 轻量化", "落地灯", "量子纠缠"}

// search 只打公开商品搜索（带关键词的列表），关键词轮流取 searchKeywords，按关键词分路由统计。
func search(ctx context.Context, c *client, o options) scenario {
	all, by, rps := loop(ctx, o, func(ctx context.Context, w, i int) (string, time.Duration, error) {
		kw := searchKeywords[(w*3+i)%len(searchKeywords)]
		path := "/products?page_size=12&keyword=" + url.QueryEscape(kw)
		d, err := timed(func() error { _, err := c.do(ctx, http.MethodGet, path, "", nil, nil); return err })
		return "search:" + kw, d, err
	})
	s := all.stats()
	sc := scenario{Name: "search", Target: "P95 < 300ms，错误率 < 0.1%", Stats: &s, RPS: rps, ByRoute: map[string]stats{}}
	for k, l := range by {
		sc.ByRoute[k] = l.stats()
	}
	sc.Pass = s.P95 < 300 && s.ErrorRate < 0.001 && s.Count > 0
	return sc
}

func userAPI(ctx context.Context, c *client, o options) (scenario, error) {
	toks, err := c.users(ctx, "api", o.concurrency)
	if err != nil {
		return scenario{}, err
	}
	paths := []struct{ route, path string }{{"cart", "/cart"}, {"orders", "/orders?page_size=10"}, {"sessions", "/agent/sessions"}, {"me", "/auth/me"}}
	all, by, rps := loop(ctx, o, func(ctx context.Context, w, i int) (string, time.Duration, error) {
		p := paths[(w+i)%len(paths)]
		d, err := timed(func() error { _, err := c.do(ctx, http.MethodGet, p.path, toks[w], nil, nil); return err })
		return p.route, d, err
	})
	s := all.stats()
	sc := scenario{Name: "api", Target: "P95 < 500ms，错误率 < 0.1%", Stats: &s, RPS: rps, ByRoute: map[string]stats{}}
	for k, l := range by {
		sc.ByRoute[k] = l.stats()
	}
	sc.Pass = s.P95 < 500 && s.ErrorRate < 0.001 && s.Count > 0
	return sc, nil
}

var questions = []string{"推荐一款通勤降噪耳机", "3000 以内拍照好的手机", "耳机保修多久", "想买个护眼台灯给孩子学习用", "有什么优惠券", "推荐一款静音无线鼠标"}

// sse：o.sse 个用户同时发消息并读完整个事件流（期间同时保持这么多条 SSE 连接）。
func sse(ctx context.Context, c *client, o options) (scenario, error) {
	toks, err := c.users(ctx, "sse", o.sse)
	if err != nil {
		return scenario{}, err
	}
	sessions := make([]string, len(toks))
	for i, tok := range toks {
		var s struct {
			SessionID string `json:"session_id"`
		}
		if _, err := c.do(ctx, http.MethodPost, "/agent/sessions", tok, map[string]any{}, &s); err != nil {
			return scenario{}, fmt.Errorf("建会话：%w", err)
		}
		sessions[i] = s.SessionID
	}
	var first, firstText, total latencies
	var concurrent, peak atomic.Int64
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i, tok := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			start := time.Now()
			f, ft, err := c.stream(ctx, tok, sessions[i], fmt.Sprintf("lt-%d", i), questions[i%len(questions)], func() {
				n := concurrent.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
			})
			concurrent.Add(-1)
			first.add(f, err)
			firstText.add(ft, err)
			total.add(time.Since(start), err)
		}()
	}
	close(gate)
	wg.Wait()
	fs, ft, ts := first.stats(), firstText.stats(), total.stats()
	sc := scenario{Name: "sse", Target: "首个进展事件 P95 < 1.5s（规则模式），成功率 100%", Stats: &fs,
		Extra: map[string]any{"connections": o.sse, "peak_open_streams": peak.Load(), "total_p50_ms": ts.P50, "total_p95_ms": ts.P95, "total_max_ms": ts.Max,
			"first_text_p50_ms": ft.P50, "first_text_p95_ms": ft.P95}}
	sc.Pass = fs.P95 < 1500 && fs.ErrorRate == 0 && fs.Count == o.sse
	return sc, nil
}

// stream 发一条消息，返回从发出请求到第一个进展事件（thinking / text_delta / block）的时间，以及到第一段回答文字（text_delta）的时间；
// 读到 message_done 才算成功。
func (c *client) stream(ctx context.Context, tok, session, clientID, content string, opened func()) (time.Duration, time.Duration, error) {
	b, _ := json.Marshal(map[string]any{"client_message_id": clientID, "content": content})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/agent/sessions/"+session+"/messages:stream", bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+tok)
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, &apiError{Status: resp.StatusCode}
	}
	opened()
	var first, firstText time.Duration
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		ev, ok := strings.CutPrefix(sc.Text(), "event: ")
		if !ok {
			continue
		}
		switch ev {
		case "thinking", "text_delta", "block":
			if first == 0 {
				first = time.Since(start)
			}
			if ev == "text_delta" && firstText == 0 {
				firstText = time.Since(start)
			}
		case "message_done":
			return first, firstText, nil
		case "error":
			return first, firstText, &apiError{Status: 200, Code: "run_failed"}
		}
	}
	return first, firstText, errors.New("stream ended without message_done")
}

type cartItem struct {
	ProductID string `json:"product_id"`
	SkuID     string `json:"sku_id"`
	Quantity  int    `json:"quantity"`
}

// checkout：三个子场景。
func checkout(ctx context.Context, c *client, o options) ([]scenario, error) {
	var out []scenario
	// 1. 库存充足：成功率
	toks, err := c.users(ctx, "co", o.checkout)
	if err != nil {
		return nil, err
	}
	for _, tok := range toks {
		if _, err := c.do(ctx, http.MethodPost, "/cart/items", tok, cartItem{"p_seed_mouse", "sku_seed_mouse_gray", 1}, nil); err != nil {
			return nil, fmt.Errorf("加购：%w", err)
		}
	}
	var lat latencies
	codes := concurrently(len(toks), func(i int) (time.Duration, error) {
		return timed(func() error {
			_, err := c.do(ctx, http.MethodPost, "/orders:checkout", toks[i], map[string]any{"idempotency_key": fmt.Sprintf("lt-co-%d", i)}, nil)
			return err
		})
	}, &lat)
	s := lat.stats()
	sc := scenario{Name: "checkout", Target: "成功率 ≥ 99%，P95 < 1s", Stats: &s, Extra: map[string]any{"success": codes["ok"], "users": len(toks)}}
	sc.Pass = float64(codes["ok"]) >= 0.99*float64(len(toks)) && s.P95 < 1000
	out = append(out, sc)

	// 2. 抢购：库存只有 stock 件
	stock, err := c.stockOf(ctx, "p_seed_earbuds", "sku_seed_earbuds_white")
	if err != nil {
		return nil, err
	}
	if stock == 0 {
		out = append(out, scenario{Name: "checkout_contention", Target: "需要有库存的演示数据", Pass: true,
			Extra: map[string]any{"skipped": "耳机库存已为 0（之前的压测买完了），用新库重跑才有这一项"}})
		return append(out, doubleSubmit(ctx, c)...), nil
	}
	toks, err = c.users(ctx, "rush", o.contention)
	if err != nil {
		return nil, err
	}
	for _, tok := range toks {
		if _, err := c.do(ctx, http.MethodPost, "/cart/items", tok, cartItem{"p_seed_earbuds", "sku_seed_earbuds_white", 1}, nil); err != nil {
			return nil, fmt.Errorf("加购：%w", err)
		}
	}
	var rlat latencies
	rc := concurrently(len(toks), func(i int) (time.Duration, error) {
		return timed(func() error {
			_, err := c.do(ctx, http.MethodPost, "/orders:checkout", toks[i], map[string]any{"idempotency_key": fmt.Sprintf("lt-rush-%d", i)}, nil)
			return err
		})
	}, &rlat)
	after, err := c.stockOf(ctx, "p_seed_earbuds", "sku_seed_earbuds_white")
	if err != nil {
		return nil, err
	}
	want := min(stock, len(toks))
	rs := rlat.stats()
	out = append(out, scenario{Name: "checkout_contention", Target: "成功单数 = min(库存, 人数)，其余都是 409 业务拒绝，库存不为负", Stats: &rs,
		Extra: map[string]any{"stock_before": stock, "stock_after": after, "success": rc["ok"], "users": len(toks), "rejected": rc},
		Pass:  rc["ok"] == want && after == stock-want && after >= 0 && rc["ok"]+conflicts(rc) == len(toks)})

	return append(out, doubleSubmit(ctx, c)...), nil
}

// doubleSubmit：同一用户两个幂等键同时结算同一购物车，只能成功一次。
func doubleSubmit(ctx context.Context, c *client) []scenario {
	toks, err := c.users(ctx, "dup", 10)
	if err != nil {
		return []scenario{{Name: "checkout_double_submit", Extra: map[string]any{"error": err.Error()}}}
	}
	dupOK := 0
	for _, tok := range toks {
		if _, err := c.do(ctx, http.MethodPost, "/cart/items", tok, cartItem{"p_seed_mouse", "sku_seed_mouse_gray", 1}, nil); err != nil {
			return []scenario{{Name: "checkout_double_submit", Extra: map[string]any{"error": "加购：" + err.Error()}}}
		}
		var dl latencies
		r := concurrently(2, func(i int) (time.Duration, error) {
			return timed(func() error {
				_, err := c.do(ctx, http.MethodPost, "/orders:checkout", tok, map[string]any{"idempotency_key": fmt.Sprintf("lt-dup-%d", i)}, nil)
				return err
			})
		}, &dl)
		if r["ok"] == 1 {
			dupOK++
		}
	}
	return []scenario{{Name: "checkout_double_submit", Target: "每个购物车只成功一次", Extra: map[string]any{"carts": len(toks), "exactly_once": dupOK},
		Pass: dupOK == len(toks)}}
}

// conflicts 是 409 业务拒绝（库存不足、商品已不可买等）的次数：抢购中没抢到只能是这类，不能是 5xx 或超时。
func conflicts(m map[string]int) int {
	n := 0
	for k, v := range m {
		if strings.HasPrefix(k, "409 ") {
			n += v
		}
	}
	return n
}

// concurrently 同时执行 n 次 fn，返回按结果分类的次数（成功为 ok，失败为 “状态码 错误码”）。
func concurrently(n int, fn func(i int) (time.Duration, error), lat *latencies) map[string]int {
	var mu sync.Mutex
	out := map[string]int{}
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			d, err := fn(i)
			lat.add(d, err)
			mu.Lock()
			if err == nil {
				out["ok"]++
			} else {
				out[errKey(err)]++
			}
			mu.Unlock()
		}()
	}
	close(gate)
	wg.Wait()
	return out
}

func (c *client) stockOf(ctx context.Context, productID, skuID string) (int, error) {
	var skus struct {
		Items []struct {
			SkuID string `json:"sku_id"`
			Stock int    `json:"stock_quantity"`
		} `json:"items"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/products/"+productID+"/skus", "", nil, &skus); err != nil {
		return 0, err
	}
	for _, s := range skus.Items {
		if s.SkuID == skuID {
			return s.Stock, nil
		}
	}
	return 0, fmt.Errorf("sku %s not found", skuID)
}

// ---------- 报告 ----------

type report struct {
	Base       string         `json:"base"`
	StartedAt  time.Time      `json:"started_at"`
	DurationMS int64          `json:"duration_ms"`
	Params     map[string]any `json:"params"`
	Scenarios  []scenario     `json:"scenarios"`
}

func (r report) write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "loadtest.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# 压测报告\n\n- 目标：%s\n- 时间：%s，耗时 %d ms\n- 参数：%v\n\n| 场景 | 结论 | 结果 |\n| --- | --- | --- |\n", r.Base,
		r.StartedAt.Format(time.RFC3339), r.DurationMS, r.Params)
	for _, s := range r.Scenarios {
		mark := "✅"
		if !s.Pass {
			mark = "❌"
		}
		fmt.Fprintf(&md, "| %s | %s | %s |\n", s.Name, mark, strings.ReplaceAll(s.line(), "|", "/"))
	}
	for _, s := range r.Scenarios {
		if len(s.ByRoute) == 0 {
			continue
		}
		fmt.Fprintf(&md, "\n## %s 分接口\n\n| 接口 | 次数 | P50 | P95 | P99 | 错误率 |\n| --- | --- | --- | --- | --- | --- |\n", s.Name)
		keys := make([]string, 0, len(s.ByRoute))
		for k := range s.ByRoute {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			st := s.ByRoute[k]
			fmt.Fprintf(&md, "| %s | %d | %.1f | %.1f | %.1f | %.2f%% |\n", k, st.Count, st.P50, st.P95, st.P99, st.ErrorRate*100)
		}
	}
	return os.WriteFile(filepath.Join(dir, "loadtest.md"), []byte(md.String()), 0o644)
}
