package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/ingest"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

type docPage struct {
	Items []documentView `json:"items"`
	Total int            `json:"total"`
}

// stubFetcher 代替真实抓取，按 URL 返回固定内容。
type stubFetcher map[string]ingest.Fetched

func (f stubFetcher) Fetch(_ context.Context, raw string) (ingest.Fetched, error) {
	if res, ok := f[raw]; ok {
		return res, nil
	}
	return ingest.Fetched{}, fmt.Errorf("%w: 对方返回状态码 404", ingest.ErrFetchFailed)
}

func (ts *testServer) useFetcher(f ingest.URLFetcher) {
	ts.Server.ingestor = ingest.NewService(ts.Server.store, nil, f, ts.logger, ts.now)
}

func TestMerchantDocumentCreateAndDedup(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	body := map[string]any{"title": "Vista Pro 常见问题", "doc_type": "faq", "product_id": "p_seed_vista",
		"content": "问：防水吗？\n答：IP68 防水。\n\n问：支持无线充电吗？\n答：支持 50W 无线充电。", "metadata": map[string]any{"lang": "zh"}}
	rec := ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, body)
	expectStatus(t, rec, http.StatusCreated, "")
	d := decodeBody[documentView](t, rec)
	if !strings.HasPrefix(d.DocumentID, "doc_") || d.MerchantID != seed.DigitalMerchant || d.ProductID != "p_seed_vista" ||
		d.DocType != "faq" || d.Status != domain.DocIndexed || d.ChunkCount != 2 || len(d.ContentHash) != 64 ||
		d.Metadata["lang"] != "zh" || d.ErrorReason != "" {
		t.Fatalf("created = %+v", d)
	}
	if !strings.Contains(ts.logs.String(), `"action":"knowledge.ingested"`) {
		t.Fatal("ingest not audited")
	}

	// 同样内容再提交：200，返回同一篇文档；force_reindex：200，重新切块。
	rec = ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, body)
	expectStatus(t, rec, http.StatusOK, "")
	if again := decodeBody[documentView](t, rec); again.DocumentID != d.DocumentID {
		t.Fatalf("duplicate returned %s", again.DocumentID)
	}
	body["force_reindex"], body["title"] = true, "Vista Pro FAQ（更新）"
	rec = ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, body)
	expectStatus(t, rec, http.StatusOK, "")
	if re := decodeBody[documentView](t, rec); re.DocumentID != d.DocumentID || re.Title != "Vista Pro FAQ（更新）" || re.Status != domain.DocIndexed {
		t.Fatalf("reindex = %+v", re)
	}

	// 详情含正文和分块。
	rec = ts.call(t, http.MethodGet, "/api/v1/merchant/documents/"+d.DocumentID, tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	detail := decodeBody[documentDetail](t, rec)
	if !strings.HasPrefix(detail.Content, "问：防水吗？") || len(detail.Chunks) != 2 || detail.Chunks[0].Title != "防水吗？" ||
		detail.Chunks[1].ChunkIndex != 1 || detail.Chunks[0].ChunkID == "" {
		t.Fatalf("detail = %+v", detail)
	}
	// 默认文档类型与上游一致。
	rec = ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, map[string]any{"title": "说明", "content": "默认类型"})
	expectStatus(t, rec, http.StatusCreated, "")
	if decodeBody[documentView](t, rec).DocType != "product_detail" {
		t.Fatal("default doc_type")
	}
}

func TestMerchantDocumentValidation(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"missing title", map[string]any{"content": "x"}, "title"},
		{"blank content", map[string]any{"title": "t", "content": " \n "}, "content"},
		{"other merchant", map[string]any{"merchant_id": seed.HomeMerchant, "title": "t", "content": "x"}, "merchant_id"},
		{"other merchant's product", map[string]any{"title": "t", "content": "x", "product_id": "p_seed_lamp"}, "product_id"},
		{"bad doc type", map[string]any{"title": "t", "content": "x", "doc_type": "secret"}, "doc_type"},
		{"title with newline", map[string]any{"title": "a\nb", "content": "x"}, "title"},
		{"nested metadata", map[string]any{"title": "t", "content": "x", "metadata": map[string]any{"a": []int{1}}}, "metadata"},
		{"metadata wrong type", map[string]any{"title": "t", "content": "x", "metadata": "str"}, "metadata"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, c.body)
			expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")
			if f := decodeError(t, rec).Field; f != c.field {
				t.Fatalf("field = %q, want %q", f, c.field)
			}
		})
	}
	if _, total, _ := ts.mem.ListDocuments(context.Background(), store.DocumentQuery{AllMerchants: true, Page: store.Page{Page: 1, PageSize: 1}}); total != 3 {
		t.Fatalf("rejected requests created documents: %d", total)
	}
}

