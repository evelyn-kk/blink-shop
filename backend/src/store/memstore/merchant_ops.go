package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ---------- 促销管理 ----------

func (s *Store) CreatePromotion(ctx context.Context, p domain.PromotionRule) (domain.PromotionRule, error) {
	defer s.lock(ctx)()
	if p.PromotionID == "" {
		p.PromotionID = domain.NewID(domain.PrefixPromotion)
	}
	if _, ok := s.data.promotions[p.PromotionID]; ok {
		return domain.PromotionRule{}, &store.ConflictError{Key: store.KeyPrimary}
	}
	now := s.timestamp()
	p.StartAt, p.EndAt = p.StartAt.UTC().Truncate(time.Millisecond), p.EndAt.UTC().Truncate(time.Millisecond)
	p.CreatedAt, p.UpdatedAt = now, now
	if err := store.ValidatePromotion(p); err != nil {
		return domain.PromotionRule{}, err
	}
	s.data.promotions[p.PromotionID] = p
	return p, nil
}

func (s *Store) GetPromotion(ctx context.Context, promotionID string) (domain.PromotionRule, error) {
	defer s.lock(ctx)()
	p, ok := s.data.promotions[promotionID]
	if !ok {
		return domain.PromotionRule{}, store.ErrNotFound
	}
	return p, nil
}

func (s *Store) UpdatePromotion(ctx context.Context, promotionID string, fn func(ctx context.Context, p *domain.PromotionRule) error) (domain.PromotionRule, error) {
	var out domain.PromotionRule
	err := s.WithTx(ctx, func(ctx context.Context) error {
		before, ok := s.data.promotions[promotionID]
		if !ok {
			return store.ErrNotFound
		}
		p := before
		if err := fn(ctx, &p); err != nil {
			return err
		}
		p.PromotionID, p.CreatedAt, p.UpdatedAt = before.PromotionID, before.CreatedAt, s.timestamp()
		p.StartAt, p.EndAt = p.StartAt.UTC().Truncate(time.Millisecond), p.EndAt.UTC().Truncate(time.Millisecond)
		if err := store.ValidatePromotion(p); err != nil {
			return err
		}
		s.data.promotions[promotionID] = p
		out = p
		return nil
	})
	return out, err
}

func (s *Store) ListPromotions(ctx context.Context, q store.PromotionListQuery) ([]domain.PromotionRule, int, error) {
	defer s.lock(ctx)()
	all := []domain.PromotionRule{}
	for _, p := range s.data.promotions {
		if (q.MerchantID != "" && p.MerchantID != q.MerchantID) || (q.Status != "" && p.Status != q.Status) {
			continue
		}
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].PromotionID > all[j].PromotionID
	})
	return pageOf(all, q.Page), len(all), nil
}

// ---------- 商家评价 ----------

// merchantReview 返回评价及其商品名称、评价人显示名；商品不属于 merchantID 时 ok 为 false。调用方持有锁。
func (s *Store) merchantReview(r domain.ProductReview, merchantID string) (store.MerchantReview, bool) {
	p, ok := s.data.products[r.ProductID]
	if !ok || p.MerchantID != merchantID {
		return store.MerchantReview{}, false
	}
	r.Tags = cloneSlice(r.Tags)
	out := store.MerchantReview{ProductReview: r, ProductName: p.Name}
	if acc, ok := s.data.accounts[r.AccountID]; ok && acc.DeletedAt == nil {
		out.ReviewerName = acc.DisplayName
	}
	return out, true
}

func (s *Store) ListMerchantReviews(ctx context.Context, q store.MerchantReviewQuery) ([]store.MerchantReview, int, error) {
	defer s.lock(ctx)()
	all := []store.MerchantReview{}
	for _, r := range s.data.reviews {
		mr, ok := s.merchantReview(r, q.MerchantID)
		if !ok || (q.Replied != nil && *q.Replied != (r.MerchantRepliedAt != nil)) {
			continue
		}
		all = append(all, mr)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ReviewID > all[j].ReviewID
	})
	return pageOf(all, q.Page), len(all), nil
}

func (s *Store) ReplyReview(ctx context.Context, merchantID, reviewID, reply string, at time.Time) (store.MerchantReview, error) {
	defer s.lock(ctx)()
	r, ok := s.data.reviews[reviewID]
	if !ok {
		return store.MerchantReview{}, store.ErrNotFound
	}
	if _, ok := s.merchantReview(r, merchantID); !ok {
		return store.MerchantReview{}, store.ErrNotFound
	}
	at = at.UTC().Truncate(time.Millisecond)
	r.MerchantReply, r.MerchantRepliedAt, r.UpdatedAt = reply, &at, s.timestamp()
	s.data.reviews[reviewID] = r
	out, _ := s.merchantReview(r, merchantID)
	return out, nil
}
