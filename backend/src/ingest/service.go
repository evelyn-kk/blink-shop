package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

var (
	// ErrInProgress 同样内容的文档正在处理中（另一个请求刚提交），稍后再试。
	ErrInProgress = errors.New("ingest: document is being processed")
	// ErrIndexFailed 文档已记录但分块/索引失败，文档状态为 failed，可以重新提交。
	ErrIndexFailed = errors.New("ingest: index failed")
)

// staleAfter：处于 parsing / indexing 超过这个时间的文档视为处理中断（例如进程重启），允许重新处理。
const staleAfter = 10 * time.Minute

// DocTypes 是允许的文档类型：前四种由商家直接提交资料时选择，后三种由非结构化采集按来源生成。
var DocTypes = []string{"product_detail", "faq", "policy", "guide", "web_article", "structured_note", "unstructured_note"}

// Request 是一次入库请求。MerchantID 为空串表示平台资料；DocType 为空时按来源类型决定。
type Request struct {
	MerchantID   string
	ProductID    string
	DocType      string
	Source       Source
	Metadata     map[string]any
	ForceReindex bool
}

// Result：Created 为 true 表示新建了文档；Duplicate 为 true 表示内容与已索引的文档相同，直接返回该文档，没有重新处理；
// 两者都为 false 表示重新处理了已有文档（force_reindex 或此前失败）。
type Result struct {
	Document      domain.KnowledgeDocument
	Created       bool
	Duplicate     bool
	TextRunes     int
	Truncated     bool
	VectorIndexed bool
}

// Service 执行“清洗 → 去重 → 分块 → 写分块 → 向量索引”的完整流程，并按状态机记录文档状态。
type Service struct {
	store   store.Store
	vector  rag.VectorIndex
	fetcher URLFetcher
	logger  *slog.Logger
	now     func() time.Time
}

// NewService：vector 为 nil 表示不建向量索引（检索走关键词）；fetcher 为 nil 时不能采集 URL。
func NewService(st store.Store, vector rag.VectorIndex, fetcher URLFetcher, logger *slog.Logger, now func() time.Time) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, vector: vector, fetcher: fetcher, logger: logger, now: now}
}

// Ingest 处理一次入库。
//
// 状态流转：新文档 uploaded → parsing（切块）→ indexing（写分块、建向量）→ indexed；任一步失败 → failed（记录原因）。
// 去重：同一商家清洗后内容 hash 相同时，已索引的文档直接返回（Duplicate）；ForceReindex 或文档此前失败时，
// 用本次的标题、类型、商品和元数据重新处理同一篇文档（indexed/failed → parsing → …），不新建文档。
//
// 错误：*InputError（400）、ErrURLNotAllowed（400）、ErrFetchFailed（502）、ErrInProgress（409）、
// ErrIndexFailed（文档已标记 failed，500），其他为内部错误。
func (s *Service) Ingest(ctx context.Context, req Request) (Result, error) {
	if err := s.checkRequest(ctx, req); err != nil {
		return Result{}, err
	}
	parsed, err := Parse(ctx, s.fetcher, req.Source)
	if err != nil {
		return Result{}, err
	}
	docType := parsed.DocType
	if req.DocType != "" {
		docType = req.DocType
	}
	want := domain.KnowledgeDocument{
		MerchantID: req.MerchantID, ProductID: req.ProductID, Title: parsed.Title, DocType: docType, Content: parsed.Content,
		SourceURL: parsed.SourceURL, ContentHash: sha256Hex(parsed.Content), Metadata: req.Metadata, Status: domain.DocUploaded,
	}
	res := Result{TextRunes: parsed.TextRunes, Truncated: parsed.Truncated}

	doc, created, duplicate, err := s.claim(ctx, want, req.ForceReindex)
	if err != nil {
		return Result{}, err
	}
	res.Created = created
	if duplicate {
		res.Document, res.Duplicate = doc, true
		return res, nil
	}

	// parsing：切块。
	id := doc.DocumentID
	chunks := rag.Split(doc.Title, doc.DocType, doc.Content)
	if len(chunks) == 0 || len(chunks) > rag.MaxChunks {
		return Result{}, s.fail(ctx, id, fmt.Sprintf("切块结果异常（%d 块），请检查内容", len(chunks)))
	}
	if _, err := s.setStatus(ctx, id, domain.DocIndexing); err != nil {
		s.logger.ErrorContext(ctx, "mark document indexing failed", "document_id", id, "error", err)
		return Result{}, s.fail(ctx, id, "更新文档状态失败，请稍后重新提交")
	}
	// indexing：写分块（同时改为 indexed），再建向量索引。
	rows := make([]domain.KnowledgeChunk, len(chunks))
	for i, c := range chunks {
		rows[i] = domain.KnowledgeChunk{Title: c.Title, Content: c.Content}
	}
	doc, err = s.store.ReplaceDocumentChunks(ctx, id, rows)
	if err != nil {
		s.logger.ErrorContext(ctx, "save knowledge chunks failed", "document_id", id, "error", err)
		return Result{}, s.fail(ctx, id, "保存分块失败，请稍后重新提交")
	}
	res.Document = doc
	res.VectorIndexed = s.indexVectors(ctx, doc)
	return res, nil
}

