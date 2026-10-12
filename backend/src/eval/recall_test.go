package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/vector"
)

// 召回方式对照（-recall）：向量模式没有索引时报错；keyword 不碰向量；vector 只用向量召回；Embedding 用量分建索引和检索两段记。
func TestRAGRecallModes(t *testing.T) {
	ctx := context.Background()
	cases := filepath.Join(t.TempDir(), "rag.jsonl")
	if err := os.WriteFile(cases, []byte(`{"id":"a","query":"耳机降噪多少分贝","expect":["earbuds_faq"]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := RAGOptions{Cases: cases, Corpus: "../../fixtures/rag/corpus.json", K: 3}
	for _, mode := range []string{rag.RecallVector, rag.RecallHybrid, "bm25"} {
		o := opts
		o.Recall = mode
		if _, err := RunRAG(ctx, o); err == nil {
			t.Fatalf("recall=%s without vector index should fail", mode)
		}
	}
	emb := vector.HashEmbedder{D: 64}
	idx := &memVector{e: emb}
	var u vector.EmbedUsage
	usage := func() vector.EmbedUsage { u.Calls, u.Texts = int64(idx.embeds), int64(idx.texts); return u }
	for _, tc := range []struct {
		mode        string
		wantQueries float64
	}{{rag.RecallKeyword, 0}, {rag.RecallVector, 1}, {rag.RecallHybrid, 1}} {
		*idx = memVector{e: emb}
		o := opts
		o.Vector, o.Recall, o.Usage = idx, tc.mode, usage
		rep, err := RunRAG(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Config["mode"] != tc.mode || rep.Metrics["embed_query_calls"] != tc.wantQueries || rep.Passed != 1 {
			t.Fatalf("%s: config=%v metrics=%v failures=%v", tc.mode, rep.Config, rep.Metrics, rep.Failures)
		}
		if tc.mode == rag.RecallKeyword && (idx.upserts != 0 || rep.Metrics["embed_index_calls"] != 0) {
			t.Fatalf("keyword mode must not index or embed: %+v %v", idx, rep.Metrics)
		}
		if tc.mode != rag.RecallKeyword && rep.Metrics["embed_index_calls"] == 0 {
			t.Fatalf("%s: index usage not recorded: %v", tc.mode, rep.Metrics)
		}
		if rep.Cases[0].Detail["search_ms"] == nil {
			t.Fatalf("per-case latency missing: %+v", rep.Cases[0])
		}
	}
}

// 商品搜索的 vector 召回关掉关键词召回：向量索引不返回时就没有推荐；keyword 模式不调向量。
func TestProductRecallModes(t *testing.T) {
	ctx := context.Background()
	cases := filepath.Join(t.TempDir(), "ps.jsonl")
	if err := os.WriteFile(cases, []byte(`{"id":"a","query":"推荐一款通勤降噪耳机","expect_top":"p_seed_earbuds"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := &countingProducts{}
	rep, err := RunProducts(ctx, ProductOptions{Cases: cases, ProductIndex: empty, Recall: rag.RecallVector})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 0 || rep.Config["recall"] != "vector" || empty.searches == 0 {
		t.Fatalf("vector-only with empty index should find nothing: %+v %v", rep.Config, rep.Failures)
	}
	hit := &countingProducts{hits: []rag.ProductHit{{ProductID: "p_seed_earbuds", Score: 0.9}}}
	if rep, err = RunProducts(ctx, ProductOptions{Cases: cases, ProductIndex: hit, Recall: rag.RecallVector}); err != nil || rep.Passed != 1 {
		t.Fatalf("vector-only with hit: %v %v", err, rep.Failures)
	}
	kw := &countingProducts{}
	if rep, err = RunProducts(ctx, ProductOptions{Cases: cases, ProductIndex: kw, Recall: rag.RecallKeyword}); err != nil || rep.Passed != 1 || kw.searches != 0 || rep.Config["recall"] != "keyword" {
		t.Fatalf("keyword mode: %v searches=%d %v", err, kw.searches, rep.Config)
	}
	if _, err := RunProducts(ctx, ProductOptions{Cases: cases, Recall: rag.RecallHybrid}); err == nil || !strings.Contains(err.Error(), "向量") {
		t.Fatalf("hybrid without index: %v", err)
	}
}

// memVector 是内存向量索引（余弦相似度），记录 Embedding 次数。
type memVector struct {
	e                      vector.HashEmbedder
	rows                   map[string][]float32
	embeds, texts, upserts int
}

func (m *memVector) Upsert(ctx context.Context, _ string, chunks []rag.IndexedChunk) error {
	if m.rows == nil {
		m.rows = map[string][]float32{}
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Title + "\n" + c.Content
	}
	vecs, _ := m.e.Embed(ctx, texts)
	m.embeds, m.texts, m.upserts = m.embeds+1, m.texts+len(texts), m.upserts+1
	for i, c := range chunks {
		m.rows[c.ChunkID] = vecs[i]
	}
	return nil
}

func (m *memVector) Search(ctx context.Context, text string, topN int, _ rag.VectorFilter) ([]rag.VectorHit, error) {
	vecs, _ := m.e.Embed(ctx, []string{text})
	m.embeds, m.texts = m.embeds+1, m.texts+1
	var out []rag.VectorHit
	for id, v := range m.rows {
		var dot float64
		for i := range v {
			dot += float64(v[i]) * float64(vecs[0][i])
		}
		out = append(out, rag.VectorHit{ChunkID: id, Score: dot})
	}
	return out, nil
}

type countingProducts struct {
	hits     []rag.ProductHit
	searches int
}

func (c *countingProducts) UpsertProducts(context.Context, []rag.IndexedProduct) error { return nil }
func (c *countingProducts) DeleteProducts(context.Context, []string) error             { return nil }
func (c *countingProducts) SearchProducts(context.Context, string, int) ([]rag.ProductHit, error) {
	c.searches++
	return c.hits, nil
}

// 图片评测：Embedder 被阈值覆盖包了一层时，仍能取到用量；逐条记前几名分数。
func TestImageUsageThroughWrapper(t *testing.T) {
	cases := filepath.Join(t.TempDir(), "img.jsonl")
	if err := os.WriteFile(cases, []byte(`{"id":"a","source":"catalog/products/p_seed_mouse.png","transforms":["crop:0.85"],"expect_top":"p_seed_mouse","tags":["product"]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &usageLocal{}
	rep, err := RunImages(context.Background(), ImageOptions{Cases: cases, Embedder: imagevector.WithThresholds(e, 0.99, 0.01), MinPass: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	// 建索引的商品图 + 1 张查询图
	if rep.Metrics["embed_images"] != float64(e.n) || e.n < 2 || rep.Metrics["embed_image_tokens"] != float64(100*e.n) || rep.Config["threshold_match"] != "0.99" {
		t.Fatalf("metrics=%v config=%v n=%d", rep.Metrics, rep.Config, e.n)
	}
	if items, _ := rep.Cases[0].Detail["items"].([]string); len(items) == 0 {
		t.Fatalf("detail: %+v", rep.Cases[0])
	}
}

type usageLocal struct {
	imagevector.Local
	n int64
}

func (u *usageLocal) Embed(ctx context.Context, data []byte) ([]float32, error) {
	v, err := u.Local.Embed(ctx, data)
	if err == nil {
		u.n++
	}
	return v, err
}

func (u *usageLocal) Usage() imagevector.Usage {
	return imagevector.Usage{Images: u.n, ImageTokens: 100 * u.n, TotalTokens: 100 * u.n}
}

// 多轮链路的业务断言：加购 → 改数量 → 结算后订单里有鼠标、待支付、购物车为空；写错的期望都会失败并说明原因。
func TestAgentOutcomeAssertions(t *testing.T) {
	ctx := context.Background()
	ok := writeCases(t,
		`{"id":"a","history":["把 Blink 静音无线鼠标 M2 加到购物车","把数量改成 3 件"],"query":"结算","expect_orders":["p_seed_mouse"],"expect_order_status":"pending_payment","expect_cart_empty":true}`,
		`{"id":"b","history":["把 Blink 静音无线鼠标 M2 加到购物车"],"query":"把数量改成 3 件","expect_cart_qty":{"p_seed_mouse":3}}`,
		`{"id":"c","history":["有什么优惠券"],"query":"帮我领一下","expect_user_coupons":["coupon_seed_digital"]}`)
	rep, err := RunAgent(ctx, AgentOptions{Cases: ok})
	if err != nil || rep.Passed != 3 {
		t.Fatalf("expected pass: %v %v", err, rep.Failures)
	}
	bad := writeCases(t,
		`{"id":"a","history":["把 Blink 静音无线鼠标 M2 加到购物车"],"query":"结算","expect_orders":["p_seed_nova"],"expect_order_status":"paid","expect_cart_qty":{"p_seed_mouse":1}}`,
		`{"id":"b","query":"把 Blink 静音无线鼠标 M2 加到购物车","expect_cart_empty":true,"expect_user_coupons":["coupon_seed_digital"]}`)
	rep, err = RunAgent(ctx, AgentOptions{Cases: bad})
	if err != nil || rep.Passed != 0 {
		t.Fatalf("expected failures: %v %v", err, rep.Failures)
	}
	all := ""
	for _, f := range rep.Failures {
		all += strings.Join(f.Problems, ";") + ";"
	}
	for _, want := range []string{"p_seed_nova 应在订单里", "最近的订单状态应为 paid", "购物车里 p_seed_mouse 应为 1 件，实际 0", "购物车应为空", "coupon_seed_digital 应已领到"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %s", want, all)
		}
	}
}
