package domain

import "testing"

func TestProductSyncFromSKUs(t *testing.T) {
	p := Product{ProductID: "p1", SKUs: []ProductSKU{
		{SkuID: "a", ProductID: "p1", Price: MustMoney("10"), StockQuantity: 4},
		{SkuID: "b", ProductID: "p1", Price: MustMoney("12.5"), StockQuantity: 9, IsDefault: true},
	}}
	if err := p.SyncFromSKUs(); err != nil {
		t.Fatal(err)
	}
	if p.Price != MustMoney("12.5") || p.StockQuantity != 13 || p.StockStatus != StockInStock ||
		p.SKUs[0].StockStatus != StockLow || p.SKUs[1].StockStatus != StockLow {
		t.Fatalf("synced = %+v", p)
	}
	for name, skus := range map[string][]ProductSKU{
		"none":          nil,
		"no default":    {{SkuID: "a", ProductID: "p1"}},
		"two defaults":  {{SkuID: "a", ProductID: "p1", IsDefault: true}, {SkuID: "b", ProductID: "p1", IsDefault: true}},
		"other product": {{SkuID: "a", ProductID: "p2", IsDefault: true}},
	} {
		q := Product{ProductID: "p1", SKUs: skus}
		if err := q.SyncFromSKUs(); err != ErrSKUInvariant {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
