package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRunRejectsBadArgsAndProduction(t *testing.T) {
	ctx := context.Background()
	if err := run(ctx, nil, env(nil), io.Discard); err == nil || !strings.Contains(err.Error(), "-generate") {
		t.Fatalf("missing -generate: %v", err)
	}
	prod := env(map[string]string{"APP_ENV": "production"})
	if err := run(ctx, []string{"-generate", "10"}, prod, io.Discard); err == nil || !strings.Contains(err.Error(), "生产环境禁止") {
		t.Fatalf("err = %v, want production refusal", err)
	}
}
