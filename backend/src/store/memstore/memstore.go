// Package memstore 是 store.Store 的内存实现，供 handler/Agent 单测使用。
// 它实现与 MySQL 相同的唯一约束和事务语义（失败整体回滚），由 storetest 保证两者行为一致。
package memstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
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
	tokens      map[string]domain.AuthToken // 按 token_hash
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
	files       map[string]domain.StoredFile
	cartItems   map[string]domain.CartItem
}

func newState() state {
	return state{
		accounts: map[string]accountRow{}, tokens: map[string]domain.AuthToken{}, merchants: map[string]domain.Merchant{}, categories: map[string]domain.Category{},
		products: map[string]domain.Product{}, skus: map[string]domain.ProductSKU{}, documents: map[string]domain.KnowledgeDocument{},
		chunks: map[string]domain.KnowledgeChunk{}, promotions: map[string]domain.PromotionRule{}, coupons: map[string]domain.Coupon{},
		userCoupons: map[string]domain.UserCoupon{}, orders: map[string]domain.Order{}, orderItems: map[string]domain.OrderItem{},
		payments: map[string]domain.Payment{}, reviews: map[string]domain.ProductReview{}, files: map[string]domain.StoredFile{},
		cartItems: map[string]domain.CartItem{},
	}
}

// clone 复制所有 map。行内的切片在存储时已复制且之后不再修改，可以共享。
func (s state) clone() state {
	return state{
		accounts: maps.Clone(s.accounts), tokens: maps.Clone(s.tokens), merchants: maps.Clone(s.merchants), categories: maps.Clone(s.categories),
		products: maps.Clone(s.products), skus: maps.Clone(s.skus), documents: maps.Clone(s.documents),
		chunks: maps.Clone(s.chunks), promotions: maps.Clone(s.promotions), coupons: maps.Clone(s.coupons),
		userCoupons: maps.Clone(s.userCoupons), orders: maps.Clone(s.orders), orderItems: maps.Clone(s.orderItems),
		payments: maps.Clone(s.payments), reviews: maps.Clone(s.reviews), files: maps.Clone(s.files),
		cartItems: maps.Clone(s.cartItems),
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
		if sameUsername(other.Username, acc.Username) {
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
		if sameUsername(row.Username, username) && row.DeletedAt == nil {
			return row.Account, row.passwordHash, nil
		}
	}
	return domain.Account{}, "", store.ErrNotFound
}

// sameUsername 模拟 MySQL utf8mb4_0900_ai_ci 排序规则：用户名比较不区分大小写。
func sameUsername(a, b string) bool { return strings.EqualFold(a, b) }

// liveAccount 返回未软删的账户行；调用方需持有锁。
func (s *Store) liveAccount(accountID string) (accountRow, error) {
	row, ok := s.data.accounts[accountID]
	if !ok || row.DeletedAt != nil {
		return accountRow{}, store.ErrNotFound
	}
	return row, nil
}

func (s *Store) UpdatePasswordHash(ctx context.Context, accountID, passwordHash string) error {
	if passwordHash == "" {
		return store.ErrInvalid
	}
	defer s.lock(ctx)()
	row, err := s.liveAccount(accountID)
	if err != nil {
		return err
	}
	row.passwordHash = passwordHash
	row.UpdatedAt = s.timestamp()
	s.data.accounts[accountID] = row
	return nil
}

func (s *Store) UpdateAccountProfile(ctx context.Context, accountID string, in store.ProfileUpdate) (domain.Account, error) {
	defer s.lock(ctx)()
	row, err := s.liveAccount(accountID)
	if err != nil {
		return domain.Account{}, err
	}
	if in.DisplayName != nil {
		row.DisplayName = *in.DisplayName
	}
	if in.AvatarURL != nil {
		row.AvatarURL = *in.AvatarURL
	}
	row.UpdatedAt = s.timestamp()
	s.data.accounts[accountID] = row
	return row.Account, nil
}

func (s *Store) UpdateAccountContact(ctx context.Context, accountID string, in store.ContactUpdate) (domain.Account, error) {
	defer s.lock(ctx)()
	row, err := s.liveAccount(accountID)
	if err != nil {
		return domain.Account{}, err
	}
	if in.Phone != nil {
		row.Phone = *in.Phone
	}
	if in.Email != nil {
		row.Email = *in.Email
	}
	row.UpdatedAt = s.timestamp()
	s.data.accounts[accountID] = row
	return row.Account, nil
}

func (s *Store) SoftDeleteAccount(ctx context.Context, accountID string) error {
	defer s.lock(ctx)()
	row, err := s.liveAccount(accountID)
	if err != nil {
		return err
	}
	now := s.timestamp()
	row.DeletedAt, row.UpdatedAt = &now, now
	s.data.accounts[accountID] = row
	for hash, t := range s.data.tokens {
		if t.AccountID == accountID {
			delete(s.data.tokens, hash)
		}
	}
	return nil
}

// ---------- 登录 token ----------

func (s *Store) CreateAuthToken(ctx context.Context, accountID string, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		return "", time.Time{}, store.ErrInvalid
	}
	defer s.lock(ctx)()
	token, hash := store.NewAuthToken()
	now := s.timestamp()
	t := domain.AuthToken{TokenHash: hash, AccountID: accountID, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	s.data.tokens[hash] = t
	return token, t.ExpiresAt, nil
}

func (s *Store) GetAccountByToken(ctx context.Context, token string) (domain.Account, error) {
	if token == "" {
		return domain.Account{}, store.ErrNotFound
	}
	defer s.lock(ctx)()
	t, ok := s.data.tokens[store.HashAuthToken(token)]
	if !ok || !t.ExpiresAt.After(s.timestamp()) {
		return domain.Account{}, store.ErrNotFound
	}
	row, err := s.liveAccount(t.AccountID)
	return row.Account, err
}

func (s *Store) DeleteAuthToken(ctx context.Context, token string) error {
	defer s.lock(ctx)()
	delete(s.data.tokens, store.HashAuthToken(token))
	return nil
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
	return s.withSKUs(p), nil
}

// withSKUs 返回商品副本并加载 SKU（默认 SKU 在前，再按 sku_id）。调用方需持有锁。
func (s *Store) withSKUs(p domain.Product) domain.Product {
	p = cloneProduct(p)
	p.SKUs = []domain.ProductSKU{}
	for _, sku := range s.data.skus {
		if sku.ProductID == p.ProductID {
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
	return p
}

// ---------- 公开目录 ----------

// pageOf 截取一页；越界返回空切片。
func pageOf[T any](items []T, page store.Page) []T {
	start := min(page.Offset(), len(items))
	end := min(start+page.PageSize, len(items))
	return slices.Clone(items[start:end])
}

func (s *Store) ListActiveMerchants(ctx context.Context, page store.Page) ([]domain.Merchant, int, error) {
	defer s.lock(ctx)()
	all := []domain.Merchant{}
	for _, m := range s.data.merchants {
		if m.Status == domain.StatusActive {
			all = append(all, m)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].MerchantID < all[j].MerchantID })
	return pageOf(all, page), len(all), nil
}

// visible 判断商品是否公开可见，并返回商家名。调用方需持有锁。
func (s *Store) visible(p domain.Product) (string, bool) {
	m, ok := s.data.merchants[p.MerchantID]
	return m.Name, ok && p.Status == domain.ProductActive && m.Status == domain.StatusActive
}

// containsFold 模拟 MySQL 不区分大小写的 LIKE '%term%'（term 已是小写）。
func containsFold(text, term string) bool { return strings.Contains(strings.ToLower(text), term) }

func (s *Store) matchesKeyword(p domain.Product, terms []string) bool {
	categoryName := s.data.categories[p.CategoryID].Name
	for _, term := range terms {
		if containsFold(p.Name, term) || containsFold(p.Brand, term) || containsFold(categoryName, term) {
			return true
		}
		for _, list := range [][]string{p.Tags, p.SellingPoints} {
			for _, v := range list {
				if containsFold(v, term) {
					return true
				}
			}
		}
	}
	return false
}

func (s *Store) SearchVisibleProducts(ctx context.Context, q store.ProductSearch) ([]store.CatalogProduct, int, error) {
	defer s.lock(ctx)()
	terms := store.SearchTerms(q.Keyword)
	if len(terms) == 0 && strings.TrimSpace(q.Keyword) != "" {
		return []store.CatalogProduct{}, 0, nil
	}
	all := []store.CatalogProduct{}
	for _, p := range s.data.products {
		name, ok := s.visible(p)
		if !ok {
			continue
		}
		if q.CategoryID != "" && p.CategoryID != q.CategoryID && s.data.categories[p.CategoryID].ParentID != q.CategoryID {
			continue
		}
		if len(terms) > 0 && !s.matchesKeyword(p, terms) {
			continue
		}
		all = append(all, store.CatalogProduct{Product: p, MerchantName: name})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].SortOrder != all[j].SortOrder {
			return all[i].SortOrder < all[j].SortOrder
		}
		return all[i].ProductID < all[j].ProductID
	})
	out := pageOf(all, q.Page)
	for i := range out {
		out[i].Product = s.withSKUs(out[i].Product)
	}
	return out, len(all), nil
}

