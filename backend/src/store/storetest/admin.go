package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func adminCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"AdminAccounts", testAdminAccounts},
		{"AdminMerchantsAndProducts", testAdminMerchantsProducts},
		{"AdminReviews", testAdminReviews},
		{"AdminStatusOverview", testStatusOverview},
		{"AuditLogsWithTx", testAuditLogs},
	}
}

var page100 = store.Page{Page: 1, PageSize: 100}

func accountIDs(list []domain.Account) []string {
	var out []string
	for _, a := range list {
		out = append(out, a.AccountID)
	}
	return out
}

func testAdminAccounts(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	odd, err := s.CreateAccount(ctx, store.NewAccount{Username: "Percent%_Under", PasswordHash: "h", DisplayName: "特殊 字符", Role: domain.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	gone, _ := s.CreateAccount(ctx, newUser("gone_user"))
	if err := s.SoftDeleteAccount(ctx, gone.AccountID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		q    store.AccountQuery
		want int
	}{
		{store.AccountQuery{Page: page100}, 6},
		{store.AccountQuery{Role: domain.RoleMerchant, Page: page100}, 2},
		{store.AccountQuery{Role: domain.RoleAdmin, Page: page100}, 1},
		{store.AccountQuery{Keyword: "BLINK_USER", Page: page100}, 2}, // 不区分大小写，blink_user 与 blink_user2
		{store.AccountQuery{Keyword: "%_", Page: page100}, 1},         // % 和 _ 按字面匹配
		{store.AccountQuery{Keyword: "特殊", Page: page100}, 1},
		{store.AccountQuery{Keyword: "gone", Page: page100}, 0},
		{store.AccountQuery{Status: domain.StatusRisk, Page: page100}, 0},
	} {
		got, total, err := s.ListAccounts(ctx, c.q)
		if err != nil || total != c.want || len(got) != c.want {
			t.Fatalf("ListAccounts(%+v) = %v (total %d), %v", c.q, accountIDs(got), total, err)
		}
	}
	if got, _, _ := s.ListAccounts(ctx, store.AccountQuery{Page: store.Page{Page: 1, PageSize: 1}}); len(got) != 1 || got[0].AccountID != odd.AccountID {
		t.Fatalf("newest first = %v", accountIDs(got))
	}

	boom := errors.New("boom")
	if _, err := s.UpdateAccountStatus(ctx, seed.UserID, func(a *domain.Account) error { a.Status = domain.StatusRisk; return boom }); !errors.Is(err, boom) {
		t.Fatalf("fn error = %v", err)
	}
	if _, err := s.UpdateAccountStatus(ctx, seed.UserID, func(a *domain.Account) error { a.Status = "deleted"; return nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad status = %v", err)
	}
	for _, next := range []domain.EntityStatus{domain.StatusRisk, domain.StatusInactive, domain.StatusActive} {
		got, err := s.UpdateAccountStatus(ctx, seed.UserID, func(a *domain.Account) error {
			a.Status, a.Username, a.Role = next, "renamed", domain.RoleAdmin // 只有状态会写入
			return nil
		})
		if err != nil || got.Status != next || got.Username != seed.UserUsername || got.Role != domain.RoleUser {
			t.Fatalf("-> %s = %+v, %v", next, got, err)
		}
		if stored, _ := s.GetAccount(ctx, seed.UserID); stored.Status != next || stored.Username != seed.UserUsername {
			t.Fatalf("stored = %+v", stored)
		}
	}
	if _, err := s.UpdateAccountStatus(ctx, gone.AccountID, func(a *domain.Account) error { a.Status = domain.StatusRisk; return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted account = %v", err)
	}
}

func testAdminMerchantsProducts(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	got, total, err := s.ListMerchants(ctx, store.MerchantQuery{Page: page100})
	if err != nil || total != 2 || len(got) != 2 {
		t.Fatalf("merchants = %+v, %v", got, err)
	}
	if got, _, _ := s.ListMerchants(ctx, store.MerchantQuery{Keyword: "家居", Page: page100}); len(got) != 1 || got[0].MerchantID != seed.HomeMerchant {
		t.Fatalf("keyword = %+v", got)
	}
	m, err := s.UpdateMerchantStatus(ctx, seed.HomeMerchant, func(m *domain.Merchant) error { m.Status, m.Name = domain.StatusRisk, "x"; return nil })
	if err != nil || m.Status != domain.StatusRisk || m.Name == "x" {
		t.Fatalf("merchant -> risk = %+v, %v", m, err)
	}
	// 店铺风控后，它的商品不再公开可见，也不在公开店铺列表里。
	if _, err := s.GetVisibleProduct(ctx, "p_seed_lamp"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lamp still visible: %v", err)
	}
	if list, _, _ := s.ListActiveMerchants(ctx, page100); len(list) != 1 {
		t.Fatalf("active merchants = %+v", list)
	}
	if got, _, _ := s.ListMerchants(ctx, store.MerchantQuery{Status: domain.StatusRisk, Page: page100}); len(got) != 1 {
		t.Fatalf("risk merchants = %+v", got)
	}
	if _, err := s.UpdateMerchantStatus(ctx, "m_missing", func(*domain.Merchant) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing merchant = %v", err)
	}
	if _, err := s.UpdateMerchantStatus(ctx, seed.HomeMerchant, func(m *domain.Merchant) error { m.Status = "closed"; return nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad merchant status = %v", err)
	}

	for _, c := range []struct {
		q    store.AdminProductQuery
		want int
	}{
		{store.AdminProductQuery{Page: page100}, 9}, // 含已删除、下架、风控
		{store.AdminProductQuery{MerchantID: seed.HomeMerchant, Page: page100}, 1},
		{store.AdminProductQuery{Status: domain.ProductDeleted, Page: page100}, 1},
		{store.AdminProductQuery{Status: domain.ProductRisk, Page: page100}, 1},
		{store.AdminProductQuery{Keyword: "blink nova", Page: page100}, 2},
	} {
		got, total, err := s.ListAllProducts(ctx, c.q)
		if err != nil || total != c.want {
			t.Fatalf("ListAllProducts(%+v) = %d, %v", c.q, total, err)
		}
		for _, p := range got {
			if p.MerchantName == "" || len(p.SKUs) != 0 {
				t.Fatalf("product row = %+v", p)
			}
		}
	}
}

func testAdminReviews(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	list, total, err := s.ListAllReviews(ctx, store.AdminReviewQuery{Page: page100})
	if err != nil || total != 1 || list[0].ReviewID != "rv_seed_mouse" || list[0].ProductName == "" || list[0].ReviewerName == "" {
		t.Fatalf("reviews = %+v, %v", list, err)
	}
	if _, err := s.UpdateReviewStatus(ctx, "rv_seed_mouse", func(r *domain.ProductReview) error { r.Status = "deleted"; return nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("deleted status = %v", err)
	}
	got, err := s.UpdateReviewStatus(ctx, "rv_seed_mouse", func(r *domain.ProductReview) error { r.Status, r.Content = domain.ReviewHidden, "改写"; return nil })
	if err != nil || got.Status != domain.ReviewHidden || got.Content == "改写" {
		t.Fatalf("hide = %+v, %v", got, err)
	}
	if public, _, _ := s.ListVisibleReviews(ctx, "p_seed_mouse", page100); len(public) != 0 {
		t.Fatalf("hidden review still public: %+v", public)
	}
	for _, c := range []struct {
		q    store.AdminReviewQuery
		want int
	}{
		{store.AdminReviewQuery{Status: domain.ReviewHidden, Page: page100}, 1},
		{store.AdminReviewQuery{Status: domain.ReviewVisible, Page: page100}, 0},
		{store.AdminReviewQuery{ProductID: "p_seed_lamp", Page: page100}, 0},
	} {
		if _, total, _ := s.ListAllReviews(ctx, c.q); total != c.want {
			t.Fatalf("ListAllReviews(%+v) = %d", c.q, total)
		}
	}
	if _, err := s.UpdateReviewStatus(ctx, "rv_missing", func(*domain.ProductReview) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing review = %v", err)
	}
}

func testStatusOverview(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	if _, err := s.UpdateAccountStatus(ctx, seed.User2ID, func(a *domain.Account) error { a.Status = domain.StatusRisk; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := s.CountStatuses(ctx)
	want := fmt.Sprint(store.StatusOverview{
		Accounts:  map[string]int{"active": 4, "risk": 1},
		Merchants: map[string]int{"active": 2},
		Products:  map[string]int{"active": 6, "inactive": 1, "risk": 1}, // 已删除的不计
	})
	if err != nil || fmt.Sprint(got) != want {
		t.Fatalf("overview = %v, want %v (%v)", got, want, err)
	}
}

func testAuditLogs(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, l := range []store.AuditLog{
		{OperatorID: seed.AdminID, OperatorName: "管理员", Action: "account.status_changed", TargetType: "account", TargetID: seed.UserID,
			BeforeValue: "active", AfterValue: "risk", Reason: "刷单", RequestID: "req1"},
		{OperatorID: seed.AdminID, Action: "merchant.status_changed", TargetType: "merchant", TargetID: seed.HomeMerchant, BeforeValue: "active", AfterValue: "inactive"},
		{OperatorID: "acct_other", Action: "account.status_changed", TargetType: "account", TargetID: seed.UserID, BeforeValue: "risk", AfterValue: "active"},
	} {
		l.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		got, err := s.InsertAuditLog(ctx, l)
		if err != nil || got.AuditID == "" {
			t.Fatalf("insert = %+v, %v", got, err)
		}
	}
	list, total, err := s.ListAuditLogs(ctx, store.AuditQuery{TargetType: "account", TargetID: seed.UserID, Page: page100})
	if err != nil || total != 2 || list[0].OperatorID != "acct_other" || list[1].Reason != "刷单" || list[1].OperatorName != "管理员" ||
		list[1].RequestID != "req1" || !list[1].CreatedAt.Equal(base) {
		t.Fatalf("account audits = %+v, %v", list, err)
	}
	if _, total, _ := s.ListAuditLogs(ctx, store.AuditQuery{OperatorID: seed.AdminID, Page: page100}); total != 2 {
		t.Fatalf("by operator = %d", total)
	}
	if _, total, _ := s.ListAuditLogs(ctx, store.AuditQuery{Page: page100}); total != 3 {
		t.Fatalf("all = %d", total)
	}
	// 同一毫秒写入的多条记录按写入先后排序（不依赖随机 ID）。
	same := base.Add(time.Hour)
	var want []string
	for i := range 5 {
		l, _ := s.InsertAuditLog(ctx, store.AuditLog{OperatorID: seed.AdminID, Action: "x", TargetType: "product", TargetID: fmt.Sprint(i), CreatedAt: same})
		want = append([]string{l.AuditID}, want...)
	}
	got, _, _ := s.ListAuditLogs(ctx, store.AuditQuery{TargetType: "product", Page: page100})
	var ids []string
	for _, l := range got {
		ids = append(ids, l.AuditID)
	}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("same-millisecond order = %v, want %v", ids, want)
	}

	// 状态修改和审计在同一个事务里：任何一步失败，两者都不留下。
	boom := errors.New("boom")
	err = s.WithTx(ctx, func(ctx context.Context) error {
		if _, err := s.UpdateAccountStatus(ctx, seed.User2ID, func(a *domain.Account) error { a.Status = domain.StatusInactive; return nil }); err != nil {
			return err
		}
		if _, err := s.InsertAuditLog(ctx, store.AuditLog{OperatorID: seed.AdminID, Action: "account.status_changed", TargetType: "account", TargetID: seed.User2ID}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("tx = %v", err)
	}
	if acc, _ := s.GetAccount(ctx, seed.User2ID); acc.Status != domain.StatusActive {
		t.Fatalf("status not rolled back: %s", acc.Status)
	}
	if _, total, _ := s.ListAuditLogs(ctx, store.AuditQuery{TargetID: seed.User2ID, Page: page100}); total != 0 {
		t.Fatalf("audit not rolled back: %d", total)
	}
}
