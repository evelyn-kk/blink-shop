package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func merchantOpsCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"PromotionCreateUpdateList", testPromotionCRUD},
		{"PromotionValidation", testPromotionValidation},
		{"MerchantReviewsAndReply", testMerchantReviews},
	}
}

func shopPromotion(name string) domain.PromotionRule {
	return domain.PromotionRule{Name: name, Scope: domain.ScopeMerchant, MerchantID: seed.HomeMerchant, Type: domain.PromotionFullReduction,
		ThresholdAmount: domain.MustMoney("100"), DiscountAmount: domain.MustMoney("10"), Stackable: true,
		StartAt: couponAt.Add(-time.Hour), EndAt: couponAt.Add(24 * time.Hour), Status: domain.StatusActive}
}

func testPromotionCRUD(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	created, err := s.CreatePromotion(ctx, shopPromotion("家居满 100 减 10"))
	if err != nil || created.PromotionID == "" || created.CreatedAt.IsZero() || !created.StartAt.Equal(couponAt.Add(-time.Hour)) {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if got, err := s.GetPromotion(ctx, created.PromotionID); err != nil || got.Name != created.Name || got.DiscountAmount != created.DiscountAmount ||
		!got.EndAt.Equal(created.EndAt) || got.Stackable != true {
		t.Fatalf("get = %+v, %v", got, err)
	}
	// 生效中的促销进入计价用的查询。
	active := func() bool {
		list, _, _ := s.ListActivePromotions(ctx, store.PromotionQuery{At: couponAt, Page: store.Page{Page: 1, PageSize: 100}})
		for _, p := range list {
			if p.PromotionID == created.PromotionID {
				return true
			}
		}
		return false
	}
	if !active() {
		t.Fatal("new promotion not active")
	}

	boom := errors.New("boom")
	if _, err := s.UpdatePromotion(ctx, created.PromotionID, func(_ context.Context, p *domain.PromotionRule) error { p.Name = "x"; return boom }); !errors.Is(err, boom) {
		t.Fatalf("fn error = %v", err)
	}
	if _, err := s.UpdatePromotion(ctx, created.PromotionID, func(_ context.Context, p *domain.PromotionRule) error { p.EndAt = p.StartAt; return nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("invalid update = %v", err)
	}
	updated, err := s.UpdatePromotion(ctx, created.PromotionID, func(_ context.Context, p *domain.PromotionRule) error {
		p.PromotionID, p.CreatedAt = "promo_other", time.Time{}
		p.Status, p.DiscountAmount = domain.StatusInactive, domain.MustMoney("15")
		return nil
	})
	if err != nil || updated.PromotionID != created.PromotionID || !updated.CreatedAt.Equal(created.CreatedAt) || updated.Status != domain.StatusInactive ||
		updated.DiscountAmount.String() != "15.00" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if got, _ := s.GetPromotion(ctx, created.PromotionID); got.Name != created.Name || got.Status != domain.StatusInactive {
		t.Fatalf("stored = %+v", got)
	}
	if active() {
		t.Fatal("inactive promotion still active")
	}
	if _, err := s.UpdatePromotion(ctx, "promo_missing", func(context.Context, *domain.PromotionRule) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := s.GetPromotion(ctx, "promo_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing = %v", err)
	}

	time.Sleep(2 * time.Millisecond)
	second, _ := s.CreatePromotion(ctx, shopPromotion("家居第二个"))
	page := store.Page{Page: 1, PageSize: 10}
	for _, c := range []struct {
		q    store.PromotionListQuery
		want []string
	}{
		{store.PromotionListQuery{MerchantID: seed.HomeMerchant, Page: page}, []string{second.PromotionID, created.PromotionID}},
		{store.PromotionListQuery{MerchantID: seed.HomeMerchant, Status: domain.StatusInactive, Page: page}, []string{created.PromotionID}},
		{store.PromotionListQuery{MerchantID: seed.HomeMerchant, Page: store.Page{Page: 2, PageSize: 1}}, []string{created.PromotionID}},
	} {
		got, total, err := s.ListPromotions(ctx, c.q)
		var ids []string
		for _, p := range got {
			ids = append(ids, p.PromotionID)
		}
		if err != nil || len(ids) != len(c.want) || (len(ids) > 0 && ids[0] != c.want[0]) {
			t.Fatalf("ListPromotions(%+v) = %v (total %d), %v", c.q, ids, total, err)
		}
	}
	if _, total, _ := s.ListPromotions(ctx, store.PromotionListQuery{MerchantID: seed.DigitalMerchant, Page: page}); total != 2 {
		t.Fatalf("digital promotions = %d", total) // 种子：店铺满减 + 耳机折扣
	}
	if _, total, _ := s.ListPromotions(ctx, store.PromotionListQuery{Page: page}); total != 4+2 { // 种子 4 个 + 新建 2 个
		t.Fatalf("all promotions = %d", total)
	}
}

func testPromotionValidation(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	cases := map[string]func(p *domain.PromotionRule){
		"empty name":          func(p *domain.PromotionRule) { p.Name = "" },
		"bad type":            func(p *domain.PromotionRule) { p.Type = "percentage" },
		"bad status":          func(p *domain.PromotionRule) { p.Status = "deleted" },
		"end before start":    func(p *domain.PromotionRule) { p.EndAt = p.StartAt.Add(-time.Second) },
		"end equals start":    func(p *domain.PromotionRule) { p.EndAt = p.StartAt },
		"negative amount":     func(p *domain.PromotionRule) { p.ThresholdAmount = -1 },
		"rate above one":      func(p *domain.PromotionRule) { p.DiscountRate = domain.Rate(15000) },
		"merchant without id": func(p *domain.PromotionRule) { p.MerchantID = "" },
		"merchant with sku":   func(p *domain.PromotionRule) { p.ProductID = "p_seed_lamp" },
		"product without id":  func(p *domain.PromotionRule) { p.Scope = domain.ScopeProduct },
		"category without id": func(p *domain.PromotionRule) { p.Scope = domain.ScopeCategory },
		"platform with shop":  func(p *domain.PromotionRule) { p.Scope = domain.ScopePlatform },
		"unknown scope":       func(p *domain.PromotionRule) { p.Scope = "global" },
		"category and product": func(p *domain.PromotionRule) {
			p.Scope, p.CategoryID, p.ProductID = domain.ScopeCategory, "c_lamp", "p_seed_lamp"
		},
	}
	for name, mutate := range cases {
		p := shopPromotion(name)
		mutate(&p)
		if _, err := s.CreatePromotion(ctx, p); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, total, _ := s.ListPromotions(ctx, store.PromotionListQuery{MerchantID: seed.HomeMerchant, Page: store.Page{Page: 1, PageSize: 10}}); total != 0 {
		t.Fatalf("invalid promotions written: %d", total)
	}
	for _, p := range []domain.PromotionRule{
		func() domain.PromotionRule {
			p := shopPromotion("单品")
			p.Scope, p.ProductID = domain.ScopeProduct, "p_seed_lamp"
			return p
		}(),
		func() domain.PromotionRule {
			p := shopPromotion("品类")
			p.Scope, p.CategoryID = domain.ScopeCategory, "c_lamp"
			return p
		}(),
		func() domain.PromotionRule {
			p := shopPromotion("折扣")
			p.Type, p.DiscountAmount, p.DiscountRate = domain.PromotionDiscount, 0, domain.MustRate("0.9")
			return p
		}(),
	} {
		if _, err := s.CreatePromotion(ctx, p); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
	}
}

func testMerchantReviews(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	// 再加一条隐藏的、未回复的评价（已完成订单的第二件商品）。
	hidden := domain.ProductReview{ReviewID: "rv_hidden", OrderID: "o_seed_completed", OrderItemID: "o_seed_completed_item2", ProductID: "p_seed_legacy",
		SkuID: "sku_seed_legacy", AccountID: seed.UserID, Rating: 2, Content: "一般", Tags: []string{"偏重"}, Status: domain.ReviewHidden,
		CreatedAt: couponAt, UpdatedAt: couponAt}
	if _, err := s.ApplySeed(ctx, store.SeedData{Reviews: []domain.ProductReview{hidden}}); err != nil {
		t.Fatal(err)
	}
	page := store.Page{Page: 1, PageSize: 10}
	list, total, err := s.ListMerchantReviews(ctx, store.MerchantReviewQuery{MerchantID: seed.DigitalMerchant, Page: page})
	if err != nil || total != 2 || list[0].ReviewID != "rv_hidden" || list[0].ProductName != "Blink Nova 9（停产）" || list[0].ReviewerName == "" ||
		list[0].Status != domain.ReviewHidden || len(list[0].Tags) != 1 || list[1].ReviewID != "rv_seed_mouse" || list[1].MerchantReply == "" {
		t.Fatalf("list = %+v (total %d), %v", list, total, err)
	}
	no, yes := false, true
	for _, c := range []struct {
		replied *bool
		want    string
	}{{&no, "rv_hidden"}, {&yes, "rv_seed_mouse"}} {
		got, total, _ := s.ListMerchantReviews(ctx, store.MerchantReviewQuery{MerchantID: seed.DigitalMerchant, Replied: c.replied, Page: page})
		if total != 1 || got[0].ReviewID != c.want {
			t.Fatalf("replied=%v: %+v", *c.replied, got)
		}
	}
	if _, total, _ := s.ListMerchantReviews(ctx, store.MerchantReviewQuery{MerchantID: seed.HomeMerchant, Page: page}); total != 0 {
		t.Fatalf("home merchant sees %d reviews", total)
	}

	at := couponAt.Add(time.Hour)
	if _, err := s.ReplyReview(ctx, seed.HomeMerchant, "rv_hidden", "谢谢", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other merchant reply = %v", err)
	}
	if _, err := s.ReplyReview(ctx, seed.DigitalMerchant, "rv_missing", "谢谢", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing review = %v", err)
	}
	got, err := s.ReplyReview(ctx, seed.DigitalMerchant, "rv_hidden", "已安排客服联系您", at)
	if err != nil || got.MerchantReply != "已安排客服联系您" || got.MerchantRepliedAt == nil || !got.MerchantRepliedAt.Equal(at) || got.ProductName == "" {
		t.Fatalf("reply = %+v, %v", got, err)
	}
	// 再次回复覆盖原来的内容。
	later := at.Add(time.Minute)
	if got, err := s.ReplyReview(ctx, seed.DigitalMerchant, "rv_hidden", "补充：已退换", later); err != nil || got.MerchantReply != "补充：已退换" || !got.MerchantRepliedAt.Equal(later) {
		t.Fatalf("overwrite = %+v, %v", got, err)
	}
	if _, total, _ := s.ListMerchantReviews(ctx, store.MerchantReviewQuery{MerchantID: seed.DigitalMerchant, Replied: &no, Page: page}); total != 0 {
		t.Fatalf("unreplied after reply = %d", total)
	}
	// 回复不改变评价的可见性。
	if public, _, _ := s.ListVisibleReviews(ctx, "p_seed_legacy", page); len(public) != 0 {
		t.Fatalf("hidden review became public: %+v", public)
	}
}
