package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/ingest"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

var (
	ErrDocumentNotFound   = &APIError{Status: http.StatusNotFound, Code: "document_not_found", Message: "资料不存在"}
	ErrDocumentForbidden  = &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "不能查看其他商家的资料"}
	ErrDocumentProcessing = &APIError{Status: http.StatusConflict, Code: "document_processing", Message: "相同内容的资料正在处理，请稍后再试"}
	ErrDocumentIndex      = &APIError{Status: http.StatusInternalServerError, Code: "document_index_failed", Message: "资料入库失败，已记录为失败状态，请稍后重新提交"}
)

type documentView struct {
	DocumentID  string                `json:"document_id"`
	MerchantID  string                `json:"merchant_id"`
	ProductID   string                `json:"product_id"`
	Title       string                `json:"title"`
	DocType     string                `json:"doc_type"`
	Status      domain.DocumentStatus `json:"status"`
	ChunkCount  int                   `json:"chunk_count"`
	SourceURL   string                `json:"source_url"`
	ContentHash string                `json:"content_hash"`
	Metadata    map[string]any        `json:"metadata"`
	ErrorReason string                `json:"error_reason"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

type chunkView struct {
	ChunkID    string `json:"chunk_id"`
	ChunkIndex int    `json:"chunk_index"`
	Title      string `json:"title"`
	Content    string `json:"content"`
}

type documentDetail struct {
	documentView
	Content string      `json:"content"`
	Chunks  []chunkView `json:"chunks"`
}

type ingestionResponse struct {
	Document  documentView `json:"document"`
	Duplicate bool         `json:"duplicate"`
	TextRunes int          `json:"text_runes"`
	Truncated bool         `json:"truncated"`
}

func toDocumentView(d domain.KnowledgeDocument) documentView {
	meta := d.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	return documentView{
		DocumentID: d.DocumentID, MerchantID: d.MerchantID, ProductID: d.ProductID, Title: d.Title, DocType: d.DocType,
		Status: d.Status, ChunkCount: d.ChunkCount, SourceURL: d.SourceURL, ContentHash: d.ContentHash, Metadata: meta,
		ErrorReason: d.ErrorReason, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// documentInput 是 POST /merchant/documents：商家直接提交的结构化资料（纯文本）。
type documentInput struct {
	MerchantID   *string        `json:"merchant_id"` // 只能是自己的店铺或不传
	Title        string         `json:"title"`
	Content      string         `json:"content"`
	DocType      string         `json:"doc_type"`
	ProductID    string         `json:"product_id"`
	Metadata     map[string]any `json:"metadata"`
	ForceReindex bool           `json:"force_reindex"`
}

// ingestionInput 是 POST /merchant|admin/unstructured-ingestions：content / html / json_text / source_url 四选一。
type ingestionInput struct {
	MerchantID   *string        `json:"merchant_id"`
	Title        string         `json:"title"`
	SourceType   string         `json:"source_type"`
	Content      string         `json:"content"`
	HTML         string         `json:"html"`
	JSONText     string         `json:"json_text"`
	SourceURL    string         `json:"source_url"`
	ProductID    string         `json:"product_id"`
	Metadata     map[string]any `json:"metadata"`
	ForceReindex bool           `json:"force_reindex"`
}

func (in ingestionInput) source() ingest.Source {
	return ingest.Source{Title: in.Title, SourceType: in.SourceType, Content: in.Content, HTML: in.HTML, JSONText: in.JSONText, SourceURL: in.SourceURL}
}

// handleCreateMerchantDocument 与上游一致：成功返回文档本身；新建 201，内容与已有文档相同（或重新处理已有文档）时 200。
func (s *Server) handleCreateMerchantDocument(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in documentInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if err := checkOwnMerchant(acc, in.MerchantID); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(in.Title) == "" {
		writeError(w, fieldError("title", "请填写资料标题"))
		return
	}
	if strings.TrimSpace(in.Content) == "" {
		writeError(w, fieldError("content", "请填写资料内容"))
		return
	}
	docType := strings.TrimSpace(in.DocType)
	if docType == "" {
		docType = "product_detail" // 与上游默认值一致
	}
	res, ok := s.ingest(w, r, ingest.Request{
		MerchantID: acc.MerchantID, ProductID: strings.TrimSpace(in.ProductID), DocType: docType,
		Source: ingest.Source{Title: in.Title, SourceType: ingest.SourceText, Content: in.Content}, Metadata: in.Metadata, ForceReindex: in.ForceReindex,
	})
	if !ok {
		return
	}
	writeJSON(w, ingestStatus(res), toDocumentView(res.Document))
}

func (s *Server) handleMerchantIngestion(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in ingestionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if err := checkOwnMerchant(acc, in.MerchantID); err != nil {
		writeError(w, err)
		return
	}
	s.writeIngestion(w, r, ingest.Request{
		MerchantID: acc.MerchantID, ProductID: strings.TrimSpace(in.ProductID), Source: in.source(),
		Metadata: in.Metadata, ForceReindex: in.ForceReindex,
	})
}

// handleAdminIngestion：管理员可为任意商家采集，merchant_id 为空或不传时是平台资料（所有商家的导购都可以引用）。
func (s *Server) handleAdminIngestion(w http.ResponseWriter, r *http.Request) {
	var in ingestionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	merchantID := ""
	if in.MerchantID != nil {
		merchantID = strings.TrimSpace(*in.MerchantID)
	}
	s.writeIngestion(w, r, ingest.Request{
		MerchantID: merchantID, ProductID: strings.TrimSpace(in.ProductID), Source: in.source(),
		Metadata: in.Metadata, ForceReindex: in.ForceReindex,
	})
}

func (s *Server) writeIngestion(w http.ResponseWriter, r *http.Request, req ingest.Request) {
	res, ok := s.ingest(w, r, req)
	if !ok {
		return
	}
	writeJSON(w, ingestStatus(res), ingestionResponse{
		Document: toDocumentView(res.Document), Duplicate: res.Duplicate, TextRunes: res.TextRunes, Truncated: res.Truncated,
	})
}

// ingestStatus：新建文档 201；重复内容或重新处理已有文档 200。
func ingestStatus(res ingest.Result) int {
	if res.Created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// ingest 执行入库并把错误转换为接口错误；成功时写审计日志。
func (s *Server) ingest(w http.ResponseWriter, r *http.Request, req ingest.Request) (ingest.Result, bool) {
	acc, _ := accountFromContext(r.Context())
	res, err := s.ingestor.Ingest(r.Context(), req)
	if err != nil {
		var in *ingest.InputError
		switch {
		case errors.As(err, &in):
			writeError(w, fieldError(in.Field, in.Message))
		case errors.Is(err, ingest.ErrURLNotAllowed):
			writeError(w, &APIError{Status: http.StatusBadRequest, Code: "url_not_allowed", Field: "source_url",
				Message: strings.TrimPrefix(err.Error(), ingest.ErrURLNotAllowed.Error()+": ")})
		case errors.Is(err, ingest.ErrFetchFailed):
			writeError(w, &APIError{Status: http.StatusBadGateway, Code: "source_fetch_failed", Field: "source_url",
				Message: "网页抓取失败：" + strings.TrimPrefix(err.Error(), ingest.ErrFetchFailed.Error()+": ")})
		case errors.Is(err, ingest.ErrInProgress):
			writeError(w, ErrDocumentProcessing)
		case errors.Is(err, ingest.ErrIndexFailed):
			s.logger.ErrorContext(r.Context(), "knowledge index failed", "request_id", requestIDFromContext(r.Context()), "error", err)
			writeError(w, ErrDocumentIndex)
		default:
			s.storeFailed(w, r, "ingest knowledge document", err)
		}
		return ingest.Result{}, false
	}
	action := "knowledge.ingested"
	if res.Duplicate {
		action = "knowledge.duplicate"
	}
	s.audit(r, action, acc.AccountID, "document_id", res.Document.DocumentID, "merchant_id", res.Document.MerchantID,
		"doc_type", res.Document.DocType, "chunk_count", res.Document.ChunkCount, "force_reindex", req.ForceReindex,
		"vector_indexed", res.VectorIndexed)
	return res, true
}

// checkOwnMerchant：请求体里的 merchant_id 只能为空或当前商家（与商品接口一致）。
func checkOwnMerchant(acc domain.Account, merchantID *string) error {
	if merchantID != nil && strings.TrimSpace(*merchantID) != "" && strings.TrimSpace(*merchantID) != acc.MerchantID {
		return fieldError("merchant_id", "不能为其他商家提交资料")
	}
	return nil
}

func (s *Server) handleListMerchantDocuments(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	s.listDocuments(w, r, store.DocumentQuery{MerchantID: acc.MerchantID})
}

// handleListAdminDocuments：不传 merchant_id 列出全部；merchant_id= （空值）只看平台资料；否则只看该商家。
func (s *Server) handleListAdminDocuments(w http.ResponseWriter, r *http.Request) {
	q := store.DocumentQuery{AllMerchants: true}
	if r.URL.Query().Has("merchant_id") {
		q.AllMerchants, q.MerchantID = false, strings.TrimSpace(r.URL.Query().Get("merchant_id"))
	}
	s.listDocuments(w, r, q)
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request, q store.DocumentQuery) {
	q.Status = domain.DocumentStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	q.Keyword = strings.TrimSpace(r.URL.Query().Get("keyword"))
	q.Page = readPage(r)
	if q.Status != "" && !q.Status.Valid() {
		writeError(w, fieldError("status", "状态只能是 uploaded、parsing、indexing、indexed 或 failed"))
		return
	}
	if utf8.RuneCountInString(q.Keyword) > maxKeywordRunes {
		writeError(w, fieldError("keyword", "关键词最多 64 个字符"))
		return
	}
	items, total, err := s.store.ListDocuments(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list documents", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toDocumentView))
}

// handleGetMerchantDocument：商家只能看自己的资料，其他商家和平台资料 403，不存在 404。
func (s *Server) handleGetMerchantDocument(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	s.getDocument(w, r, func(d domain.KnowledgeDocument) error {
		if d.MerchantID != acc.MerchantID {
			return ErrDocumentForbidden
		}
		return nil
	})
}

func (s *Server) handleGetAdminDocument(w http.ResponseWriter, r *http.Request) {
	s.getDocument(w, r, func(domain.KnowledgeDocument) error { return nil })
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request, allow func(domain.KnowledgeDocument) error) {
	d, err := s.store.GetDocument(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrDocumentNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "get document", err)
		return
	}
	if err := allow(d); err != nil {
		writeError(w, err)
		return
	}
	chunks, err := s.store.ListDocumentChunks(r.Context(), d.DocumentID)
	if err != nil {
		s.storeFailed(w, r, "list document chunks", err)
		return
	}
	views := make([]chunkView, len(chunks))
	for i, c := range chunks {
		views[i] = chunkView{ChunkID: c.ChunkID, ChunkIndex: c.ChunkIndex, Title: c.Title, Content: c.Content}
	}
	writeJSON(w, http.StatusOK, documentDetail{documentView: toDocumentView(d), Content: d.Content, Chunks: views})
}
