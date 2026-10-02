package mysqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const documentColumns = `document_id, merchant_id, product_id, title, doc_type, content, status, chunk_count, source_url,
	content_hash, metadata_json, error_reason, created_at, updated_at`

// 列表不读正文，正文位置用空串占位，扫描逻辑与详情共用。
const documentListColumns = `document_id, merchant_id, product_id, title, doc_type, '', status, chunk_count, source_url,
	content_hash, metadata_json, error_reason, created_at, updated_at`

func scanDocument(row rowScanner) (domain.KnowledgeDocument, error) {
	var d domain.KnowledgeDocument
	var meta []byte
	if err := row.Scan(&d.DocumentID, &d.MerchantID, &d.ProductID, &d.Title, &d.DocType, &d.Content, &d.Status, &d.ChunkCount,
		&d.SourceURL, &d.ContentHash, &meta, &d.ErrorReason, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return domain.KnowledgeDocument{}, mapErr(err)
	}
	if len(meta) > 0 {
		if err := fromJSON("knowledge_documents.metadata_json", meta, &d.Metadata); err != nil {
			return domain.KnowledgeDocument{}, err
		}
	}
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	return d, nil
}

func (s *Store) CreateDocument(ctx context.Context, d domain.KnowledgeDocument) (domain.KnowledgeDocument, error) {
	if err := store.ValidateDocument(d); err != nil {
		return domain.KnowledgeDocument{}, err
	}
	if d.DocumentID == "" {
		d.DocumentID = domain.NewID(domain.PrefixDocument)
	}
	now := s.timestamp()
	d.CreatedAt, d.UpdatedAt, d.ChunkCount = now, now, 0
	if err := s.insertDocument(ctx, d); err != nil {
		return domain.KnowledgeDocument{}, err
	}
	return d, nil
}

func (s *Store) GetDocument(ctx context.Context, documentID string) (domain.KnowledgeDocument, error) {
	return scanDocument(s.q(ctx).QueryRowContext(ctx, `SELECT `+documentColumns+` FROM knowledge_documents WHERE document_id = ?`, documentID))
}

func (s *Store) GetDocumentByHash(ctx context.Context, merchantID, contentHash string) (domain.KnowledgeDocument, error) {
	return scanDocument(s.q(ctx).QueryRowContext(ctx,
		`SELECT `+documentColumns+` FROM knowledge_documents WHERE merchant_id = ? AND content_hash = ?`, merchantID, contentHash))
}

func (s *Store) UpdateDocument(ctx context.Context, documentID string, fn func(d *domain.KnowledgeDocument) error) (domain.KnowledgeDocument, error) {
	var out domain.KnowledgeDocument
	err := s.WithTx(ctx, func(ctx context.Context) error {
		d, err := scanDocument(s.q(ctx).QueryRowContext(ctx,
			`SELECT `+documentColumns+` FROM knowledge_documents WHERE document_id = ? FOR UPDATE`, documentID))
		if err != nil {
			return err
		}
		before := d
		if err := fn(&d); err != nil {
			return err
		}
		// 主键、归属、hash 与分块数不能通过这里修改。
		d.DocumentID, d.MerchantID, d.ContentHash, d.ChunkCount, d.CreatedAt = before.DocumentID, before.MerchantID, before.ContentHash, before.ChunkCount, before.CreatedAt
		if err := store.ValidateDocument(d); err != nil {
			return err
		}
		if err := store.CheckDocumentTransition(before.Status, d.Status); err != nil {
			return err
		}
		meta, err := nullableJSON(d.Metadata)
		if err != nil {
			return err
		}
		d.UpdatedAt = s.timestamp()
		_, err = s.q(ctx).ExecContext(ctx, `UPDATE knowledge_documents SET product_id = ?, title = ?, doc_type = ?, content = ?,
			status = ?, source_url = ?, metadata_json = ?, error_reason = ?, updated_at = ? WHERE document_id = ?`,
			d.ProductID, d.Title, d.DocType, d.Content, d.Status, d.SourceURL, meta, d.ErrorReason, d.UpdatedAt, d.DocumentID)
		if err != nil {
			return mapErr(err)
		}
		out = d
		return nil
	})
	return out, err
}

func (s *Store) ReplaceDocumentChunks(ctx context.Context, documentID string, chunks []domain.KnowledgeChunk) (domain.KnowledgeDocument, error) {
	var out domain.KnowledgeDocument
	err := s.WithTx(ctx, func(ctx context.Context) error {
		d, err := scanDocument(s.q(ctx).QueryRowContext(ctx,
			`SELECT `+documentColumns+` FROM knowledge_documents WHERE document_id = ? FOR UPDATE`, documentID))
		if err != nil {
			return err
		}
		if d.Status != domain.DocIndexing {
			return fmt.Errorf("%w: 文档状态为 %s，只有 indexing 的文档可以写入分块", store.ErrInvalid, d.Status)
		}
		prepared, err := store.PrepareChunks(d, chunks, s.timestamp())
		if err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM knowledge_chunks WHERE document_id = ?`, documentID); err != nil {
			return mapErr(err)
		}
		for _, c := range prepared {
			if err := s.insertChunk(ctx, c); err != nil {
				return err
			}
		}
		d.Status, d.ChunkCount, d.ErrorReason, d.UpdatedAt = domain.DocIndexed, len(prepared), "", s.timestamp()
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE knowledge_documents SET status = ?, chunk_count = ?, error_reason = '', updated_at = ?
			WHERE document_id = ?`, d.Status, d.ChunkCount, d.UpdatedAt, documentID); err != nil {
			return mapErr(err)
		}
		out = d
		return nil
	})
	return out, err
}

