package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// recordingIndex 记录商品向量的写入和删除。
type recordingIndex struct {
	mu       sync.Mutex
	upserted map[string]string // product_id → text
	deleted  []string
}

func (r *recordingIndex) UpsertProducts(_ context.Context, items []rag.IndexedProduct) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, it := range items {
		r.upserted[it.ProductID] = it.Text
	}
	return nil
}

func (r *recordingIndex) DeleteProducts(_ context.Context, ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, ids...)
	return nil
}

func (r *recordingIndex) SearchProducts(context.Context, string, int) ([]rag.ProductHit, error) {
	return nil, nil
}

// 商品新建、修改、删除和管理员上下架后，商品向量异步同步：可见的写入（带分类名等检索文字），不可见的删除。
func TestProductVectorSync(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	idx := &recordingIndex{upserted: map[string]string{}}
	ts.productIndex = idx
	tok := ts.merchantToken(t, seed.MerchantUsername)
	p := createProduct(t, ts, tok, fullProduct())
	ts.WaitVectorSync()
	if text := idx.upserted[p.ProductID]; text == "" || !containsAll(text, "Blink 轻薄键盘 K3", "键盘", "办公", "安静轻薄") {
		t.Fatalf("created: %q", text)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, merchantProducts+"/"+p.ProductID, tok, map[string]any{"name": "Blink 轻薄键盘 K3 Pro"}), http.StatusOK, "")
	ts.WaitVectorSync()
	if !containsAll(idx.upserted[p.ProductID], "K3 Pro") {
		t.Fatalf("updated: %q", idx.upserted[p.ProductID])
	}
	// 管理员把商品设为风控：不再公开可见 → 删除向量
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/products/"+p.ProductID, admin, map[string]any{"status": "risk", "reason": "测试"}), http.StatusOK, "")
	ts.WaitVectorSync()
	if len(idx.deleted) != 1 || idx.deleted[0] != p.ProductID {
		t.Fatalf("risk: %v", idx.deleted)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/products/"+p.ProductID, admin, map[string]any{"status": "active", "reason": "恢复"}), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodDelete, merchantProducts+"/"+p.ProductID, tok, nil), http.StatusOK, "")
	ts.WaitVectorSync()
	if n := len(idx.deleted); n < 2 || idx.deleted[n-1] != p.ProductID {
		t.Fatalf("deleted: %v", idx.deleted)
	}
	// 没有配置索引：什么都不做
	ts.productIndex = nil
	ts.syncProductVectors("p_seed_nova")
	ts.WaitVectorSync()
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
