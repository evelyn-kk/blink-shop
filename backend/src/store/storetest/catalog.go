package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func catalogCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"ListActiveMerchants", testListActiveMerchants},
		{"SearchVisibleProductsFilters", testSearchVisibleProducts},
		{"SearchVisibleProductsPaging", testSearchPaging},
		{"GetVisibleProduct", testGetVisibleProduct},
		{"ListVisibleReviews", testListVisibleReviews},
		{"ListActivePromotions", testListActivePromotions},
	}
}

// catalogFixture 在开发种子之上补充可见性边界数据：停用商家及其 active 商品/促销、隐藏评价、已注销的评价人、
// 未开始和分类级促销。
func catalogFixture(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC)
	from, until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC)
	gone := base.Add(time.Hour)
	extra := store.SeedData{
		Accounts: []store.SeedAccount{{PasswordHash: "h", Account: domain.Account{
			AccountID: "acct_cat_gone", Username: "cat_gone", DisplayName: "已注销的人", Role: domain.RoleUser,
			Status: domain.StatusActive, DeletedAt: &gone,
		}}},
		Merchants: []domain.Merchant{{MerchantID: "m_cat_off", Name: "停用店铺", Status: domain.StatusInactive, CreatedAt: base, UpdatedAt: base}},
		Products: []domain.Product{{
			ProductID: "p_cat_off_shop", MerchantID: "m_cat_off", CategoryID: "c_mouse", Name: "停用店铺的鼠标",
			Price: domain.MustMoney("10"), Status: domain.ProductActive, StockQuantity: 5, StockStatus: domain.StockLow,
			SortOrder: 1, CreatedAt: base, UpdatedAt: base,
		}},
		Reviews: []domain.ProductReview{
			{ReviewID: "rv_cat_hidden", OrderID: "o_x", OrderItemID: "oi_cat_hidden", ProductID: "p_seed_mouse", AccountID: seed.User2ID,
				Rating: 1, Content: "被隐藏", Status: domain.ReviewHidden, CreatedAt: base.Add(10 * time.Hour), UpdatedAt: base},
			{ReviewID: "rv_cat_gone", OrderID: "o_y", OrderItemID: "oi_cat_gone", ProductID: "p_seed_mouse", AccountID: "acct_cat_gone",
				Rating: 4, Content: "注销用户的评价", Tags: []string{}, Status: domain.ReviewVisible, CreatedAt: base.Add(5 * time.Hour), UpdatedAt: base},
		},
		Promotions: []domain.PromotionRule{
			{PromotionID: "promo_cat_future", Name: "未开始", Scope: domain.ScopePlatform, Type: domain.PromotionFullReduction,
				ThresholdAmount: domain.MustMoney("100"), DiscountAmount: domain.MustMoney("5"),
				StartAt: until, EndAt: until.Add(time.Hour), Status: domain.StatusActive, CreatedAt: base.Add(3 * time.Hour), UpdatedAt: base},
			{PromotionID: "promo_cat_off_shop", Name: "停用店铺促销", Scope: domain.ScopeMerchant, MerchantID: "m_cat_off", Type: domain.PromotionFullReduction,
				ThresholdAmount: domain.MustMoney("100"), DiscountAmount: domain.MustMoney("5"),
				StartAt: from, EndAt: until, Status: domain.StatusActive, CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base},
			{PromotionID: "promo_cat_audio", Name: "耳机音箱 9 折", Scope: domain.ScopeCategory, CategoryID: "c_audio", Type: domain.PromotionDiscount,
				DiscountRate: domain.MustRate("0.9"), StartAt: from, EndAt: until, Status: domain.StatusActive, CreatedAt: base.Add(time.Hour), UpdatedAt: base},
		},
	}
	if _, err := s.ApplySeed(ctx, extra); err != nil {
		t.Fatal(err)
	}
}

func page(n, size int) store.Page { return store.Page{Page: n, PageSize: size} }

func productIDs(items []store.CatalogProduct) []string {
	out := []string{}
	for _, p := range items {
		out = append(out, p.ProductID)
	}
	return out
}

