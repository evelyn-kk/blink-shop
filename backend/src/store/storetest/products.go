package storetest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func productCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"CreateProduct", testCreateProduct},
		{"CreateProductRejectsBrokenSKUs", testCreateProductInvalid},
		{"UpdateProductReplacesSKUs", testUpdateProduct},
		{"UpdateProductRollsBack", testUpdateProductRollback},
		{"UpdateProductConcurrent", testUpdateProductConcurrent},
		{"ListMerchantProducts", testListMerchantProducts},
	}
}

func newProduct(name string, skus ...domain.ProductSKU) domain.Product {
	return domain.Product{
		MerchantID: seed.DigitalMerchant, CategoryID: "c_mouse", Name: name, Brand: "Blink",
		MarketPrice: domain.MustMoney("99"), Tags: []string{"新品"}, ImageURLs: []string{}, SellingPoints: []string{},
		RiskNotes: []string{}, Attributes: []domain.ProductAttribute{}, SuitableFor: []string{}, NotSuitableFor: []string{},
		Status: domain.ProductActive, SortOrder: 1000, SKUs: skus,
	}
}

func sku(name, price string, stock int, isDefault bool) domain.ProductSKU {
	return domain.ProductSKU{SkuName: name, Price: domain.MustMoney(price), StockQuantity: stock, IsDefault: isDefault,
		Specs: map[string]string{"颜色": name}}
}

