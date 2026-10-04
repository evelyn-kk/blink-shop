package memstore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func (s *Store) ListCartLines(ctx context.Context, accountID string) ([]store.CartLine, error) {
	defer s.lock(ctx)()
	out := []store.CartLine{}
	for _, it := range s.data.cartItems {
		if it.AccountID != accountID {
			continue
		}
		out = append(out, s.lineState(it))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		if out[i].ProductID != out[j].ProductID {
			return out[i].ProductID < out[j].ProductID
		}
		return out[i].SkuID < out[j].SkuID
	})
	return out, nil
}

// lineState 返回购物车项及其商品、店铺、规格的当前状态。调用方持有锁。
func (s *Store) lineState(it domain.CartItem) store.CartLine {
	l := store.CartLine{CartItem: it}
	if p, ok := s.data.products[it.ProductID]; ok {
		l.ProductName, l.ProductStatus, l.ImageURL, l.CategoryID, l.MerchantID = p.Name, p.Status, p.ImageURL, p.CategoryID, p.MerchantID
		if m, ok := s.data.merchants[p.MerchantID]; ok {
			l.MerchantName, l.MerchantStatus = m.Name, m.Status
		}
	}
	if sku, ok := s.data.skus[it.SkuID]; ok && sku.ProductID == it.ProductID {
		l.SkuFound, l.SkuName, l.UnitPrice, l.StockQuantity = true, sku.SkuName, sku.Price, sku.StockQuantity
	}
	return l
}

func (s *Store) AddCartItem(ctx context.Context, accountID, productID, skuID string, fn func(line store.CartLine, lines int) (int, error)) (domain.CartItem, error) {
	defer s.lock(ctx)()
	if _, ok := s.data.accounts[accountID]; !ok {
		return domain.CartItem{}, store.ErrNotFound
	}
	var existing *domain.CartItem
	lines := 0
	for _, it := range s.data.cartItems {
		if it.AccountID != accountID {
			continue
		}
		lines++
		if it.ProductID == productID && it.SkuID == skuID {
			copied := it
			existing = &copied
		}
	}
	current := 0
	if existing != nil {
		current = existing.Quantity
	}
	qty, err := fn(s.lineState(domain.CartItem{AccountID: accountID, ProductID: productID, SkuID: skuID, Quantity: current}), lines)
	if err != nil {
		return domain.CartItem{}, err
	}
	if qty <= 0 {
		return domain.CartItem{}, fmt.Errorf("%w: 购物车数量必须大于 0", store.ErrInvalid)
	}
	now := s.timestamp()
	if existing != nil {
		existing.Quantity, existing.Selected, existing.UpdatedAt = qty, true, now
		s.data.cartItems[existing.CartItemID] = *existing
		return *existing, nil
	}
	it := domain.CartItem{CartItemID: domain.NewID(domain.PrefixCartItem), AccountID: accountID, ProductID: productID, SkuID: skuID,
		Quantity: qty, Selected: true, CreatedAt: now, UpdatedAt: now}
	s.data.cartItems[it.CartItemID] = it
	return it, nil
}

func (s *Store) UpdateCartItem(ctx context.Context, accountID, cartItemID string, fn func(item *domain.CartItem, line store.CartLine) error) (domain.CartItem, error) {
	defer s.lock(ctx)()
	before, ok := s.data.cartItems[cartItemID]
	if !ok || before.AccountID != accountID {
		return domain.CartItem{}, store.ErrNotFound
	}
	it := before
	if err := fn(&it, s.lineState(before)); err != nil {
		return domain.CartItem{}, err
	}
	if it.Quantity <= 0 {
		return domain.CartItem{}, fmt.Errorf("%w: 购物车数量必须大于 0", store.ErrInvalid)
	}
	it.CartItemID, it.AccountID, it.ProductID, it.SkuID, it.CreatedAt = before.CartItemID, before.AccountID, before.ProductID, before.SkuID, before.CreatedAt
	it.UpdatedAt = s.timestamp()
	s.data.cartItems[cartItemID] = it
	return it, nil
}

