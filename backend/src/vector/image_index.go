package vector

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
)

// ImageIndex 是商品图的向量索引（实现 imagesearch.Index）。集合字段：id(<product_id>#<序号>)、vector、product_id、merchant_id、image_url。
type ImageIndex struct {
	m          *Milvus
	dim        int
	collection string
	once       sync.Mutex
	ready      bool
}

// NewImageIndex 创建索引；m 为 nil 或 dim 不合法时返回 nil。dim 必须与图片 Embedder 一致。
func NewImageIndex(m *Milvus, dim int, collection string) *ImageIndex {
	if m == nil || dim <= 0 {
		return nil
	}
	return &ImageIndex{m: m, dim: dim, collection: collection}
}

var imageFields = []Field{{"product_id", 64}, {"merchant_id", 64}, {"image_url", 1024}}

func (x *ImageIndex) ensure(ctx context.Context) error {
	x.once.Lock()
	defer x.once.Unlock()
	if x.ready {
		return nil
	}
	if err := x.m.EnsureCollection(ctx, x.collection, x.dim, imageFields); err != nil {
		return err
	}
	x.ready = true
	return nil
}

func (x *ImageIndex) ReplaceProduct(ctx context.Context, productID string, images []imagesearch.IndexedImage) error {
	if err := x.ensure(ctx); err != nil {
		return err
	}
	if err := x.m.Delete(ctx, x.collection, "product_id == "+quote(productID)); err != nil {
		return err
	}
	if len(images) == 0 {
		return nil
	}
	rows := make([]map[string]any, len(images))
	for i, img := range images {
		rows[i] = map[string]any{"id": productID + "#" + strconv.Itoa(i), "vector": img.Vector, "product_id": productID,
			"merchant_id": img.MerchantID, "image_url": img.ImageURL}
	}
	return x.m.Upsert(ctx, x.collection, rows)
}

func (x *ImageIndex) DeleteProducts(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := x.ensure(ctx); err != nil {
		return err
	}
	return x.m.Delete(ctx, x.collection, inList("product_id", ids))
}

func (x *ImageIndex) Search(ctx context.Context, vec []float32, k int) ([]imagesearch.Hit, error) {
	if err := x.ensure(ctx); err != nil {
		return nil, err
	}
	hits, err := x.m.SearchFields(ctx, x.collection, vec, k, "", "image_url")
	if err != nil {
		return nil, err
	}
	out := make([]imagesearch.Hit, 0, len(hits))
	for _, h := range hits {
		pid, _, ok := strings.Cut(h.ID, "#")
		if !ok || h.Distance <= 0 {
			continue
		}
		out = append(out, imagesearch.Hit{ProductID: pid, ImageURL: h.ImageURL, Score: h.Distance})
	}
	return out, nil
}