func (s *Store) GetVisibleProduct(ctx context.Context, productID string) (store.CatalogProduct, error) {
	defer s.lock(ctx)()
	p, ok := s.data.products[productID]
	if !ok {
		return store.CatalogProduct{}, store.ErrNotFound
	}
	name, ok := s.visible(p)
	if !ok {
		return store.CatalogProduct{}, store.ErrNotFound
	}
	return store.CatalogProduct{Product: s.withSKUs(p), MerchantName: name}, nil
}

func (s *Store) ListVisibleReviews(ctx context.Context, productID string, page store.Page) ([]store.PublicReview, int, error) {
	defer s.lock(ctx)()
	all := []store.PublicReview{}
	for _, r := range s.data.reviews {
		if r.ProductID != productID || r.Status != domain.ReviewVisible {
			continue
		}
		r.Tags = cloneSlice(r.Tags)
		pr := store.PublicReview{ProductReview: r}
		if acc, ok := s.data.accounts[r.AccountID]; ok && acc.DeletedAt == nil {
			pr.ReviewerName = acc.DisplayName
		}
		all = append(all, pr)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ReviewID > all[j].ReviewID
	})
	return pageOf(all, page), len(all), nil
}

func (s *Store) ListActivePromotions(ctx context.Context, q store.PromotionQuery) ([]domain.PromotionRule, int, error) {
	defer s.lock(ctx)()
	at := q.At.UTC()
	applies := func(p domain.PromotionRule) bool {
		if q.ProductID == "" {
			return true
		}
		switch p.Scope {
		case domain.ScopePlatform:
			return true
		case domain.ScopeMerchant:
			return p.MerchantID == q.MerchantID
		case domain.ScopeProduct:
			return p.ProductID == q.ProductID
		case domain.ScopeCategory:
			return slices.Contains(q.CategoryIDs, p.CategoryID)
		}
		return false
	}
	all := []domain.PromotionRule{}
	for _, p := range s.data.promotions {
		if p.Status != domain.StatusActive || p.StartAt.After(at) || !p.EndAt.After(at) || !applies(p) {
			continue
		}
		if p.MerchantID != "" && s.data.merchants[p.MerchantID].Status != domain.StatusActive {
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
				if err := store.ValidatePromotion(p); err != nil {
					return err
				}
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
