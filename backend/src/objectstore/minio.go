package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIOConfig 是连接参数；Endpoint 形如 127.0.0.1:9000（不带协议）。
type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// MinIO 是 Store 的 MinIO 实现。桶在第一次写入时按需创建；创建失败下次写入会重试。
type MinIO struct {
	client *minio.Client
	bucket string

	mu          sync.Mutex
	bucketReady bool
}

func NewMinIO(cfg MinIOConfig) (*MinIO, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("objectstore: endpoint 和 bucket 不能为空")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		// 默认重试 10 次；存储不可用时让上传尽快失败并返回 object_storage_unavailable，而不是拖到请求超时。
		MaxRetries: 3,
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: 创建 MinIO 客户端失败: %w", err)
	}
	return &MinIO{client: client, bucket: cfg.Bucket}, nil
}

func (m *MinIO) ensureBucket(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bucketReady {
		return nil
	}
	exists, err := m.client.BucketExists(ctx, m.bucket)
	if err != nil {
		return fmt.Errorf("%w: 检查桶失败: %v", ErrUnavailable, err)
	}
	if !exists {
		if err := m.client.MakeBucket(ctx, m.bucket, minio.MakeBucketOptions{}); err != nil &&
			minio.ToErrorResponse(err).Code != "BucketAlreadyOwnedByYou" {
			return fmt.Errorf("%w: 创建桶失败: %v", ErrUnavailable, err)
		}
	}
	m.bucketReady = true
	return nil
}

func (m *MinIO) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if err := m.ensureBucket(ctx); err != nil {
		return err
	}
	// 已知大小时 minio-go 单次 PUT（大文件自动分片），失败不会留下对象。
	if _, err := m.client.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("%w: 写入对象失败: %v", ErrUnavailable, err)
	}
	return nil
}

func (m *MinIO) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: 读取对象失败: %v", ErrUnavailable, err)
	}
	// GetObject 是惰性的，先 Stat 一次把“不存在”和连接错误区分出来。
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: 读取对象失败: %v", ErrUnavailable, err)
	}
	return obj, nil
}

func (m *MinIO) Delete(ctx context.Context, key string) error {
	if err := m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("%w: 删除对象失败: %v", ErrUnavailable, err)
	}
	return nil
}

func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" || resp.StatusCode == http.StatusNotFound
}
