package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func knowledgeCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"DocumentLifecycle", testDocumentLifecycle},
		{"DocumentDedupByMerchantHash", testDocumentDedup},
		{"DocumentStatusMachine", testDocumentStatusMachine},
		{"DocumentReindexReplacesChunks", testDocumentReindex},
		{"DocumentConcurrentCreate", testDocumentConcurrentCreate},
		{"ListDocuments", testListDocuments},
		{"SearchKnowledgeVisibility", testSearchKnowledgeVisibility},
		{"SearchKnowledgeFilters", testSearchKnowledgeFilters},
		{"GetMerchant", testGetMerchant},
	}
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newDocument(merchantID, title, content string) domain.KnowledgeDocument {
	return domain.KnowledgeDocument{
		MerchantID: merchantID, Title: title, DocType: "policy", Content: content, Status: domain.DocUploaded,
		ContentHash: hashText(content), Metadata: map[string]any{"lang": "zh"},
	}
}

// indexDocument 走完整状态机：uploaded → parsing → indexing → 写分块（indexed）。
func indexDocument(t *testing.T, s store.Store, d domain.KnowledgeDocument, chunks ...string) domain.KnowledgeDocument {
	t.Helper()
	ctx := context.Background()
	created, err := s.CreateDocument(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.DocumentStatus{domain.DocParsing, domain.DocIndexing} {
		if _, err := s.UpdateDocument(ctx, created.DocumentID, func(d *domain.KnowledgeDocument) error { d.Status = st; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	var cs []domain.KnowledgeChunk
	for _, c := range chunks {
		cs = append(cs, domain.KnowledgeChunk{Title: d.Title, Content: c})
	}
	out, err := s.ReplaceDocumentChunks(ctx, created.DocumentID, cs)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testDocumentLifecycle(t *testing.T, s store.Store) {
	ctx := context.Background()
	in := newDocument(seed.DigitalMerchant, "退换货说明", "七天无理由\n\n保修一年")
	in.ProductID = "p_seed_nova"
	in.SourceURL = "https://example.com/policy"
	d := indexDocument(t, s, in, "七天无理由", "保修一年")
	if d.Status != domain.DocIndexed || d.ChunkCount != 2 || d.ErrorReason != "" {
		t.Fatalf("indexed doc: %+v", d)
	}
	got, err := s.GetDocument(ctx, d.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != in.Title || got.Content != in.Content || got.ProductID != "p_seed_nova" || got.SourceURL != in.SourceURL ||
		got.ContentHash != in.ContentHash || !reflect.DeepEqual(got.Metadata, map[string]any{"lang": "zh"}) || got.ChunkCount != 2 {
		t.Fatalf("GetDocument: %+v", got)
	}
	byHash, err := s.GetDocumentByHash(ctx, seed.DigitalMerchant, in.ContentHash)
	if err != nil || byHash.DocumentID != d.DocumentID {
		t.Fatalf("GetDocumentByHash: %+v %v", byHash, err)
	}
	if _, err := s.GetDocumentByHash(ctx, seed.HomeMerchant, in.ContentHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other merchant hash: %v", err)
	}
	chunks, err := s.ListDocumentChunks(ctx, d.DocumentID)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks: %v %v", chunks, err)
	}
	for i, c := range chunks {
		if c.ChunkIndex != i || c.DocumentID != d.DocumentID || c.MerchantID != seed.DigitalMerchant || c.ProductID != "p_seed_nova" ||
			c.Source != in.Title || c.ChunkID == "" {
			t.Fatalf("chunk %d: %+v", i, c)
		}
	}
	_, err = s.GetDocument(ctx, "doc_missing")
	expectNotFound(t, err, "missing document")
	if _, err := s.CreateDocument(ctx, domain.KnowledgeDocument{Title: "x", DocType: "policy", Status: domain.DocUploaded, ContentHash: "bad"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("invalid hash accepted: %v", err)
	}
}

func testDocumentDedup(t *testing.T, s store.Store) {
	ctx := context.Background()
	first, err := s.CreateDocument(ctx, newDocument(seed.DigitalMerchant, "A", "同样的内容"))
	if err != nil {
		t.Fatal(err)
	}
	var conflict *store.ConflictError
	if _, err := s.CreateDocument(ctx, newDocument(seed.DigitalMerchant, "B", "同样的内容")); !errors.As(err, &conflict) || conflict.Key != store.KeyDocumentMerchantHash {
		t.Fatalf("duplicate err = %v", err)
	}
	// 不同商家、平台资料可以有相同内容。
	if _, err := s.CreateDocument(ctx, newDocument(seed.HomeMerchant, "B", "同样的内容")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocument(ctx, newDocument("", "平台", "同样的内容")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDocument(ctx, first.DocumentID); got.Title != "A" {
		t.Fatalf("conflict overwrote document: %+v", got)
	}
}

func testDocumentStatusMachine(t *testing.T, s store.Store) {
	ctx := context.Background()
	d, err := s.CreateDocument(ctx, newDocument(seed.DigitalMerchant, "状态", "状态机"))
	if err != nil {
		t.Fatal(err)
	}
	set := func(st domain.DocumentStatus) error {
		_, err := s.UpdateDocument(ctx, d.DocumentID, func(d *domain.KnowledgeDocument) error { d.Status = st; return nil })
		return err
	}
	// uploaded 不能直接到 indexed / indexing。
	for _, bad := range []domain.DocumentStatus{domain.DocIndexed, domain.DocIndexing, "bogus"} {
		if err := set(bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("uploaded -> %s: %v", bad, err)
		}
	}
	// 只有 indexing 的文档可以写分块。
	if _, err := s.ReplaceDocumentChunks(ctx, d.DocumentID, []domain.KnowledgeChunk{{Title: "t", Content: "c"}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("chunks on uploaded doc: %v", err)
	}
	if err := set(domain.DocParsing); err != nil {
		t.Fatal(err)
	}
	// 解析失败 → failed，记录原因；failed 可以重新解析。
	failed, err := s.UpdateDocument(ctx, d.DocumentID, func(d *domain.KnowledgeDocument) error {
		d.Status, d.ErrorReason = domain.DocFailed, "分块失败"
		return nil
	})
	if err != nil || failed.Status != domain.DocFailed || failed.ErrorReason != "分块失败" {
		t.Fatalf("to failed: %+v %v", failed, err)
	}
	if err := set(domain.DocIndexed); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("failed -> indexed: %v", err)
	}
	if err := set(domain.DocParsing); err != nil {
		t.Fatal(err)
	}
	if err := set(domain.DocIndexing); err != nil {
		t.Fatal(err)
	}
	// 空分块与缺内容的分块被拒绝，文档状态不变。
	for _, bad := range [][]domain.KnowledgeChunk{nil, {{Title: "t", Content: ""}}} {
		if _, err := s.ReplaceDocumentChunks(ctx, d.DocumentID, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("bad chunks %v: %v", bad, err)
		}
	}
	if got, _ := s.GetDocument(ctx, d.DocumentID); got.Status != domain.DocIndexing {
		t.Fatalf("status after rejected chunks = %s", got.Status)
	}
	// fn 出错时整体不修改；不能借 UpdateDocument 改归属、hash 或分块数。
	if _, err := s.UpdateDocument(ctx, d.DocumentID, func(d *domain.KnowledgeDocument) error {
		d.Title = "改了"
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	got, err := s.UpdateDocument(ctx, d.DocumentID, func(d *domain.KnowledgeDocument) error {
		d.MerchantID, d.ContentHash, d.ChunkCount = seed.HomeMerchant, hashText("x"), 99
		return nil
	})
	if err != nil || got.MerchantID != seed.DigitalMerchant || got.ContentHash != hashText("状态机") || got.ChunkCount != 0 || got.Title != "状态" {
		t.Fatalf("protected fields changed: %+v %v", got, err)
	}
	_, err = s.UpdateDocument(ctx, "doc_missing", func(*domain.KnowledgeDocument) error { return nil })
	expectNotFound(t, err, "update missing")
}

func testDocumentReindex(t *testing.T, s store.Store) {
	ctx := context.Background()
	d := indexDocument(t, s, newDocument(seed.DigitalMerchant, "重建", "旧"), "旧一", "旧二", "旧三")
	old, _ := s.ListDocumentChunks(ctx, d.DocumentID)
	for _, st := range []domain.DocumentStatus{domain.DocParsing, domain.DocIndexing} {
		if _, err := s.UpdateDocument(ctx, d.DocumentID, func(d *domain.KnowledgeDocument) error { d.Status = st; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// 重建期间旧分块不参与检索。
	if hits, _ := s.SearchKnowledge(ctx, store.KnowledgeQuery{Terms: []string{"旧一"}}); len(hits) != 0 {
		t.Fatalf("chunks of a document being reindexed are searchable: %v", hits)
	}
	got, err := s.ReplaceDocumentChunks(ctx, d.DocumentID, []domain.KnowledgeChunk{{Title: "重建", Content: "新内容"}})
	if err != nil || got.ChunkCount != 1 || got.Status != domain.DocIndexed {
		t.Fatalf("reindex: %+v %v", got, err)
	}
	chunks, _ := s.ListDocumentChunks(ctx, d.DocumentID)
	if len(chunks) != 1 || chunks[0].Content != "新内容" || chunks[0].ChunkIndex != 0 || chunks[0].ChunkID == old[0].ChunkID {
		t.Fatalf("chunks after reindex: %+v", chunks)
	}
	if hits, _ := s.SearchKnowledge(ctx, store.KnowledgeQuery{ChunkIDs: []string{old[0].ChunkID, old[1].ChunkID}}); len(hits) != 0 {
		t.Fatalf("old chunks still exist: %v", hits)
	}
}

func testDocumentConcurrentCreate(t *testing.T, s store.Store) {
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.CreateDocument(ctx, newDocument(seed.DigitalMerchant, "并发", "并发内容"))
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, store.ErrConflict):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent creates succeeded, want 1", ok)
	}
}

func testListDocuments(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	mine := indexDocument(t, s, newDocument(seed.DigitalMerchant, "新的 100% 说明", "a"), "a")
	other := indexDocument(t, s, newDocument(seed.HomeMerchant, "家居说明", "b"), "b")
	platform := indexDocument(t, s, newDocument("", "平台公告", "c"), "c")
	failed, _ := s.CreateDocument(ctx, newDocument(seed.DigitalMerchant, "失败的", "d"))

	page := store.Page{Page: 1, PageSize: 10}
	ids := func(docs []domain.KnowledgeDocument) []string {
		var out []string
		for _, d := range docs {
			if d.Content != "" {
				t.Fatalf("list returned content for %s", d.DocumentID)
			}
			out = append(out, d.DocumentID)
		}
		return out
	}
	digital, total, err := s.ListDocuments(ctx, store.DocumentQuery{MerchantID: seed.DigitalMerchant, Page: page})
	if err != nil || total != 5 || len(digital) != 5 {
		t.Fatalf("digital docs: %v total=%d %v", ids(digital), total, err)
	}
	if digital[0].DocumentID != failed.DocumentID && digital[0].DocumentID != mine.DocumentID {
		t.Fatalf("newest first: %v", ids(digital))
	}
	for _, d := range digital {
		if d.MerchantID != seed.DigitalMerchant {
			t.Fatalf("other merchant doc listed: %+v", d)
		}
	}
	if docs, total, _ := s.ListDocuments(ctx, store.DocumentQuery{MerchantID: "", Page: page}); total != 1 || docs[0].DocumentID != platform.DocumentID {
		t.Fatalf("platform docs: %v", ids(docs))
	}
	all, total, _ := s.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Page: page})
	if total != 7 || len(all) != 7 {
		t.Fatalf("all docs total=%d", total)
	}
	if docs, total, _ := s.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Status: domain.DocUploaded, Page: page}); total != 1 || docs[0].DocumentID != failed.DocumentID {
		t.Fatalf("status filter: %v", ids(docs))
	}
	// 关键词按字面匹配，% 不是通配符。
	if docs, total, _ := s.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Keyword: "100%", Page: page}); total != 1 || docs[0].DocumentID != mine.DocumentID {
		t.Fatalf("keyword filter: %v", ids(docs))
	}
	if docs, _, _ := s.ListDocuments(ctx, store.DocumentQuery{AllMerchants: true, Page: store.Page{Page: 2, PageSize: 5}}); len(docs) != 2 {
		t.Fatalf("page 2 = %d docs", len(docs))
	}
	_ = other
}

func hitIDs(hits []store.KnowledgeHit) []string {
	out := []string{}
	for _, h := range hits {
		out = append(out, h.DocumentID+"/"+h.Content)
	}
	sort.Strings(out)
	return out
}

func testSearchKnowledgeVisibility(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	visible := indexDocument(t, s, newDocument(seed.DigitalMerchant, "可见", "v"), "关键词 可见资料")
	platform := indexDocument(t, s, newDocument("", "平台", "p"), "关键词 平台资料")
	inactiveProduct := newDocument(seed.DigitalMerchant, "下架商品资料", "ip")
	inactiveProduct.ProductID = "p_seed_speaker"
	indexDocument(t, s, inactiveProduct, "关键词 下架商品")
	notIndexed, _ := s.CreateDocument(ctx, newDocument(seed.DigitalMerchant, "未索引", "n"))
	_ = notIndexed

	hits, err := s.SearchKnowledge(ctx, store.KnowledgeQuery{Terms: []string{"关键词"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{platform.DocumentID + "/关键词 平台资料", visible.DocumentID + "/关键词 可见资料"}
	sort.Strings(want)
	if got := hitIDs(hits); !reflect.DeepEqual(got, want) {
		t.Fatalf("visible hits = %v, want %v", got, want)
	}
	for _, h := range hits {
		if h.DocumentTitle == "" || h.DocType != "policy" {
			t.Fatalf("hit missing document info: %+v", h)
		}
	}
	// 文档标题也能命中；大小写不敏感。
	if hits, _ := s.SearchKnowledge(ctx, store.KnowledgeQuery{Terms: []string{"nova 12"}}); len(hits) != 2 {
		t.Fatalf("seed nova hits = %v", hitIDs(hits))
	}
	// 没有检索词也没有 ID：不返回任何内容。
	if hits, _ := s.SearchKnowledge(ctx, store.KnowledgeQuery{}); len(hits) != 0 {
		t.Fatalf("empty query returned %d hits", len(hits))
	}
	// % 按字面匹配。
	if hits, _ := s.SearchKnowledge(ctx, store.KnowledgeQuery{Terms: []string{"%"}}); len(hits) != 0 {
		t.Fatalf("%% matched everything: %v", hitIDs(hits))
	}
}

func testSearchKnowledgeFilters(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	home := indexDocument(t, s, newDocument(seed.HomeMerchant, "台灯保修", "h"), "保修两年")
	q := func(q store.KnowledgeQuery) []string {
		q.Terms = []string{"保修"}
		hits, err := s.SearchKnowledge(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return hitIDs(hits)
	}
	if got := q(store.KnowledgeQuery{}); len(got) != 2 {
		t.Fatalf("all 保修 hits = %v", got)
	}
	if got := q(store.KnowledgeQuery{MerchantIDs: []string{seed.HomeMerchant}}); !reflect.DeepEqual(got, []string{home.DocumentID + "/保修两年"}) {
		t.Fatalf("merchant filter = %v", got)
	}
	if got := q(store.KnowledgeQuery{DocTypes: []string{"faq"}}); len(got) != 0 {
		t.Fatalf("doc type filter = %v", got)
	}
	if got := q(store.KnowledgeQuery{Limit: 1}); len(got) != 1 {
		t.Fatalf("limit = %v", got)
	}
	nova, _ := s.SearchKnowledge(ctx, store.KnowledgeQuery{Terms: []string{"续航", "拍照"}, ProductIDs: []string{"p_seed_nova"}})
	if len(nova) != 2 {
		t.Fatalf("product filter = %v", hitIDs(nova))
	}
	byID, _ := s.SearchKnowledge(ctx, store.KnowledgeQuery{ChunkIDs: []string{nova[0].ChunkID, "ck_missing"}, MerchantIDs: []string{seed.DigitalMerchant}})
	if len(byID) != 1 || byID[0].ChunkID != nova[0].ChunkID {
		t.Fatalf("chunk id lookup = %v", hitIDs(byID))
	}
	// 商家停业后其资料不可检索（种子中的商家状态通过直接构造验证：用不存在的商家 ID 写一篇文档）。
	ghost := indexDocument(t, s, newDocument("m_missing", "无主资料", "g"), "保修 无主")
	for _, h := range mustSearch(t, s, store.KnowledgeQuery{Terms: []string{"无主"}}) {
		if h.DocumentID == ghost.DocumentID {
			t.Fatal("document of unknown merchant is searchable")
		}
	}
}

func mustSearch(t *testing.T, s store.Store, q store.KnowledgeQuery) []store.KnowledgeHit {
	t.Helper()
	hits, err := s.SearchKnowledge(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func testGetMerchant(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetMerchant(ctx, seed.HomeMerchant)
	if err != nil || m.MerchantID != seed.HomeMerchant || m.Name == "" || m.CreatedAt.IsZero() {
		t.Fatalf("GetMerchant = %+v, %v", m, err)
	}
	_, err = s.GetMerchant(ctx, "m_missing")
	expectNotFound(t, err, "missing merchant")
}