func (s *Store) DeleteCartItem(ctx context.Context, accountID, cartItemID string) error {
	defer s.lock(ctx)()
	it, ok := s.data.cartItems[cartItemID]
	if !ok || it.AccountID != accountID {
		return store.ErrNotFound
	}
	delete(s.data.cartItems, cartItemID)
	return nil
}

func (s *Store) claimable(c domain.Coupon, at time.Time) bool {
	if c.Status != domain.StatusActive || at.Before(c.StartAt) || !at.Before(c.EndAt) {
		return false
	}
	return c.MerchantID == "" || s.data.merchants[c.MerchantID].Status == domain.StatusActive
}

func (s *Store) ListClaimableCoupons(ctx context.Context, at time.Time, page store.Page) ([]domain.Coupon, int, error) {
	defer s.lock(ctx)()
	all := []domain.Coupon{}
	for _, c := range s.data.coupons {
		if s.claimable(c, at) {
			all = append(all, c)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].CouponID > all[j].CouponID
	})
	return pageOf(all, page), len(all), nil
}

func (s *Store) CountClaimed(ctx context.Context, accountID string, couponIDs []string) (map[string]int, error) {
	defer s.lock(ctx)()
	want := map[string]bool{}
	for _, id := range couponIDs {
		want[id] = true
	}
	out := map[string]int{}
	for _, uc := range s.data.userCoupons {
		if uc.AccountID == accountID && want[uc.CouponID] {
			out[uc.CouponID]++
		}
	}
	return out, nil
}

func (s *Store) ListUserCoupons(ctx context.Context, q store.UserCouponQuery) ([]store.OwnedCoupon, int, error) {
	switch q.Status {
	case "", domain.UserCouponUnused, domain.UserCouponUsed, domain.UserCouponExpired:
	default:
		return nil, 0, fmt.Errorf("%w: 券状态 %q 不合法", store.ErrInvalid, q.Status)
	}
	defer s.lock(ctx)()
	all := []store.OwnedCoupon{}
	for _, uc := range s.data.userCoupons {
		c, ok := s.data.coupons[uc.CouponID]
		if uc.AccountID != q.AccountID || !ok {
			continue
		}
		oc := store.OwnedCoupon{UserCoupon: uc, Coupon: c}
		oc.Status = store.EffectiveCouponStatus(uc.Status, c, q.At)
		if q.Status != "" && oc.Status != q.Status {
			continue
		}
		all = append(all, oc)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].ClaimedAt.Equal(all[j].ClaimedAt) {
			return all[i].ClaimedAt.After(all[j].ClaimedAt)
		}
		return all[i].UserCouponID > all[j].UserCouponID
	})
	return pageOf(all, q.Page), len(all), nil
}

func (s *Store) ClaimCoupon(ctx context.Context, accountID, couponID string, at time.Time) (store.OwnedCoupon, error) {
	defer s.lock(ctx)()
	c, ok := s.data.coupons[couponID]
	if !ok {
		return store.OwnedCoupon{}, store.ErrNotFound
	}
	owned := 0
	for _, uc := range s.data.userCoupons {
		if uc.AccountID == accountID && uc.CouponID == couponID {
			owned++
		}
	}
	merchantStatus := domain.EntityStatus("")
	if m, ok := s.data.merchants[c.MerchantID]; ok {
		merchantStatus = m.Status
	}
	if err := store.CheckClaim(c, merchantStatus, owned, at); err != nil {
		return store.OwnedCoupon{}, err
	}
	uc := domain.UserCoupon{UserCouponID: domain.NewID(domain.PrefixUserCoup), CouponID: couponID, AccountID: accountID,
		Status: domain.UserCouponUnused, ClaimedAt: s.timestamp()}
	s.data.userCoupons[uc.UserCouponID] = uc
	c.ClaimedCount++
	c.UpdatedAt = uc.ClaimedAt
	s.data.coupons[couponID] = c
	return store.OwnedCoupon{UserCoupon: uc, Coupon: c}, nil
}
