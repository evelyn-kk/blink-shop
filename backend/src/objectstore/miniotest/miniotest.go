// Package miniotest 为集成测试提供真实 MinIO 的连接参数。
//
// 需要设置 MinIO 地址，例如：
//
//	BLINK_TEST_MINIO_ENDPOINT=127.0.0.1:9000
//
// 凭据默认 minioadmin，可用 BLINK_TEST_MINIO_ACCESS_KEY / BLINK_TEST_MINIO_SECRET_KEY 覆盖。
// 未设置时本地跳过；CI 环境（CI 非空）下未设置直接失败，保证集成测试一定被执行。
package miniotest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const EndpointEnv = "BLINK_TEST_MINIO_ENDPOINT"

// Conn 是连接参数；Bucket 每次随机，由被测代码在首次写入时创建，用例结束后连同其中对象一起删除。
type Conn struct {
	Endpoint, AccessKey, SecretKey, Bucket string
}

func Fresh(t testing.TB) Conn {
	t.Helper()
	endpoint := os.Getenv(EndpointEnv)
	if endpoint == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s 未设置：CI 中必须运行 MinIO 集成测试", EndpointEnv)
		}
		t.Skipf("未设置 %s，跳过 MinIO 集成测试", EndpointEnv)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	c := Conn{
		Endpoint:  endpoint,
		AccessKey: envOr("BLINK_TEST_MINIO_ACCESS_KEY", "minioadmin"),
		SecretKey: envOr("BLINK_TEST_MINIO_SECRET_KEY", "minioadmin"),
		Bucket:    "blink-test-" + hex.EncodeToString(b[:]),
	}
	t.Cleanup(func() { removeBucket(c) })
	return c
}

func removeBucket(c Conn) {
	client, err := minio.New(c.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(c.AccessKey, c.SecretKey, "")})
	if err != nil {
		return
	}
	ctx := context.Background()
	for obj := range client.ListObjects(ctx, c.Bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err == nil {
			_ = client.RemoveObject(ctx, c.Bucket, obj.Key, minio.RemoveObjectOptions{})
		}
	}
	_ = client.RemoveBucket(ctx, c.Bucket)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
