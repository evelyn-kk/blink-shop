package vector

import (
	"context"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
)

const maxEmbedRunes = 2000

// KnowledgeIndex 是知识分块的向量索引（实现 rag.VectorIndex）。集合字段：id(chunk_id)、vector、document_id、merchant_id、product_id、doc_type。
type KnowledgeIndex struct {
	m          *Milvus
	e          Embedder
	collection string
	once       sync.Mutex
	ready      bool
}

// NewKnowledgeIndex 创建索引；m 或 e 为 nil 时返回 nil。
func NewKnowledgeIndex(m *Milvus, e Embedder, collection string) *KnowledgeIndex {
	if m == nil || e == nil {
		return nil
	}
	return &KnowledgeIndex{m: m, e: e, collection: collection}
}

var knowledgeFields = []Field{{"document_id", 64}, {"merchant_id", 64}, {"product_id", 64}, {"doc_type", 32}}

func (k *KnowledgeIndex) ensure(ctx context.Context) error {
	k.once.Lock()
	defer k.once.Unlock()
	if k.ready {
		return nil
	}
	if err := k.m.EnsureCollection(ctx, k.collection, k.e.Dim(), knowledgeFields); err != nil {
		return err
	}
	k.ready = true
	return nil
}

// Upsert 先删掉文档已有的向量再写入新分块。
func (k *KnowledgeIndex) Upsert(ctx context.Context, documentID string, chunks []rag.IndexedChunk) error {
	if err := k.ensure(ctx); err != nil {
		return err
	}
	if err := k.m.Delete(ctx, k.collection, "document_id == "+quote(documentID)); err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = clip(c.Title + "\n" + c.Content)
	}
	vecs, err := k.e.Embed(ctx, texts)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, len(chunks))
	for i, c := range chunks {
		rows[i] = map[string]any{"id": c.ChunkID, "vector": vecs[i], "document_id": c.DocumentID, "merchant_id": c.MerchantID,
			"product_id": c.ProductID, "doc_type": c.DocType}
	}
	return k.m.Upsert(ctx, k.collection, rows)
}

// Search 检索相似分块；相似度小于 0 的丢弃。过滤条件与关键词检索一致（MerchantIDs 里的空串表示平台资料）。
func (k *KnowledgeIndex) Search(ctx context.Context, text string, topN int, f rag.VectorFilter) ([]rag.VectorHit, error) {
	if err := k.ensure(ctx); err != nil {
		return nil, err
	}
	vecs, err := k.e.Embed(ctx, []string{clip(text)})
	if err != nil {
		return nil, err
	}
	hits, err := k.m.Search(ctx, k.collection, vecs[0], topN, and(inList("merchant_id", f.MerchantIDs), inList("product_id", f.ProductIDs), inList("doc_type", f.DocTypes)))
	if err != nil {
		return nil, err
	}
	out := make([]rag.VectorHit, 0, len(hits))
	for _, h := range hits {
		if h.Distance > 0 {
			out = append(out, rag.VectorHit{ChunkID: h.ID, Score: h.Distance})
		}
	}
	return out, nil
}

// ProductIndex 是商品的向量索引（实现 rag.ProductIndex）。集合字段：id(product_id)、vector、merchant_id、category_id。
type ProductIndex struct {
	m          *Milvus
	e          Embedder
	collection string
	once       sync.Mutex
	ready      bool
}

// NewProductIndex 创建索引；m 或 e 为 nil 时返回 nil。
func NewProductIndex(m *Milvus, e Embedder, collection string) *ProductIndex {
	if m == nil || e == nil {
		return nil
	}
	return &ProductIndex{m: m, e: e, collection: collection}
}

var productFields = []Field{{"merchant_id", 64}, {"category_id", 64}}

func (p *ProductIndex) ensure(ctx context.Context) error {
	p.once.Lock()
	defer p.once.Unlock()
	if p.ready {
		return nil
	}
	if err := p.m.EnsureCollection(ctx, p.collection, p.e.Dim(), productFields); err != nil {
		return err
	}
	p.ready = true
	return nil
}

func (p *ProductIndex) UpsertProducts(ctx context.Context, items []rag.IndexedProduct) error {
	if len(items) == 0 {
		return nil
	}
	if err := p.ensure(ctx); err != nil {
		return err
	}
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = clip(it.Text)
	}
	vecs, err := p.e.Embed(ctx, texts)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, len(items))
	for i, it := range items {
		rows[i] = map[string]any{"id": it.ProductID, "vector": vecs[i], "merchant_id": it.MerchantID, "category_id": it.CategoryID}
	}
	return p.m.Upsert(ctx, p.collection, rows)
}

func (p *ProductIndex) DeleteProducts(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := p.ensure(ctx); err != nil {
		return err
	}
	return p.m.Delete(ctx, p.collection, inList("id", ids))
}

func (p *ProductIndex) SearchProducts(ctx context.Context, text string, topN int) ([]rag.ProductHit, error) {
	if err := p.ensure(ctx); err != nil {
		return nil, err
	}
	vecs, err := p.e.Embed(ctx, []string{clip(text)})
	if err != nil {
		return nil, err
	}
	hits, err := p.m.Search(ctx, p.collection, vecs[0], topN, "")
	if err != nil {
		return nil, err
	}
	out := make([]rag.ProductHit, 0, len(hits))
	for _, h := range hits {
		if h.Distance > 0 {
			out = append(out, rag.ProductHit{ProductID: h.ID, Score: h.Distance})
		}
	}
	return out, nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxEmbedRunes {
		return string([]rune(s)[:maxEmbedRunes])
	}
	return s
}