func TestUnstructuredIngestion(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	ts.useFetcher(stubFetcher{
		"https://blog.example.com/vista": {Body: "<html><head><title>Vista 评测</title><script>x()</script></head><body><p>屏幕 120Hz。</p></body></html>",
			MediaType: "text/html", FinalURL: "https://blog.example.com/vista-review"},
	})
	post := func(body map[string]any) (int, ingestionResponse, ErrorResponse) {
		rec := ts.call(t, http.MethodPost, "/api/v1/merchant/unstructured-ingestions", tok, body)
		if rec.Code >= 400 {
			return rec.Code, ingestionResponse{}, decodeError(t, rec)
		}
		return rec.Code, decodeBody[ingestionResponse](t, rec), ErrorResponse{}
	}

	code, res, _ := post(map[string]any{"html": "<h1>标题</h1><p>正文<script>alert(1)</script></p>"})
	if code != 201 || res.Duplicate || res.Document.DocType != "web_article" || res.Document.Title != "标题" || res.TextRunes != 6 || res.Truncated {
		t.Fatalf("html = %d %+v", code, res)
	}
	code, res, _ = post(map[string]any{"title": "参数", "json_text": `{"b":2,"a":"一"}`, "product_id": "p_seed_mouse"})
	if code != 201 || res.Document.DocType != "structured_note" || res.Document.ProductID != "p_seed_mouse" {
		t.Fatalf("json = %d %+v", code, res)
	}
	// 键的顺序不同，展开后的内容相同 → 重复。
	code, res, _ = post(map[string]any{"json_text": `{"a":"一","b":2}`})
	if code != 200 || !res.Duplicate {
		t.Fatalf("json duplicate = %d %+v", code, res)
	}
	code, res, _ = post(map[string]any{"source_url": "https://blog.example.com/vista"})
	if code != 201 || res.Document.SourceURL != "https://blog.example.com/vista-review" || res.Document.Title != "Vista 评测" || res.Document.DocType != "web_article" {
		t.Fatalf("url = %d %+v", code, res)
	}

	for _, c := range []struct {
		body   map[string]any
		status int
		code   string
		field  string
	}{
		{map[string]any{"content": "a", "html": "<p>b</p>"}, 400, "invalid_argument", "content"},
		{map[string]any{}, 400, "invalid_argument", "content"},
		{map[string]any{"json_text": "{bad"}, 400, "invalid_argument", "json_text"},
		{map[string]any{"source_type": "pdf", "content": "x"}, 400, "invalid_argument", "source_type"},
		{map[string]any{"source_url": "http://169.254.169.254/latest/meta-data/"}, 400, "url_not_allowed", "source_url"},
		{map[string]any{"source_url": "file:///etc/passwd"}, 400, "url_not_allowed", "source_url"},
		{map[string]any{"source_url": "https://blog.example.com/missing"}, 502, "source_fetch_failed", "source_url"},
		{map[string]any{"merchant_id": seed.HomeMerchant, "content": "x"}, 400, "invalid_argument", "merchant_id"},
	} {
		code, _, e := post(c.body)
		if code != c.status || e.Code != c.code || e.Field != c.field {
			t.Errorf("%v: %d %+v", c.body, code, e)
		}
	}
}

// 默认抓取器：内网地址在发起任何连接之前就被拒绝。
func TestIngestionURLBlockedByDefaultFetcher(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	for _, u := range []string{"http://127.0.0.1:8080/api/v1/health", "http://localhost/", "http://10.0.0.5/", "http://[::1]/", "https://user:pw@example.com/"} {
		rec := ts.call(t, http.MethodPost, "/api/v1/merchant/unstructured-ingestions", tok, map[string]any{"source_url": u})
		expectStatus(t, rec, http.StatusBadRequest, "url_not_allowed")
	}
}

