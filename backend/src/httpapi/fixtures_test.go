package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// fixture 是 backend/fixtures/http 下的请求/响应样例；Web 和 Android 的契约测试复用同一批文件。
type fixture struct {
	Request struct {
		Method    string            `json:"method"`
		Path      string            `json:"path"`
		Headers   map[string]string `json:"headers"`
		Body      json.RawMessage   `json:"body"`       // JSON 请求体，自动带 Content-Type: application/json
		BodyBytes int               `json:"body_bytes"` // 或者：n 字节的填充内容（测请求体上限）
		Repeat    int               `json:"repeat"`
		As        string            `json:"as"` // 以该演示账号登录后发请求（密码为 seed.DevPassword）
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any    `json:"body"`
	} `json:"response"`
}

// 响应体中的占位符：值随机或随时间变化的字段只校验格式。
var fixturePlaceholders = map[string]func(got any, rec *httptest.ResponseRecorder) bool{
	"<request_id>": func(got any, rec *httptest.ResponseRecorder) bool { return got == rec.Header().Get(headerRequestID) },
	"<token>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && len(s) == 43
	},
	"<timestamp>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		_, err := time.Parse(time.RFC3339Nano, s)
		return ok && err == nil
	},
	"<account_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, "acct_")
	},
}

// matchFixture 递归比较 fixture 期望值与实际 JSON，返回第一处不一致的路径。
func matchFixture(want, got any, rec *httptest.ResponseRecorder, path string) string {
	if s, ok := want.(string); ok {
		if check, ok := fixturePlaceholders[s]; ok {
			if !check(got, rec) {
				return fmt.Sprintf("%s = %v, want %s", path, got, s)
			}
			return ""
		}
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s = %v, want object", path, got)
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return fmt.Sprintf("%s.%s 未在 fixture 中声明", path, k)
			}
		}
		for k, v := range w {
			if msg := matchFixture(v, g[k], rec, path+"."+k); msg != "" {
				return msg
			}
		}
		return ""
	default:
		if !reflect.DeepEqual(want, got) {
			return fmt.Sprintf("%s = %#v, want %#v", path, got, want)
		}
		return ""
	}
}

const fixtureDir = "../../fixtures/http"

// TestHTTPFixtures 用默认配置跑每个 fixture，确认实际响应与 fixture 一致。
// 响应头 "<seconds>" 匹配正整数；响应体占位符见 fixturePlaceholders。响应体不能有 fixture 未声明的字段。
func TestHTTPFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found in %s: %v", fixtureDir, err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var fx fixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}

			dbErr := error(nil)
			if name == "ready_not_ready" {
				dbErr = errors.New("connection refused")
			}
			ts := newTestServer(t, nil, []ReadinessCheck{{Name: "mysql", Check: func(context.Context) error { return dbErr }}}, nil)

			var rec *httptest.ResponseRecorder
			token := ""
			if fx.Request.As != "" {
				token = ts.login(t, fx.Request.As, seed.DevPassword).Token
			}
			for i := 0; i < max(fx.Request.Repeat, 1); i++ {
				var body *strings.Reader
				switch {
				case len(fx.Request.Body) > 0:
					body = strings.NewReader(string(fx.Request.Body))
				case fx.Request.BodyBytes > 0:
					body = strings.NewReader(strings.Repeat("a", fx.Request.BodyBytes))
				default:
					body = strings.NewReader("")
				}
				req := httptest.NewRequest(fx.Request.Method, fx.Request.Path, body)
				if len(fx.Request.Body) > 0 {
					req.Header.Set("Content-Type", "application/json")
				}
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				for k, v := range fx.Request.Headers {
					req.Header.Set(k, v)
				}
				rec = ts.do(req)
			}

			if rec.Code != fx.Response.Status {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, fx.Response.Status, rec.Body)
			}
			for k, want := range fx.Response.Headers {
				got := rec.Header().Get(k)
				if want == "<seconds>" {
					if n, err := strconv.Atoi(got); err != nil || n < 1 {
						t.Errorf("header %s = %q, want positive seconds", k, got)
					}
					continue
				}
				if got != want {
					t.Errorf("header %s = %q, want %q", k, got, want)
				}
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if msg := matchFixture(map[string]any(fx.Response.Body), got, rec, "body"); msg != "" {
				t.Fatalf("%s\nbody=%s", msg, rec.Body)
			}
		})
	}
}
