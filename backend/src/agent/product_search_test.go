package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

func (e *env) search(t *testing.T, args map[string]any) ProductSearchResult {
	t.Helper()
	return e.mustOK(t, e.tc(seed.User2ID, IntentProductSearch), ToolSearchProducts, args).(ProductSearchResult)
}

func ids(res ProductSearchResult) string {
	var out []string
	for _, p := range res.Products {
		out = append(out, p.ProductID)
	}
	return strings.Join(out, ",")
}

func TestProductSearchStructuredConstraints(t *testing.T) {
	e := newEnv(t)
	// 价格区间
	if res := e.search(t, map[string]any{"query": "手机", "min_price": 1000.0, "max_price": 3500.0}); ids(res) != "p_seed_nova" || res.Filtered != 1 {
		t.Fatalf("range: %s filtered=%d", ids(res), res.Filtered)
	}
	if res := e.search(t, map[string]any{"query": "手机", "min_price": 3000.0}); ids(res) != "p_seed_vista" {
		t.Fatalf("floor: %s", ids(res))
	}
	// 品牌：精确匹配（不区分大小写）
	if res := e.search(t, map[string]any{"query": "台灯", "brands": []any{"blink home"}}); ids(res) != "p_seed_lamp" {
		t.Fatalf("brand: %s", ids(res))
	}
	if res := e.search(t, map[string]any{"query": "台灯", "brands": []any{"Blink"}}); ids(res) != "" || res.Filtered != 1 {
		t.Fatalf("brand mismatch: %s %d", ids(res), res.Filtered)
	}
	// 用途匹配“适合”：旅行拍照 → Vista 排在 Nova 前（虽然更贵）
	res := e.search(t, map[string]any{"query": "旅行拍照 手机"})
	if !strings.HasPrefix(ids(res), "p_seed_vista") {
		t.Fatalf("use boost: %s %+v", ids(res), res.Rerank.Scores)
	}
	if res.Rerank.Mode != "rule" || len(res.Rerank.Scores) != 2 || !containsStr(res.Rerank.Scores[0].Reasons, "use:旅行拍照") {
		t.Fatalf("rerank detail: %+v", res.Rerank)
	}
	// 冲突：用途写在“注意 / 不适合”里 → 不作为推荐（weak），并说明冲突
	res = e.search(t, map[string]any{"query": "游泳 耳机"})
	if res.Relevance != RelevanceWeak || len(res.Conflicts) != 1 || res.Conflicts[0] != "Blink Air 降噪耳机" {
		t.Fatalf("conflict: %+v", res)
	}
	res = e.search(t, map[string]any{"query": "重度手游 手机"})
	if ids(res) == "" || res.Products[0].ProductID != "p_seed_vista" {
		t.Fatalf("conflicting product should rank last: %s", ids(res))
	}
	// 缺货降权：键盘缺货
	res = e.search(t, map[string]any{"query": "键盘"})
	if !containsStr(res.Rerank.Scores[0].Reasons, "out_of_stock") {
		t.Fatalf("out of stock reason: %+v", res.Rerank.Scores)
	}
}

