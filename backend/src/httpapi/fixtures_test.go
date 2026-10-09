package httpapi

import (
	"bytes"
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
		// Events 是流式（text/event-stream）响应的全部事件，按顺序逐个比较 data。
		Events []struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		} `json:"events"`
	} `json:"response"`
}

// parseSSE 把 text/event-stream 响应体解析成事件（忽略注释行）。
func parseSSE(t *testing.T, body string) []fixtureEvent {
	t.Helper()
	var out []fixtureEvent
	for _, frame := range strings.Split(body, "\n\n") {
		var ev fixtureEvent
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.Data); err != nil {
					t.Fatalf("bad event data %q: %v", line, err)
				}
			}
		}
		if ev.Event != "" {
			out = append(out, ev)
		}
	}
	return out
}

type fixtureEvent struct {
	Event string
	Data  map[string]any
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
	"<duration_ms>": func(got any, _ *httptest.ResponseRecorder) bool {
		n, ok := got.(float64)
		return ok && n >= 0 && n == float64(int64(n))
	},
	"<session_id>":     idPlaceholder("s_"),
	"<message_id>":     idPlaceholder("msg_"),
	"<run_id>":         idPlaceholder("run_"),
	"<trace_id>":       idPlaceholder("tr_"),
	"<trace_event_id>": idPlaceholder("te_"),
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
		// "<pattern:正则>"：文本里嵌着订单号等随机值时，整段按正则匹配。
		if expr, ok := strings.CutPrefix(s, "<pattern:"); ok && strings.HasSuffix(expr, ">") {
			re, err := regexp.Compile("^" + strings.TrimSuffix(expr, ">") + "$")
			gs, isStr := got.(string)
			if err != nil || !isStr || !re.MatchString(gs) {
				return fmt.Sprintf("%s = %q, want %s", path, got, s)
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
			subs := map[string]string{}
			substitute := func(p string) string {
				for k, v := range subs {
					p = strings.ReplaceAll(p, k, v)
				}
				return p
			}
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
				rec := ts.call(t, sr.Method, substitute(sr.Path), ts.login(t, sr.As, seed.DevPassword).Token, body)
				if rec.Code < 200 || rec.Code > 299 {
					t.Fatalf("setup request %d: %d %s", i, rec.Code, rec.Body)
				}
				// 导购：创建会话的准备请求提供 {setup_session_id}，流式请求提供 {setup_run_id}
				var session struct {
					SessionID string `json:"session_id"`
				}
				if json.Unmarshal(rec.Body.Bytes(), &session) == nil && session.SessionID != "" {
					subs["{setup_session_id}"] = session.SessionID
				}
				if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
					if evs := parseSSE(t, rec.Body.String()); len(evs) > 0 {
						subs["{setup_run_id}"], _ = evs[0].Data["run_id"].(string)
					}
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

			path = substitute(path)
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
			if os.Getenv("BLINK_UPDATE_FIXTURES") == "1" && strings.HasPrefix(name, "agent_") {
				fx.Response = recordFixtureResponse(t, file, raw, fx, rec)
			}
			if len(fx.Response.Events) > 0 {
				evs := parseSSE(t, rec.Body.String())
				if len(evs) != len(fx.Response.Events) {
					t.Fatalf("%d events, want %d; body=%s", len(evs), len(fx.Response.Events), rec.Body)
				}
				for i, want := range fx.Response.Events {
					if evs[i].Event != want.Event {
						t.Fatalf("event[%d] = %s, want %s", i, evs[i].Event, want.Event)
					}
					if msg := matchFixture(map[string]any(want.Data), evs[i].Data, rec, fmt.Sprintf("events[%d]", i)); msg != "" {
						t.Fatalf("%s\nbody=%s", msg, rec.Body)
					}
				}
				return
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

// ---------- 录制 ----------

// recordedPlaceholders 把随机或随时间变化的字段替换成占位符（按字段名），录制导购 fixture 时使用。
var recordedPlaceholders = map[string]string{
	"run_id": "<run_id>", "message_id": "<message_id>", "session_id": "<session_id>", "trace_id": "<trace_id>", "trace_event_id": "<trace_event_id>",
	"duration_ms": "<duration_ms>", "request_id": "<request_id>", "cart_item_id": "<cart_item_id>", "order_id": "<order_id>",
	"order_item_id": "<order_item_id>", "payment_id": "<payment_id>", "order_no": "<order_no>", "transaction_no": "<transaction_no>",
	"checkout_request_id": "<checkout_request_id>", "user_coupon_id": "<user_coupon_id>", "review_id": "<review_id>",
	"created_at": "<timestamp>", "updated_at": "<timestamp>", "claimed_at": "<timestamp>", "paid_at": "<timestamp>", "expires_at": "<timestamp>",
	"payment_deadline_at": "<timestamp>", "last_message_at": "<timestamp>", "pinned_at": "<timestamp>",
}

func placeholderize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if ph, ok := recordedPlaceholders[k]; ok {
				if s, isStr := val.(string); (isStr && s != "") || (!isStr && val != nil) {
					out[k] = ph
					continue
				}
			}
			out[k] = placeholderize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = placeholderize(val)
		}
		return out
	case string:
		// 文本里嵌着的订单号 / 交易号：换成正则占位。
		if embeddedRandom.MatchString(x) {
			parts := embeddedRandom.Split(x, -1)
			found := embeddedRandom.FindAllString(x, -1)
			var b strings.Builder
			for i, p := range parts {
				b.WriteString(regexp.QuoteMeta(p))
				if i < len(found) {
					if strings.HasPrefix(found[i], "BS") {
						b.WriteString(`BS\d{20}`)
					} else {
						b.WriteString(`MOCK[0-9A-F]{20}`)
					}
				}
			}
			return "<pattern:" + b.String() + ">"
		}
	}
	return v
}

var embeddedRandom = regexp.MustCompile(`BS\d{20}|MOCK[0-9A-F]{20}`)

// marshalFixture 序列化而不转义 < >（占位符要原样可读）。
func marshalFixture(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type recordedResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    map[string]any    `json:"body,omitempty"`
	Events  []fixtureEvent    `json:"events,omitempty"`
}

func (e fixtureEvent) MarshalJSON() ([]byte, error) {
	return marshalFixture(struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}{e.Event, e.Data})
}

