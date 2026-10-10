package rag_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
)

func TestQueryTerms(t *testing.T) {
	got := rag.QueryTerms("耳机降噪多少分贝？LDAC 吗 a 5")
	var texts []string
	weights := map[string]float64{}
	for _, term := range got {
		texts = append(texts, term.Text)
		weights[term.Text] = term.Weight
	}
	want := []string{"耳机", "机降", "降噪", "噪多", "多少", "少分", "分贝", "ldac", "5"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("terms = %v, want %v", texts, want)
	}
	if weights["多少"] != 0.2 || weights["ldac"] != 2 || weights["耳机"] != 1 {
		t.Fatalf("weights = %v", weights)
	}
	if terms := rag.QueryTerms("吗？的了"); len(terms) != 0 {
		t.Fatalf("stop words only = %v", terms)
	}
	long := rag.QueryTerms(strings.Repeat("甲乙丙丁戊己庚辛壬癸", 5))
	if len(long) != rag.MaxQueryTerms && len(long) != 10 {
		t.Fatalf("long query terms = %d", len(long))
	}
}

// fakeVector 按配置返回固定的向量结果，或返回错误。
type fakeVector struct {
	hits   []rag.VectorHit
	err    error
	filter rag.VectorFilter
}

func (f *fakeVector) Upsert(context.Context, string, []rag.IndexedChunk) error { return nil }
func (f *fakeVector) Search(_ context.Context, _ string, _ int, filter rag.VectorFilter) ([]rag.VectorHit, error) {
	f.filter = filter
	return f.hits, f.err
}

func chunkOf(t *testing.T, st store.Store, docID string, index int) string {
	t.Helper()
	chunks, err := st.ListDocumentChunks(context.Background(), docID)
	if err != nil || len(chunks) <= index {
		t.Fatalf("chunks of %s: %v %v", docID, chunks, err)
	}
	return chunks[index].ChunkID
}

