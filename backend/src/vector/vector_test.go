package vector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/resilience"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

func TestOpenAIEmbedderBatchesAndValidates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
			Dim   int      `json:"dimensions"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer k" || req.Model != "emb" || req.Dim != 3 {
			t.Errorf("request: %s %v %s", r.URL.Path, req, r.Header.Get("Authorization"))
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i := len(req.Input) - 1; i >= 0; i-- { // 乱序返回，按 index 归位
			data = append(data, item{Index: i, Embedding: []float32{float32(len(req.Input[i])), 0, 1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": 5 * len(req.Input), "total_tokens": 5 * len(req.Input)}})
	}))
	defer srv.Close()
	if NewOpenAIEmbedder(EmbedOptions{APIKey: "", Model: "m", Dim: 3}) != nil || NewOpenAIEmbedder(EmbedOptions{APIKey: "k", Model: "m"}) != nil {
		t.Fatal("unconfigured embedder must be nil")
	}
	e := NewOpenAIEmbedder(EmbedOptions{BaseURL: srv.URL + "/v1/", APIKey: "k", Model: "emb", Dim: 3, BatchSize: 2})
	vecs, err := e.Embed(context.Background(), []string{"a", "bb", "ccc"})
	if err != nil || len(vecs) != 3 || vecs[0][0] != 1 || vecs[2][0] != 3 || calls != 2 {
		t.Fatalf("vecs=%v err=%v calls=%d", vecs, err, calls)
	}
	if e.Name() != "openai-compatible:emb:3" {
		t.Fatal(e.Name())
	}
	// 用量：1 次 Embed、按批 2 个请求、3 条文本、服务端返回的 token 累加
	before := e.Usage()
	if want := (EmbedUsage{Calls: 1, Requests: 2, Texts: 3, Tokens: 15}); before != want {
		t.Fatalf("usage = %+v, want %+v", before, want)
	}
	if _, err := e.Embed(context.Background(), []string{"d"}); err != nil {
		t.Fatal(err)
	}
	if d := e.Usage().Sub(before); d != (EmbedUsage{Calls: 1, Requests: 1, Texts: 1, Tokens: 5}) {
		t.Fatalf("usage delta = %+v", d)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1}}}})
	}))
	defer bad.Close()
	e2 := NewOpenAIEmbedder(EmbedOptions{BaseURL: bad.URL, APIKey: "k", Model: "emb", Dim: 3})
	if _, err := e2.Embed(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "dim") {
		t.Fatalf("dim check: %v", err)
	}
	if u := e2.Usage(); u != (EmbedUsage{}) {
		t.Fatalf("failed call must not count: %+v", u)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	if _, err := NewOpenAIEmbedder(EmbedOptions{BaseURL: down.URL, APIKey: "k", Model: "emb", Dim: 3}).Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("503 should fail")
	}
}

func TestHashEmbedderDeterministicAndSimilar(t *testing.T) {
	h := HashEmbedder{D: 64}
	v, _ := h.Embed(context.Background(), []string{"护眼台灯 学习", "护眼台灯 夜读", "降噪耳机", ""})
	cos := func(a, b []float32) float64 {
		var s float64
		for i := range a {
			s += float64(a[i]) * float64(b[i])
		}
		return s
	}
	if cos(v[0], v[1]) <= cos(v[0], v[2]) {
		t.Fatalf("similar texts should be closer: %f vs %f", cos(v[0], v[1]), cos(v[0], v[2]))
	}
	again, _ := h.Embed(context.Background(), []string{"护眼台灯 学习"})
	if cos(v[0], again[0]) < 0.9999 || len(v[3]) != 64 {
		t.Fatal("not deterministic")
	}
}

func TestFilterExpressions(t *testing.T) {
	if got := and(inList("merchant_id", []string{"m1", ""}), inList("doc_type", nil), "x == 1"); got != `merchant_id in ["m1",""] and x == 1` {
		t.Fatal(got)
	}
	if quote(`a"b`) != `"a\"b"` {
		t.Fatal(quote(`a"b`))
	}
	if NewMilvus("", "", 0) != nil || NewMilvus("127.0.0.1:19530", "", 0).base != "http://127.0.0.1:19530" {
		t.Fatal("milvus addr")
	}
}

