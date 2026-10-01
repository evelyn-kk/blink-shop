package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// syncBuffer 让 run 的 goroutine 写日志时测试也能安全读取。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunRejectsUnsafeProductionConfig(t *testing.T) {
	addr := freeAddr(t)
	logs := &syncBuffer{}
	err := run(context.Background(), envOf(map[string]string{"APP_ENV": "production", "API_ADDR": addr}), logs)
	if err == nil || !strings.Contains(err.Error(), "生产环境配置不安全") {
		t.Fatalf("run error = %v, want unsafe production config", err)
	}
	// 必须在监听端口之前失败。
	if conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond); dialErr == nil {
		conn.Close()
		t.Fatal("server should not be listening")
	}
	if strings.Contains(logs.String(), "api listening") {
		t.Fatalf("server started despite unsafe config: %s", logs)
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	err := run(context.Background(), envOf(map[string]string{"HTTP_REQUEST_TIMEOUT": "forever"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTP_REQUEST_TIMEOUT") {
		t.Fatalf("run error = %v, want invalid HTTP_REQUEST_TIMEOUT", err)
	}
}

// MySQL 不可达时进程照常启动：/health 200，/ready 503；ctx 取消后优雅退出。
func TestRunWithMySQLDown(t *testing.T) {
	addr := freeAddr(t)
	env := map[string]string{
		"API_ADDR":  addr,
		"MYSQL_DSN": "blink:pw@tcp(" + freeAddr(t) + ")/blink_shop?timeout=200ms",
	}
	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, envOf(env), logs) }()

	base := "http://" + addr + "/api/v1"
	waitUntilUp(t, base+"/health")
	if code := get(t, base+"/health"); code != http.StatusOK {
		t.Fatalf("/health = %d, want 200", code)
	}
	if code := get(t, base+"/ready"); code != http.StatusServiceUnavailable {
		t.Fatalf("/ready = %d, want 503", code)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not shut down")
	}
	out := logs.String()
	if !strings.Contains(out, "mysql unavailable at startup") || !strings.Contains(out, "api shutdown completed") {
		t.Fatalf("missing lifecycle logs: %s", out)
	}
	if strings.Contains(out, "blink:pw@") {
		t.Fatalf("DSN leaked into logs: %s", out)
	}
}

// 真实 MySQL：未迁移时 /ready 503；RUN_MIGRATIONS 开启后后台迁移完成，/ready 变为 200。
func TestReadyWaitsForMigrations(t *testing.T) {
	dsn := mysqltest.FreshDSN(t)
	for _, tc := range []struct {
		runMigrations string
		want          int
	}{
		{"false", http.StatusServiceUnavailable},
		{"true", http.StatusOK},
	} {
		t.Run("RUN_MIGRATIONS="+tc.runMigrations, func(t *testing.T) {
			addr := freeAddr(t)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			logs := &syncBuffer{}
			go func() {
				done <- run(ctx, envOf(map[string]string{"API_ADDR": addr, "MYSQL_DSN": dsn, "RUN_MIGRATIONS": tc.runMigrations}), logs)
			}()
			defer func() {
				cancel()
				<-done
			}()
			base := "http://" + addr + "/api/v1"
			waitUntilUp(t, base+"/health")
			deadline := time.Now().Add(15 * time.Second)
			code := get(t, base+"/ready")
			for code != tc.want && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				code = get(t, base+"/ready")
			}
			if code != tc.want {
				t.Fatalf("/ready = %d, want %d; logs: %s", code, tc.want, logs)
			}
			if tc.runMigrations == "false" && !strings.Contains(logs.String(), "数据库迁移未执行") {
				t.Fatalf("readiness log should mention pending migrations: %s", logs)
			}
		})
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitUntilUp(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not start", url)
}

func get(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
