package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const vectorSyncTimeout = 20 * time.Second

// syncProductVectors 在商品增改删、上下架后异步更新商品向量：当前公开可见的写入，不可见的删除。失败只记日志——
// 检索时向量结果会按可见性重新取回，索引过期只影响召回，不会把下架商品推荐出去。店铺停业影响的商品不逐个同步，靠这一层过滤。
// 商品图向量同理（imageSearch 不为 nil 时）：可见的按当前图片重建，不可见的删除。
func (s *Server) syncProductVectors(ids ...string) {
	if (s.productIndex == nil && s.imageSearch == nil) || len(ids) == 0 {
		return
	}
	s.vectorSync.Add(1)
	go func() {
		defer s.vectorSync.Done()
		ctx, cancel := context.WithTimeout(context.Background(), vectorSyncTimeout)
		defer cancel()
		if err := s.syncProductVectorsNow(ctx, ids); err != nil {
			s.logger.Warn("product vector sync failed", "product_ids", ids, "error", err)
		}
		if s.imageSearch != nil {
			if _, err := s.imageSearch.Sync(ctx, ids...); err != nil {
				s.logger.Warn("product image vector sync failed", "product_ids", ids, "error", err)
			}
		}
	}()
}

func (s *Server) syncProductVectorsNow(ctx context.Context, ids []string) error {
	if s.productIndex == nil {
		return nil
	}
	cats, err := s.store.ListCategories(ctx)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, c := range cats {
		names[c.CategoryID] = c.Name
	}
	var upsert []rag.IndexedProduct
	var remove []string
	for _, id := range ids {
		p, err := s.store.GetVisibleProduct(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			remove = append(remove, id)
		case err != nil:
			return err
		default:
			upsert = append(upsert, rag.IndexedProduct{ProductID: p.ProductID, MerchantID: p.MerchantID, CategoryID: p.CategoryID, Text: rag.ProductText(p, names[p.CategoryID])})
		}
	}
	if err := s.productIndex.DeleteProducts(ctx, remove); err != nil {
		return err
	}
	return s.productIndex.UpsertProducts(ctx, upsert)
}

// WaitVectorSync 等待进行中的向量同步结束（测试和优雅关闭用）。
func (s *Server) WaitVectorSync() { s.vectorSync.Wait() }
