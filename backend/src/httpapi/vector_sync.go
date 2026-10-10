package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const (
	vectorSyncTimeout = 20 * time.Second
	// maxSyncRounds：商品在同步期间一直在变时，一个任务最多重做的轮数；超出后等下一次变更再触发。
	maxSyncRounds = 5
)

// productSyncs 保证同一商品同时只有一个同步任务：任务进行中又收到同步请求时只记 dirty，
// 当前一轮写完后再按 Store 的最新状态重做一轮。这样任务之间不会乱序覆盖，最后写入的总是最新快照。
type productSyncs struct {
	mu      sync.Mutex
	running map[string]*productSync
}

type productSync struct {
	dirty bool
}

// syncProductVectors 在商品增改删、上下架后异步更新商品文本向量和商品图向量：当前公开可见的写入，不可见的删除。
// 失败只记日志——检索时向量结果会按可见性重新取回，索引过期只影响召回，不会把下架商品推荐出去。
// 店铺停业影响的商品不逐个同步，靠这一层过滤。
func (s *Server) syncProductVectors(ids ...string) {
	if s.productIndex == nil && s.imageSearch == nil {
		return
	}
	s.syncs.mu.Lock()
	defer s.syncs.mu.Unlock()
	if s.syncs.running == nil {
		s.syncs.running = map[string]*productSync{}
	}
	for _, id := range ids {
		if st, ok := s.syncs.running[id]; ok {
			st.dirty = true // 正在同步：当前一轮结束后按最新状态重做
			continue
		}
		s.syncs.running[id] = &productSync{}
		s.vectorSync.Add(1)
		go s.runProductSync(id)
	}
}

// runProductSync 是一个商品的同步任务：读快照 → 写两个索引 → 再读一次；期间有新请求（dirty）或者
// 内容指纹变了（例如另一个进程的 cmd/vectorindex 写过旧快照、或改动没经过本进程），就再做一轮。
func (s *Server) runProductSync(id string) {
	defer s.vectorSync.Done()
	ctx, cancel := context.WithTimeout(context.Background(), vectorSyncTimeout)
	defer cancel()
	for round := 1; ; round++ {
		s.syncs.mu.Lock()
		s.syncs.running[id].dirty = false
		s.syncs.mu.Unlock()

		snap, err := s.indexSnapshot(ctx, id)
		if err == nil {
			err = s.applySnapshot(ctx, snap)
		}
		var again indexSnapshot
		if err == nil {
			again, err = s.indexSnapshot(ctx, id)
		}
		if err != nil {
			s.logger.Warn("product vector sync failed", "product_id", id, "round", round, "error", err)
		}

		s.syncs.mu.Lock()
		redo := err == nil && (s.syncs.running[id].dirty || again.fingerprint() != snap.fingerprint())
		if err != nil && s.syncs.running[id].dirty {
			redo = true // 失败了但又有新的变更：用新状态再试一次
		}
		if !redo || round >= maxSyncRounds || ctx.Err() != nil {
			if redo {
				s.logger.Warn("product vector sync gave up after repeated changes", "product_id", id, "rounds", round)
			}
			delete(s.syncs.running, id)
			s.syncs.mu.Unlock()
			return
		}
		s.syncs.mu.Unlock()
	}
}

// indexSnapshot 是一个商品写进索引的全部内容（同一次读取，文本和图片保持一致）。
type indexSnapshot struct {
	id         string
	visible    bool
	merchantID string
	categoryID string
	text       string
	images     []string
	product    store.CatalogProduct
}

// fingerprint 是快照里会影响索引的部分；相同表示索引内容相同。
func (n indexSnapshot) fingerprint() string {
	if !n.visible {
		return "hidden"
	}
	return n.merchantID + "\x00" + n.categoryID + "\x00" + n.text + "\x00" + strings.Join(n.images, "\x00")
}

func (s *Server) indexSnapshot(ctx context.Context, id string) (indexSnapshot, error) {
	p, err := s.store.GetVisibleProduct(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return indexSnapshot{id: id}, nil
	}
	if err != nil {
		return indexSnapshot{}, err
	}
	cats, err := s.store.ListCategories(ctx)
	if err != nil {
		return indexSnapshot{}, err
	}
	name := ""
	for _, c := range cats {
		if c.CategoryID == p.CategoryID {
			name = c.Name
			break
		}
	}
	return indexSnapshot{id: id, visible: true, merchantID: p.MerchantID, categoryID: p.CategoryID, text: rag.ProductText(p, name),
		images: imagesearch.ProductImages(p.Product), product: p}, nil
}

func (s *Server) applySnapshot(ctx context.Context, n indexSnapshot) error {
	var errs []error
	if s.productIndex != nil {
		if n.visible {
			errs = append(errs, s.productIndex.UpsertProducts(ctx, []rag.IndexedProduct{{ProductID: n.id, MerchantID: n.merchantID, CategoryID: n.categoryID, Text: n.text}}))
		} else {
			errs = append(errs, s.productIndex.DeleteProducts(ctx, []string{n.id}))
		}
	}
	if s.imageSearch != nil {
		if n.visible {
			_, err := s.imageSearch.IndexProduct(ctx, n.product.Product)
			errs = append(errs, err)
		} else {
			errs = append(errs, s.imageSearch.Remove(ctx, n.id))
		}
	}
	return errors.Join(errs...)
}

// ResyncAllProducts 把全部在售商品重新写入商品文本向量和商品图向量（启动时的全量索引）。
// 走与增量同步相同的按商品串行任务，不会和运行中的增量同步互相覆盖。
func (s *Server) ResyncAllProducts(ctx context.Context) (int, error) {
	if s.productIndex == nil && s.imageSearch == nil {
		return 0, nil
	}
	n := 0
	for page := 1; ; page++ {
		items, total, err := s.store.SearchVisibleProducts(ctx, store.ProductSearch{Page: store.Page{Page: page, PageSize: 100}})
		if err != nil {
			return n, err
		}
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ProductID)
		}
		s.syncProductVectors(ids...)
		n += len(ids)
		if page*100 >= total {
			break
		}
	}
	s.WaitVectorSync()
	return n, nil
}

// WaitVectorSync 等待进行中的向量同步结束（测试、全量索引和优雅关闭用）。
func (s *Server) WaitVectorSync() { s.vectorSync.Wait() }