func TestDocumentAccess(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	digital := ts.merchantToken(t, seed.MerchantUsername)
	home := ts.merchantToken(t, seed.Merchant2Username)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token

	rec := ts.call(t, http.MethodPost, "/api/v1/admin/unstructured-ingestions", admin, map[string]any{"title": "平台公告", "content": "平台统一售后电话 400-000-0000。"})
	expectStatus(t, rec, http.StatusCreated, "")
	platform := decodeBody[ingestionResponse](t, rec).Document
	if platform.MerchantID != "" {
		t.Fatalf("platform doc merchant = %q", platform.MerchantID)
	}
	rec = ts.call(t, http.MethodPost, "/api/v1/admin/unstructured-ingestions", admin, map[string]any{"merchant_id": seed.HomeMerchant, "content": "家居店资料"})
	expectStatus(t, rec, http.StatusCreated, "")
	homeDoc := decodeBody[ingestionResponse](t, rec).Document
	rec = ts.call(t, http.MethodPost, "/api/v1/admin/unstructured-ingestions", admin, map[string]any{"merchant_id": "m_missing", "content": "x"})
	expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")
	rec = ts.call(t, http.MethodPost, "/api/v1/admin/unstructured-ingestions", admin, map[string]any{"product_id": "p_seed_nova", "content": "平台资料关联商品"})
	expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")

	// 商家只看到自己的资料。
	list := func(token, query string) docPage {
		rec := ts.call(t, http.MethodGet, query, token, nil)
		expectStatus(t, rec, http.StatusOK, "")
		return decodeBody[docPage](t, rec)
	}
	if p := list(digital, "/api/v1/merchant/documents"); p.Total != 3 {
		t.Fatalf("digital docs = %+v", p)
	}
	if p := list(home, "/api/v1/merchant/documents"); p.Total != 1 || p.Items[0].DocumentID != homeDoc.DocumentID {
		t.Fatalf("home docs = %+v", p)
	}
	if p := list(home, "/api/v1/merchant/documents?status=failed"); p.Total != 0 {
		t.Fatalf("status filter = %+v", p)
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents?status=bogus", home, nil), 400, "invalid_argument")
	// 管理员：全部 / 指定商家 / 只看平台资料（merchant_id 为空值）。
	if p := list(admin, "/api/v1/admin/documents"); p.Total != 5 {
		t.Fatalf("admin all = %d", p.Total)
	}
	if p := list(admin, "/api/v1/admin/documents?merchant_id="+seed.HomeMerchant); p.Total != 1 {
		t.Fatalf("admin by merchant = %d", p.Total)
	}
	if p := list(admin, "/api/v1/admin/documents?merchant_id="); p.Total != 1 || p.Items[0].DocumentID != platform.DocumentID {
		t.Fatalf("admin platform = %+v", p)
	}
	if p := list(admin, "/api/v1/admin/documents?keyword=Nova"); p.Total != 1 {
		t.Fatalf("admin keyword = %d", p.Total)
	}

	// 详情：其他商家与平台资料 403，不存在 404；管理员都能看。
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents/doc_seed_nova", home, nil), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents/"+platform.DocumentID, digital, nil), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents/doc_missing", digital, nil), 404, "document_not_found")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents/doc_seed_nova", digital, nil), 200, "")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/documents/"+homeDoc.DocumentID, admin, nil), 200, "")

	// 角色：用户与商家不能调用管理端，用户与管理员不能调用商家端，匿名 401。
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/documents", digital, nil), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/admin/unstructured-ingestions", user, map[string]any{"content": "x"}), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents", user, nil), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/merchant/documents", admin, map[string]any{"title": "t", "content": "x"}), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents", "", nil), 401, "unauthorized")
}

// chunkFailStore 让写分块失败一次，用来验证 failed 状态和重新提交。
type chunkFailStore struct {
	store.Store
	fail bool
}

func (s *chunkFailStore) ReplaceDocumentChunks(ctx context.Context, id string, chunks []domain.KnowledgeChunk) (domain.KnowledgeDocument, error) {
	if s.fail {
		return domain.KnowledgeDocument{}, errors.New("write chunks: connection reset")
	}
	return s.Store.ReplaceDocumentChunks(ctx, id, chunks)
}

