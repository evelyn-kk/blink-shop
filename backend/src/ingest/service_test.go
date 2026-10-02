package ingest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

const digital = "m_seed_digital"

func seededStore(t *testing.T) *memstore.Store {
	t.Helper()
	st := memstore.New()
	if _, err := st.ApplySeed(context.Background(), storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	return st
}

type recordingVector struct {
	mu      sync.Mutex
	upserts map[string][]rag.IndexedChunk
	err     error
}

func (v *recordingVector) Upsert(_ context.Context, docID string, chunks []rag.IndexedChunk) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return v.err
	}
	if v.upserts == nil {
		v.upserts = map[string][]rag.IndexedChunk{}
	}
	v.upserts[docID] = chunks
	return nil
}

func (v *recordingVector) Search(context.Context, string, int, rag.VectorFilter) ([]rag.VectorHit, error) {
	return nil, nil
}

func text(title, content string) Source { return Source{Title: title, Content: content} }

func TestIngestNewDocument(t *testing.T) {
	st := seededStore(t)
	vec := &recordingVector{}
	svc := NewService(st, vec, nil, nil, nil)
	ctx := context.Background()
	res, err := svc.Ingest(ctx, Request{
		MerchantID: digital, ProductID: "p_seed_vista", DocType: "faq", Metadata: map[string]any{"lang": "zh", "version": 2.0},
		Source: text("Vista 常见问题", "问：防水吗？\n答：IP68。\n\n问：有耳机孔吗？\n答：没有。"),
	})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Document
	if res.Duplicate || !res.Created || !res.VectorIndexed || res.TextRunes == 0 || d.Status != domain.DocIndexed || d.ChunkCount != 2 ||
		d.DocType != "faq" || d.ProductID != "p_seed_vista" || d.MerchantID != digital || d.ErrorReason != "" {
		t.Fatalf("result = %+v", res)
	}
	chunks, _ := st.ListDocumentChunks(ctx, d.DocumentID)
	if len(chunks) != 2 || chunks[0].Title != "防水吗？" || chunks[1].ProductID != "p_seed_vista" {
		t.Fatalf("chunks = %+v", chunks)
	}
	got := vec.upserts[d.DocumentID]
	if len(got) != 2 || got[0].ChunkID != chunks[0].ChunkID || got[0].DocType != "faq" || got[0].MerchantID != digital {
		t.Fatalf("vector upsert = %+v", got)
	}
}

func TestIngestDedupAndForceReindex(t *testing.T) {
	st := seededStore(t)
	svc := NewService(st, nil, nil, nil, nil)
	ctx := context.Background()
	first, err := svc.Ingest(ctx, Request{MerchantID: digital, Source: text("保修", "手机保修一年。")})
	if err != nil {
		t.Fatal(err)
	}
	// 清洗后内容相同（多余空白、换行风格不同）视为重复，直接返回已有文档。
	dup, err := svc.Ingest(ctx, Request{MerchantID: digital, Source: text("另一个标题", "  手机保修一年。 \r\n\r\n")})
	if err != nil || !dup.Duplicate || dup.Created || dup.Document.DocumentID != first.Document.DocumentID || dup.Document.Title != "保修" {
		t.Fatalf("duplicate = %+v, %v", dup, err)
	}
	// 其他商家、平台各自独立。
	other, err := svc.Ingest(ctx, Request{MerchantID: "m_seed_home", Source: text("保修", "手机保修一年。")})
	if err != nil || other.Duplicate {
		t.Fatalf("other merchant = %+v, %v", other, err)
	}
	oldChunks, _ := st.ListDocumentChunks(ctx, first.Document.DocumentID)

	// force_reindex：同一篇文档用新的标题、类型、元数据重新切块，不新建文档。
	re, err := svc.Ingest(ctx, Request{MerchantID: digital, DocType: "policy", ForceReindex: true, Metadata: map[string]any{"v": "2"},
		Source: text("保修政策（新）", "手机保修一年。")})
	if err != nil || re.Duplicate || re.Created || re.Document.DocumentID != first.Document.DocumentID || re.Document.Title != "保修政策（新）" ||
		re.Document.DocType != "policy" || re.Document.Status != domain.DocIndexed || re.Document.Metadata["v"] != "2" {
		t.Fatalf("reindex = %+v, %v", re, err)
	}
	newChunks, _ := st.ListDocumentChunks(ctx, first.Document.DocumentID)
	if len(newChunks) != 1 || newChunks[0].ChunkID == oldChunks[0].ChunkID || newChunks[0].Title != "保修政策（新）" {
		t.Fatalf("chunks after reindex: %+v", newChunks)
	}
	docs, total, _ := st.ListDocuments(ctx, store.DocumentQuery{MerchantID: digital, Keyword: "保修", Page: store.Page{Page: 1, PageSize: 10}})
	if total != 1 {
		t.Fatalf("documents after reindex = %+v", docs)
	}
}