// claim 找到或创建本次要处理的文档，并把它置为 parsing。created 表示新建；duplicate 为 true 时文档已索引，无需处理。
func (s *Service) claim(ctx context.Context, want domain.KnowledgeDocument, force bool) (doc domain.KnowledgeDocument, created, duplicate bool, err error) {
	for attempt := 0; attempt < 3; attempt++ {
		existing, err := s.store.GetDocumentByHash(ctx, want.MerchantID, want.ContentHash)
		switch {
		case errors.Is(err, store.ErrNotFound):
			newDoc, err := s.store.CreateDocument(ctx, want)
			if errors.Is(err, store.ErrConflict) {
				continue // 并发提交了同样的内容：按已有文档处理
			}
			if err != nil {
				return domain.KnowledgeDocument{}, false, false, err
			}
			doc, err := s.setStatus(ctx, newDoc.DocumentID, domain.DocParsing)
			return doc, true, false, err
		case err != nil:
			return domain.KnowledgeDocument{}, false, false, err
		}

		if existing.Status == domain.DocUploaded || existing.Status == domain.DocParsing || existing.Status == domain.DocIndexing {
			if s.now().Sub(existing.UpdatedAt) < staleAfter {
				return domain.KnowledgeDocument{}, false, false, ErrInProgress
			}
			// 处理中断（如进程重启）的文档先记为失败，下一轮按 failed 重新处理。
			if _, err := s.setStatusWithReason(ctx, existing.DocumentID, domain.DocFailed, "处理中断"); err != nil {
				return domain.KnowledgeDocument{}, false, false, err
			}
			continue
		}
		if existing.Status == domain.DocIndexed && !force {
			return existing, false, true, nil
		}
		doc, err = s.store.UpdateDocument(ctx, existing.DocumentID, func(d *domain.KnowledgeDocument) error {
			if d.Status != existing.Status || !d.UpdatedAt.Equal(existing.UpdatedAt) {
				return ErrInProgress // 读取之后被其他请求接手了
			}
			d.Status, d.ErrorReason = domain.DocParsing, ""
			d.ProductID, d.Title, d.DocType, d.Content, d.SourceURL, d.Metadata = want.ProductID, want.Title, want.DocType, want.Content, want.SourceURL, want.Metadata
			return nil
		})
		return doc, false, false, err
	}
	return domain.KnowledgeDocument{}, false, false, ErrInProgress
}

func (s *Service) setStatus(ctx context.Context, id string, st domain.DocumentStatus) (domain.KnowledgeDocument, error) {
	return s.setStatusWithReason(ctx, id, st, "")
}

