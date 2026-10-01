package seed

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

var update = flag.Bool("update", false, "重新生成 fixtures/domain 下的序列化样例")

func plainHash(pw string) (string, error) { return "hashed:" + pw, nil }

func devData(t *testing.T) store.SeedData {
	t.Helper()
	d, err := Dev(plainHash)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestDomainFixtures 把领域对象序列化后与 fixtures/domain 下的样例逐字节比较，并验证能原样反序列化回来。
// 字段改名、金额格式或时间格式变化都会在这里暴露。修改领域类型后用 go test ./src/seed -update 更新样例。
func TestDomainFixtures(t *testing.T) {
	d := devData(t)
	completed := d.Orders[3]
	samples := map[string]any{
		"account":            d.Accounts[3].Account,
		"merchant":           d.Merchants[0],
		"category":           d.Categories[1],
		"product":            d.Products[0],
		"order":              completed,
		"payment":            d.Payments[3],
		"promotion_rule":     d.Promotions[2],
		"coupon":             d.Coupons[0],
		"user_coupon":        d.UserCoupons[0],
		"product_review":     d.Reviews[0],
		"knowledge_document": d.Documents[0],
		"knowledge_chunk":    d.Chunks[0],
	}
	dir := filepath.Join("..", "..", "fixtures", "domain")
	for name, v := range samples {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join(dir, name+".json")
			if *update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture (run with -update to create): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s 序列化结果与 fixture 不一致\n got: %s\nwant: %s", name, got, want)
			}

			// 反序列化回同一类型应完全相等（json:"-" 字段除外）。
			back := reflect.New(reflect.TypeOf(v))
			if err := json.Unmarshal(want, back.Interface()); err != nil {
				t.Fatal(err)
			}
			again, _ := json.MarshalIndent(back.Elem().Interface(), "", "  ")
			if !bytes.Equal(append(again, '\n'), want) {
				t.Fatalf("%s round trip changed the JSON:\n%s", name, again)
			}
			for _, banned := range []string{"password", "hashed:", "token_hash", "object_key", "deleted_at"} {
				if strings.Contains(string(want), banned) {
					t.Fatalf("%s fixture contains sensitive field %q", name, banned)
				}
			}
		})
	}
}

