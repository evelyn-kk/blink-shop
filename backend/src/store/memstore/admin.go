package memstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func newestFirst(ai, aj time.Time, idi, idj string) bool {
	if !ai.Equal(aj) {
		return ai.After(aj)
	}
	return idi > idj
}

func (s *Store) ListAccounts(ctx context.Context, q store.AccountQuery) ([]domain.Account, int, error) {
	defer s.lock(ctx)()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	all := []domain.Account{}
	for _, row := range s.data.accounts {
		a := row.Account
		if a.DeletedAt != nil || (q.Role != "" && a.Role != q.Role) || (q.Status != "" && a.Status != q.Status) {
			continue
		}
		if kw != "" && !containsFold(a.Username, kw) && !containsFold(a.DisplayName, kw) {
			continue
		}
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool {
		return newestFirst(all[i].CreatedAt, all[j].CreatedAt, all[i].AccountID, all[j].AccountID)
	})
	return pageOf(all, q.Page), len(all), nil
}

func (s *Store) UpdateAccountStatus(ctx context.Context, accountID string, fn func(a *domain.Account) error) (domain.Account, error) {
	defer s.lock(ctx)()
	row, ok := s.data.accounts[accountID]
	if !ok || row.DeletedAt != nil {
		return domain.Account{}, store.ErrNotFound
	}
	next := row.Account
	if err := fn(&next); err != nil {
		return domain.Account{}, err
	}
	if err := store.CheckEntityTransition(row.Status, next.Status); err != nil {
		return domain.Account{}, err
	}
	row.Status, row.UpdatedAt = next.Status, s.timestamp()
	s.data.accounts[accountID] = row
	return row.Account, nil
}

func (s *Store) ListMerchants(ctx context.Context, q store.MerchantQuery) ([]domain.Merchant, int, error) {
	defer s.lock(ctx)()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	all := []domain.Merchant{}
	for _, m := range s.data.merchants {
		if (q.Status != "" && m.Status != q.Status) || (kw != "" && !containsFold(m.Name, kw)) {
			continue
		}
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool {
		return newestFirst(all[i].CreatedAt, all[j].CreatedAt, all[i].MerchantID, all[j].MerchantID)
	})
	return pageOf(all, q.Page), len(all), nil
}

func (s *Store) UpdateMerchantStatus(ctx context.Context, merchantID string, fn func(m *domain.Merchant) error) (domain.Merchant, error) {
	defer s.lock(ctx)()
	m, ok := s.data.merchants[merchantID]
	if !ok {
		return domain.Merchant{}, store.ErrNotFound
	}
	next := m
	if err := fn(&next); err != nil {
		return domain.Merchant{}, err
	}
	if err := store.CheckEntityTransition(m.Status, next.Status); err != nil {
		return domain.Merchant{}, err
	}
	m.Status, m.UpdatedAt = next.Status, s.timestamp()
	s.data.merchants[merchantID] = m
	return m, nil
}

func (s *Store) ListAllProducts(ctx context.Context, q store.AdminProductQuery) ([]store.CatalogProduct, int, error) {
	defer s.lock(ctx)()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	all := []store.CatalogProduct{}
	for _, p := range s.data.products {
		if (q.MerchantID != "" && p.MerchantID != q.MerchantID) || (q.Status != "" && p.Status != q.Status) || (kw != "" && !containsFold(p.Name, kw)) {
			continue
		}
		cp := cloneProduct(p)
		cp.SKUs = nil
		all = append(all, store.CatalogProduct{Product: cp, MerchantName: s.data.merchants[p.MerchantID].Name})
	}
	sort.Slice(all, func(i, j int) bool {
		return newestFirst(all[i].UpdatedAt, all[j].UpdatedAt, all[i].ProductID, all[j].ProductID)
	})
	return pageOf(all, q.Page), len(all), nil
}

