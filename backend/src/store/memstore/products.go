package memstore

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ---------- 商家商品管理 ----------

func (s *Store) ListMerchantProducts(ctx context.Context, q store.MerchantProductQuery) ([]domain.Product, int, error) {
	defer s.lock(ctx)()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	all := []domain.Product{}
	for _, p := range s.data.products {
		if p.MerchantID != q.MerchantID || p.Status == domain.ProductDeleted ||
			(q.Status != "" && p.Status != q.Status) || (kw != "" && !containsFold(p.Name, kw)) {
			continue
		}
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
			return all[i].UpdatedAt.After(all[j].UpdatedAt)
		}
		return all[i].ProductID > all[j].ProductID
	})
	out := pageOf(all, q.Page)
	for i := range out {
		out[i] = s.withSKUs(out[i])
	}
	return out, len(all), nil
}

func (s *Store) CreateProduct(ctx context.Context, p domain.Product) (domain.Product, error) {
	if err := store.PrepareProduct(&p); err != nil {
		return domain.Product{}, err
	}
	err := s.WithTx(ctx, func(context.Context) error {
		if _, ok := s.data.products[p.ProductID]; ok {
			return &store.ConflictError{Key: store.KeyPrimary}
		}
		for _, sku := range p.SKUs {
			if _, ok := s.data.skus[sku.SkuID]; ok {
				return &store.ConflictError{Key: store.KeyPrimary}
			}
		}
		now := s.timestamp()
		p.CreatedAt, p.UpdatedAt = now, now
		s.putProduct(p, now)
		return nil
	})
	if err != nil {
		return domain.Product{}, err
	}
	return s.GetProduct(ctx, p.ProductID)
}

func (s *Store) UpdateProduct(ctx context.Context, productID string, fn func(p *domain.Product) error) (domain.Product, error) {
	err := s.WithTx(ctx, func(context.Context) error {
		stored, ok := s.data.products[productID]
		if !ok {
			return store.ErrNotFound
		}
		p := s.withSKUs(stored)
		if err := fn(&p); err != nil {
			return err
		}
		p.ProductID, p.MerchantID, p.CreatedAt = productID, stored.MerchantID, stored.CreatedAt
		if err := store.PrepareProduct(&p); err != nil {
			return err
		}
		keep := map[string]bool{}
		for _, sku := range p.SKUs {
			if other, ok := s.data.skus[sku.SkuID]; ok && other.ProductID != productID {
				return &store.ConflictError{Key: store.KeyPrimary}
			}
			keep[sku.SkuID] = true
		}
		for id, sku := range s.data.skus {
			if sku.ProductID == productID && !keep[id] {
				delete(s.data.skus, id)
			}
		}
		now := s.timestamp()
		p.UpdatedAt = now
		s.putProduct(p, now)
		return nil
	})
	if err != nil {
		return domain.Product{}, err
	}
	return s.GetProduct(ctx, productID)
}

// putProduct 写入商品和 SKU（SKU 保留原 created_at）。调用方需持有锁。
func (s *Store) putProduct(p domain.Product, now time.Time) {
	for _, sku := range p.SKUs {
		sku.Specs = maps.Clone(sku.Specs)
		if sku.Specs == nil {
			sku.Specs = map[string]string{}
		}
		if old, ok := s.data.skus[sku.SkuID]; ok {
			sku.CreatedAt = old.CreatedAt
		} else {
			sku.CreatedAt = now
		}
		sku.UpdatedAt = now
		s.data.skus[sku.SkuID] = sku
	}
	s.data.products[p.ProductID] = cloneProduct(p)
}
