package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/objectstore/miniotest"
)

func TestNewObjectKey(t *testing.T) {
	at := time.Date(2026, 1, 31, 23, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	pattern := regexp.MustCompile(`^uploads/2026/01/[0-9a-f]{32}\.png$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		key := NewObjectKey(at, ".png")
		if !pattern.MatchString(key) {
			t.Fatalf("key = %q", key)
		}
		if seen[key] {
			t.Fatalf("duplicate key %q", key)
		}
		seen[key] = true
	}
}

// runStoreContract 是 Memory 和 MinIO 共用的行为约束。
func runStoreContract(t *testing.T, s Store) {
	ctx := context.Background()
	key := NewObjectKey(time.Now(), ".png")
	data := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 1000))

	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing err = %v, want ErrNotFound", err)
	}
	if err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "image/png"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get = %d bytes, %v", len(got), err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v", err)
	}
	// 删除不存在的对象不算错误（补偿删除可能重复执行）。
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	m := NewMemory()
	runStoreContract(t, m)

	ctx := context.Background()
	m.SetFailPut(ErrUnavailable)
	if err := m.Put(ctx, "k", strings.NewReader("x"), 1, "text/plain"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("FailPut err = %v", err)
	}
	if len(m.Keys()) != 0 {
		t.Fatal("failed put stored an object")
	}
	m.SetFailPut(nil)
	if err := m.Put(ctx, "k", strings.NewReader("xy"), 1, "text/plain"); err == nil {
		t.Fatal("size mismatch accepted")
	}
}

// TestMinIO 每次使用新桶，验证按需建桶、读写删和“不存在”的识别。
func TestMinIO(t *testing.T) {
	conn := miniotest.Fresh(t)
	cfg := MinIOConfig{Endpoint: conn.Endpoint, AccessKey: conn.AccessKey, SecretKey: conn.SecretKey, Bucket: conn.Bucket}
	s, err := NewMinIO(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runStoreContract(t, s)

	// 同一个桶的第二个客户端：桶已存在时不报错。
	s2, err := NewMinIO(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runStoreContract(t, s2)

	// 凭据错误：写入失败归为 ErrUnavailable。
	bad := cfg
	bad.SecretKey = "wrong-secret"
	sb, _ := NewMinIO(bad)
	if err := sb.Put(context.Background(), "k", strings.NewReader("x"), 1, "text/plain"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("bad credentials err = %v", err)
	}
}

func TestMinIOUnreachable(t *testing.T) {
	s, err := NewMinIO(MinIOConfig{Endpoint: "127.0.0.1:1", AccessKey: "a", SecretKey: "b", Bucket: "blink-shop"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.Put(ctx, "k", strings.NewReader("x"), 1, "text/plain"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Put err = %v, want ErrUnavailable", err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrUnavailable", err)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatalf("unreachable storage took %v to fail", time.Since(start))
	}
	if _, err := NewMinIO(MinIOConfig{Bucket: "x"}); err == nil {
		t.Fatal("empty endpoint accepted")
	}
}
