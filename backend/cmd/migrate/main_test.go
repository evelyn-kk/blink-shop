package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// 空库：只列出不执行；-apply 执行全部；再执行一次什么都不做；数据库里有更新的版本时拒绝 -apply。
func TestMigrateCommand(t *testing.T) {
	dsn := mysqltest.FreshDSN(t)
	env := envOf(map[string]string{"MYSQL_DSN": dsn})
	var out strings.Builder
	if err := run(context.Background(), nil, env, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "已应用 0 个") || strings.Contains(out.String(), "本次执行") {
		t.Fatalf("dry run: %s", out.String())
	}
	out.Reset()
	if err := run(context.Background(), []string{"-apply"}, env, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "待执行 0 个") && !strings.Contains(out.String(), "本次执行") {
		t.Fatalf("apply: %s", out.String())
	}
	out.Reset()
	if err := run(context.Background(), []string{"-apply"}, env, &out); err != nil || !strings.Contains(out.String(), "待执行 0 个") || !strings.Contains(out.String(), "本次执行 0 个") {
		t.Fatalf("second apply: %v %s", err, out.String())
	}
	// 模拟回滚：数据库里多一个程序不认识的版本
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (9999, '9999_future.sql', 'x', NOW(3))"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(context.Background(), nil, env, &out); err != nil || !strings.Contains(out.String(), "不认识的版本 [9999]") {
		t.Fatalf("unknown version listing: %v %s", err, out.String())
	}
	if err := run(context.Background(), []string{"-apply"}, env, &out); err == nil || !strings.Contains(err.Error(), "比程序新") {
		t.Fatalf("apply on newer db: %v", err)
	}
}