// reviewDetail 返回评价及商品名称、评价人显示名。调用方持有锁。
func (s *Store) reviewDetail(r domain.ProductReview) store.MerchantReview {
	r.Tags = cloneSlice(r.Tags)
	out := store.MerchantReview{ProductReview: r, ProductName: s.data.products[r.ProductID].Name}
	if acc, ok := s.data.accounts[r.AccountID]; ok && acc.DeletedAt == nil {
		out.ReviewerName = acc.DisplayName
	}
	return out
}

func (s *Store) ListAllReviews(ctx context.Context, q store.AdminReviewQuery) ([]store.MerchantReview, int, error) {
	defer s.lock(ctx)()
	all := []store.MerchantReview{}
	for _, r := range s.data.reviews {
		if _, ok := s.data.products[r.ProductID]; !ok || (q.Status != "" && r.Status != q.Status) || (q.ProductID != "" && r.ProductID != q.ProductID) {
			continue
		}
		all = append(all, s.reviewDetail(r))
	}
	sort.Slice(all, func(i, j int) bool {
		return newestFirst(all[i].CreatedAt, all[j].CreatedAt, all[i].ReviewID, all[j].ReviewID)
	})
	return pageOf(all, q.Page), len(all), nil
}

func (s *Store) UpdateReviewStatus(ctx context.Context, reviewID string, fn func(r *domain.ProductReview) error) (store.MerchantReview, error) {
	defer s.lock(ctx)()
	r, ok := s.data.reviews[reviewID]
	if !ok {
		return store.MerchantReview{}, store.ErrNotFound
	}
	next := r
	if err := fn(&next); err != nil {
		return store.MerchantReview{}, err
	}
	if err := store.CheckReviewTransition(r.Status, next.Status); err != nil {
		return store.MerchantReview{}, err
	}
	r.Status, r.UpdatedAt = next.Status, s.timestamp()
	s.data.reviews[reviewID] = r
	return s.reviewDetail(r), nil
}

func (s *Store) CountStatuses(ctx context.Context) (store.StatusOverview, error) {
	defer s.lock(ctx)()
	out := store.StatusOverview{Accounts: map[string]int{}, Merchants: map[string]int{}, Products: map[string]int{}}
	for _, a := range s.data.accounts {
		if a.DeletedAt == nil {
			out.Accounts[string(a.Status)]++
		}
	}
	for _, m := range s.data.merchants {
		out.Merchants[string(m.Status)]++
	}
	for _, p := range s.data.products {
		if p.Status != domain.ProductDeleted {
			out.Products[string(p.Status)]++
		}
	}
	return out, nil
}

func (s *Store) InsertAuditLog(ctx context.Context, l store.AuditLog) (store.AuditLog, error) {
	defer s.lock(ctx)()
	if l.AuditID == "" {
		l.AuditID = domain.NewID(domain.PrefixAudit)
	}
	if _, ok := s.data.audits[l.AuditID]; ok {
		return store.AuditLog{}, &store.ConflictError{Key: store.KeyPrimary}
	}
	l.CreatedAt = s.orNow(l.CreatedAt)
	s.data.audits[l.AuditID] = l
	s.data.auditSeq[l.AuditID] = int64(len(s.data.auditSeq)) + 1
	return l, nil
}

func (s *Store) ListAuditLogs(ctx context.Context, q store.AuditQuery) ([]store.AuditLog, int, error) {
	defer s.lock(ctx)()
	all := []store.AuditLog{}
	for _, l := range s.data.audits {
		if (q.TargetType != "" && l.TargetType != q.TargetType) || (q.TargetID != "" && l.TargetID != q.TargetID) || (q.OperatorID != "" && l.OperatorID != q.OperatorID) {
			continue
		}
		all = append(all, l)
	}
	sort.Slice(all, func(i, j int) bool { return s.data.auditSeq[all[i].AuditID] > s.data.auditSeq[all[j].AuditID] })
	return pageOf(all, q.Page), len(all), nil
}
