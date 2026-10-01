// Package memstore 是 store.Store 的内存实现，供 handler/Agent 单测使用。
// 它实现与 MySQL 相同的唯一约束和事务语义（失败整体回滚），由 storetest 保证两者行为一致。
package memstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

type accountRow struct {
	domain.Account
	passwordHash string
}

// state 是全部表数据；事务开始时整体复制一份用于回滚。
type state struct {
	accounts    map[string]accountRow
	merchants   map[string]domain.Merchant
	categories  map[string]domain.Category
	products    map[string]domain.Product // 不含 SKUs，SKU 单独存
	skus        map[string]domain.ProductSKU
	documents   map[string]domain.KnowledgeDocument
	chunks      map[string]domain.KnowledgeChunk
	promotions  map[string]domain.PromotionRule
	coupons     map[string]domain.Coupon
	userCoupons map[string]domain.UserCoupon
	orders      map[string]domain.Order // 不含 Items
	orderItems  map[string]domain.OrderItem
	payments    map[string]domain.Payment
	reviews     map[string]domain.ProductReview
}

func newState() state {
	return state{
		accounts: map[string]accountRow{}, merchants: map[string]domain.Merchant{}, categories: map[string]domain.Category{},
		products: map[string]domain.Product{}, skus: map[string]domain.ProductSKU{}, documents: map[string]domain.KnowledgeDocument{},
		chunks: map[string]domain.KnowledgeChunk{}, promotions: map[string]domain.PromotionRule{}, coupons: map[string]domain.Coupon{},
		userCoupons: map[string]domain.UserCoupon{}, orders: map[string]domain.Order{}, orderItems: map[string]domain.OrderItem{},
		payments: map[string]domain.Payment{}, reviews: map[string]domain.ProductReview{},
	}
}

// clone 复制所有 map。行内的切片在存储时已复制且之后不再修改，可以共享。
func (s state) clone() state {
	return state{
		accounts: maps.Clone(s.accounts), merchants: maps.Clone(s.merchants), categories: maps.Clone(s.categories),
		products: maps.Clone(s.products), skus: maps.Clone(s.skus), documents: maps.Clone(s.documents),
		chunks: maps.Clone(s.chunks), promotions: maps.Clone(s.promotions), coupons: maps.Clone(s.coupons),
		userCoupons: maps.Clone(s.userCoupons), orders: maps.Clone(s.orders), orderItems: maps.Clone(s.orderItems),
		payments: maps.Clone(s.payments), reviews: maps.Clone(s.reviews),
	}
}

// Store 实现 store.Store。
type Store struct {
	mu   sync.Mutex
	data state
	now  func() time.Time
}

var _ store.Store = (*Store)(nil)

func New() *Store {
	return &Store{data: newState(), now: func() time.Time { return time.Now().UTC() }}
}

// SetClock 仅供测试固定时间。
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) timestamp() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

func (s *Store) orNow(t time.Time) time.Time {
	if t.IsZero() {
		return s.timestamp()
	}
	return t.UTC().Truncate(time.Millisecond)
}

type txKey struct{ s *Store }

func (s *Store) inTx(ctx context.Context) bool { return ctx.Value(txKey{s}) != nil }