func testCreateProduct(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateProduct(ctx, newProduct("新鼠标", sku("黑", "59", 3, false), sku("白", "69", 20, true)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ProductID, domain.PrefixProduct) || created.Price != domain.MustMoney("69") ||
		created.StockQuantity != 23 || created.StockStatus != domain.StockInStock || created.CreatedAt.IsZero() {
		t.Fatalf("created = %+v", created)
	}
	if len(created.SKUs) != 2 || !created.SKUs[0].IsDefault || created.SKUs[0].SkuName != "白" ||
		created.SKUs[1].StockStatus != domain.StockLow || !strings.HasPrefix(created.SKUs[1].SkuID, domain.PrefixSKU) {
		t.Fatalf("skus = %+v", created.SKUs)
	}
	got, err := s.GetProduct(ctx, created.ProductID)
	if err != nil || !reflect.DeepEqual(got, created) {
		t.Fatalf("GetProduct = %+v, %v", got, err)
	}
	// 新商品立即出现在公开目录。
	items, _, _ := s.SearchVisibleProducts(ctx, store.ProductSearch{CategoryID: "c_mouse", Page: page(1, 10)})
	if got := productIDs(items); !reflect.DeepEqual(got, []string{"p_seed_mouse", created.ProductID}) {
		t.Fatalf("public category = %v", got)
	}
}

func testCreateProductInvalid(t *testing.T, s store.Store) {
	ctx := context.Background()
	for name, p := range map[string]domain.Product{
		"no sku":       newProduct("x"),
		"two defaults": newProduct("x", sku("a", "1", 1, true), sku("b", "1", 1, true)),
		"no default":   newProduct("x", sku("a", "1", 1, false)),
		"no name":      newProduct("", sku("a", "1", 1, true)),
	} {
		if _, err := s.CreateProduct(ctx, p); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	items, total, _ := s.ListMerchantProducts(ctx, store.MerchantProductQuery{MerchantID: seed.DigitalMerchant, Page: page(1, 10)})
	if total != 0 || len(items) != 0 {
		t.Fatalf("invalid products were written: %d", total)
	}
}

func testUpdateProduct(t *testing.T, s store.Store) {
	ctx := context.Background()
	clock := useClock(t, s)
	created, err := s.CreateProduct(ctx, newProduct("旧名字", sku("黑", "59", 3, true), sku("白", "69", 20, false)))
	if err != nil {
		t.Fatal(err)
	}
	black, white := created.SKUs[0], created.SKUs[1]
	clock.Advance(time.Minute)

	updated, err := s.UpdateProduct(ctx, created.ProductID, func(p *domain.Product) error {
		p.Name = "新名字"
		p.MerchantID = "m_someone_else" // 不能改归属
		p.Status = domain.ProductInactive
		keep := p.SKUs[0]
		keep.Price, keep.StockQuantity, keep.IsDefault = domain.MustMoney("49"), 0, false
		p.SKUs = []domain.ProductSKU{keep, sku("红", "79", 5, true)} // 删掉白色，新增红色并设为默认
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "新名字" || updated.MerchantID != seed.DigitalMerchant || updated.Status != domain.ProductInactive ||
		updated.Price != domain.MustMoney("79") || updated.StockQuantity != 5 || updated.StockStatus != domain.StockLow {
		t.Fatalf("updated = %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("timestamps: created %v updated %v", updated.CreatedAt, updated.UpdatedAt)
	}
	if len(updated.SKUs) != 2 || updated.SKUs[0].SkuName != "红" || updated.SKUs[1].SkuID != black.SkuID ||
		updated.SKUs[1].StockStatus != domain.StockOutOfStock || !updated.SKUs[1].CreatedAt.Equal(black.CreatedAt) {
		t.Fatalf("skus = %+v", updated.SKUs)
	}
	for _, sk := range updated.SKUs {
		if sk.SkuID == white.SkuID {
			t.Fatal("被移除的 SKU 仍然存在")
		}
	}
	if _, err := s.UpdateProduct(ctx, "p_missing", func(*domain.Product) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: err = %v", err)
	}
}

func testUpdateProductRollback(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetProduct(ctx, "p_seed_mouse")
	boom := errors.New("boom")
	cases := map[string]struct {
		fn   func(p *domain.Product) error
		want error
	}{
		"fn error": {func(p *domain.Product) error { p.Name = "改了"; return boom }, boom},
		"broken skus": {func(p *domain.Product) error {
			p.Name = "改了"
			p.SKUs[0].IsDefault = false
			return nil
		}, store.ErrInvalid},
		// 借用其他商品的 SKU ID：主键冲突，整体回滚。
		"foreign sku id": {func(p *domain.Product) error {
			p.Name = "改了"
			p.SKUs = append(p.SKUs, domain.ProductSKU{SkuID: "sku_seed_nova_128", SkuName: "偷来的", Price: domain.MustMoney("1")})
			return nil
		}, store.ErrConflict},
	}
	for name, c := range cases {
		if _, err := s.UpdateProduct(ctx, "p_seed_mouse", c.fn); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
		after, _ := s.GetProduct(ctx, "p_seed_mouse")
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("%s: product changed despite rollback:\n%+v\n%+v", name, after, before)
		}
	}
	nova, _ := s.GetProduct(ctx, "p_seed_nova")
	if len(nova.SKUs) != 2 || nova.SKUs[0].SkuName != "Blink Nova 12 8+128G" {
		t.Fatalf("other product touched: %+v", nova.SKUs)
	}
}

// testUpdateProductConcurrent：并发的“读-改-写”在行锁下串行，增量不丢失。
func testUpdateProductConcurrent(t *testing.T, s store.Store) {
	ctx := context.Background()
	created, err := s.CreateProduct(ctx, newProduct("并发商品", sku("默认", "10", 0, true)))
	if err != nil {
		t.Fatal(err)
	}
	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateProduct(ctx, created.ProductID, func(p *domain.Product) error {
				p.SKUs[0].StockQuantity++
				return nil
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetProduct(ctx, created.ProductID)
	if got.StockQuantity != n || got.SKUs[0].StockQuantity != n {
		t.Fatalf("stock = %d / %d, want %d", got.StockQuantity, got.SKUs[0].StockQuantity, n)
	}
}

func testListMerchantProducts(t *testing.T, s store.Store) {
	ctx := context.Background()
	clock := useClock(t, s)
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	fresh, err := s.CreateProduct(ctx, newProduct("最新的鼠标", sku("默认", "10", 1, true)))
	if err != nil {
		t.Fatal(err)
	}
	ids := func(items []domain.Product) []string {
		out := []string{}
		for _, p := range items {
			out = append(out, p.ProductID)
		}
		return out
	}
	q := store.MerchantProductQuery{MerchantID: seed.DigitalMerchant, Page: page(1, 100)}
	all, total, err := s.ListMerchantProducts(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	// 本店所有未删除商品（含下架、风控），最近修改的在前；种子商品 updated_at 相同，按 product_id 倒序。
	want := []string{fresh.ProductID, "p_seed_vista", "p_seed_speaker", "p_seed_powerbank", "p_seed_nova", "p_seed_mouse",
		"p_seed_keyboard", "p_seed_earbuds"}
	if got := ids(all); !reflect.DeepEqual(got, want) || total != len(want) {
		t.Fatalf("all = %v (total %d)\nwant %v", got, total, want)
	}
	if len(all[0].SKUs) != 1 {
		t.Fatalf("skus not loaded: %+v", all[0])
	}
	q.Status = domain.ProductInactive
	if got, _, _ := s.ListMerchantProducts(ctx, q); !reflect.DeepEqual(ids(got), []string{"p_seed_speaker"}) {
		t.Fatalf("inactive = %v", ids(got))
	}
	q.Status, q.Keyword = "", "鼠标"
	if got, _, _ := s.ListMerchantProducts(ctx, q); !reflect.DeepEqual(ids(got), []string{fresh.ProductID, "p_seed_mouse"}) {
		t.Fatalf("keyword = %v", ids(got))
	}
	q.Keyword = "%"
	if got, _, _ := s.ListMerchantProducts(ctx, q); len(got) != 0 {
		t.Fatalf("%% should be literal: %v", ids(got))
	}
	home, total, _ := s.ListMerchantProducts(ctx, store.MerchantProductQuery{MerchantID: seed.HomeMerchant, Page: page(1, 100)})
	if !reflect.DeepEqual(ids(home), []string{"p_seed_lamp"}) || total != 1 {
		t.Fatalf("home = %v", ids(home))
	}
	paged, total, _ := s.ListMerchantProducts(ctx, store.MerchantProductQuery{MerchantID: seed.DigitalMerchant, Page: page(2, 3)})
	if !reflect.DeepEqual(ids(paged), want[3:6]) || total != len(want) {
		t.Fatalf("paged = %v", ids(paged))
	}
}