func (s *Store) ListDocuments(ctx context.Context, q store.DocumentQuery) ([]domain.KnowledgeDocument, int, error) {
	where, args := ` WHERE 1 = 1`, []any{}
	if !q.AllMerchants {
		where += ` AND merchant_id = ?`
		args = append(args, q.MerchantID)
	}
	if q.Status != "" {
		where += ` AND status = ?`
		args = append(args, q.Status)
	}
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		where += ` AND title LIKE ?`
		args = append(args, "%"+likeEscape.Replace(kw)+"%")
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_documents`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+documentListColumns+` FROM knowledge_documents`+where+
		` ORDER BY updated_at DESC, document_id DESC LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.KnowledgeDocument{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

const chunkColumns = `c.chunk_id, c.document_id, c.merchant_id, c.product_id, c.chunk_index, c.title, c.content, c.source, c.created_at`

func scanChunk(row rowScanner, extra ...any) (domain.KnowledgeChunk, error) {
	var c domain.KnowledgeChunk
	dest := append([]any{&c.ChunkID, &c.DocumentID, &c.MerchantID, &c.ProductID, &c.ChunkIndex, &c.Title, &c.Content, &c.Source, &c.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return domain.KnowledgeChunk{}, mapErr(err)
	}
	c.CreatedAt = c.CreatedAt.UTC()
	return c, nil
}

func (s *Store) ListDocumentChunks(ctx context.Context, documentID string) ([]domain.KnowledgeChunk, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+chunkColumns+` FROM knowledge_chunks c WHERE c.document_id = ? ORDER BY c.chunk_index`, documentID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.KnowledgeChunk{}
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// 可检索的分块：文档已索引；平台资料或商家营业中；没有关联商品或商品公开可见。
const knowledgeFrom = ` FROM knowledge_chunks c
	JOIN knowledge_documents d ON d.document_id = c.document_id AND d.status = 'indexed'
	LEFT JOIN merchants m ON m.merchant_id = d.merchant_id
	LEFT JOIN products p ON p.product_id = c.product_id
	LEFT JOIN merchants pm ON pm.merchant_id = p.merchant_id
	WHERE (d.merchant_id = '' OR m.status = 'active')
	  AND (c.product_id = '' OR (p.status = 'active' AND pm.status = 'active'))`

func (s *Store) SearchKnowledge(ctx context.Context, q store.KnowledgeQuery) ([]store.KnowledgeHit, error) {
	if len(q.Terms) == 0 && len(q.ChunkIDs) == 0 {
		return []store.KnowledgeHit{}, nil
	}
	where, args := "", []any{}
	order, orderArgs := `c.chunk_id`, []any{}
	if len(q.Terms) > 0 {
		var ors, counts []string
		for _, term := range q.Terms {
			like := "%" + likeEscape.Replace(term) + "%"
			match := `(c.title LIKE ? OR c.content LIKE ? OR d.title LIKE ?)`
			ors, counts = append(ors, match), append(counts, match)
			args = append(args, like, like, like)
			orderArgs = append(orderArgs, like, like, like)
		}
		where += ` AND (` + strings.Join(ors, ` OR `) + `)`
		// 命中词多的分块优先，超过 Limit 时截掉的是最不相关的。
		order = `(` + strings.Join(counts, ` + `) + `) DESC, c.chunk_id`
	}
	for _, f := range []struct {
		column string
		values []string
	}{{"c.chunk_id", q.ChunkIDs}, {"d.merchant_id", q.MerchantIDs}, {"c.product_id", q.ProductIDs}, {"d.doc_type", q.DocTypes}} {
		if len(f.values) == 0 {
			continue
		}
		where += ` AND ` + f.column + ` IN (?` + strings.Repeat(`, ?`, len(f.values)-1) + `)`
		for _, v := range f.values {
			args = append(args, v)
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = store.DefaultKnowledgeLimit
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+chunkColumns+`, d.title, d.doc_type, d.source_url`+knowledgeFrom+where+
		` ORDER BY `+order+` LIMIT ?`, append(append(args, orderArgs...), limit)...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.KnowledgeHit{}
	for rows.Next() {
		var h store.KnowledgeHit
		if h.KnowledgeChunk, err = scanChunk(rows, &h.DocumentTitle, &h.DocType, &h.SourceURL); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
