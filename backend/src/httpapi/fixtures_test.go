package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// fixture 是 backend/fixtures/http 下的请求/响应样例；Web 和 Android 的契约测试复用同一批文件。
type fixture struct {
	Request struct {
		Method    string            `json:"method"`
		Path      string            `json:"path"`
		Headers   map[string]string `json:"headers"`
		BodyBytes int               `json:"body_bytes"`
		Repeat    int               `json:"repeat"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any    `json:"body"`
	} `json:"response"`
}

const fixtureDir = "../../fixtures/http"

// TestHTTPFixtures 用默认配置跑每个 fixture，确认实际响应与 fixture 一致。
// "<request_id>" 匹配响应头中的 X-Request-ID，"<seconds>" 匹配正整数。
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
			for i := 0; i < max(fx.Request.Repeat, 1); i++ {
				var body *strings.Reader
				if fx.Request.BodyBytes > 0 {
					body = strings.NewReader(strings.Repeat("a", fx.Request.BodyBytes))
				} else {
					body = strings.NewReader("")
				}
				req := httptest.NewRequest(fx.Request.Method, fx.Request.Path, body)
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
			want := map[string]any{}
			for k, v := range fx.Response.Body {
				if v == "<request_id>" {
					v = rec.Header().Get(headerRequestID)
				}
				want[k] = v
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("body = %v, want %v", got, want)
			}
		})
	}
}
