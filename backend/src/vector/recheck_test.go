package vector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
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
		fn() // fn 可以重新设置 onUpsert（连续变更）
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

// 每次重写后商品都又被改名，连续 6 次（超过以前的 3 轮上限）后停：最终是最后一次的名字；最后一次是下架时删除。
func TestRecheckKeepsLastChangeAfterManyRounds(t *testing.T) {
	old := imagesearch.RecheckDelay
	imagesearch.RecheckDelay = func(int) time.Duration { return 0 }
	t.Cleanup(func() { imagesearch.RecheckDelay = old })
	for _, hideLast := range []bool{false, true} {
		ctx := context.Background()
		mem := memstore.New()
		if _, err := mem.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
			t.Fatal(err)
		}
		cur, _ := mem.GetVisibleProduct(ctx, "p_seed_mouse")
		names := map[string]string{cur.CategoryID: "鼠标"}
		written := rag.IndexedProduct{ProductID: cur.ProductID, MerchantID: cur.MerchantID, CategoryID: cur.CategoryID, Text: rag.ProductText(cur, "鼠标")}
		idx := &fakeProductIndex{text: map[string]string{cur.ProductID: written.Text}}
		changes := 0
		var next func()
		next = func() {
			if changes == 6 {
				return
			}
			changes++
			n := changes
			_, _ = mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error {
				p.Name = fmt.Sprintf("Blink 鼠标 第%d版", n)
				if n == 6 && hideLast {
					p.Status = domain.ProductInactive
				}
				return nil
			})
			idx.onUpsert = next
		}
		next() // 第 1 次变更发生在全量写入之后、复查之前
		if err := recheckProduct(ctx, mem, idx, names, written); err != nil {
			t.Fatal(err)
		}
		got, ok := idx.text["p_seed_mouse"]
		if changes != 6 || (hideLast && ok) || (!hideLast && !strings.Contains(got, "第6版")) {
			t.Fatalf("hideLast=%v changes=%d text=%q ok=%v", hideLast, changes, got, ok)
		}
	}
}