// lock 在非事务调用时加锁；事务内已持有锁。返回对应的解锁函数。
func (s *Store) lock(ctx context.Context) func() {
	if s.inTx(ctx) {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s *Store) Ping(context.Context) error { return nil }

// WithTx 持有全局锁执行 fn，失败或 panic 时恢复到事务开始前的快照。嵌套调用复用外层事务。
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if s.inTx(ctx) {
		return fn(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.data.clone()
	defer func() {
		if p := recover(); p != nil {
			s.data = snapshot
			panic(p)
		}
		if err != nil {
			s.data = snapshot
		}
	}()
	return fn(context.WithValue(ctx, txKey{s}, true))
}

// ---------- 账户 ----------

func (s *Store) CreateAccount(ctx context.Context, in store.NewAccount) (domain.Account, error) {
	if err := store.ValidateNewAccount(in); err != nil {
		return domain.Account{}, err
	}
	defer s.lock(ctx)()
	now := s.timestamp()
	acc := domain.Account{
		AccountID: in.AccountID, Username: in.Username, DisplayName: in.DisplayName, AvatarURL: in.AvatarURL,
		Phone: in.Phone, Email: in.Email, Role: in.Role, MerchantID: in.MerchantID,
		Status: domain.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if acc.AccountID == "" {
		acc.AccountID = domain.NewID(domain.PrefixAccount)
	}
	if err := s.insertAccount(acc, in.PasswordHash); err != nil {
		return domain.Account{}, err
	}
	return acc, nil
}

func (s *Store) insertAccount(acc domain.Account, hash string) error {
	if _, ok := s.data.accounts[acc.AccountID]; ok {
		return &store.ConflictError{Key: store.KeyPrimary}
	}
	for _, other := range s.data.accounts {
		if other.Username == acc.Username {
			return &store.ConflictError{Key: store.KeyAccountUsername}
		}
	}
	acc.CreatedAt, acc.UpdatedAt = s.orNow(acc.CreatedAt), s.orNow(acc.UpdatedAt)
	s.data.accounts[acc.AccountID] = accountRow{Account: acc, passwordHash: hash}
	return nil
}

func (s *Store) GetAccount(ctx context.Context, accountID string) (domain.Account, error) {
	defer s.lock(ctx)()
	row, ok := s.data.accounts[accountID]
	if !ok || row.DeletedAt != nil {
		return domain.Account{}, store.ErrNotFound
	}
	return row.Account, nil
}

func (s *Store) GetAccountByUsername(ctx context.Context, username string) (domain.Account, string, error) {
	defer s.lock(ctx)()
	for _, row := range s.data.accounts {
		if row.Username == username && row.DeletedAt == nil {
			return row.Account, row.passwordHash, nil
		}
	}
	return domain.Account{}, "", store.ErrNotFound
}

// ---------- 目录 ----------

func (s *Store) ListCategories(ctx context.Context) ([]domain.Category, error) {
	defer s.lock(ctx)()
	out := slices.AppendSeq(make([]domain.Category, 0, len(s.data.categories)), maps.Values(s.data.categories))
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ParentID != b.ParentID {
			return a.ParentID < b.ParentID
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return a.CategoryID < b.CategoryID
	})
	return out, nil
}

func (s *Store) GetProduct(ctx context.Context, productID string) (domain.Product, error) {
	defer s.lock(ctx)()
	p, ok := s.data.products[productID]
	if !ok {
		return domain.Product{}, store.ErrNotFound
	}
	p = cloneProduct(p)
	p.SKUs = []domain.ProductSKU{}
	for _, sku := range s.data.skus {
		if sku.ProductID == productID {
			sku.Specs = maps.Clone(sku.Specs)
			p.SKUs = append(p.SKUs, sku)
		}
	}
	sort.Slice(p.SKUs, func(i, j int) bool {
		if p.SKUs[i].IsDefault != p.SKUs[j].IsDefault {
			return p.SKUs[i].IsDefault
		}
		return p.SKUs[i].SkuID < p.SKUs[j].SkuID
	})
	return p, nil
}

// cloneProduct 复制切片，并把 nil 切片规范为空切片（与 MySQL 读出 [] 一致）。
func cloneProduct(p domain.Product) domain.Product {
	p.ImageURLs = cloneSlice(p.ImageURLs)
	p.Tags = cloneSlice(p.Tags)
	p.SellingPoints = cloneSlice(p.SellingPoints)
	p.RiskNotes = cloneSlice(p.RiskNotes)
	p.Attributes = cloneSlice(p.Attributes)
	p.SuitableFor = cloneSlice(p.SuitableFor)
	p.NotSuitableFor = cloneSlice(p.NotSuitableFor)
	p.SKUs = nil
	return p
}

func cloneSlice[T any](v []T) []T {
	out := make([]T, len(v))
	copy(out, v)
	return out
}

// ---------- 种子 ----------

func (s *Store) ApplySeed(ctx context.Context, data store.SeedData) (store.SeedResult, error) {
	result := store.SeedResult{}
	err := s.WithTx(ctx, func(ctx context.Context) error {
		ins := &seedInserter{result: result}
		for _, a := range data.Accounts {
			ins.row("accounts", has(s.data.accounts, a.AccountID), func() error { return s.insertAccount(a.Account, a.PasswordHash) })
		}
		for _, m := range data.Merchants {
			ins.row("merchants", has(s.data.merchants, m.MerchantID), func() error {
				m.CreatedAt, m.UpdatedAt = s.orNow(m.CreatedAt), s.orNow(m.UpdatedAt)
				s.data.merchants[m.MerchantID] = m
				return nil
			})
		}
		for _, c := range data.Categories {
			ins.row("categories", has(s.data.categories, c.CategoryID), func() error {
				c.Children = nil
				c.CreatedAt, c.UpdatedAt = s.orNow(c.CreatedAt), s.orNow(c.UpdatedAt)
				s.data.categories[c.CategoryID] = c
				return nil
			})
		}
		for _, p := range data.Products {
			ins.row("products", has(s.data.products, p.ProductID), func() error {
				stored := cloneProduct(p)
				stored.CreatedAt, stored.UpdatedAt = s.orNow(p.CreatedAt), s.orNow(p.UpdatedAt)
				s.data.products[p.ProductID] = stored
				return nil
			})
			for _, sku := range p.SKUs {
				ins.row("product_skus", has(s.data.skus, sku.SkuID), func() error {
					sku.Specs = maps.Clone(sku.Specs)
					if sku.Specs == nil {
						sku.Specs = map[string]string{}
					}
					sku.CreatedAt, sku.UpdatedAt = s.orNow(sku.CreatedAt), s.orNow(sku.UpdatedAt)
					s.data.skus[sku.SkuID] = sku
					return nil
				})
			}
		}
		for _, d := range data.Documents {
			ins.row("knowledge_documents", has(s.data.documents, d.DocumentID), func() error {
				for _, other := range s.data.documents {
					if other.MerchantID == d.MerchantID && other.ContentHash == d.ContentHash {
						return &store.ConflictError{Key: store.KeyDocumentMerchantHash}
					}
				}
				d.CreatedAt, d.UpdatedAt = s.orNow(d.CreatedAt), s.orNow(d.UpdatedAt)
				s.data.documents[d.DocumentID] = d
				return nil
			})
		}
		for _, c := range data.Chunks {
			ins.row("knowledge_chunks", has(s.data.chunks, c.ChunkID), func() error {
				for _, other := range s.data.chunks {
					if other.DocumentID == c.DocumentID && other.ChunkIndex == c.ChunkIndex {
						return &store.ConflictError{Key: store.KeyChunkDocumentIndex}
					}
				}
				c.CreatedAt = s.orNow(c.CreatedAt)
				s.data.chunks[c.ChunkID] = c
				return nil
			})
		}
		for _, p := range data.Promotions {
			ins.row("promotion_rules", has(s.data.promotions, p.PromotionID), func() error {
				p.CreatedAt, p.UpdatedAt = s.orNow(p.CreatedAt), s.orNow(p.UpdatedAt)
				s.data.promotions[p.PromotionID] = p
				return nil
			})
		}
		for _, c := range data.Coupons {
			ins.row("coupons", has(s.data.coupons, c.CouponID), func() error {
				c.CreatedAt, c.UpdatedAt = s.orNow(c.CreatedAt), s.orNow(c.UpdatedAt)
				s.data.coupons[c.CouponID] = c
				return nil
			})
		}
		for _, uc := range data.UserCoupons {
			ins.row("user_coupons", has(s.data.userCoupons, uc.UserCouponID), func() error {
				uc.ClaimedAt = s.orNow(uc.ClaimedAt)
				s.data.userCoupons[uc.UserCouponID] = uc
				return nil
			})
		}
		for _, o := range data.Orders {
			ins.row("orders", has(s.data.orders, o.OrderID), func() error {
				for _, other := range s.data.orders {
					if other.OrderNo == o.OrderNo {
						return &store.ConflictError{Key: store.KeyOrderNo}
					}
				}
				stored := o
				stored.Items = nil
				stored.CreatedAt, stored.UpdatedAt = s.orNow(o.CreatedAt), s.orNow(o.UpdatedAt)
				s.data.orders[o.OrderID] = stored
				return nil
			})
			for _, it := range o.Items {
				ins.row("order_items", has(s.data.orderItems, it.OrderItemID), func() error {
					it.CreatedAt = s.orNow(it.CreatedAt)
					s.data.orderItems[it.OrderItemID] = it
					return nil
				})
			}
		}
		for _, p := range data.Payments {
			ins.row("payments", has(s.data.payments, p.PaymentID), func() error {
				p.CreatedAt, p.UpdatedAt = s.orNow(p.CreatedAt), s.orNow(p.UpdatedAt)
				s.data.payments[p.PaymentID] = p
				return nil
			})
		}
		for _, r := range data.Reviews {
			ins.row("product_reviews", has(s.data.reviews, r.ReviewID), func() error {
				for _, other := range s.data.reviews {
					if other.OrderItemID == r.OrderItemID {
						return &store.ConflictError{Key: store.KeyReviewOrderItem}
					}
				}
				r.Tags = cloneSlice(r.Tags)
				r.CreatedAt, r.UpdatedAt = s.orNow(r.CreatedAt), s.orNow(r.UpdatedAt)
				s.data.reviews[r.ReviewID] = r
				return nil
			})
		}
		return ins.err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func has[V any](m map[string]V, id string) bool {
	_, ok := m[id]
	return ok
}

type seedInserter struct {
	result store.SeedResult
	err    error
}

func (i *seedInserter) row(table string, exists bool, insert func() error) {
	if i.err != nil {
		return
	}
	if _, ok := i.result[table]; !ok {
		i.result[table] = 0
	}
	if exists {
		return
	}
	if err := insert(); err != nil {
		i.err = fmt.Errorf("seed %s: %w", table, err)
		return
	}
	i.result[table]++
}
