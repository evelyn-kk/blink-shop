package imagesearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
)

// ErrSourceNotAllowed 表示商品图地址不在可读取范围内（不是平台图片，也不是白名单域名的 https 链接）。
var ErrSourceNotAllowed = errors.New("imagesearch: image source not allowed")

// Loader 读取商品图的字节。
type Loader interface {
	Load(ctx context.Context, rawURL string) ([]byte, error)
}

// Source 读取商品图：`/api/v1/assets/...` 读内嵌资源；https 链接只从 AllowedHosts 下载（主机名精确匹配、不跟随重定向、
// 拒绝连到回环 / 内网 / 链路本地地址、限时限大小、响应必须是图片）。其余地址一律不读，防止服务端请求伪造。
type Source struct {
	AllowedHosts []string
	client       *http.Client
}

const (
	downloadTimeout = 10 * time.Second
	assetPrefix     = "/api/v1/assets/"
)

func NewSource(allowedHosts []string) *Source {
	hosts := make([]string, 0, len(allowedHosts))
	for _, h := range allowedHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: denyPrivate}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: downloadTimeout, MaxIdleConns: 4}
	return &Source{AllowedHosts: hosts, client: &http.Client{Timeout: downloadTimeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *Source) Load(ctx context.Context, rawURL string) ([]byte, error) {
	if name, ok := strings.CutPrefix(rawURL, assetPrefix); ok {
		if !fs.ValidPath(name) {
			return nil, ErrSourceNotAllowed
		}
		data, err := fs.ReadFile(assets.FS, name)
		if err != nil {
			return nil, fmt.Errorf("read asset %s: %w", name, err)
		}
		return data, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || !s.allowed(u) {
		return nil, ErrSourceNotAllowed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/*")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "image/") {
		return nil, fmt.Errorf("download image: content type %q", ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, imagevector.MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > imagevector.MaxImageBytes {
		return nil, fmt.Errorf("download image: larger than %d bytes", imagevector.MaxImageBytes)
	}
	return data, nil
}

func (s *Source) allowed(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != "443" {
		return false
	}
	for _, h := range s.AllowedHosts {
		if host == h {
			return true
		}
	}
	return false
}

// denyPrivate 在建立连接前检查实际解析出的地址，挡住白名单域名被解析到内网（DNS 重绑定）。
func denyPrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		return fmt.Errorf("imagesearch: refusing to connect to %s", ip)
	}
	return nil
}
