package ingest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPlainText(t *testing.T) {
	in := "  第一行\t\t有  空白 \r\n\r\n\r\n\n第二段​\x00第一行\r第二段第二行\n\n\n   \n"
	want := "第一行 有 空白\n\n第二段第一行\n第二段第二行"
	if got := PlainText(in); got != want {
		t.Fatalf("PlainText = %q, want %q", got, want)
	}
	if PlainText(" \n\t\n ") != "" {
		t.Fatal("blank input should clean to empty")
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<!doctype html><html><head><title> Nova 12 &amp; 评测 </title><style>.a{color:red}</style>
<script>var secret = "不应出现";</script></head>
<body><nav>首页 | 分类</nav>
<h1>Blink Nova 12</h1><p>主摄 5000 万像素，<b>夜景</b>模式。<br>支持 33W 快充。</p>
<ul><li>续航一天半</li><li>128GB &lt;起&gt;</li></ul>
<table><tr><td>颜色</td><td>黑色</td></tr></table>
<noscript>请开启脚本</noscript><svg><text>图标</text></svg><template><p>模板</p></template>
<p>未闭合的段落<div>块</div></body></html>`
	title, text := HTMLToText(in)
	if title != "Nova 12 & 评测" {
		t.Fatalf("title = %q", title)
	}
	want := "Blink Nova 12\n\n主摄 5000 万像素，夜景模式。\n支持 33W 快充。\n\n续航一天半\n128GB <起>\n\n颜色 黑色\n\n未闭合的段落\n块"
	if text != want {
		t.Fatalf("text =\n%q\nwant\n%q", text, want)
	}
	for _, bad := range []string{"secret", "color", "首页", "请开启脚本", "图标", "模板"} {
		if strings.Contains(text, bad) {
			t.Errorf("text contains %q", bad)
		}
	}
}

func TestJSONToTextIsDeterministic(t *testing.T) {
	a := `{"name":"Nova 12","specs":{"ram":"8GB","battery":5000},"tags":["拍照","快充"],"ok":true,"none":null,"price":2999.00}`
	b := `{"price":2999.00,"ok":true,"tags":["拍照","快充"],"specs":{"battery":5000,"ram":"8GB"},"none":null,"name":"Nova 12"}`
	want := "name: Nova 12\nok: true\nprice: 2999.00\nspecs.battery: 5000\nspecs.ram: 8GB\ntags[0]: 拍照\ntags[1]: 快充"
	for _, in := range []string{a, b} {
		got, err := JSONToText(in)
		if err != nil || got != want {
			t.Fatalf("JSONToText(%s) = %q, %v", in, got, err)
		}
	}
	arr, err := JSONToText(`[{"q":"保修多久","a":"一年"},{"q":"能退货吗","a":"7 天"}]`)
	if err != nil || arr != "[0].a: 一年\n[0].q: 保修多久\n\n[1].a: 7 天\n[1].q: 能退货吗" {
		t.Fatalf("array = %q, %v", arr, err)
	}
	for _, bad := range []string{`{"a":`, `{"a":1} {"b":2}`, `not json`} {
		if _, err := JSONToText(bad); err == nil {
			t.Errorf("JSONToText(%q) accepted", bad)
		}
	}
}

type fakeFetcher struct {
	got string
	res Fetched
	err error
}

func (f *fakeFetcher) Fetch(_ context.Context, raw string) (Fetched, error) {
	f.got = raw
	return f.res, f.err
}

func TestParse(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name                                string
		src                                 Source
		fetch                               *fakeFetcher
		title, docType, sourceType, content string
	}{
		{"text infers title from first line", Source{Content: "  保修政策\n手机一年保修。"}, nil, "保修政策", "unstructured_note", "text", "保修政策\n手机一年保修。"},
		{"explicit title", Source{Title: " 售后 ", Content: "七天无理由"}, nil, "售后", "unstructured_note", "text", "七天无理由"},
		{"html field", Source{HTML: "<title>页面标题</title><p>正文</p>"}, nil, "页面标题", "web_article", "html", "正文"},
		{"content as html", Source{SourceType: "web", Content: "<p>正文</p>"}, nil, "正文", "web_article", "html", "正文"},
		{"json field", Source{JSONText: `{"b":"2","a":"1"}`}, nil, "a: 1", "structured_note", "json", "a: 1\nb: 2"},
		{"url html", Source{SourceURL: "https://example.com/a"}, &fakeFetcher{res: Fetched{Body: "<h1>标题</h1><p>内容</p>", MediaType: "text/html", FinalURL: "https://example.com/b"}}, "标题", "web_article", "html", "标题\n\n内容"},
		{"url json", Source{SourceURL: "https://example.com/a.json"}, &fakeFetcher{res: Fetched{Body: `{"k":"v"}`, MediaType: "application/json", FinalURL: "https://example.com/a.json"}}, "k: v", "structured_note", "json", "k: v"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f URLFetcher
			if c.fetch != nil {
				f = c.fetch
			}
			p, err := Parse(ctx, f, c.src)
			if err != nil {
				t.Fatal(err)
			}
			if p.Title != c.title || p.DocType != c.docType || p.SourceType != c.sourceType || p.Content != c.content || p.TextRunes != len([]rune(c.content)) || p.Truncated {
				t.Fatalf("parsed = %+v", p)
			}
			if c.fetch != nil && p.SourceURL != c.fetch.res.FinalURL {
				t.Fatalf("source_url = %q", p.SourceURL)
			}
		})
	}
}

func TestParseTruncatesLongText(t *testing.T) {
	p, err := Parse(context.Background(), nil, Source{Title: "长文", Content: strings.Repeat("字", MaxTextRunes+10)})
	if err != nil || !p.Truncated || p.TextRunes != MaxTextRunes {
		t.Fatalf("truncate: %+v %v", p.TextRunes, err)
	}
}

func TestParseRejects(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		src   Source
		field string
	}{
		{"nothing", Source{Title: "x"}, "content"},
		{"two sources", Source{Content: "a", HTML: "<p>b</p>"}, "content"},
		{"only whitespace", Source{Content: " \n\t "}, "content"},
		{"html without text", Source{HTML: "<script>alert(1)</script><style>p{}</style>"}, "html"},
		{"bad json", Source{JSONText: `{"a":`}, "json_text"},
		{"json no values", Source{JSONText: `{"a":null,"b":{}}`}, "json_text"},
		{"bad source type", Source{SourceType: "pdf", Content: "x"}, "source_type"},
		{"title with newline", Source{Title: "a\nb", Content: "x"}, "title"},
		{"title too long", Source{Title: strings.Repeat("长", 121), Content: "x"}, "title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(ctx, nil, c.src)
			var in *InputError
			if !errors.As(err, &in) || in.Field != c.field {
				t.Fatalf("err = %v, want InputError field %s", err, c.field)
			}
		})
	}
	// 抓取错误原样传出；没有配置抓取器时不能采集 URL。
	f := &fakeFetcher{err: fmt.Errorf("%w: 对方返回状态码 500", ErrFetchFailed)}
	if _, err := Parse(ctx, f, Source{SourceURL: "https://example.com"}); !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("fetch error = %v", err)
	}
	if _, err := Parse(ctx, nil, Source{SourceURL: "https://example.com"}); !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("nil fetcher = %v", err)
	}
	// 静态检查不通过的地址不会交给抓取器。
	f = &fakeFetcher{}
	if _, err := Parse(ctx, f, Source{SourceURL: "http://127.0.0.1/admin"}); !errors.Is(err, ErrURLNotAllowed) || f.got != "" {
		t.Fatalf("blocked url: %v, fetched %q", err, f.got)
	}
}

func TestCheckURL(t *testing.T) {
	ok := []string{"https://example.com/a?b=1#frag", "http://example.com:8080/", "https://93.184.216.34/", "https://[2606:2800:220:1:248:1893:25c8:1946]/"}
	for _, raw := range ok {
		if _, err := CheckURL(raw); err != nil {
			t.Errorf("CheckURL(%q) = %v", raw, err)
		}
	}
	blocked := []string{
		"", "ftp://example.com/", "file:///etc/passwd", "gopher://example.com", "javascript:alert(1)", "//example.com",
		"https://user:pw@example.com/", "https://example.com:22/", "https://example.com:6379/",
		"http://localhost/", "http://LOCALHOST./", "http://api.localhost/", "http://metadata.google.internal/", "http://printer.local/",
		"http://127.0.0.1/", "http://127.1.2.3/", "http://0.0.0.0/", "http://10.0.0.1/", "http://172.16.0.1/", "http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/", "http://100.64.0.1/", "http://[::1]/", "http://[fd00::1]/", "http://[fe80::1]/",
		"http://[::ffff:127.0.0.1]/", "http://[::ffff:169.254.169.254]/", "http://[64:ff9b::a00:1]/", "http://224.0.0.1/",
		"https://" + strings.Repeat("a", 1100) + ".com/",
	}
	for _, raw := range blocked {
		if _, err := CheckURL(raw); !errors.Is(err, ErrURLNotAllowed) {
			t.Errorf("CheckURL(%q) = %v, want ErrURLNotAllowed", raw, err)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "2606:4700::1111"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "10.1.1.1", "172.31.255.255", "192.168.0.1", "169.254.169.254", "100.100.100.200", "0.1.2.3", "::1", "fc00::1", "fe80::1", "ff02::1", "198.18.0.1", "240.0.0.1"} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
}

// redirectDial 让所有连接都连到测试服务器，但仍经过 Fetcher 的拨号器，连接前的 IP 检查照常生效。
func redirectDial(f *Fetcher, addr string) func(ctx context.Context, network, _ string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return f.dialer.DialContext(ctx, network, addr)
	}
}

func TestFetcher(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<h1>你好</h1>")
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/page", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/to-metadata", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("x", 2048))
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "MZ")
	})
	mux.HandleFunc("/gbk", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		fmt.Fprint(w, "x")
	})
	mux.HandleFunc("/500", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// httptest 监听随机端口，不在允许端口内：测试里把服务器“当作” 8080 端口的外网地址访问。
	ctx := context.Background()
	f := NewFetcher(FetcherOptions{AllowLoopback: true, MaxBytes: 1024, Timeout: 500 * time.Millisecond})
	f.client.Transport.(*http.Transport).DialContext = redirectDial(f, srv.Listener.Addr().String())
	base := "http://example.com:8080"

	got, err := f.Fetch(ctx, base+"/redirect")
	if err != nil || got.Body != "<h1>你好</h1>" || got.MediaType != "text/html" || got.FinalURL != base+"/page" {
		t.Fatalf("redirect fetch = %+v, %v", got, err)
	}
	for path, want := range map[string]error{
		"/loop": ErrFetchFailed, "/to-metadata": ErrURLNotAllowed, "/big": ErrFetchFailed, "/binary": ErrFetchFailed,
		"/gbk": ErrFetchFailed, "/500": ErrFetchFailed, "/slow": ErrFetchFailed,
	} {
		if _, err := f.Fetch(ctx, base+path); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", path, err, want)
		}
	}

	// 默认配置下连接到回环地址会在拨号前被拦截（模拟 DNS 解析到内网的域名）。
	strict := NewFetcher(FetcherOptions{Timeout: 500 * time.Millisecond})
	strict.client.Transport.(*http.Transport).DialContext = redirectDial(strict, srv.Listener.Addr().String())
	if _, err := strict.Fetch(ctx, base+"/page"); !errors.Is(err, ErrURLNotAllowed) {
		t.Fatalf("rebinding to loopback: %v", err)
	}
}
