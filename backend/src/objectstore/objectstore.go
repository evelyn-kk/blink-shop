// Package objectstore 封装私有文件的对象存储（MinIO / S3 兼容）。
// 业务层只依赖 Store 接口；MinIO 实现见 minio.go，测试和失败注入用 Memory。
package objectstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"time"
)

var (
	// ErrNotFound 对象不存在。
	ErrNotFound = errors.New("objectstore: object not found")
	// ErrUnavailable 对象存储未配置或暂时不可用；调用方据此返回 object_storage_unavailable。
	ErrUnavailable = errors.New("objectstore: unavailable")
)

// Store 是对象存储的最小接口。所有对象都是私有的，只能经过 API 授权后读取。
type Store interface {
	// Put 写入 size 字节；写入失败时不留下可读对象。
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get 打开对象；不存在返回 ErrNotFound。调用方负责 Close。
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete 删除对象；对象不存在不算错误。
	Delete(ctx context.Context, key string) error
}

// NewObjectKey 生成对象 key：uploads/<年>/<月>/<32 位随机 hex><扩展名>。
// key 不含账户、文件名等任何客户端可控或可推测的内容，也不会返回给客户端。
func NewObjectKey(now time.Time, ext string) string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // Go 1.24 起 crypto/rand.Read 不会返回错误
	return "uploads/" + now.UTC().Format("2006/01") + "/" + hex.EncodeToString(b[:]) + ext
}
