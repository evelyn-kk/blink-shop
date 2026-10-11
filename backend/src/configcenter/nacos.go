package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/metrics"
)

// ErrConfigCenter 表示配置中心不可用或拒绝了请求（写入未生效）。
var ErrConfigCenter = errors.New("config center unavailable")

// NacosOptions 是 Nacos 连接配置。
type NacosOptions struct {
	Addr      string // host:port，或带 http(s):// 前缀
	Namespace string
	Group     string
	Username  string
	Password  string
	Timeout   time.Duration
}

// NacosClient 调 Nacos 配置的 Open API（v1 cs/configs），开启鉴权时先登录拿 accessToken。
type NacosClient struct {
	base    string
	opts    NacosOptions
	http    *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewNacosClient(o NacosOptions) *NacosClient {
	if o.Addr == "" {
		return nil
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	base := strings.TrimRight(o.Addr, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &NacosClient{base: base + "/nacos", opts: o, http: &http.Client{Timeout: o.Timeout}}
}

func (c *NacosClient) accessToken(ctx context.Context) (string, error) {
	if c.opts.Username == "" {
		return "", nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"username": {c.opts.Username}, "password": {c.opts.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nacos login: status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		TokenTTL    int64  `json:"tokenTtl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("nacos login: bad response")
	}
	ttl := time.Duration(out.TokenTTL) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	c.token, c.expires = out.AccessToken, time.Now().Add(ttl*9/10)
	return c.token, nil
}

func (c *NacosClient) params(ctx context.Context, dataID string) (url.Values, error) {
	v := url.Values{"dataId": {dataID}, "group": {c.opts.Group}}
	if c.opts.Namespace != "" {
		v.Set("tenant", c.opts.Namespace)
	}
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	if tok != "" {
		v.Set("accessToken", tok)
	}
	return v, nil
}

// Get 读取配置内容；不存在时返回空串和 found=false。
func (c *NacosClient) Get(ctx context.Context, dataID string) (content string, found bool, err error) {
	defer metrics.Track("nacos", time.Now(), &err)
	v, err := c.params(ctx, dataID)
	if err != nil {
		return "", false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/cs/configs?"+v.Encode(), nil)
	if err != nil {
		return "", false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return string(body), true, nil
	case http.StatusNotFound:
		return "", false, nil
	}
	return "", false, fmt.Errorf("nacos get %s: status %d", dataID, resp.StatusCode)
}

// Publish 写入（覆盖）配置。
func (c *NacosClient) Publish(ctx context.Context, dataID, content string) (err error) {
	defer metrics.Track("nacos", time.Now(), &err)
	v, err := c.params(ctx, dataID)
	if err != nil {
		return err
	}
	v.Set("content", content)
	v.Set("type", "text")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/cs/configs", strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "true" {
		return nil
	}
	return fmt.Errorf("nacos publish %s: status %d", dataID, resp.StatusCode)
}

// Delete 删除配置（测试清理用）。
func (c *NacosClient) Delete(ctx context.Context, dataID string) (err error) {
	defer metrics.Track("nacos", time.Now(), &err)
	v, err := c.params(ctx, dataID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/v1/cs/configs?"+v.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nacos delete %s: status %d", dataID, resp.StatusCode)
	}
	return nil
}

// NacosSource 是 Nacos 上的动态配置：每个配置项一个 dataId（前缀 + 配置名，内容就是值）。
//   - 读：按配置项缓存最近一次成功拉取的值，超过 TTL 才重新拉；Nacos 不可用时继续用旧值（从没拉到过则视为未设置，
//     即用环境变量 / 默认值），并在下一个 TTL 后再试；
//   - 写：只写这一项（空值表示删除），写成功才更新本地缓存；写失败返回 ErrConfigCenter，不生效。
//
// 每项一个 dataId 而不是一个 JSON 合集：Nacos 的 HTTP 接口不支持 casMd5（只有 gRPC 客户端支持），合集的“读-改-写”在多实例
// 同时修改不同配置时会互相覆盖；分开存放后不同配置互不影响，同一配置并发修改以最后一次写入为准。
// Nacos 的读是最终一致的（写入后约 1 秒内其他实例可见），本实例写入后立即生效。
//
// 熔断：任何一次拉取失败（连不上、超时、非 200）后，一个 TTL 内所有配置项都不再请求 Nacos，直接用缓存 / 未设置；
// 过了 TTL 只放一个请求去探测，成功后恢复按项刷新。这样 Nacos 黑洞或丢包时，启动和请求最多等一次超时，
// 而不是“配置项数 × 超时”。调用方主动取消（context.Canceled）不算 Nacos 失败。
type NacosSource struct {
	client *NacosClient
	prefix string
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*nacosEntry
	lastErr error
	lastAt  time.Time
	down    bool      // 最近一次拉取失败
	retryAt time.Time // down 时，过了这个时间才放一个探测请求
	probing bool      // 有探测请求在路上，其他请求不再访问 Nacos
	fetches int       // 发给 Nacos 的读请求数（测试用）
}

type nacosEntry struct {
	value     string
	found     bool
	fetchedAt time.Time
}

// NewNacosSource 创建来源；client 为 nil 时返回 nil。prefix 是 dataId 前缀（如 blink_shop.config.），ttl 为 0 时用 10 秒。
func NewNacosSource(client *NacosClient, prefix string, ttl time.Duration) *NacosSource {
	if client == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &NacosSource{client: client, prefix: prefix, ttl: ttl, now: time.Now, entries: map[string]*nacosEntry{}}
}

// SetClock 替换时钟（测试 TTL 用）。
func (n *NacosSource) SetClock(now func() time.Time) { n.now = now }

// DataID 是配置项在 Nacos 里的 dataId。
func (n *NacosSource) DataID(name string) string { return n.prefix + name }

func (n *NacosSource) Lookup(ctx context.Context, name string) (string, bool) {
	n.mu.Lock()
	e := n.entries[name]
	cached := func() (string, bool) {
		if e == nil {
			return "", false
		}
		return e.value, e.found
	}
	if e != nil && n.now().Sub(e.fetchedAt) < n.ttl {
		n.mu.Unlock()
		return e.value, e.found
	}
	if ctx.Err() != nil {
		n.mu.Unlock()
		return cached() // 调用方已经放弃（如启动预算用完）：不访问 Nacos，也不算 Nacos 失败
	}
	probe := false
	if n.down {
		if n.probing || n.now().Before(n.retryAt) {
			n.mu.Unlock()
			return cached() // 熔断中：不访问 Nacos
		}
		n.probing, probe = true, true
	}
	n.fetches++
	n.mu.Unlock()

	// 拉取不持锁：一项配置的网络请求不会挡住其他配置的读取。调用方的截止时间（如启动预算）同样生效。
	rctx, cancel := context.WithTimeout(ctx, n.client.opts.Timeout)
	defer cancel()
	value, found, err := n.client.Get(rctx, n.DataID(name))
	n.mu.Lock()
	defer n.mu.Unlock()
	if probe {
		n.probing = false
	}
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		return cached() // 调用方走了，不是 Nacos 的问题
	}
	n.lastAt = n.now()
	if err != nil {
		n.lastErr = err
		n.down, n.retryAt = true, n.now().Add(n.ttl)
		return cached() // 沿用旧值；旧条目保持过期，恢复后第一时间重新拉取
	}
	n.lastErr, n.down = nil, false
	n.entries[name] = &nacosEntry{value: strings.TrimSpace(value), found: found, fetchedAt: n.now()}
	return strings.TrimSpace(value), found
}

// Fetches 返回发给 Nacos 的读请求数（测试用）。
func (n *NacosSource) Fetches() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.fetches
}

// Status 返回最近一次拉取的时间和错误（管理页展示配置中心是否可用）。
func (n *NacosSource) Status() (at time.Time, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastAt, n.lastErr
}

// Write 把一项配置写入 Nacos（value 为空表示删除该项）。
func (n *NacosSource) Write(ctx context.Context, name, value string) error {
	var err error
	if value == "" {
		err = n.client.Delete(ctx, n.DataID(name))
	} else {
		err = n.client.Publish(ctx, n.DataID(name), value)
	}
	if err != nil {
		n.mu.Lock()
		n.lastErr, n.lastAt = err, n.now()
		n.down, n.retryAt = true, n.now().Add(n.ttl)
		n.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrConfigCenter, err)
	}
	n.mu.Lock()
	n.entries[name] = &nacosEntry{value: value, found: value != "", fetchedAt: n.now()}
	n.lastErr, n.lastAt, n.down = nil, n.now(), false // 写成功说明 Nacos 已经恢复
	n.mu.Unlock()
	return nil
}

// OpenDynamic 按配置返回动态配置来源：配置了 Nacos 时是 NacosSource（不可用时沿用缓存 / 默认值），否则是进程内存。
func OpenDynamic(c NacosConfig) Writable {
	if src := NewNacosSource(NewNacosClient(NacosOptions{Addr: c.Addr, Namespace: c.Namespace, Group: c.Group, Username: c.Username,
		Password: c.Password}), c.DataIDPrefix, c.Refresh); src != nil {
		return src
	}
	return NewMemorySource(nil)
}

// NacosPromptMirror 把发布后的 Prompt 推送到 Nacos（dataId = 前缀 + prompt key，内容为 JSON：key、版本、内容），
// 供配置中心查看和其他系统订阅。运行时以数据库为准，推送失败不影响生效。
type NacosPromptMirror struct {
	client *NacosClient
	prefix string
}

func NewNacosPromptMirror(client *NacosClient, prefix string) *NacosPromptMirror {
	if client == nil {
		return nil
	}
	return &NacosPromptMirror{client: client, prefix: prefix}
}

func (m *NacosPromptMirror) Target(key string) string { return m.prefix + key }

func (m *NacosPromptMirror) Publish(ctx context.Context, key string, version int, content string) error {
	body, _ := json.Marshal(map[string]any{"prompt_key": key, "version": version, "content": content})
	return m.client.Publish(ctx, m.Target(key), string(body))
}