func testListActiveMerchants(t *testing.T, s store.Store) {
	catalogFixture(t, s)
	ctx := context.Background()
	all, total, err := s.ListActiveMerchants(ctx, page(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(all) != 2 || all[0].MerchantID != seed.DigitalMerchant || all[1].MerchantID != seed.HomeMerchant {
		t.Fatalf("merchants = %+v, total %d", all, total)
	}
	second, total, _ := s.ListActiveMerchants(ctx, page(2, 1))
	if total != 2 || len(second) != 1 || second[0].MerchantID != seed.HomeMerchant {
		t.Fatalf("page 2: %+v", second)
	}
	beyond, total, _ := s.ListActiveMerchants(ctx, page(3, 1))
	if total != 2 || beyond == nil || len(beyond) != 0 {
		t.Fatalf("page 3: %#v total %d", beyond, total)
	}
}

func testSearchVisibleProducts(t *testing.T, s store.Store) {
	catalogFixture(t, s)
	ctx := context.Background()
	allVisible := []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}
	cases := []struct {
		name, keyword, category string
		want                    []string
	}{
		// 下架、风控、已删除、停用商家的商品都不出现；无货但上架的键盘出现。
		{"all", "", "", allVisible},
		{"leaf category", "", "c_phone", []string{"p_seed_nova", "p_seed_vista"}},
		{"parent category", "", "c_digital", []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds"}},
		{"unknown category", "", "c_missing", []string{}},
		{"name", "耳机", "", []string{"p_seed_earbuds"}},
		{"case insensitive", "NOVA", "", []string{"p_seed_nova"}},
		{"han ngram", "降噪耳机", "", []string{"p_seed_earbuds"}},
		{"tag", "静音", "", []string{"p_seed_mouse"}},
		{"selling point", "热插拔", "", []string{"p_seed_keyboard"}},
		{"category name", "台灯照明", "", []string{"p_seed_lamp"}},
		{"brand", "HOME", "", []string{"p_seed_lamp"}},
		{"any term matches", "blink home", "", allVisible},
		{"keyword and category", "blink", "c_audio", []string{"p_seed_earbuds"}},
		{"no match", "不存在的东西", "", []string{}},
		{"single char", "a", "", []string{}},
		{"percent is literal", "%%", "", []string{}},
		{"underscore is literal", "__", "", []string{}},
		{"json syntax", `","`, "", []string{}},
		{"hidden product by name", "移动电源", "", []string{}},
		{"inactive merchant product", "停用店铺", "", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items, total, err := s.SearchVisibleProducts(ctx, store.ProductSearch{Keyword: c.keyword, CategoryID: c.category, Page: page(1, 100)})
			if err != nil {
				t.Fatal(err)
			}
			if got := productIDs(items); !reflect.DeepEqual(got, c.want) || total != len(c.want) {
				t.Fatalf("got %v (total %d), want %v", got, total, c.want)
			}
		})
	}
	items, _, _ := s.SearchVisibleProducts(ctx, store.ProductSearch{Keyword: "nova", Page: page(1, 10)})
	if len(items) != 1 || items[0].MerchantName != "Blink 数码旗舰店" || len(items[0].SKUs) != 2 ||
		!items[0].SKUs[0].IsDefault || len(items[0].Tags) == 0 || len(items[0].Attributes) == 0 {
		t.Fatalf("search item not fully loaded: %+v", items)
	}
}

func testSearchPaging(t *testing.T, s store.Store) {
	catalogFixture(t, s)
	ctx := context.Background()
	for _, c := range []struct {
		page, size int
		want       []string
	}{
		{1, 2, []string{"p_seed_nova", "p_seed_vista"}},
		{2, 2, []string{"p_seed_earbuds", "p_seed_mouse"}},
		{3, 2, []string{"p_seed_keyboard", "p_seed_lamp"}},
		{4, 2, []string{}},
		{2, 5, []string{"p_seed_lamp"}},
		{1, 100, []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}},
	} {
		items, total, err := s.SearchVisibleProducts(ctx, store.ProductSearch{Page: page(c.page, c.size)})
		if err != nil {
			t.Fatal(err)
		}
		if got := productIDs(items); !reflect.DeepEqual(got, c.want) || total != 6 {
			t.Errorf("page %d size %d: got %v total %d, want %v", c.page, c.size, got, total, c.want)
		}
	}
}

func testGetVisibleProduct(t *testing.T, s store.Store) {
	catalogFixture(t, s)
	ctx := context.Background()
	got, err := s.GetVisibleProduct(ctx, "p_seed_nova")
	if err != nil {
		t.Fatal(err)
	}
	if got.MerchantName != "Blink 数码旗舰店" || len(got.SKUs) != 2 || got.SKUs[0].SkuID != "sku_seed_nova_128" ||
		len(got.SuitableFor) == 0 || len(got.NotSuitableFor) == 0 || len(got.ImageURLs) != 2 {
		t.Fatalf("detail: %+v", got)
	}
	// 与内部 GetProduct 读到的商品一致。
	internal, _ := s.GetProduct(ctx, "p_seed_nova")
	if !reflect.DeepEqual(got.Product, internal) {
		t.Fatalf("GetVisibleProduct 与 GetProduct 不一致:\n%+v\n%+v", got.Product, internal)
	}
	for _, id := range []string{"p_seed_speaker", "p_seed_powerbank", "p_seed_legacy", "p_cat_off_shop", "p_missing"} {
		if _, err := s.GetVisibleProduct(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", id, err)
		}
	}
	// 内部查询仍能读到不可见商品（订单快照、商家后台使用）。
	if _, err := s.GetProduct(ctx, "p_seed_legacy"); err != nil {
		t.Errorf("GetProduct(deleted) = %v", err)
	}
}

