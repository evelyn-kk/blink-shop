package vector

import (
	"context"
	"log/slog"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// Stats 是一次全量建索引的结果。
type Stats struct {
	Documents int `json:"documents"`
	Chunks    int `json:"chunks"`
	Products  int `json:"products"`
	Failed    int `json:"failed"`
}

const bootstrapPage = 100

// Bootstrap 把已索引的知识文档和全部在售商品写入向量索引（可重复执行：按主键覆盖）。k 或 p 为 nil 时跳过对应部分。
// 单篇文档或单批商品失败只计数并记日志，不中断；Store 读取失败返回错误。
func Bootstrap(ctx context.Context, st store.Store, k *KnowledgeIndex, p *ProductIndex, logger *slog.Logger) (Stats, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var s Stats
	if k != nil {
		for page := 1; ; page++ {
			docs, total, err := st.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Status: domain.DocIndexed, Page: store.Page{Page: page, PageSize: bootstrapPage}})
			if err != nil {
				return s, err
			}
			for _, d := range docs {
				chunks, err := st.ListDocumentChunks(ctx, d.DocumentID)
				if err != nil {
					return s, err
				}
				items := make([]rag.IndexedChunk, len(chunks))
				for i, c := range chunks {
					items[i] = rag.IndexedChunk{ChunkID: c.ChunkID, DocumentID: c.DocumentID, MerchantID: c.MerchantID, ProductID: c.ProductID,
						DocType: d.DocType, Title: c.Title, Content: c.Content}
				}
				if err := k.Upsert(ctx, d.DocumentID, items); err != nil {
					s.Failed++
					logger.WarnContext(ctx, "vector bootstrap: document failed", "document_id", d.DocumentID, "error", err)
					continue
				}
				s.Documents++
				s.Chunks += len(items)
			}
			if page*bootstrapPage >= total {
				break
			}
		}
	}
	if p != nil {
		cats, err := st.ListCategories(ctx)
		if err != nil {
			return s, err
		}
		names := map[string]string{}
		for _, c := range cats {
			names[c.CategoryID] = c.Name
		}
		for page := 1; ; page++ {
			items, total, err := st.SearchVisibleProducts(ctx, store.ProductSearch{Page: store.Page{Page: page, PageSize: bootstrapPage}})
			if err != nil {
				return s, err
			}
			batch := make([]rag.IndexedProduct, 0, len(items))
			for _, it := range items {
				batch = append(batch, rag.IndexedProduct{ProductID: it.ProductID, MerchantID: it.MerchantID, CategoryID: it.CategoryID, Text: rag.ProductText(it, names[it.CategoryID])})
			}
			if err := p.UpsertProducts(ctx, batch); err != nil {
				s.Failed++
				logger.WarnContext(ctx, "vector bootstrap: product batch failed", "page", page, "error", err)
			} else {
				s.Products += len(batch)
			}
			if page*bootstrapPage >= total {
				break
			}
		}
	}
	return s, nil
}
