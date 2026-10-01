// Package logging 提供结构化 JSON 日志，并在写出前统一脱敏。
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// Masked 是敏感字段脱敏后的固定值。
const Masked = "***"

// New 返回写 JSON 的 logger，所有字段都会经过脱敏。
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(NewRedactingHandler(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})))
}

// NewRedactingHandler 包装任意 slog.Handler：
//   - 键名含 authorization / password / token / secret / api_key / cookie / dsn 的字段整体替换为 ***；
//   - 其他字符串值中的手机号、邮箱做部分掩码。
func NewRedactingHandler(inner slog.Handler) slog.Handler {
	return &redactingHandler{inner: inner}
}

type redactingHandler struct {
	inner slog.Handler
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, MaskText(record.Message), record.PC)
	record.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, Masked)
	}
	switch a.Value.Kind() {
	case slog.KindGroup:
		group := a.Value.Group()
		cleaned := make([]any, len(group))
		for i, g := range group {
			cleaned[i] = redactAttr(g)
		}
		return slog.Group(a.Key, cleaned...)
	case slog.KindString:
		return slog.String(a.Key, MaskText(a.Value.String()))
	case slog.KindAny:
		return slog.Any(a.Key, redactAny(a.Value.Any()))
	}
	return a
}

// redactAny 处理 error、结构体、map、切片等任意值：先转成 JSON 结构，再按字段名和文本规则递归脱敏，
// 避免敏感字段藏在结构体里绕过脱敏。
func redactAny(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case error:
		return MaskText(t.Error())
	case fmt.Stringer:
		return MaskText(t.String())
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return MaskText(fmt.Sprint(v))
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return MaskText(string(raw))
	}
	return redactJSON(generic)
}

func redactJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, item := range t {
			if IsSensitiveKey(k) {
				t[k] = Masked
			} else {
				t[k] = redactJSON(item)
			}
		}
		return t
	case []any:
		for i, item := range t {
			t[i] = redactJSON(item)
		}
		return t
	case string:
		return MaskText(t)
	default:
		return t
	}
}

var sensitiveKeyParts = []string{"authorization", "password", "passwd", "token", "secret", "apikey", "cookie", "dsn", "credential"}

// IsSensitiveKey 判断字段名是否属于必须整体隐藏的敏感字段（忽略大小写、下划线和连字符）。
func IsSensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	for _, part := range sensitiveKeyParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

var (
	// 中国大陆手机号，可带 +86 / 86 前缀；前后不能紧挨其他数字。
	phonePattern  = regexp.MustCompile(`(^|[^0-9])((?:\+?86[- ]?)?1[3-9][0-9])([0-9]{4})([0-9]{4})($|[^0-9])`)
	emailPattern  = regexp.MustCompile(`([A-Za-z0-9._%+\-])[A-Za-z0-9._%+\-]*@([A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)
	bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=\-]+`)
)

// MaskText 掩码文本中的手机号（保留前 3 后 4 位）、邮箱（保留首字符和域名）和 Bearer token。
func MaskText(s string) string {
	if s == "" {
		return s
	}
	s = bearerPattern.ReplaceAllString(s, "${1}"+Masked)
	s = emailPattern.ReplaceAllString(s, "${1}***@${2}")
	// 连续号码之间共享分隔符，循环直到没有新的匹配。
	for {
		next := phonePattern.ReplaceAllString(s, "${1}${2}****${4}${5}")
		if next == s {
			return s
		}
		s = next
	}
}