// TestDevSeedIntegrity 检查种子数据自身的一致性（数据库没有外键，引用关系靠这里保证）。
func TestDevSeedIntegrity(t *testing.T) {
	d := devData(t)
	ids := func(n int, f func(i int) string) map[string]bool {
		m := map[string]bool{}
		for i := 0; i < n; i++ {
			if m[f(i)] {
				t.Errorf("duplicate id %s", f(i))
			}
			m[f(i)] = true
		}
		return m
	}
	accounts := ids(len(d.Accounts), func(i int) string { return d.Accounts[i].AccountID })
	merchants := ids(len(d.Merchants), func(i int) string { return d.Merchants[i].MerchantID })
	categories := ids(len(d.Categories), func(i int) string { return d.Categories[i].CategoryID })
	products := ids(len(d.Products), func(i int) string { return d.Products[i].ProductID })
	documents := ids(len(d.Documents), func(i int) string { return d.Documents[i].DocumentID })
	coupons := ids(len(d.Coupons), func(i int) string { return d.Coupons[i].CouponID })
	orders := ids(len(d.Orders), func(i int) string { return d.Orders[i].OrderID })

	// 三种角色都有账号，商家账号绑定存在的商家。
	roles := map[domain.Role]int{}
	for _, a := range d.Accounts {
		roles[a.Role]++
		if a.Role == domain.RoleMerchant && !merchants[a.MerchantID] {
			t.Errorf("merchant account %s bound to unknown merchant %s", a.Username, a.MerchantID)
		}
		if a.PasswordHash != "hashed:"+DevPassword {
			t.Errorf("%s password not produced by hash func", a.Username)
		}
	}
	if roles[domain.RoleAdmin] < 1 || roles[domain.RoleMerchant] < 2 || roles[domain.RoleUser] < 2 {
		t.Errorf("roles = %v, want ≥1 admin, ≥2 merchants, ≥2 users", roles)
	}

	// 两级分类。
	levels := map[bool]int{}
	for _, c := range d.Categories {
		levels[c.ParentID == ""]++
		if c.ParentID != "" && !categories[c.ParentID] {
			t.Errorf("category %s has unknown parent %s", c.CategoryID, c.ParentID)
		}
	}
	if levels[true] < 2 || levels[false] < 2 {
		t.Errorf("category levels = %v", levels)
	}

	// 至少 6 个商品，同时有可售与不可售；库存等于 SKU 之和，价格等于默认 SKU。
	skuOf := map[string]domain.ProductSKU{}
	statuses := map[domain.ProductStatus]bool{}
	sellable := 0
	for _, p := range d.Products {
		statuses[p.Status] = true
		if p.Sellable() {
			sellable++
		}
		if !merchants[p.MerchantID] || !categories[p.CategoryID] {
			t.Errorf("product %s references unknown merchant/category", p.ProductID)
		}
		sum := 0
		for _, s := range p.SKUs {
			sum += s.StockQuantity
			skuOf[s.SkuID] = s
			if s.ProductID != p.ProductID {
				t.Errorf("sku %s belongs to %s, listed under %s", s.SkuID, s.ProductID, p.ProductID)
			}
		}
		if len(p.SKUs) == 0 || sum != p.StockQuantity || p.Price != p.SKUs[0].Price || !p.SKUs[0].IsDefault {
			t.Errorf("product %s stock/price inconsistent with SKUs", p.ProductID)
		}
	}
	if len(d.Products) < 6 || sellable < 3 || sellable == len(d.Products) {
		t.Errorf("products = %d, sellable = %d", len(d.Products), sellable)
	}
	for _, s := range []domain.ProductStatus{domain.ProductActive, domain.ProductInactive, domain.ProductRisk, domain.ProductDeleted} {
		if !statuses[s] {
			t.Errorf("no product with status %s", s)
		}
	}

	// 知识：chunk 归属存在的文档，chunk 数一致。
	chunkCount := map[string]int{}
	for _, c := range d.Chunks {
		if !documents[c.DocumentID] || (c.ProductID != "" && !products[c.ProductID]) {
			t.Errorf("chunk %s has dangling reference", c.ChunkID)
		}
		chunkCount[c.DocumentID]++
	}
	for _, doc := range d.Documents {
		if doc.ChunkCount != chunkCount[doc.DocumentID] || len(doc.ContentHash) != 64 {
			t.Errorf("document %s chunk_count/hash inconsistent", doc.DocumentID)
		}
	}

	// 订单：覆盖每个状态；金额自洽；订单项价格为下单时 SKU 价格。
	seen := map[domain.OrderStatus]bool{}
	itemIDs := map[string]domain.Order{}
	for _, o := range d.Orders {
		seen[o.Status] = true
		if !accounts[o.AccountID] || !merchants[o.MerchantID] {
			t.Errorf("order %s dangling account/merchant", o.OrderID)
		}
		var total domain.Money
		for _, it := range o.Items {
			itemIDs[it.OrderItemID] = o
			sku, ok := skuOf[it.SkuID]
			if !ok || sku.ProductID != it.ProductID || it.Price != sku.Price || it.OrderID != o.OrderID {
				t.Errorf("order item %s inconsistent", it.OrderItemID)
			}
			total += it.Price.Mul(it.Quantity)
		}
		if total != o.TotalAmount || o.PayAmount != o.TotalAmount-o.DiscountAmount || o.PayAmount < 0 {
			t.Errorf("order %s amounts: total=%s discount=%s pay=%s (items %s)", o.OrderID, o.TotalAmount, o.DiscountAmount, o.PayAmount, total)
		}
	}
	for _, s := range []domain.OrderStatus{domain.OrderPendingPayment, domain.OrderPaid, domain.OrderShipped, domain.OrderCompleted, domain.OrderCancelled} {
		if !seen[s] {
			t.Errorf("no order with status %s", s)
		}
	}
	for _, p := range d.Payments {
		if !orders[p.OrderID] {
			t.Errorf("payment %s dangling order", p.PaymentID)
		}
	}
	for _, uc := range d.UserCoupons {
		if !coupons[uc.CouponID] || !accounts[uc.AccountID] || (uc.OrderID != "" && !orders[uc.OrderID]) {
			t.Errorf("user coupon %s dangling reference", uc.UserCouponID)
		}
	}
	for _, r := range d.Reviews {
		o, ok := itemIDs[r.OrderItemID]
		if !ok || o.Status != domain.OrderCompleted || o.OrderID != r.OrderID || r.Rating < 1 || r.Rating > 5 {
			t.Errorf("review %s must reference an item of a completed order", r.ReviewID)
		}
	}
}