// recordFixtureResponse 用实际响应重写 fixture 的 response 段（BLINK_UPDATE_FIXTURES=1，只对 agent_* 生效）：
// 其他段原样保留；随机 ID 和时间按字段名换成占位符。写回后返回新的期望值，随后仍按正常流程比对一次。
func recordFixtureResponse(t *testing.T, file string, raw []byte, fx fixture, rec *httptest.ResponseRecorder) (out struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    map[string]any    `json:"body"`
	Events  []struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	} `json:"events"`
}) {
	t.Helper()
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatal(err)
	}
	resp := recordedResponse{Status: rec.Code, Headers: fx.Response.Headers}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		for _, ev := range parseSSE(t, rec.Body.String()) {
			resp.Events = append(resp.Events, fixtureEvent{Event: ev.Event, Data: placeholderize(ev.Data).(map[string]any)})
		}
	} else {
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		resp.Body = placeholderize(body).(map[string]any)
	}
	respRaw, err := marshalFixture(resp)
	if err != nil {
		t.Fatal(err)
	}
	sections["response"] = respRaw
	var buf strings.Builder
	buf.WriteString("{\n")
	order := []string{"description", "setup", "setup_requests", "request", "response"}
	first := true
	for _, k := range order {
		v, ok := sections[k]
		if !ok {
			continue
		}
		if !first {
			buf.WriteString(",\n")
		}
		first = false
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, v, "  ", "  "); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&buf, "  %q: %s", k, pretty.String())
	}
	buf.WriteString("\n}\n")
	if err := os.WriteFile(file, []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var reread fixture
	if err := json.Unmarshal([]byte(buf.String()), &reread); err != nil {
		t.Fatal(err)
	}
	return reread.Response
}
