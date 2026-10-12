package mysqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
)

// 关键词搜索的计数和分页都要从命中集合开始连接。分类名的 LIKE 一多，优化器会把命中集合估得很大，改成扫全部在售商品、
// 逐行去命中集合里查（10 万商品时查不到的词也要 0.3s）。这里在生成目录上看执行计划：命中集合必须是被扫描的那张表，
// 而不是被逐行查找的一方。
func TestKeywordSearchDrivesFromHits(t *testing.T) {
	ctx := context.Background()
	s, err := Open(mysqltest.FreshDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	data, err := seed.Dev(func(string) (string, error) { return "$2a$04$placeholder", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySeed(ctx, data); err != nil {
		t.Fatal(err)
	}
	if err := s.BulkInsertProducts(ctx, seed.Catalog(5000, 3), 500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, "ANALYZE TABLE products, categories, merchants"); err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{"量子纠缠", "游戏鼠标 轻量化", "K8"} {
		hint, from, _, where, args, ok := visibleSearch(store.ProductSearch{Keyword: kw})
		if !ok {
			t.Fatalf("%q: no terms", kw)
		}
		for name, sql := range map[string]string{
			"count": `SELECT ` + hint + `COUNT(*)` + from + where,
			"list":  `SELECT ` + hint + `p.product_id` + from + where + ` ORDER BY p.sort_order, p.product_id LIMIT 12`,
		} {
			var plan string
			if err := s.DB().QueryRowContext(ctx, "EXPLAIN FORMAT=TREE "+sql, args...).Scan(&plan); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan, "Table scan on hit") || strings.Contains(plan, "lookup on hit") {
				t.Errorf("%q %s: hit set is not the driving table:\n%s", kw, name, plan)
			}
		}
	}
	// 不带关键词、不按分类的计数：不连分类表，只扫 (status, merchant_id) 覆盖索引
	hint, _, countFrom, where, args, _ := visibleSearch(store.ProductSearch{})
	var plan string
	if err := s.DB().QueryRowContext(ctx, "EXPLAIN FORMAT=TREE SELECT "+hint+"COUNT(*)"+countFrom+where, args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "Covering index lookup on p using idx_products_status_merchant") || strings.Contains(plan, "categories") {
		t.Errorf("plain count plan:\n%s", plan)
	}
	// 按分类过滤时仍要连分类表（父分类）
	if _, _, countFrom, _, _, _ := visibleSearch(store.ProductSearch{CategoryID: "c_digital"}); !strings.Contains(countFrom, "categories") {
		t.Errorf("category count must join categories: %s", countFrom)
	}
}
