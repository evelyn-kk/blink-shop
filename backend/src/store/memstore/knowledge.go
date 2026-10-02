package memstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func (s *Store) CreateDocument(ctx context.Context, d domain.KnowledgeDocument) (domain.KnowledgeDocument, error) {
	if err := store.ValidateDocument(d); err != nil {
		return domain.KnowledgeDocument{}, err
	}
	defer s.lock(ctx)()
	if d.DocumentID == "" {
		d.DocumentID = domain.NewID(domain.PrefixDocument)
	}
	if _, ok := s.data.documents[d.DocumentID]; ok {
		return domain.KnowledgeDocument{}, &store.ConflictError{Key: store.KeyPrimary}
	}
	for _, other := range s.data.documents {
		if other.MerchantID == d.MerchantID && other.ContentHash == d.ContentHash {
			return domain.KnowledgeDocument{}, &store.ConflictError{Key: store.KeyDocumentMerchantHash}
		}
	}
	now := s.timestamp()
	d.CreatedAt, d.UpdatedAt, d.ChunkCount = now, now, 0
	d.Metadata = maps.Clone(d.Metadata)
	s.data.documents[d.DocumentID] = d
	return d, nil
}

func (s *Store) GetDocument(ctx context.Context, documentID string) (domain.KnowledgeDocument, error) {
	defer s.lock(ctx)()
	d, ok := s.data.documents[documentID]
	if !ok {
		return domain.KnowledgeDocument{}, store.ErrNotFound
	}
	d.Metadata = maps.Clone(d.Metadata)
	return d, nil
}

func (s *Store) GetDocumentByHash(ctx context.Context, merchantID, contentHash string) (domain.KnowledgeDocument, error) {
	defer s.lock(ctx)()
	for _, d := range s.data.documents {
		if d.MerchantID == merchantID && d.ContentHash == contentHash {
			d.Metadata = maps.Clone(d.Metadata)
			return d, nil
		}
	}
	return domain.KnowledgeDocument{}, store.ErrNotFound
}

func (s *Store) UpdateDocument(ctx context.Context, documentID string, fn func(d *domain.KnowledgeDocument) error) (domain.KnowledgeDocument, error) {
	var out domain.KnowledgeDocument
	err := s.WithTx(ctx, func(ctx context.Context) error {
		defer s.lock(ctx)()
		before, ok := s.data.documents[documentID]
		if !ok {
			return store.ErrNotFound
		}
		d := before
		d.Metadata = maps.Clone(before.Metadata)
		if err := fn(&d); err != nil {
			return err
		}
		d.DocumentID, d.MerchantID, d.ContentHash, d.ChunkCount, d.CreatedAt = before.DocumentID, before.MerchantID, before.ContentHash, before.ChunkCount, before.CreatedAt
		if err := store.ValidateDocument(d); err != nil {
			return err
		}
		if err := store.CheckDocumentTransition(before.Status, d.Status); err != nil {
			return err
		}
		d.UpdatedAt = s.timestamp()
		d.Metadata = maps.Clone(d.Metadata)
		s.data.documents[documentID] = d
		out = d
		return nil
	})
	return out, err
}

func (s *Store) ReplaceDocumentChunks(ctx context.Context, documentID string, chunks []domain.KnowledgeChunk) (domain.KnowledgeDocument, error) {
	defer s.lock(ctx)()
	d, ok := s.data.documents[documentID]
	if !ok {
		return domain.KnowledgeDocument{}, store.ErrNotFound
	}
	if d.Status != domain.DocIndexing {
		return domain.KnowledgeDocument{}, fmt.Errorf("%w: 文档状态为 %s，只有 indexing 的文档可以写入分块", store.ErrInvalid, d.Status)
	}
	prepared, err := store.PrepareChunks(d, chunks, s.timestamp())
	if err != nil {
		return domain.KnowledgeDocument{}, err
	}
	for id, c := range s.data.chunks {
		if c.DocumentID == documentID {
			delete(s.data.chunks, id)
		}
	}
	for _, c := range prepared {
		s.data.chunks[c.ChunkID] = c
	}
	d.Status, d.ChunkCount, d.ErrorReason, d.UpdatedAt = domain.DocIndexed, len(prepared), "", s.timestamp()
	s.data.documents[documentID] = d
	d.Metadata = maps.Clone(d.Metadata)
	return d, nil
}

