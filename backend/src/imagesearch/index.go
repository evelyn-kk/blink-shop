// Package imagesearch 是图片找商品：商品图建索引（读取平台图片或白名单域名的 https 图片 → 图片向量 → 索引），
// 用户上传的图片检索相似的可售商品。向量结果一律回到 Store 按可见性取回。
package imagesearch

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
)

// IndexedImage 是写入索引的一张商品图。
type IndexedImage struct {
	ProductID  string
	MerchantID string
	ImageURL   string
	Vector     []float32
}

// Hit 是一张图的检索命中（余弦相似度）。
type Hit struct {
	ProductID string
	ImageURL  string
	Score     float64
}

// Index 是商品图向量索引。
type Index interface {
	// ReplaceProduct 删除商品已有的图再写入 images（images 为空等于删除）。
	ReplaceProduct(ctx context.Context, productID string, images []IndexedImage) error
	DeleteProducts(ctx context.Context, ids []string) error
	// Search 返回最相似的 k 张图（不按商品合并）。
	Search(ctx context.Context, vec []float32, k int) ([]Hit, error)
}

// MemoryIndex 是进程内的暴力检索索引，用于测试、评测和没有 Milvus 的本地演示。
type MemoryIndex struct {
	mu   sync.RWMutex
	byID map[string][]IndexedImage
}

func NewMemoryIndex() *MemoryIndex { return &MemoryIndex{byID: map[string][]IndexedImage{}} }

func (m *MemoryIndex) ReplaceProduct(_ context.Context, productID string, images []IndexedImage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(images) == 0 {
		delete(m.byID, productID)
		return nil
	}
	m.byID[productID] = slices.Clone(images)
	return nil
}

func (m *MemoryIndex) DeleteProducts(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.byID, id)
	}
	return nil
}

func (m *MemoryIndex) Search(_ context.Context, vec []float32, k int) ([]Hit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var hits []Hit
	for _, imgs := range m.byID {
		for _, img := range imgs {
			hits = append(hits, Hit{ProductID: img.ProductID, ImageURL: img.ImageURL, Score: imagevector.Cosine(vec, img.Vector)})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ImageURL < hits[j].ImageURL
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// Products 返回已索引的商品 ID（测试用）。
func (m *MemoryIndex) Products() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.byID))
	for id := range m.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
