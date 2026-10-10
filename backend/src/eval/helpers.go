package eval

import (
	"context"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func storeChunkQuery(ids []string) store.KnowledgeQuery { return store.KnowledgeQuery{ChunkIDs: ids} }

// indexCorpus 把 Store 里已索引的全部知识分块写入向量索引。
func indexCorpus(ctx context.Context, st store.Store, v rag.VectorIndex) error {
	for page := 1; ; page++ {
		docs, total, err := st.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Status: domain.DocIndexed, Page: store.Page{Page: page, PageSize: 100}})
		if err != nil {
			return err
		}
		for _, d := range docs {
			chunks, err := st.ListDocumentChunks(ctx, d.DocumentID)
			if err != nil {
				return err
			}
			items := make([]rag.IndexedChunk, len(chunks))
			for i, c := range chunks {
				items[i] = rag.IndexedChunk{ChunkID: c.ChunkID, DocumentID: c.DocumentID, MerchantID: c.MerchantID, ProductID: c.ProductID, DocType: d.DocType, Title: c.Title, Content: c.Content}
			}
			if err := v.Upsert(ctx, d.DocumentID, items); err != nil {
				return err
			}
		}
		if page*100 >= total {
			return nil
		}
	}
}

// SeedProducts 把开发种子里的在售商品写进商品向量索引（评测用临时集合）。
func SeedProducts(ctx context.Context, p rag.ProductIndex) error {
	st, err := newSeededStore(ctx)
	if err != nil {
		return err
	}
	cats, err := st.ListCategories(ctx)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, c := range cats {
		names[c.CategoryID] = c.Name
	}
	items, _, err := st.SearchVisibleProducts(ctx, store.ProductSearch{Page: store.Page{Page: 1, PageSize: 100}})
	if err != nil {
		return err
	}
	batch := make([]rag.IndexedProduct, 0, len(items))
	for _, it := range items {
		batch = append(batch, rag.IndexedProduct{ProductID: it.ProductID, MerchantID: it.MerchantID, CategoryID: it.CategoryID, Text: rag.ProductText(it, names[it.CategoryID])})
	}
	return p.UpsertProducts(ctx, batch)
}