func (s *Service) setStatusWithReason(ctx context.Context, id string, st domain.DocumentStatus, reason string) (domain.KnowledgeDocument, error) {
	return s.store.UpdateDocument(ctx, id, func(d *domain.KnowledgeDocument) error {
		d.Status, d.ErrorReason = st, reason
		return nil
	})
}

// fail 把文档标记为 failed 并返回 ErrIndexFailed。请求可能已取消，标记不继承取消信号。
func (s *Service) fail(ctx context.Context, id, reason string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.setStatusWithReason(ctx, id, domain.DocFailed, reason); err != nil {
		s.logger.ErrorContext(ctx, "mark document failed", "document_id", id, "error", err)
	}
	return fmt.Errorf("%w: %s", ErrIndexFailed, reason)
}

// indexVectors 建向量索引；失败不影响文档（关键词检索照常可用），只记日志。
func (s *Service) indexVectors(ctx context.Context, doc domain.KnowledgeDocument) bool {
	if s.vector == nil {
		return false
	}
	chunks, err := s.store.ListDocumentChunks(ctx, doc.DocumentID)
	if err == nil {
		items := make([]rag.IndexedChunk, len(chunks))
		for i, c := range chunks {
			items[i] = rag.IndexedChunk{ChunkID: c.ChunkID, DocumentID: c.DocumentID, MerchantID: c.MerchantID,
				ProductID: c.ProductID, DocType: doc.DocType, Title: c.Title, Content: c.Content}
		}
		err = s.vector.Upsert(ctx, doc.DocumentID, items)
	}
	if err != nil {
		s.logger.WarnContext(ctx, "vector index failed, keyword search still available", "document_id", doc.DocumentID, "error", err)
		return false
	}
	return true
}

// checkRequest 校验归属与元数据：商家（如有）存在；关联商品属于该商家且未删除；文档类型合法；元数据为扁平的小对象。
func (s *Service) checkRequest(ctx context.Context, req Request) error {
	if req.DocType != "" && !contains(DocTypes, req.DocType) {
		return &InputError{Field: "doc_type", Message: "doc_type 不合法"}
	}
	if req.MerchantID != "" {
		if _, err := s.store.GetMerchant(ctx, req.MerchantID); errors.Is(err, store.ErrNotFound) {
			return &InputError{Field: "merchant_id", Message: "商家不存在"}
		} else if err != nil {
			return err
		}
	}
	if req.ProductID != "" {
		if req.MerchantID == "" {
			return &InputError{Field: "product_id", Message: "平台资料不能关联商品"}
		}
		p, err := s.store.GetProduct(ctx, req.ProductID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (p.MerchantID != req.MerchantID || p.Status == domain.ProductDeleted)) {
			return &InputError{Field: "product_id", Message: "商品不存在或不属于该商家"}
		}
		if err != nil {
			return err
		}
	}
	return checkMetadata(req.Metadata)
}

const (
	maxMetadataKeys  = 20
	maxMetadataBytes = 4096
	maxMetadataValue = 500
)

func checkMetadata(m map[string]any) error {
	bad := func(msg string) error { return &InputError{Field: "metadata", Message: msg} }
	if len(m) > maxMetadataKeys {
		return bad("metadata 最多 20 个字段")
	}
	for k, v := range m {
		if k == "" || utf8.RuneCountInString(k) > 64 {
			return bad("metadata 的键长度必须为 1–64 个字")
		}
		switch val := v.(type) {
		case nil, bool, float64, json.Number:
		case string:
			if utf8.RuneCountInString(val) > maxMetadataValue {
				return bad("metadata 的值不能超过 500 个字")
			}
		default:
			return bad("metadata 只能包含字符串、数字、布尔值或 null，不能嵌套")
		}
	}
	if b, _ := json.Marshal(m); len(b) > maxMetadataBytes {
		return bad("metadata 不能超过 4KB")
	}
	return nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