// failingChunks 让写分块失败，用来验证 failed 状态。
type failingChunks struct {
	*memstore.Store
	fail bool
}

func (f *failingChunks) ReplaceDocumentChunks(ctx context.Context, id string, chunks []domain.KnowledgeChunk) (domain.KnowledgeDocument, error) {
	if f.fail {
		return domain.KnowledgeDocument{}, errors.New("disk full")
	}
	return f.Store.ReplaceDocumentChunks(ctx, id, chunks)
}

func TestIngestFailureAndRetry(t *testing.T) {
	st := &failingChunks{Store: seededStore(t), fail: true}
	svc := NewService(st, nil, nil, nil, nil)
	ctx := context.Background()
	req := Request{MerchantID: digital, Source: text("会失败", "第一次写分块会失败。")}
	_, err := svc.Ingest(ctx, req)
	if !errors.Is(err, ErrIndexFailed) || strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	docs, _, _ := st.ListDocuments(ctx, store.DocumentQuery{MerchantID: digital, Keyword: "会失败", Page: store.Page{Page: 1, PageSize: 10}})
	if len(docs) != 1 || docs[0].Status != domain.DocFailed || docs[0].ErrorReason == "" || docs[0].ChunkCount != 0 {
		t.Fatalf("failed doc = %+v", docs)
	}
	// 失败的文档不可检索。
	if hits, _ := st.SearchKnowledge(ctx, store.KnowledgeQuery{Terms: []string{"失败"}}); len(hits) != 0 {
		t.Fatalf("failed doc searchable: %v", hits)
	}
	// 再次提交相同内容（不需要 force）会重新处理同一篇文档。
	st.fail = false
	res, err := svc.Ingest(ctx, req)
	if err != nil || res.Duplicate || res.Document.DocumentID != docs[0].DocumentID || res.Document.Status != domain.DocIndexed || res.Document.ErrorReason != "" {
		t.Fatalf("retry = %+v, %v", res, err)
	}
}