func TestDocumentIndexFailure(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	failing := &chunkFailStore{Store: ts.mem, fail: true}
	ts.Server.store = failing
	ts.Server.ingestor = ingest.NewService(failing, nil, nil, ts.logger, ts.now)
	body := map[string]any{"title": "会失败", "content": "写分块会失败一次。"}

	rec := ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, body)
	expectStatus(t, rec, http.StatusInternalServerError, "document_index_failed")
	if strings.Contains(rec.Body.String(), "connection reset") {
		t.Fatalf("internal error leaked: %s", rec.Body)
	}
	rec = ts.call(t, http.MethodGet, "/api/v1/merchant/documents?status=failed", tok, nil)
	p := decodeBody[docPage](t, rec)
	if p.Total != 1 || p.Items[0].ErrorReason == "" || p.Items[0].ChunkCount != 0 {
		t.Fatalf("failed docs = %+v", p)
	}
	failing.fail = false
	rec = ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, body)
	expectStatus(t, rec, http.StatusOK, "")
	if d := decodeBody[documentView](t, rec); d.DocumentID != p.Items[0].DocumentID || d.Status != domain.DocIndexed || d.ErrorReason != "" {
		t.Fatalf("retry = %+v", d)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestDocumentProcessingConflict(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	ctx := context.Background()
	p, _ := ingest.Parse(ctx, nil, ingest.Source{Content: "处理中的资料"})
	d, err := ts.mem.CreateDocument(ctx, domain.KnowledgeDocument{MerchantID: seed.DigitalMerchant, Title: "处理中", DocType: "policy",
		Content: p.Content, Status: domain.DocUploaded, ContentHash: sha256Hex(p.Content)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.mem.UpdateDocument(ctx, d.DocumentID, func(d *domain.KnowledgeDocument) error { d.Status = domain.DocParsing; return nil }); err != nil {
		t.Fatal(err)
	}
	rec := ts.call(t, http.MethodPost, "/api/v1/merchant/documents", tok, map[string]any{"title": "处理中", "content": "处理中的资料"})
	expectStatus(t, rec, http.StatusConflict, "document_processing")
}

// REV-007：抓取到的正文不是合法 UTF-8 时（无论是否声明 charset）拒绝入库，不静默删除字节。
// 使用真实的 Fetcher（连接改连到测试服务器，IP 检查照常），覆盖从抓取到入库的完整路径。
func TestIngestionRejectsInvalidUTF8(t *testing.T) {
	mux := http.NewServeMux()
	page := func(contentType string, body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(body)
		}
	}
	mux.Handle("/bad-utf8", page("text/plain; charset=utf-8", []byte("售后\xff政策")))
	mux.Handle("/bad-no-charset", page("text/plain", []byte("售后\xff政策")))
	mux.Handle("/bad-html", page("text/html", []byte("<p>售后\xc3政策</p>")))
	mux.Handle("/good", page("text/plain; charset=utf-8", []byte("售后政策：七天无理由退货。")))
	mux.Handle("/ascii", page("text/plain; charset=us-ascii", []byte("Warranty: one year.")))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ts := newTestServer(t, nil, nil, nil)
	ts.useFetcher(ingest.NewFetcher(ingest.FetcherOptions{AllowLoopback: true, DialAddr: srv.Listener.Addr().String()}))
	tok := ts.merchantToken(t, seed.MerchantUsername)
	ctx := context.Background()
	count := func() (docs, chunks int) {
		list, total, err := ts.mem.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Page: store.Page{Page: 1, PageSize: 100}})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range list {
			cs, _ := ts.mem.ListDocumentChunks(ctx, d.DocumentID)
			chunks += len(cs)
		}
		return total, chunks
	}
	docsBefore, chunksBefore := count()

	for _, path := range []string{"/bad-utf8", "/bad-no-charset", "/bad-html"} {
		rec := ts.call(t, http.MethodPost, "/api/v1/merchant/unstructured-ingestions", tok, map[string]any{"source_url": "http://example.com:8080" + path})
		expectStatus(t, rec, http.StatusBadGateway, "source_fetch_failed")
		if e := decodeError(t, rec); e.Field != "source_url" || !strings.Contains(e.Message, "UTF-8") {
			t.Fatalf("%s: error = %+v", path, e)
		}
	}
	if d, c := count(); d != docsBefore || c != chunksBefore {
		t.Fatalf("rejected pages wrote data: docs %d→%d chunks %d→%d", docsBefore, d, chunksBefore, c)
	}

	for path, want := range map[string]string{"/good": "售后政策：七天无理由退货。", "/ascii": "Warranty: one year."} {
		rec := ts.call(t, http.MethodPost, "/api/v1/merchant/unstructured-ingestions", tok, map[string]any{"source_url": "http://example.com:8080" + path})
		expectStatus(t, rec, http.StatusCreated, "")
		res := decodeBody[ingestionResponse](t, rec)
		detail := decodeBody[documentDetail](t, ts.call(t, http.MethodGet, "/api/v1/merchant/documents/"+res.Document.DocumentID, tok, nil))
		if detail.Content != want || res.Document.SourceURL != "http://example.com:8080"+path {
			t.Fatalf("%s: content %q source %q", path, detail.Content, res.Document.SourceURL)
		}
	}
}
