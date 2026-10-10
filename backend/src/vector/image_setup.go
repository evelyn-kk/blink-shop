package vector

import (
	"log/slog"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ImageEmbedder 按配置创建图片 Embedder：local（默认，本地特征）或 dashscope（多模态模型，Load 已校验密钥）。
func ImageEmbedder(cfg configcenter.Config) imagevector.Embedder {
	if cfg.ImageEmbeddingProvider == "dashscope" {
		if d := imagevector.NewDashScope(imagevector.DashScopeOptions{BaseURL: cfg.ImageEmbeddingBaseURL, APIKey: cfg.ImageEmbeddingAPIKey,
			Model: cfg.ImageEmbeddingModel, Dim: cfg.ImageEmbeddingDim}); d != nil {
			return d
		}
	}
	return imagevector.Local{}
}

// OpenImageSearch 按配置创建图片搜索（Milvus 商品图集合 + 图片 Embedder）；没有配置 Milvus 时返回 nil。
// objects 为 nil 时只能建索引，不能按上传文件检索。
func OpenImageSearch(cfg configcenter.Config, st store.Store, objects objectstore.Store, logger *slog.Logger) *imagesearch.Service {
	if !cfg.ImageSearchEnabled() {
		return nil
	}
	e := ImageEmbedder(cfg)
	idx := NewImageIndex(NewMilvus(cfg.MilvusAddr, cfg.MilvusToken, 10*time.Second), e.Dim(), cfg.MilvusImageCollection)
	if idx == nil {
		return nil
	}
	return imagesearch.New(e, idx, imagesearch.NewSource(cfg.ImageFetchAllowedHosts), st, objects, logger)
}
