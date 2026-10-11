package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/vector"
)

const vectorStatusTimeout = 5 * time.Second

// vectorIndexView 是一类向量索引的状态。Collection 为空表示没有用 Milvus（未启用，或图片用的是进程内存索引）。
type vectorIndexView struct {
	Enabled         bool                     `json:"enabled"`
	Backend         string                   `json:"backend"` // milvus / memory / ""
	Embedder        string                   `json:"embedder"`
	Collection      *vector.CollectionStatus `json:"collection"`
	IndexedProducts *int                     `json:"indexed_products,omitempty"`
}

type vectorStatusView struct {
	Knowledge vectorIndexView  `json:"knowledge"`
	Products  vectorIndexView  `json:"products"`
	Images    vectorIndexView  `json:"images"`
	Sync      productSyncStats `json:"sync"`
	CheckedAt time.Time        `json:"checked_at"`
}

type milvusStatus interface {
	Status(ctx context.Context) (vector.CollectionStatus, string)
}

// handleVectorStatus：GET /admin/vector/status。只读查询本服务配置的 3 个集合（不会创建或修改集合），以及本进程的商品向量同步结果。
func (s *Server) handleVectorStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), vectorStatusTimeout)
	defer cancel()
	out := vectorStatusView{Sync: s.syncs.stats(), CheckedAt: s.now().UTC()}
	if idx, ok := s.vectorIndex.(milvusStatus); ok {
		out.Knowledge = milvusIndexView(ctx, idx)
	}
	if idx, ok := s.productIndex.(milvusStatus); ok {
		out.Products = milvusIndexView(ctx, idx)
	}
	if s.imageSearch != nil {
		out.Images = vectorIndexView{Enabled: true, Embedder: s.imageSearch.EmbedderName()}
		switch idx := s.imageSearch.Index().(type) {
		case *vector.ImageIndex:
			st := idx.Status(ctx)
			out.Images.Backend, out.Images.Collection = "milvus", &st
		case *imagesearch.MemoryIndex:
			n := len(idx.Products())
			out.Images.Backend, out.Images.IndexedProducts = "memory", &n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func milvusIndexView(ctx context.Context, idx milvusStatus) vectorIndexView {
	st, embedder := idx.Status(ctx)
	return vectorIndexView{Enabled: true, Backend: "milvus", Embedder: embedder, Collection: &st}
}