func TestProductSearchThroughRunner(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	r := e.run(t, seed.User2ID, sid, "1000 到 3500 的手机")
	if pl := r.block(BlockProductList); pl == nil || strings.Join(productIDs(pl), ",") != "p_seed_nova" || !strings.Contains(r.text.String(), "¥1000–¥3500") {
		t.Fatalf("range answer: %q", r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "推荐一款 Blink Home 的台灯")
	if pl := r.block(BlockProductList); pl == nil || productIDs(pl)[0] != "p_seed_lamp" || !strings.Contains(r.text.String(), "品牌“Blink Home”") {
		t.Fatalf("brand answer: %q", r.text.String())
	}
	r = e.run(t, seed.User2ID, sid, "推荐游泳用的耳机")
	if !strings.Contains(r.text.String(), "不太合适") || r.block(BlockProductList)["title"] != "不太合适的商品" {
		t.Fatalf("conflict answer: %q", r.text.String())
	}
	if rr := r.traceMeta("retrieval", "products"); rr["conflicts"] != 1 || rr["relevance"] != RelevanceWeak {
		t.Fatalf("retrieval trace: %v", rr)
	}
	// 冲突商品不能被“第一个”当作推荐来加购？——它展示过，用户明确说要还是可以加；这里只验证没有自动推荐
	if strings.Contains(r.text.String(), "优先推荐") {
		t.Fatalf("conflict should not be recommended: %q", r.text.String())
	}
}

func TestProductModelRerank(t *testing.T) {
	e := newEnv(t)
	mock := &llm.Mock{}
	st := settings(false, false)
	st.RerankEnabled = true
	e.withModel(mock, st)
	mock.Then(llm.Text(`{"ranking":["p_seed_vista","p_seed_nova"]}`))
	res := e.search(t, map[string]any{"query": "手机"})
	if ids(res) != "p_seed_vista,p_seed_nova" || res.Rerank.Mode != "model" {
		t.Fatalf("model rerank: %s %+v", ids(res), res.Rerank)
	}
	if req := mock.Last(); req.Model != "small-m" || !req.JSON || !strings.Contains(req.Messages[1].Content, "p_seed_nova") {
		t.Fatalf("rerank request: %+v", req)
	}
	// 模型给了不在候选里的 ID / 坏 JSON / 超时 → 规则顺序
	for _, rep := range []llm.Reply{llm.Text(`{"ranking":["p_fake","p_seed_nova"]}`), llm.Text("nope"), llm.Fail(context.DeadlineExceeded)} {
		mock.Then(rep)
		res = e.search(t, map[string]any{"query": "手机"})
		if ids(res) != "p_seed_nova,p_seed_vista" || res.Rerank.Mode != "rule" || res.Rerank.Error == "" {
			t.Fatalf("rerank fallback: %s %+v", ids(res), res.Rerank)
		}
	}
	// 模型想把冲突商品排到前面：冲突的始终在后
	mock.Then(llm.Text(`{"ranking":["p_seed_nova","p_seed_vista"]}`))
	res = e.search(t, map[string]any{"query": "重度手游 手机"})
	if !strings.HasPrefix(ids(res), "p_seed_vista") {
		t.Fatalf("conflict after model rerank: %s", ids(res))
	}
	// 运行器轨迹：rerank 失败记 error + fallback
	sid := e.newSession(t, seed.User2ID)
	mock.Then(llm.Text("bad"))
	r := e.run(t, seed.User2ID, sid, "推荐一款手机")
	if m := r.traceMeta("rerank", "products"); m["fallback"] != "rule" || m["mode"] != "rule" {
		t.Fatalf("rerank trace: %v", m)
	}
}

// fakeProductIndex 是测试用商品向量索引：对任何查询返回固定命中。
type fakeProductIndex struct {
	hits []rag.ProductHit
	err  error
}

func (f *fakeProductIndex) UpsertProducts(context.Context, []rag.IndexedProduct) error { return nil }
func (f *fakeProductIndex) DeleteProducts(context.Context, []string) error             { return nil }
func (f *fakeProductIndex) SearchProducts(context.Context, string, int) ([]rag.ProductHit, error) {
	return f.hits, f.err
}

func TestProductVectorRecall(t *testing.T) {
	e := newEnv(t)
	idx := &fakeProductIndex{hits: []rag.ProductHit{{ProductID: "p_seed_speaker", Score: 0.95}, {ProductID: "p_seed_lamp", Score: 0.9}, {ProductID: "p_missing", Score: 0.9}}}
	deps := e.runner.deps
	deps.ProductIndex = idx
	e.runner = NewRuleRunner(deps)
	e.reg = e.runner.Registry()
	// 关键词召回不到（“夜里看书”没有字面命中），向量召回出台灯；下架的音箱和不存在的商品被丢弃
	res := e.search(t, map[string]any{"query": "夜里看书"})
	if ids(res) != "p_seed_lamp" || res.Relevance != RelevanceOK || res.Recall.Vector != 1 {
		t.Fatalf("vector recall: %s %+v %+v", ids(res), res.Recall, res.Rerank)
	}
	// 向量结果同样受价格过滤
	if res := e.search(t, map[string]any{"query": "夜里看书", "max_price": 100.0}); ids(res) != "" || res.Filtered != 1 {
		t.Fatalf("vector + price: %s", ids(res))
	}
	// 向量检索失败：降级关键词，记 vector_error
	idx.err, idx.hits = errors.New("milvus down"), nil
	res = e.search(t, map[string]any{"query": "台灯"})
	if ids(res) != "p_seed_lamp" || res.Recall.VectorError == "" {
		t.Fatalf("vector fallback: %s %+v", ids(res), res.Recall)
	}
	_ = domain.StockInStock
}