func TestIngestInProgressAndStale(t *testing.T) {
	st := seededStore(t)
	now := time.Now()
	svc := NewService(st, nil, nil, nil, func() time.Time { return now })
	ctx := context.Background()
	content := "正在处理的文档。"
	sum := hashOf(content)
	doc, err := st.CreateDocument(ctx, domain.KnowledgeDocument{MerchantID: digital, Title: "处理中", DocType: "policy", Content: content,
		Status: domain.DocUploaded, ContentHash: sum})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []domain.DocumentStatus{domain.DocParsing, domain.DocIndexing} {
		if _, err := st.UpdateDocument(ctx, doc.DocumentID, func(d *domain.KnowledgeDocument) error { d.Status = s; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	req := Request{MerchantID: digital, Source: text("处理中", content)}
	if _, err := svc.Ingest(ctx, req); !errors.Is(err, ErrInProgress) {
		t.Fatalf("in progress err = %v", err)
	}
	if _, err := svc.Ingest(ctx, Request{MerchantID: digital, ForceReindex: true, Source: text("处理中", content)}); !errors.Is(err, ErrInProgress) {
		t.Fatalf("force while in progress err = %v", err)
	}
	// 超过 10 分钟仍在 indexing：视为中断，记为失败后重新处理。
	now = now.Add(11 * time.Minute)
	res, err := svc.Ingest(ctx, req)
	if err != nil || res.Document.DocumentID != doc.DocumentID || res.Document.Status != domain.DocIndexed {
		t.Fatalf("stale recovery = %+v, %v", res, err)
	}
}

func TestIngestConcurrentSameContent(t *testing.T) {
	st := seededStore(t)
	svc := NewService(st, nil, nil, nil, nil)
	var wg sync.WaitGroup
	results := make([]error, 6)
	creates := make([]bool, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := svc.Ingest(context.Background(), Request{MerchantID: digital, Source: text("并发", "并发提交同样的内容。")})
			results[i], creates[i] = err, res.Created
		}()
	}
	wg.Wait()
	created := 0
	for i, err := range results {
		switch {
		case err == nil && creates[i]:
			created++
		case err == nil, errors.Is(err, ErrInProgress):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	docs, total, _ := st.ListDocuments(context.Background(), store.DocumentQuery{MerchantID: digital, Keyword: "并发", Page: store.Page{Page: 1, PageSize: 10}})
	if created != 1 || total != 1 || docs[0].Status != domain.DocIndexed {
		t.Fatalf("created=%d docs=%+v", created, docs)
	}
}

func TestIngestValidation(t *testing.T) {
	st := seededStore(t)
	svc := NewService(st, nil, nil, nil, nil)
	ctx := context.Background()
	ok := text("t", "内容")
	cases := []struct {
		name  string
		req   Request
		field string
	}{
		{"bad doc type", Request{MerchantID: digital, DocType: "secret", Source: ok}, "doc_type"},
		{"unknown merchant", Request{MerchantID: "m_missing", Source: ok}, "merchant_id"},
		{"other merchant's product", Request{MerchantID: digital, ProductID: "p_seed_lamp", Source: ok}, "product_id"},
		{"deleted product", Request{MerchantID: digital, ProductID: "p_seed_legacy", Source: ok}, "product_id"},
		{"missing product", Request{MerchantID: digital, ProductID: "p_missing", Source: ok}, "product_id"},
		{"platform with product", Request{ProductID: "p_seed_nova", Source: ok}, "product_id"},
		{"nested metadata", Request{MerchantID: digital, Metadata: map[string]any{"a": map[string]any{"b": 1}}, Source: ok}, "metadata"},
		{"array metadata", Request{MerchantID: digital, Metadata: map[string]any{"a": []any{1}}, Source: ok}, "metadata"},
		{"long metadata value", Request{MerchantID: digital, Metadata: map[string]any{"a": strings.Repeat("长", 501)}, Source: ok}, "metadata"},
		{"too many metadata keys", Request{MerchantID: digital, Metadata: manyKeys(21), Source: ok}, "metadata"},
		{"empty content", Request{MerchantID: digital, Source: text("t", "  ")}, "content"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Ingest(ctx, c.req)
			var in *InputError
			if !errors.As(err, &in) || in.Field != c.field {
				t.Fatalf("err = %v, want field %s", err, c.field)
			}
		})
	}
	if _, total, _ := st.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Page: store.Page{Page: 1, PageSize: 1}}); total != 3 {
		t.Fatalf("rejected requests created documents: total=%d", total)
	}
	// 风控/下架中的自有商品可以关联资料（检索时会被过滤）。
	if _, err := svc.Ingest(ctx, Request{MerchantID: digital, ProductID: "p_seed_speaker", Source: ok}); err != nil {
		t.Fatalf("inactive own product: %v", err)
	}
}

func manyKeys(n int) map[string]any {
	m := map[string]any{}
	for i := 0; i < n; i++ {
		m[strings.Repeat("k", i+1)] = "v"
	}
	return m
}

func TestIngestVectorFailureKeepsDocument(t *testing.T) {
	st := seededStore(t)
	svc := NewService(st, &recordingVector{err: errors.New("milvus down")}, nil, nil, nil)
	res, err := svc.Ingest(context.Background(), Request{MerchantID: digital, Source: text("向量失败", "向量索引失败不影响关键词。")})
	if err != nil || res.VectorIndexed || res.Document.Status != domain.DocIndexed {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if hits, _ := st.SearchKnowledge(context.Background(), store.KnowledgeQuery{Terms: []string{"向量索引"}}); len(hits) != 1 {
		t.Fatalf("keyword search after vector failure: %v", hits)
	}
}

func hashOf(content string) string {
	p, _ := Parse(context.Background(), nil, Source{Content: content})
	return sha256Hex(p.Content)
}