func (s *Store) ListDocuments(ctx context.Context, q store.DocumentQuery) ([]domain.KnowledgeDocument, int, error) {
	defer s.lock(ctx)()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	all := []domain.KnowledgeDocument{}
	for _, d := range s.data.documents {
		if (!q.AllMerchants && d.MerchantID != q.MerchantID) || (q.Status != "" && d.Status != q.Status) ||
			(kw != "" && !containsFold(d.Title, kw)) {
			continue
		}
		d.Content = ""
		d.Metadata = maps.Clone(d.Metadata)
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
			return all[i].UpdatedAt.After(all[j].UpdatedAt)
		}
		return all[i].DocumentID > all[j].DocumentID
	})
	return pageOf(all, q.Page), len(all), nil
}

func (s *Store) ListDocumentChunks(ctx context.Context, documentID string) ([]domain.KnowledgeChunk, error) {
	defer s.lock(ctx)()
	out := []domain.KnowledgeChunk{}
	for _, c := range s.data.chunks {
		if c.DocumentID == documentID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChunkIndex < out[j].ChunkIndex })
	return out, nil
}

func (s *Store) SearchKnowledge(ctx context.Context, q store.KnowledgeQuery) ([]store.KnowledgeHit, error) {
	if len(q.Terms) == 0 && len(q.ChunkIDs) == 0 {
		return []store.KnowledgeHit{}, nil
	}
	defer s.lock(ctx)()
	terms := make([]string, len(q.Terms))
	for i, t := range q.Terms {
		terms[i] = strings.ToLower(t)
	}
	in := func(values []string, v string) bool { return len(values) == 0 || slices.Contains(values, v) }
	out := []store.KnowledgeHit{}
	matched := map[string]int{}
	for _, c := range s.data.chunks {
		d, ok := s.data.documents[c.DocumentID]
		if !ok || d.Status != domain.DocIndexed || !s.knowledgeVisible(d, c) {
			continue
		}
		if !in(q.ChunkIDs, c.ChunkID) || !in(q.MerchantIDs, d.MerchantID) || !in(q.ProductIDs, c.ProductID) || !in(q.DocTypes, d.DocType) {
			continue
		}
		n := 0
		for _, t := range terms {
			if containsFold(c.Title, t) || containsFold(c.Content, t) || containsFold(d.Title, t) {
				n++
			}
		}
		if len(terms) > 0 && n == 0 {
			continue
		}
		matched[c.ChunkID] = n
		out = append(out, store.KnowledgeHit{KnowledgeChunk: c, DocumentTitle: d.Title, DocType: d.DocType, SourceURL: d.SourceURL})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := matched[out[i].ChunkID], matched[out[j].ChunkID]; a != b {
			return a > b
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	limit := q.Limit
	if limit <= 0 {
		limit = store.DefaultKnowledgeLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// knowledgeVisible 与 MySQL 实现的 knowledgeFrom 条件一致。
func (s *Store) knowledgeVisible(d domain.KnowledgeDocument, c domain.KnowledgeChunk) bool {
	if d.MerchantID != "" && s.data.merchants[d.MerchantID].Status != domain.StatusActive {
		return false
	}
	if c.ProductID == "" {
		return true
	}
	p, ok := s.data.products[c.ProductID]
	if !ok {
		return false
	}
	_, visible := s.visible(p)
	return visible
}

func (s *Store) GetMerchant(ctx context.Context, merchantID string) (domain.Merchant, error) {
	defer s.lock(ctx)()
	m, ok := s.data.merchants[merchantID]
	if !ok {
		return domain.Merchant{}, store.ErrNotFound
	}
	return m, nil
}
