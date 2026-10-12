package seed

import (
	"reflect"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func TestCatalogDeterministicAndValid(t *testing.T) {
	a, b := Catalog(2000, 42), Catalog(2000, 42)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed must generate the same catalog")
	}
	if reflect.DeepEqual(a[:50], Catalog(50, 43)) {
		t.Fatal("different seed should differ")
	}
	ids, skus, notActive := map[string]bool{}, 0, 0
	cats := map[string]int{}
	for _, p := range a {
		if ids[p.ProductID] {
			t.Fatalf("duplicate id %s", p.ProductID)
		}
		ids[p.ProductID] = true
		cp := p
		if err := store.PrepareProduct(&cp); err != nil || cp.ProductID != p.ProductID {
			t.Fatalf("%s invalid: %v", p.ProductID, err)
		}
		if len(p.SKUs) < 1 || len(p.SKUs) > 3 || !p.SKUs[0].IsDefault || p.Price != p.SKUs[0].Price {
			t.Fatalf("%s skus: %+v", p.ProductID, p.SKUs)
		}
		skus += len(p.SKUs)
		if p.Status != domain.ProductActive {
			notActive++
		}
		cats[p.CategoryID]++
	}
	// 约 5% 下架 / 风控；五个叶子分类都有
	if notActive < 50 || notActive > 160 || len(cats) != 5 || skus < 2000 {
		t.Fatalf("notActive=%d cats=%v skus=%d", notActive, cats, skus)
	}
	// 生成的商家和分类都在开发种子里
	d, err := Dev(func(string) (string, error) { return "x", nil })
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, c := range d.Categories {
		known[c.CategoryID] = true
	}
	for _, m := range d.Merchants {
		known[m.MerchantID] = true
	}
	for c := range cats {
		if !known[c] {
			t.Fatalf("unknown category %s", c)
		}
	}
	for _, p := range a[:100] {
		if !known[p.MerchantID] {
			t.Fatalf("unknown merchant %s", p.MerchantID)
		}
	}
}