func TestHybridSearch(t *testing.T) {
	st := memstore.New()
	ids := loadCorpus(t, st)
	ctx := context.Background()
	battery := chunkOf(t, st, ids["vista_battery"], 0)
	hidden := chunkOf(t, st, ids["speaker_hidden"], 0) // 关联的商品已下架，不可检索
	vec := &fakeVector{hits: []rag.VectorHit{{ChunkID: battery, Score: 0.92}, {ChunkID: hidden, Score: 0.99}, {ChunkID: "ck_deleted", Score: 0.9}}}
	r := rag.NewRetriever(st, vec, nil)

	// “多久能充满电”在关键词上只有很弱的匹配；向量结果把电池说明补进来。
	res, err := r.Search(ctx, rag.Query{Text: "手机多久能充满电", MerchantIDs: []string{"m_seed_digital"}, TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "hybrid" || res.VectorError != "" || len(res.Citations) == 0 {
		t.Fatalf("result = %+v", res)
	}
	top := res.Citations[0]
	if top.ChunkID != battery || !slices.Contains(top.MatchedBy, "vector") {
		t.Fatalf("top = %+v", top)
	}
	for _, c := range res.Citations {
		if c.ChunkID == hidden || c.ChunkID == "ck_deleted" {
			t.Fatalf("invisible vector hit returned: %+v", c)
		}
	}
	if !reflect.DeepEqual(vec.filter.MerchantIDs, []string{"m_seed_digital"}) {
		t.Fatalf("filter not passed to vector index: %+v", vec.filter)
	}

	// 只用关键词时同一问题的结果（作为对照）。
	kw, _ := rag.NewRetriever(st, nil, nil).Search(ctx, rag.Query{Text: "手机多久能充满电", MerchantIDs: []string{"m_seed_digital"}, TopK: 3})
	if kw.Mode != "keyword" {
		t.Fatalf("keyword mode = %s", kw.Mode)
	}
}

func TestVectorFailureFallsBackToKeyword(t *testing.T) {
	st := memstore.New()
	loadCorpus(t, st)
	var logs bytes.Buffer
	r := rag.NewRetriever(st, &fakeVector{err: errors.New("milvus: connection refused")}, slog.New(slog.NewJSONHandler(&logs, nil)))
	res, err := r.Search(context.Background(), rag.Query{Text: "耳机降噪多少分贝"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "keyword" || res.VectorError == "" || strings.Contains(res.VectorError, "milvus") || len(res.Citations) == 0 {
		t.Fatalf("fallback result = %+v", res)
	}
	if !strings.Contains(logs.String(), "fallback to keyword") {
		t.Fatalf("fallback not logged: %s", logs.String())
	}
	for _, c := range res.Citations {
		if !reflect.DeepEqual(c.MatchedBy, []string{"keyword"}) {
			t.Fatalf("matched_by = %v", c.MatchedBy)
		}
	}
	// 请求被取消时返回取消错误，而不是悄悄降级。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := &fakeVector{err: context.Canceled}
	if _, err := rag.NewRetriever(st, cancelled, nil).Search(ctx, rag.Query{Text: "耳机"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search err = %v", err)
	}
}

func TestSearchLimitsAndEmptyQuery(t *testing.T) {
	st := memstore.New()
	loadCorpus(t, st)
	r := rag.NewRetriever(st, nil, nil)
	ctx := context.Background()
	for _, q := range []string{"", "  ", "？！", "吗"} {
		res, err := r.Search(ctx, rag.Query{Text: q})
		if err != nil || len(res.Citations) != 0 || res.Citations == nil {
			t.Fatalf("%q: %+v %v", q, res, err)
		}
	}
	many, _ := r.Search(ctx, rag.Query{Text: "Blink 支持", TopK: 100})
	if len(many.Citations) > rag.MaxTopK {
		t.Fatalf("top_k not clamped: %d", len(many.Citations))
	}
	def, _ := r.Search(ctx, rag.Query{Text: "Blink 支持"})
	if len(def.Citations) != rag.DefaultTopK {
		t.Fatalf("default top_k = %d", len(def.Citations))
	}
	for i := 1; i < len(many.Citations); i++ {
		a, b := many.Citations[i-1], many.Citations[i]
		if a.Score < b.Score || (a.Score == b.Score && a.ChunkID > b.ChunkID) {
			t.Fatalf("not sorted at %d: %v %v", i, a, b)
		}
		if b.Score < rag.MinScore {
			t.Fatalf("score below threshold returned: %v", b)
		}
	}
	// 商品过滤：只返回关联该商品的分块。
	res, _ := r.Search(ctx, rag.Query{Text: "续航 电池", ProductIDs: []string{"p_seed_earbuds"}})
	for _, c := range res.Citations {
		if c.ProductID != "p_seed_earbuds" {
			t.Fatalf("product filter: %+v", c)
		}
	}
	if len(res.Citations) == 0 {
		t.Fatal("product filter returned nothing")
	}
}

// 向量只能加分：相似度很低（或向量没命中）的分块，混合模式下的分数不低于纯关键词分，关键词能召回的结果不会因此丢失。
func TestHybridNeverDropsKeywordResults(t *testing.T) {
	st := memstore.New()
	loadCorpus(t, st)
	ctx := context.Background()
	q := rag.Query{Text: "台灯坏了能换新吗", TopK: 5}
	kw, err := rag.NewRetriever(st, nil, nil).Search(ctx, q)
	if err != nil || len(kw.Citations) == 0 {
		t.Fatalf("keyword: %+v %v", kw, err)
	}
	weak := make([]rag.VectorHit, 0, len(kw.Citations))
	for _, c := range kw.Citations {
		weak = append(weak, rag.VectorHit{ChunkID: c.ChunkID, Score: 0.01})
	}
	for _, vec := range []*fakeVector{{hits: weak}, {hits: nil}} {
		hy, err := rag.NewRetriever(st, vec, nil).Search(ctx, q)
		if err != nil || hy.Mode != "hybrid" {
			t.Fatalf("hybrid: %+v %v", hy, err)
		}
		score := map[string]float64{}
		for _, c := range hy.Citations {
			score[c.ChunkID] = c.Score
		}
		for _, c := range kw.Citations {
			if s, ok := score[c.ChunkID]; !ok || s < c.Score {
				t.Fatalf("keyword result %s dropped or lowered: kw=%v hybrid=%v", c.ChunkID, c.Score, s)
			}
		}
	}
}
