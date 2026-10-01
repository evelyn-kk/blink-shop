package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSeedRefusesProduction(t *testing.T) {
	err := run(context.Background(), envOf(map[string]string{"APP_ENV": "production"}), &bytes.Buffer{}, storetest.FastHash)
	if err == nil || !strings.Contains(err.Error(), "生产环境禁止") {
		t.Fatalf("err = %v, want production refusal", err)
	}
}

// 空库执行两次：第一次迁移并写入全部数据，第二次什么都不新增。
func TestSeedTwiceOnEmptyDatabase(t *testing.T) {
	env := envOf(map[string]string{"MYSQL_DSN": mysqltest.FreshDSN(t)})

	var first bytes.Buffer
	if err := run(context.Background(), env, &first, storetest.FastHash); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !strings.Contains(first.String(), "本次执行 2 个 [0001_init.sql 0002_promotion_discount_rate_check.sql]") || strings.Contains(first.String(), "本次新增 0 行") {
		t.Fatalf("unexpected first output:\n%s", first.String())
	}
	if !strings.Contains(first.String(), "blink_admin") || !strings.Contains(first.String(), "blink_merchant") || !strings.Contains(first.String(), "blink_user") {
		t.Fatalf("accounts not listed:\n%s", first.String())
	}

	var second bytes.Buffer
	if err := run(context.Background(), env, &second, storetest.FastHash); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(second.String(), "本次执行 0 个") || !strings.Contains(second.String(), "本次新增 0 行") {
		t.Fatalf("second run should be a no-op:\n%s", second.String())
	}
}