// milvusAddr 返回测试用 Milvus 地址（BLINK_TEST_MILVUS_ADDR）；没配置时跳过——CI 没有 Milvus 服务，只在本地跑。
func milvusAddr(t *testing.T) string {
	addr := os.Getenv("BLINK_TEST_MILVUS_ADDR")
	if addr == "" {
		t.Skip("BLINK_TEST_MILVUS_ADDR not set (Milvus integration test runs locally only)")
	}
	return addr
}

// tempCollection 返回带随机后缀的临时集合名，测试结束删除。
func tempCollection(t *testing.T, m *Milvus, prefix string) string {
	name := fmt.Sprintf("blink_shop_test_%s_%d_%d", prefix, time.Now().UnixNano()%1e9, rand.Intn(1e6))
	t.Cleanup(func() { _ = m.DropCollection(context.Background(), name) })
	return name
}

func TestMilvusKnowledgeAndProductIndexes(t *testing.T) {
	ctx := context.Background()
	m := NewMilvus(milvusAddr(t), os.Getenv("BLINK_TEST_MILVUS_TOKEN"), 10*time.Second)
	e := HashEmbedder{D: 64}

	kname := tempCollection(t, m, "kn")
	k := NewKnowledgeIndex(m, e, kname)
	if err := k.Upsert(ctx, "d1", []rag.IndexedChunk{
		{ChunkID: "c1", DocumentID: "d1", MerchantID: "m1", ProductID: "p1", DocType: "product_detail", Title: "护眼台灯", Content: "无频闪护眼台灯，适合夜读学习"},
		{ChunkID: "c2", DocumentID: "d1", MerchantID: "m1", DocType: "policy", Title: "退货", Content: "七天无理由退货"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := k.Upsert(ctx, "d2", []rag.IndexedChunk{{ChunkID: "c3", DocumentID: "d2", MerchantID: "", DocType: "policy", Title: "平台规则", Content: "平台七天无理由退货规则"}}); err != nil {
		t.Fatal(err)
	}
	hits, err := k.Search(ctx, "夜读用的护眼台灯", 5, rag.VectorFilter{})
	if err != nil || len(hits) == 0 || hits[0].ChunkID != "c1" || hits[0].Score <= 0 || hits[0].Score > 1 {
		t.Fatalf("knowledge search: %+v %v", hits, err)
	}
	// 过滤：只看平台资料
	hits, err = k.Search(ctx, "七天无理由退货", 5, rag.VectorFilter{MerchantIDs: []string{""}})
	if err != nil || len(hits) != 1 || hits[0].ChunkID != "c3" {
		t.Fatalf("merchant filter: %+v %v", hits, err)
	}
	// 重新写入文档：旧分块被删除
	if err := k.Upsert(ctx, "d1", []rag.IndexedChunk{{ChunkID: "c9", DocumentID: "d1", MerchantID: "m1", DocType: "policy", Title: "保修", Content: "一年保修"}}); err != nil {
		t.Fatal(err)
	}
	hits, _ = k.Search(ctx, "护眼台灯", 10, rag.VectorFilter{})
	for _, h := range hits {
		if h.ChunkID == "c1" || h.ChunkID == "c2" {
			t.Fatalf("stale chunk %s after re-upsert", h.ChunkID)
		}
	}
	// 维度变化：已有集合与新 Embedder 不一致
	if err := NewKnowledgeIndex(m, HashEmbedder{D: 32}, kname).Upsert(ctx, "d3", nil); !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("dim mismatch: %v", err)
	}

	pname := tempCollection(t, m, "pr")
	p := NewProductIndex(m, e, pname)
	if err := p.UpsertProducts(ctx, []rag.IndexedProduct{{ProductID: "p_lamp", Text: "护眼台灯 学习 夜读"}, {ProductID: "p_ear", Text: "降噪耳机 通勤"}}); err != nil {
		t.Fatal(err)
	}
	ph, err := p.SearchProducts(ctx, "晚上夜读的台灯", 5)
	if err != nil || len(ph) == 0 || ph[0].ProductID != "p_lamp" {
		t.Fatalf("product search: %+v %v", ph, err)
	}
	if err := p.DeleteProducts(ctx, []string{"p_lamp"}); err != nil {
		t.Fatal(err)
	}
	ph, _ = p.SearchProducts(ctx, "晚上夜读的台灯", 5)
	for _, h := range ph {
		if h.ProductID == "p_lamp" {
			t.Fatal("deleted product still returned")
		}
	}
}

func TestBootstrapIndexesSeed(t *testing.T) {
	ctx := context.Background()
	m := NewMilvus(milvusAddr(t), os.Getenv("BLINK_TEST_MILVUS_TOKEN"), 10*time.Second)
	mem := memstore.New()
	if _, err := mem.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	e := HashEmbedder{D: 64}
	k := NewKnowledgeIndex(m, e, tempCollection(t, m, "bk"))
	p := NewProductIndex(m, e, tempCollection(t, m, "bp"))
	stats, err := Bootstrap(ctx, mem, k, p, nil)
	if err != nil || stats.Documents != 3 || stats.Chunks != 5 || stats.Products != 6 || stats.Failed != 0 {
		t.Fatalf("bootstrap: %+v %v", stats, err)
	}
	// 可重复执行
	if again, err := Bootstrap(ctx, mem, k, p, nil); err != nil || again != stats {
		t.Fatalf("rerun: %+v %v", again, err)
	}
	ph, err := p.SearchProducts(ctx, "护眼台灯 学习", 3)
	if err != nil || len(ph) == 0 || ph[0].ProductID != "p_seed_lamp" {
		t.Fatalf("search after bootstrap: %+v %v", ph, err)
	}
	// 与关键词检索混合：rag.Retriever 进入 hybrid 模式
	res, err := rag.NewRetriever(mem, k, nil).Search(ctx, rag.Query{Text: "七天无理由退货"})
	if err != nil || res.Mode != "hybrid" || len(res.Citations) == 0 || res.Citations[0].DocumentID != "doc_seed_after_sales" {
		t.Fatalf("hybrid: %+v %v", res, err)
	}
}

// TestMilvusImageIndex：商品图集合按商品整体替换、按商品删除、检索返回商品 ID 和图片地址；图片搜索服务在 Milvus 上的结果与内存索引一致。
func TestMilvusImageIndex(t *testing.T) {
	ctx := context.Background()
	m := NewMilvus(milvusAddr(t), os.Getenv("BLINK_TEST_MILVUS_TOKEN"), 10*time.Second)
	name := tempCollection(t, m, "img")
	x := NewImageIndex(m, 3, name)
	if NewImageIndex(nil, 3, name) != nil || NewImageIndex(m, 0, name) != nil {
		t.Fatal("invalid index should be nil")
	}
	if err := x.ReplaceProduct(ctx, "p1", []imagesearch.IndexedImage{{ProductID: "p1", ImageURL: "/a/1.png", Vector: []float32{1, 0, 0}},
		{ProductID: "p1", ImageURL: "/a/2.png", Vector: []float32{0, 1, 0}}}); err != nil {
		t.Fatal(err)
	}
	if err := x.ReplaceProduct(ctx, "p2", []imagesearch.IndexedImage{{ProductID: "p2", ImageURL: "/b/1.png", Vector: []float32{0, 0, 1}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := x.Search(ctx, []float32{0.1, 1, 0}, 5)
	if err != nil || len(hits) == 0 || hits[0].ProductID != "p1" || hits[0].ImageURL != "/a/2.png" {
		t.Fatalf("search: %+v %v", hits, err)
	}
	// 替换：旧图被删
	if err := x.ReplaceProduct(ctx, "p1", []imagesearch.IndexedImage{{ProductID: "p1", ImageURL: "/a/3.png", Vector: []float32{0, 0, 1}}}); err != nil {
		t.Fatal(err)
	}
	hits, _ = x.Search(ctx, []float32{0, 1, 0}, 5)
	for _, h := range hits {
		if h.ImageURL == "/a/2.png" || h.ImageURL == "/a/1.png" {
			t.Fatalf("stale image after replace: %+v", hits)
		}
	}
	if err := x.DeleteProducts(ctx, []string{"p1", "p2"}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := x.Search(ctx, []float32{0, 0, 1}, 5); len(hits) != 0 {
		t.Fatalf("after delete: %+v", hits)
	}
	if err := NewImageIndex(m, 4, name).ReplaceProduct(ctx, "p3", nil); !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("dim mismatch: %v", err)
	}

	// 整条链路：种子商品图写入 Milvus，按图检索
	mem := memstore.New()
	if _, err := mem.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	e := imagevector.Local{}
	svc := imagesearch.New(e, NewImageIndex(m, e.Dim(), tempCollection(t, m, "imgsvc")), imagesearch.NewSource(nil), mem, nil, nil)
	stats, err := svc.Bootstrap(ctx)
	if err != nil || stats.Products != 6 || stats.Images != 12 {
		t.Fatalf("bootstrap: %+v %v", stats, err)
	}
	raw, _ := fs.ReadFile(assets.FS, "catalog/products/p_seed_lamp.png")
	res, err := svc.Search(ctx, raw, 3)
	if err != nil || res.Status != imagesearch.StatusMatched || res.Items[0].Product.ProductID != "p_seed_lamp" {
		t.Fatalf("image search: %+v %v", res, err)
	}
}

// TestMilvusStatus：集合不存在时 exists=false 且不会被创建；存在时有维度、行数和加载状态；Milvus 不可达时只给出错误类别。
func TestMilvusStatus(t *testing.T) {
	ctx := context.Background()
	m := NewMilvus(milvusAddr(t), "", 10*time.Second)
	name := tempCollection(t, m, "status")
	if st := m.Status(ctx, name); st.Exists || st.Error != "" {
		t.Fatalf("missing collection: %+v", st)
	}
	if st := m.Status(ctx, name); st.Exists {
		t.Fatal("status must not create the collection")
	}
	idx := NewProductIndex(m, HashEmbedder{D: 32}, name)
	if err := idx.UpsertProducts(ctx, []rag.IndexedProduct{{ProductID: "p1", Text: "降噪耳机"}, {ProductID: "p2", Text: "无线鼠标"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, embedder := idx.Status(ctx)
		if st.Exists && st.Dimension == 32 && st.RowCount == 2 && st.LoadState != "" && st.Error == "" && embedder == "hash:32" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status: %+v %s", st, embedder)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st := NewMilvus("127.0.0.1:1", "", 300*time.Millisecond).Status(ctx, name); st.Error != "unreachable" || strings.Contains(st.Error, "127.0.0.1") {
		t.Fatalf("unreachable: %+v", st)
	}
}

type failingEmbedder struct{ calls int }

func (f *failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	f.calls++
	return nil, errors.New("embedding 503")
}
func (f *failingEmbedder) Dim() int     { return 8 }
func (f *failingEmbedder) Name() string { return "failing" }

// Embedding 熔断：连续失败后不再调用，直接报错（检索退回关键词）；名称和维度不变。
func TestEmbedderBreaker(t *testing.T) {
	f := &failingEmbedder{}
	e := WithBreaker(f, resilience.NewBreaker("embedding_test", 3, time.Hour))
	for range 5 {
		_, _ = e.Embed(context.Background(), []string{"x"})
	}
	if f.calls != 3 || e.Name() != "failing" || e.Dim() != 8 {
		t.Fatalf("calls=%d name=%s dim=%d", f.calls, e.Name(), e.Dim())
	}
	if _, err := e.Embed(context.Background(), nil); !errors.Is(err, resilience.ErrOpen) {
		t.Fatalf("want ErrOpen, got %v", err)
	}
}

// Milvus 熔断：连不上时连续失败达到阈值后不再发请求；Milvus 正常返回的业务错误不算失败。
func TestMilvusBreaker(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.Contains(r.URL.Path, "/has") {
			fmt.Fprint(w, `{"code":100,"message":"collection not found"}`)
			return
		}
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	m := NewMilvus(srv.URL, "", time.Second)
	m.SetBreaker(resilience.NewBreaker("milvus_test", 2, time.Hour))
	for range 4 {
		var has struct{}
		_ = m.call(context.Background(), "/v2/vectordb/collections/has", map[string]any{}, &has)
	}
	if hits != 4 {
		t.Fatalf("business errors should not open the breaker: hits=%d", hits)
	}
	for range 4 {
		_ = m.call(context.Background(), "/v2/vectordb/entities/search", map[string]any{}, nil)
	}
	if hits != 6 {
		t.Fatalf("breaker should stop after 2 transport failures: hits=%d", hits)
	}
	if err := m.call(context.Background(), "/v2/vectordb/collections/has", map[string]any{}, nil); !errors.Is(err, resilience.ErrOpen) {
		t.Fatalf("want ErrOpen, got %v", err)
	}
}
