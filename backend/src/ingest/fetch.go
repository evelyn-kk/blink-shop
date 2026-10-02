package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultMaxFetchBytes = 2 << 20
	defaultFetchTimeout  = 8 * time.Second
	maxRedirects         = 3
)

var (
	// ErrURLNotAllowed 地址不合法或指向内网等禁止访问的目标（SSRF 防护），属于客户端输入错误。
	ErrURLNotAllowed = errors.New("ingest: url not allowed")
	// ErrFetchFailed 目标可以访问但抓取失败（超时、非 2xx、过大、类型不支持），原因见错误文本。
	ErrFetchFailed = errors.New("ingest: fetch failed")
)

// Fetcher 抓取外部网页用于知识采集。防护：只允许 http/https 和 80/443/8080/8443 端口，URL 不能带账号密码；
// 每次建立连接时检查实际连接的 IP（重定向和 DNS 重绑定同样受检），拒绝回环、内网、链路本地、组播、
// 运营商 NAT、文档保留段和云厂商元数据地址；最多 3 次重定向；限制时间和大小；只接受文本类响应。
type Fetcher struct {
	client   *http.Client
	dialer   *net.Dialer
	maxBytes int64
	// allowAddr 判断连接目标是否允许，测试中可放开回环地址。
	allowAddr func(netip.Addr) bool
}

// FetcherOptions 只在测试中使用：AllowLoopback 允许访问 127.0.0.1 上的测试服务器，其他限制不变。
type FetcherOptions struct {
	AllowLoopback bool
	MaxBytes      int64
	Timeout       time.Duration
}

func NewFetcher(opts FetcherOptions) *Fetcher {
	f := &Fetcher{maxBytes: opts.MaxBytes, allowAddr: publicAddr}
	if f.maxBytes <= 0 {
		f.maxBytes = DefaultMaxFetchBytes
	}
	if opts.AllowLoopback {
		f.allowAddr = func(a netip.Addr) bool { return a.IsLoopback() || publicAddr(a) }
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		// Control 在 DNS 解析之后、真正连接之前执行，拿到的是实际要连的 IP。
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrURLNotAllowed
			}
			addr, err := netip.ParseAddr(host)
			if err != nil || !f.allowAddr(addr.Unmap()) {
				return fmt.Errorf("%w: 不能访问内网或保留地址", ErrURLNotAllowed)
			}
			return nil
		},
	}
	f.dialer = dialer
	transport := &http.Transport{
		Proxy:                 nil, // 不走环境变量代理，否则检查的是代理地址而不是目标地址
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("%w: 重定向次数过多", ErrFetchFailed)
			}
			if _, err := CheckURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	return f
}

var allowedPorts = map[string]bool{"": true, "80": true, "443": true, "8080": true, "8443": true}

// CheckURL 做不需要联网的静态检查：协议、端口、账号密码、主机名。IP 字面量在这里就检查；
// 域名在连接时按解析结果检查。返回规范化后的 URL（去掉 fragment）。
func CheckURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 {
		return nil, fmt.Errorf("%w: 地址不能为空或超过 1024 个字符", ErrURLNotAllowed)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return nil, fmt.Errorf("%w: 只支持 http 或 https 地址", ErrURLNotAllowed)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: 地址不能包含账号密码", ErrURLNotAllowed)
	}
	if !allowedPorts[u.Port()] {
		return nil, fmt.Errorf("%w: 只允许 80、443、8080、8443 端口", ErrURLNotAllowed)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || host == "metadata.google.internal" {
		return nil, fmt.Errorf("%w: 不能访问内网或保留地址", ErrURLNotAllowed)
	}
	if addr, err := netip.ParseAddr(host); err == nil && !publicAddr(addr.Unmap()) {
		return nil, fmt.Errorf("%w: 不能访问内网或保留地址", ErrURLNotAllowed)
	}
	u.Fragment = ""
	return u, nil
}

// 不允许访问的保留网段（除 netip 自带判断外）。
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // 运营商 NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64，可映射到内网 IPv4
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("100::/64"),
}

func publicAddr(a netip.Addr) bool {
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Fetched 是抓取结果：正文和响应的媒体类型（小写，不含参数）。
type Fetched struct {
	Body      string
	MediaType string
	FinalURL  string
}

var fetchableTypes = map[string]bool{
	"text/html": true, "application/xhtml+xml": true, "text/plain": true, "text/markdown": true,
	"application/json": true, "text/json": true,
}

// Fetch 抓取 URL。地址不允许时返回 ErrURLNotAllowed，其他失败返回 ErrFetchFailed。
func (f *Fetcher) Fetch(ctx context.Context, raw string) (Fetched, error) {
	u, err := CheckURL(raw)
	if err != nil {
		return Fetched{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Fetched{}, fmt.Errorf("%w: 地址不合法", ErrURLNotAllowed)
	}
	req.Header.Set("User-Agent", "BlinkShop-KnowledgeIngest/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain;q=0.9")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrURLNotAllowed) {
			return Fetched{}, fmt.Errorf("%w: 不能访问内网或保留地址", ErrURLNotAllowed)
		}
		if errors.Is(err, ErrFetchFailed) {
			return Fetched{}, err
		}
		return Fetched{}, fmt.Errorf("%w: 无法访问该地址", ErrFetchFailed)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Fetched{}, fmt.Errorf("%w: 对方返回状态码 %d", ErrFetchFailed, resp.StatusCode)
	}
	mediaType, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	mediaType = strings.ToLower(mediaType)
	if !fetchableTypes[mediaType] {
		return Fetched{}, fmt.Errorf("%w: 不支持的内容类型 %q，只支持网页、JSON 和纯文本", ErrFetchFailed, mediaType)
	}
	if cs := strings.ToLower(params["charset"]); cs != "" && cs != "utf-8" && cs != "utf8" && cs != "us-ascii" {
		return Fetched{}, fmt.Errorf("%w: 只支持 UTF-8 编码的页面", ErrFetchFailed)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return Fetched{}, fmt.Errorf("%w: 读取内容失败", ErrFetchFailed)
	}
	if int64(len(body)) > f.maxBytes {
		return Fetched{}, fmt.Errorf("%w: 页面超过 %d KB", ErrFetchFailed, f.maxBytes>>10)
	}
	return Fetched{Body: strings.ToValidUTF8(string(body), ""), MediaType: mediaType, FinalURL: resp.Request.URL.String()}, nil
}