func testListVisibleReviews(t *testing.T, s store.Store) {
	catalogFixture(t, s)
	ctx := context.Background()
	items, total, err := s.ListVisibleReviews(ctx, "p_seed_mouse", page(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	// 隐藏评价不出现；按时间倒序；已注销评价人的名字为空。
	if total != 2 || len(items) != 2 || items[0].ReviewID != "rv_seed_mouse" || items[1].ReviewID != "rv_cat_gone" {
		t.Fatalf("reviews = %+v total %d", items, total)
	}
	if items[0].ReviewerName != "演示用户" || items[1].ReviewerName != "" {
		t.Fatalf("reviewer names = %q, %q", items[0].ReviewerName, items[1].ReviewerName)
	}
	if items[0].MerchantReply == "" || items[0].MerchantRepliedAt == nil || !reflect.DeepEqual(items[0].Tags, []string{"静音", "手感好"}) {
		t.Fatalf("review fields: %+v", items[0])
	}
	if items[1].Tags == nil || items[1].MerchantRepliedAt != nil {
		t.Fatalf("empty fields: %+v", items[1])
	}
	second, total, _ := s.ListVisibleReviews(ctx, "p_seed_mouse", page(2, 1))
	if total != 2 || len(second) != 1 || second[0].ReviewID != "rv_cat_gone" {
		t.Fatalf("page 2: %+v", second)
	}
	none, total, _ := s.ListVisibleReviews(ctx, "p_seed_lamp", page(1, 10))
	if total != 0 || none == nil || len(none) != 0 {
		t.Fatalf("no reviews: %#v", none)
	}
}

func testListActivePromotions(t *testing.T, s store.Store) {
	catalogFixture(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ids := func(items []domain.PromotionRule) []string {
		out := []string{}
		for _, p := range items {
			out = append(out, p.PromotionID)
		}
		return out
	}
	// 种子促销 created_at 相同，按 promotion_id 倒序；已结束、未开始、停用商家的促销都不出现。
	all, total, err := s.ListActivePromotions(ctx, store.PromotionQuery{At: at, Page: page(1, 10)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"promo_cat_audio", "promo_seed_platform", "promo_seed_earbuds", "promo_seed_digital"}
	if got := ids(all); !reflect.DeepEqual(got, want) || total != 4 {
		t.Fatalf("all = %v total %d, want %v", got, total, want)
	}
	if all[0].DiscountRate != domain.MustRate("0.9") || !all[0].EndAt.Equal(time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("promotion fields: %+v", all[0])
	}

	earbuds := store.PromotionQuery{At: at, ProductID: "p_seed_earbuds", MerchantID: seed.DigitalMerchant,
		CategoryIDs: []string{"c_audio", "c_digital"}, Page: page(1, 10)}
	got, total, _ := s.ListActivePromotions(ctx, earbuds)
	if want := []string{"promo_cat_audio", "promo_seed_platform", "promo_seed_earbuds", "promo_seed_digital"}; !reflect.DeepEqual(ids(got), want) || total != 4 {
		t.Fatalf("earbuds = %v", ids(got))
	}
	lamp := store.PromotionQuery{At: at, ProductID: "p_seed_lamp", MerchantID: seed.HomeMerchant, CategoryIDs: []string{"c_lamp", "c_home"}, Page: page(1, 10)}
	got, _, _ = s.ListActivePromotions(ctx, lamp)
	if want := []string{"promo_seed_platform"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("lamp = %v", ids(got))
	}
	// 时间边界：start_at 当刻有效，end_at 当刻失效。
	future := store.PromotionQuery{At: time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC), Page: page(1, 10)}
	got, _, _ = s.ListActivePromotions(ctx, future)
	if want := []string{"promo_cat_future", "promo_seed_platform", "promo_seed_earbuds", "promo_seed_digital"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("at boundary = %v", ids(got))
	}
	paged, total, _ := s.ListActivePromotions(ctx, store.PromotionQuery{At: at, Page: page(2, 3)})
	if want := []string{"promo_seed_digital"}; !reflect.DeepEqual(ids(paged), want) || total != 4 {
		t.Fatalf("paged = %v total %d", ids(paged), total)
	}
}
