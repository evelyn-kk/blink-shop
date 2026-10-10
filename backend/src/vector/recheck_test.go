package vector

import (
	"context"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

type fakeProductIndex struct {
	text     map[string]string
	onUpsert func()
}

func (f *fakeProductIndex) UpsertProducts(_ context.Context, items []rag.IndexedProduct) error {
	for _, it := range items {
		f.text[it.ProductID] = it.Text
	}
	if f.onUpsert != nil {
		fn := f.onUpsert
		f.onUpsert = nil
		fn()
	}
	return nil
}

func (f *fakeProductIndex) DeleteProducts(_ context.Context, ids []string) error {
	for _, id := range ids {
		delete(f.text, id)
	}
	return nil
}

func (f *fakeProductIndex) SearchProducts(context.Context, string, int) ([]rag.ProductHit, error) {
	return nil, nil
}

// 全量索引写入后商品又被改名 / 下架（另一个进程的增量同步同时在跑）：复查按最新状态重写或删除。
func TestRecheckProductAfterWrite(t *testing.T) {
	ctx := context.Background()
	mem := memstore.New()
	if _, err := mem.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	old, err := mem.GetVisibleProduct(ctx, "p_seed_mouse")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{old.CategoryID: "鼠标"}
	written := rag.IndexedProduct{ProductID: old.ProductID, MerchantID: old.MerchantID, CategoryID: old.CategoryID, Text: rag.ProductText(old, "鼠标")}
	idx := &fakeProductIndex{text: map[string]string{old.ProductID: written.Text}}

	if _, err := mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error { p.Name = "Blink 静音鼠标 M3"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := recheckProduct(ctx, mem, idx, names, written); err != nil {
		t.Fatal(err)
	}
	if got := idx.text["p_seed_mouse"]; got == written.Text || !strings.Contains(got, "M3") {
		t.Fatalf("renamed: %q", got)
	}
	// 写入新文本的同时商品被下架：下一轮复查发现不可见，删除
	cur, _ := mem.GetVisibleProduct(ctx, "p_seed_mouse")
	written = rag.IndexedProduct{ProductID: cur.ProductID, MerchantID: cur.MerchantID, CategoryID: cur.CategoryID, Text: "过期文本"}
	idx.onUpsert = func() {
		_, _ = mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error { p.Status = domain.ProductInactive; return nil })
	}
	if err := recheckProduct(ctx, mem, idx, names, written); err != nil {
		t.Fatal(err)
	}
	if got, ok := idx.text["p_seed_mouse"]; ok {
		t.Fatalf("inactive product still indexed: %q", got)
	}
}
