package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// fixtureMultipart 是一个 multipart 文件分段；ContentType 是客户端声明的类型（服务端不信任）。
type fixtureMultipart struct {
	Field         string `json:"field"`
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	ContentBase64 string `json:"content_base64"`
}

// fixture 是 backend/fixtures/http 下的请求/响应样例；Web 和 Android 的契约测试复用同一批文件。
type fixture struct {
	// Setup 在正式请求前以 As 账号上传一个文件，请求路径中的 {setup_file_id} 替换为它的 file_id。
	Setup *struct {
		As        string            `json:"as"`
		Multipart *fixtureMultipart `json:"multipart"`
	} `json:"setup"`
	// SetupRequests 在正式请求前依次以 As 账号发送的 JSON 请求（如先加购再试算），每个都必须返回 2xx。
	// 返回订单列表（结算）的准备请求会把请求路径中的 {setup_order_id} 替换为第一个订单的 ID。
	SetupRequests []struct {
		As     string          `json:"as"`
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	} `json:"setup_requests"`
	Request struct {
		Method    string            `json:"method"`
		Path      string            `json:"path"`
		Headers   map[string]string `json:"headers"`
		Body      json.RawMessage   `json:"body"`       // JSON 请求体，自动带 Content-Type: application/json
		BodyBytes int               `json:"body_bytes"` // 或者：n 字节的填充内容（测请求体上限）
		Repeat    int               `json:"repeat"`
		As        string            `json:"as"`        // 以该演示账号登录后发请求（密码为 seed.DevPassword）
		Multipart *fixtureMultipart `json:"multipart"` // multipart/form-data 请求体
		// ObjectStorage 为 "unavailable" 时模拟未配置对象存储。
		ObjectStorage string `json:"object_storage"`
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
	"<product_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, "p_") && len(s) == 26
	},
	"<sku_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, "sku_") && len(s) == 28
	},
	"<cart_item_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, "ci_") && len(s) == 27
	},
	"<user_coupon_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, "uc_") && len(s) == 27
	},
	"<order_id>":            idPlaceholder("o_"),
	"<order_item_id>":       idPlaceholder("oi_"),
	"<payment_id>":          idPlaceholder("pay_"),
	"<checkout_request_id>": idPlaceholder("chk_"),
	"<review_id>":           idPlaceholder("rv_"),
	"<promotion_id>":        idPlaceholder("promo_"),
	"<audit_id>":            idPlaceholder("aud_"),
	// 记录在数据里的、其他请求的 request id（例如审计记录里的），只校验格式。
	"<recorded_request_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && randomIDPattern.MatchString(s)
	},
	"<order_no>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && orderNoPattern.MatchString(s)
	},
	"<transaction_no>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && transactionNoPattern.MatchString(s)
	},
	"<document_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, "doc_") && len(s) == 28
	},
	"<file_id>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, "file_") && len(s) == 29
	},
	"<file_url>": func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, fileURLPrefix+"file_") && len(s) == len(fileURLPrefix)+29
	},
}

var (
	orderNoPattern       = regexp.MustCompile(`^BS\d{20}$`)
	transactionNoPattern = regexp.MustCompile(`^MOCK[0-9A-F]{20}$`)
	randomIDPattern      = regexp.MustCompile(`^[0-9a-f]{24}$`)
)

// idPlaceholder 匹配 “前缀 + 24 位 hex” 的随机 ID（domain.NewID）。
func idPlaceholder(prefix string) func(got any, _ *httptest.ResponseRecorder) bool {
	return func(got any, _ *httptest.ResponseRecorder) bool {
		s, ok := got.(string)
		return ok && strings.HasPrefix(s, prefix) && randomIDPattern.MatchString(strings.TrimPrefix(s, prefix))
	}
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
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Sprintf("%s = %v, want %d items", path, got, len(w))
		}
		for i := range w {
			if msg := matchFixture(w[i], g[i], rec, fmt.Sprintf("%s[%d]", path, i)); msg != "" {
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
			if fx.Request.ObjectStorage == "unavailable" {
				ts.Server.objects = nil
			}

			path := fx.Request.Path
			if fx.Setup != nil {
				req := fixtureUploadRequest(t, fx.Setup.Multipart)
				req.Header.Set("Authorization", "Bearer "+ts.login(t, fx.Setup.As, seed.DevPassword).Token)
				rec := ts.do(req)
				if rec.Code != 201 {
					t.Fatalf("setup upload: %d %s", rec.Code, rec.Body)
				}
				path = strings.ReplaceAll(path, "{setup_file_id}", decodeBody[uploadFileResponse](t, rec).File.FileID)
			}

			for i, sr := range fx.SetupRequests {
				var body any
				if len(sr.Body) > 0 {
					body = sr.Body
				}
				rec := ts.call(t, sr.Method, sr.Path, ts.login(t, sr.As, seed.DevPassword).Token, body)
				if rec.Code < 200 || rec.Code > 299 {
					t.Fatalf("setup request %d: %d %s", i, rec.Code, rec.Body)
				}
				// 结算类的准备请求：请求路径中的 {setup_order_id} 替换为它创建的第一个订单。
				var created struct {
					Items []struct {
						OrderID string `json:"order_id"`
					} `json:"items"`
				}
				if json.Unmarshal(rec.Body.Bytes(), &created) == nil && len(created.Items) > 0 && created.Items[0].OrderID != "" {
					path = strings.ReplaceAll(path, "{setup_order_id}", created.Items[0].OrderID)
				}
				time.Sleep(2 * time.Millisecond) // 时间精确到毫秒：隔开以保持加入顺序
			}

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
				req := httptest.NewRequest(fx.Request.Method, path, body)
				if len(fx.Request.Body) > 0 {
					req.Header.Set("Content-Type", "application/json")
				}
				if fx.Request.Multipart != nil {
					req = fixtureUploadRequest(t, fx.Request.Multipart)
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

func fixtureUploadRequest(t *testing.T, m *fixtureMultipart) *http.Request {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(m.ContentBase64)
	if err != nil {
		t.Fatalf("content_base64: %v", err)
	}
	body, ct := multipartBody(t, filePart{field: m.Field, filename: m.Filename, contentType: m.ContentType, data: data})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", body)
	req.Header.Set("Content-Type", ct)
	return req
}
