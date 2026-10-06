package configcenter

import (
	"context"
	"errors"
	"testing"
)

func TestAdminSettings(t *testing.T) {
	ctx := context.Background()
	env := map[string]string{"RATE_LIMIT_IP_PER_MINUTE": "600", "AI_API_KEY": "sk-real"}
	mem := NewMemorySource(nil)
	a := NewAdmin(NewResolver(func(k string) string { return env[k] }, mem), mem)

	seen := map[string]bool{}
	for _, e := range a.List(ctx) {
		if seen[e.Key.Name] || e.Description == "" {
			t.Fatalf("duplicate or undescribed setting %q", e.Key.Name)
		}
		seen[e.Key.Name] = true
		if e.Key.Secret && e.Runtime {
			t.Fatalf("secret %s must not be runtime-editable", e.Key.Name)
		}
	}
	if e, _ := a.Get(ctx, "ai.api_key"); e.Value != "sk-real" || e.Source != "env" {
		t.Fatalf("secret entry = %+v", e)
	}

	for _, c := range []struct {
		key, value string
		want       error
	}{
		{"no.such.key", "1", ErrUnknownKey},
		{"mysql.dsn", "x", ErrNotRuntime},
		{"ai.api_key", "x", ErrNotRuntime},
		{"http.addr", ":9090", ErrNotRuntime},
		{"http.rate_limit.ip_per_minute", "100", ErrOverriddenByEnv},
	} {
		if _, _, err := a.Set(ctx, c.key, c.value); !errors.Is(err, c.want) {
			t.Fatalf("Set(%s) = %v, want %v", c.key, err, c.want)
		}
	}
	for key, bad := range map[string]string{
		"http.rate_limit.account_per_minute": "0",
		"http.request_timeout":               "soon",
		"http.trust_all_proxies":             "maybe",
		"http.trusted_proxy_cidrs":           "10.0.0.0/33",
		"files.allowed_mime_types":           "text/html",
		"http.cors.allowed_origins":          "evil.example",
		"risk.blocked_words":                 "一二三四五六七八九十一二三四五六七八九十一",
	} {
		if _, _, err := a.Set(ctx, key, bad); err == nil {
			t.Fatalf("Set(%s, %q) accepted", key, bad)
		}
		if e, _ := a.Get(ctx, key); e.Source != "default" {
			t.Fatalf("%s changed by invalid value: %+v", key, e)
		}
	}

	before, after, err := a.Set(ctx, "risk.blocked_words", " 刷单, 套现 ")
	if err != nil || before.Source != "default" || after.Value != "刷单, 套现" || after.Source != "dynamic" {
		t.Fatalf("set = %+v -> %+v, %v", before, after, err)
	}
	// 空值删除动态配置，退回默认值。
	if _, after, err := a.Set(ctx, "risk.blocked_words", ""); err != nil || after.Source != "default" || after.Value != KeyRiskBlockedWords.Default {
		t.Fatalf("reset = %+v, %v", after, err)
	}
	// 修改立即影响按请求读取的 HTTP 配置。
	if _, _, err := a.Set(ctx, "http.request_timeout", "5s"); err != nil {
		t.Fatal(err)
	}
	if got := NewHTTPSettingsProvider(a.resolver, false).Current(ctx).RequestTimeout.String(); got != "5s" {
		t.Fatalf("request timeout = %s", got)
	}
}
