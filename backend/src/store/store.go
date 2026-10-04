// Package store 定义业务层唯一依赖的数据访问边界。
// handler、Agent 等只面向 Store 接口；MySQL 实现在 mysqlstore，测试用内存实现在 memstore，
// 两者由 storetest 中同一套用例约束行为一致。
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

var (
	// ErrNotFound 记录不存在（含已软删的记录）。
	ErrNotFound = errors.New("store: not found")
	// ErrConflict 违反唯一约束，例如用户名已存在。用 errors.Is 判断，用 errors.As 取 ConflictError 看具体键。
	ErrConflict = errors.New("store: conflict")
	// ErrInvalid 输入不满足存储层的基本约束。
	ErrInvalid = errors.New("store: invalid input")

	// 领券失败的原因（ClaimCoupon）。
	ErrCouponUnavailable  = errors.New("store: coupon not claimable") // 停用、未开始、已结束或所属店铺停业
	ErrCouponSoldOut      = errors.New("store: coupon sold out")
	ErrCouponLimitReached = errors.New("store: coupon per-user limit reached")

	// 评价失败的原因（CreateReview）。
	ErrOrderItemNotFound = errors.New("store: order item not found")
)

// ConflictError 指出冲突的唯一键名（与 migration 中的 UNIQUE KEY 名一致）。
type ConflictError struct {
	Key string
}

func (e *ConflictError) Error() string        { return fmt.Sprintf("store: conflict on %s", e.Key) }
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// 唯一键名；MySQL 与内存实现都用这些名字报告冲突。
const (
	KeyAccountUsername      = "uk_accounts_username"
	KeyOrderNo              = "uk_orders_order_no"
	KeyReviewOrderItem      = "uk_product_reviews_order_item"
	KeyDocumentMerchantHash = "uk_knowledge_documents_merchant_hash"
	KeyChunkDocumentIndex   = "uk_knowledge_chunks_document_index"
	KeyStoredFileObjectKey  = "uk_stored_files_object_key"
	KeyCartItem             = "uk_cart_items_account_product_sku"
	KeyCheckoutKey          = "uk_checkout_requests_account_key"
	KeyPrimary              = "PRIMARY"
)

// Page 是分页参数：Page 从 1 开始，PageSize 为 1–100（由调用方规范化）。
type Page struct {
	Page     int
	PageSize int
}

func (p Page) Offset() int { return (p.Page - 1) * p.PageSize }

// ProductSearch 是公开商品搜索条件。CategoryID 同时匹配该分类及其子分类；Keyword 见 SearchTerms。
type ProductSearch struct {
	Keyword    string
	CategoryID string
	Page       Page
}

// MerchantProductQuery 是商家查看自己商品的条件。Status 为空表示全部（不含 deleted）；Keyword 按名称包含匹配。
type MerchantProductQuery struct {
	MerchantID string
	Status     domain.ProductStatus
	Keyword    string
	Page       Page
}

// PrepareProduct 是两种实现写入商品前共用的处理：补齐 ID、校验 SKU 不变量并重算派生字段。
func PrepareProduct(p *domain.Product) error {
	if p.ProductID == "" {
		p.ProductID = domain.NewID(domain.PrefixProduct)
	}
	for i := range p.SKUs {
		if p.SKUs[i].SkuID == "" {
			p.SKUs[i].SkuID = domain.NewID(domain.PrefixSKU)
		}
		p.SKUs[i].ProductID = p.ProductID
	}
	if p.MerchantID == "" || p.CategoryID == "" || p.Name == "" {
		return fmt.Errorf("%w: 商品缺少商家、分类或名称", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, sku := range p.SKUs {
		if seen[sku.SkuID] {
			return fmt.Errorf("%w: SKU %s 重复", ErrInvalid, sku.SkuID)
		}
		seen[sku.SkuID] = true
	}
	if err := p.SyncFromSKUs(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// CatalogProduct 是带商家名称的商品，SKUs 已加载（默认 SKU 在前）。
type CatalogProduct struct {
	domain.Product
	MerchantName string
}

// PublicReview 是评价及评价人当前的显示名（账户已注销时为空）。
type PublicReview struct {
	domain.ProductReview
	ReviewerName string
}

// DocumentQuery 列出知识文档。AllMerchants 为 true 时不按商家过滤（管理员）；否则只返回 MerchantID 的文档（空串为平台资料）。
// Status 为空表示全部；Keyword 按标题包含匹配。
type DocumentQuery struct {
	AllMerchants bool
	MerchantID   string
	Status       domain.DocumentStatus
	Keyword      string
	Page         Page
}

// KnowledgeQuery 是分块召回条件。Terms（标题、正文或文档标题包含任一词）与 ChunkIDs（按 ID 取回，用于向量结果）
// 至少给一个，都为空时返回空结果。MerchantIDs / ProductIDs / DocTypes 为空表示不过滤；MerchantIDs 中的空串表示平台资料。
// Limit 为 0 时取 DefaultKnowledgeLimit；结果按命中的检索词个数降序、chunk_id 升序（截断时保留命中多的），最终排名由 rag 包负责。
type KnowledgeQuery struct {
	Terms       []string
	ChunkIDs    []string
	MerchantIDs []string
	ProductIDs  []string
	DocTypes    []string
	Limit       int
}

const DefaultKnowledgeLimit = 200

// KnowledgeHit 是召回的分块及其所属文档的信息。
type KnowledgeHit struct {
	domain.KnowledgeChunk
	DocumentTitle string
	DocType       string
	SourceURL     string
}

// CartLine 是购物车项及其关联信息。商品或规格被删除时 SkuFound 为 false、名称等为空。
type CartLine struct {
	domain.CartItem
	ProductName    string
	ProductStatus  domain.ProductStatus
	ImageURL       string
	CategoryID     string
	MerchantID     string
	MerchantName   string
	MerchantStatus domain.EntityStatus
	SkuFound       bool
	SkuName        string
	UnitPrice      domain.Money
	StockQuantity  int
}

// MaxCartLines 是一个购物车最多的行数（不同商品规格数）。
const MaxCartLines = 100

// UserCouponQuery 列出账户的券。Status 为空表示全部；unused / used / expired 按 At 计算。
type UserCouponQuery struct {
	AccountID string
	Status    string
	At        time.Time
	Page      Page
}

// OwnedCoupon 是用户持有的一张券及其定义；Status 已按查询时间计算（未使用但已过期的为 expired）。
type OwnedCoupon struct {
	domain.UserCoupon
	Coupon domain.Coupon
}

// PromotionQuery 查询某时刻有效的促销。ProductID 非空时只返回适用于该商品的促销：
// 平台促销、该商品商家的促销、指定该商品的促销，以及作用于 CategoryIDs（商品分类及其父分类）的促销。
type PromotionQuery struct {
	At          time.Time
	ProductID   string
	MerchantID  string
	CategoryIDs []string
	Page        Page
}

// ProfileUpdate 是个人资料的部分更新，nil 表示不修改。
type ProfileUpdate struct {
	DisplayName *string
	AvatarURL   *string
}

// ContactUpdate 是联系方式的部分更新，nil 表示不修改。
type ContactUpdate struct {
	Phone *string
	Email *string
}

// CheckoutState 是结算事务内读到并加锁的数据：账户已选中的购物车项（含商品、店铺、规格的当前状态，商品和规格行已加排他锁）
// 和账户全部未使用的券（已加锁，Status 是存储的状态 unused；是否已过期由调用方按事务内的当前时间判断）。
type CheckoutState struct {
	Lines   []CartLine
	Coupons []OwnedCoupon
}

// PlannedItem 是计划写入的订单项；CartItemID 是它来自的购物车项，结算成功后删除。
type PlannedItem struct {
	domain.OrderItem
	CartItemID string
}

// PlannedOrder 是一个店铺的订单。ID、订单号、状态和时间由 Store 填写；UserCouponIDs 是用在这个订单上的券
// （平台券可以同时用在多个订单上，记录在第一个订单上）。
type PlannedOrder struct {
	MerchantID     string
	TotalAmount    domain.Money
	DiscountAmount domain.Money
	PayAmount      domain.Money
	Items          []PlannedItem
	UserCouponIDs  []string
}

// CheckoutPlan 是调用方根据 CheckoutState 算出的下单方案。
type CheckoutPlan struct {
	Orders          []PlannedOrder
	PaymentDeadline time.Time
}

// CheckoutResult 是一次结算的结果。Replayed 为 true 表示同一幂等键已经成功结算过，Orders 是那次的订单（当前状态）。
type CheckoutResult struct {
	RequestID string
	Replayed  bool
	Orders    []OrderDetail
}

// OrderDetail 是订单及其订单项和店铺名称。Payment 是最新的支付单，ReviewIDs 是已评价订单项的评价 ID（order_item_id → review_id），
// 只有 GetOrder / UpdateOrder 填写。
type OrderDetail struct {
	domain.Order
	MerchantName string
	Payment      *domain.Payment
	ReviewIDs    map[string]string
}

// OrderQuery 列出订单。AccountID / MerchantID / Status / OrderNo 为空表示不按该项过滤。按 created_at、order_id 倒序。
type OrderQuery struct {
	AccountID  string
	MerchantID string
	Status     domain.OrderStatus
	OrderNo    string
	Page       Page
}

// MaxCheckoutLines 是一次结算最多的购物车项数，与购物车行数上限一致。
const MaxCheckoutLines = MaxCartLines

// ValidateCheckoutPlan 是两种实现写入订单前共用的校验：每个订单至少一项，订单项来自已选中的购物车项且不重复，
// 数量和单价与加锁后读到的购物车项一致（单价即当前规格价），数量不超过库存，金额自洽（实付 = 原价 − 优惠，原价 = Σ 单价 × 数量），用到的券属于本账户且未使用（店铺券只用在本店订单上）。
func ValidateCheckoutPlan(st CheckoutState, plan CheckoutPlan) error {
	if len(plan.Orders) == 0 {
		return fmt.Errorf("%w: 没有要创建的订单", ErrInvalid)
	}
	lines := map[string]CartLine{}
	for _, l := range st.Lines {
		lines[l.CartItemID] = l
	}
	coupons := map[string]OwnedCoupon{}
	for _, c := range st.Coupons {
		if c.Status == domain.UserCouponUnused {
			coupons[c.UserCouponID] = c
		}
	}
	usedItems, usedCoupons := map[string]bool{}, map[string]bool{}
	for _, o := range plan.Orders {
		if len(o.Items) == 0 || o.MerchantID == "" {
			return fmt.Errorf("%w: 订单缺少店铺或商品", ErrInvalid)
		}
		var total domain.Money
		for _, it := range o.Items {
			l, ok := lines[it.CartItemID]
			switch {
			case !ok || usedItems[it.CartItemID]:
				return fmt.Errorf("%w: 订单项 %s 不是未结算的已选购物车项", ErrInvalid, it.CartItemID)
			case it.ProductID != l.ProductID || it.SkuID != l.SkuID || it.MerchantID != o.MerchantID || l.MerchantID != o.MerchantID:
				return fmt.Errorf("%w: 订单项 %s 与购物车项不一致", ErrInvalid, it.CartItemID)
			case it.Quantity != l.Quantity || it.Price != l.UnitPrice:
				return fmt.Errorf("%w: 订单项 %s 的数量或单价与购物车当前数据不一致", ErrInvalid, it.CartItemID)
			case it.Quantity <= 0 || !l.SkuFound || it.Quantity > l.StockQuantity:
				return fmt.Errorf("%w: 订单项 %s 数量 %d 超过库存", ErrInvalid, it.CartItemID, it.Quantity)
			}
			usedItems[it.CartItemID] = true
			total += it.Price.Mul(it.Quantity)
		}
		if total != o.TotalAmount || o.DiscountAmount < 0 || o.DiscountAmount > o.TotalAmount || o.PayAmount != o.TotalAmount-o.DiscountAmount {
			return fmt.Errorf("%w: 订单金额不一致（原价 %s / %s，优惠 %s，实付 %s）", ErrInvalid, total, o.TotalAmount, o.DiscountAmount, o.PayAmount)
		}
		inOrder := map[string]bool{}
		for _, id := range o.UserCouponIDs {
			c, ok := coupons[id]
			// 店铺券只能用在本店订单上且只用一次；平台券可以分摊到多个订单，但每个订单只记一次。
			shopCoupon := c.Coupon.MerchantID != ""
			if !ok || inOrder[id] || (shopCoupon && (usedCoupons[id] || c.Coupon.MerchantID != o.MerchantID)) {
				return fmt.Errorf("%w: 券 %s 不可用", ErrInvalid, id)
			}
			inOrder[id], usedCoupons[id] = true, true
		}
	}
	return nil
}

// NewOrderNo 生成订单号：BS + 下单时间（UTC，到秒）+ 6 位随机数字。唯一键冲突时由调用方重试。
func NewOrderNo(at time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1000000
	return fmt.Sprintf("BS%s%06d", at.UTC().Format("20060102150405"), n)
}

// CheckOrderUpdate 校验 UpdateOrder 的修改：不可变字段不变，订单与支付单的状态变化符合状态机（相同状态视为未变）。
func CheckOrderUpdate(before, after domain.Order, payBefore, payAfter *domain.Payment) error {
	if before.OrderID != after.OrderID || before.OrderNo != after.OrderNo || before.AccountID != after.AccountID ||
		before.MerchantID != after.MerchantID || before.CheckoutRequestID != after.CheckoutRequestID || before.TotalAmount != after.TotalAmount ||
		before.DiscountAmount != after.DiscountAmount || before.PayAmount != after.PayAmount || !before.CreatedAt.Equal(after.CreatedAt) {
		return fmt.Errorf("%w: 不能修改订单的归属、金额或编号", ErrInvalid)
	}
	if before.Status != after.Status {
		if err := before.Status.CanTransitionTo(after.Status); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if payBefore != nil && payAfter != nil {
		if payBefore.PaymentID != payAfter.PaymentID || payBefore.Amount != payAfter.Amount || payBefore.OrderID != payAfter.OrderID {
			return fmt.Errorf("%w: 不能修改支付单的编号或金额", ErrInvalid)
		}
		if payBefore.Status != payAfter.Status {
			if err := payBefore.Status.CanTransitionTo(payAfter.Status); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
		}
	}
	// 订单与支付单的状态必须同步：已支付的订单必须有已支付的支付单，取消的订单不能有已支付的支付单。
	if payAfter != nil {
		paidOrder := after.Status == domain.OrderPaid || after.Status == domain.OrderShipped || after.Status == domain.OrderCompleted
		if paidOrder != (payAfter.Status == domain.PaymentPaid) {
			return fmt.Errorf("%w: 订单状态 %s 与支付单状态 %s 不一致", ErrInvalid, after.Status, payAfter.Status)
		}
	}
	return nil
}

// NewAuthToken 生成 32 字节随机 token（base64url，无填充）及其摘要。
func NewAuthToken() (token, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:]) // Go 1.24 起 crypto/rand.Read 不会返回错误
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, HashAuthToken(token)
}

// HashAuthToken 返回 token 的 SHA-256 hex，即 auth_tokens.token_hash。
func HashAuthToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewAccount 是创建账户的输入；AccountID 为空时自动生成。
type NewAccount struct {
	AccountID    string
	Username     string
	PasswordHash string
	DisplayName  string
	AvatarURL    string
	Phone        string
	Email        string
	Role         domain.Role
	MerchantID   string
}

// Store 是数据访问边界。所有方法都接收 context：事务通过 WithTx 放进 context 传递，
// 在 fn 内用同一个 ctx 调用的方法自动加入该事务；嵌套 WithTx 复用外层事务。
type Store interface {
	Ping(ctx context.Context) error
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error

	// 账户。Get* 不返回已软删的账户。GetAccountByUsername 额外返回 bcrypt 密码哈希，仅供认证使用。
	CreateAccount(ctx context.Context, in NewAccount) (domain.Account, error)
	GetAccount(ctx context.Context, accountID string) (domain.Account, error)
	GetAccountByUsername(ctx context.Context, username string) (domain.Account, string, error)
	UpdatePasswordHash(ctx context.Context, accountID, passwordHash string) error
	// UpdateAccountProfile / UpdateAccountContact 只修改非 nil 字段（空串表示清空），在存储层合并，并发的部分更新互不覆盖。
	UpdateAccountProfile(ctx context.Context, accountID string, in ProfileUpdate) (domain.Account, error)
	UpdateAccountContact(ctx context.Context, accountID string, in ContactUpdate) (domain.Account, error)
	// SoftDeleteAccount 在一个事务内写 deleted_at 并删除该账户的全部 token；已注销的账户返回 ErrNotFound。
	SoftDeleteAccount(ctx context.Context, accountID string) error

	// 登录 token。只保存摘要，明文只在 CreateAuthToken 的返回值里出现一次。
	// GetAccountByToken 只认未过期 token 且账户未软删；账户状态（inactive/risk）由调用方决定如何处理。
	CreateAuthToken(ctx context.Context, accountID string, ttl time.Duration) (token string, expiresAt time.Time, err error)
	GetAccountByToken(ctx context.Context, token string) (domain.Account, error)
	DeleteAuthToken(ctx context.Context, token string) error

	// 目录（只读部分；写操作随 2.x 节点补充）。
	ListCategories(ctx context.Context) ([]domain.Category, error)
	// GetProduct 不区分状态，供内部使用；公开接口用 GetVisibleProduct。
	GetProduct(ctx context.Context, productID string) (domain.Product, error)

	// 公开目录。“可见”= 商品 active 且所属商家 active；不可见的商品按不存在处理（ErrNotFound）。
	ListActiveMerchants(ctx context.Context, page Page) ([]domain.Merchant, int, error)
	// GetMerchant 不区分状态，供管理端校验使用。
	GetMerchant(ctx context.Context, merchantID string) (domain.Merchant, error)
	SearchVisibleProducts(ctx context.Context, q ProductSearch) ([]CatalogProduct, int, error)
	GetVisibleProduct(ctx context.Context, productID string) (CatalogProduct, error)
	// ListVisibleReviews 只返回 visible 评价，按 created_at、review_id 倒序。
	ListVisibleReviews(ctx context.Context, productID string, page Page) ([]PublicReview, int, error)
	// ListActivePromotions 只返回 active、在有效期内且所属商家（如有）为 active 的促销，按 created_at、promotion_id 倒序。
	ListActivePromotions(ctx context.Context, q PromotionQuery) ([]domain.PromotionRule, int, error)

	// 商家商品管理。归属和状态规则由调用方校验；这里保证写入的商品与 SKU 一致（见 Product.SyncFromSKUs）。
	// ListMerchantProducts 不含 deleted，按 updated_at、product_id 倒序，SKUs 已加载。
	ListMerchantProducts(ctx context.Context, q MerchantProductQuery) ([]domain.Product, int, error)
	// CreateProduct 在一个事务内写入商品及其 SKU；ProductID / SKU ID 为空时自动生成。
	CreateProduct(ctx context.Context, p domain.Product) (domain.Product, error)
	// UpdateProduct 在事务中锁定商品行并加载 SKU，交给 fn 修改后整体写回：SKU 按 ID 更新或新增，
	// 不在新列表中的删除。fn 返回错误或写回失败时整体回滚。商品不存在返回 ErrNotFound。
	UpdateProduct(ctx context.Context, productID string, fn func(p *domain.Product) error) (domain.Product, error)

	// 私有文件元数据。CreateStoredFile 前用 ValidateStoredFile 校验；FileID 为空时自动生成，object_key 重复返回 ErrConflict。
	CreateStoredFile(ctx context.Context, f domain.StoredFile) (domain.StoredFile, error)
	GetStoredFile(ctx context.Context, fileID string) (domain.StoredFile, error)

	// 知识文档。商家只能看自己的文档由调用方保证；MerchantID 为空串表示平台资料。
	// CreateDocument 插入新文档（DocumentID 为空时生成），同一商家内容 hash 重复返回 ErrConflict（键 KeyDocumentMerchantHash）。
	CreateDocument(ctx context.Context, d domain.KnowledgeDocument) (domain.KnowledgeDocument, error)
	GetDocument(ctx context.Context, documentID string) (domain.KnowledgeDocument, error)
	GetDocumentByHash(ctx context.Context, merchantID, contentHash string) (domain.KnowledgeDocument, error)
	// UpdateDocument 锁定文档交给 fn 修改后写回（不含分块）；状态变化必须符合 DocumentStatus 状态机，否则返回 ErrInvalid。
	UpdateDocument(ctx context.Context, documentID string, fn func(d *domain.KnowledgeDocument) error) (domain.KnowledgeDocument, error)
	// ReplaceDocumentChunks 在一个事务内用 chunks 替换文档的全部分块，并把文档从 indexing 改为 indexed、更新分块数。
	// 文档不在 indexing 状态返回 ErrInvalid；chunk 的 ID、文档、商家、商品字段由这里填写。
	ReplaceDocumentChunks(ctx context.Context, documentID string, chunks []domain.KnowledgeChunk) (domain.KnowledgeDocument, error)
	// ListDocuments 按 updated_at、document_id 倒序，结果不含正文（Content 为空）。
	ListDocuments(ctx context.Context, q DocumentQuery) ([]domain.KnowledgeDocument, int, error)
	// ListDocumentChunks 按 chunk_index 顺序返回文档的全部分块。
	ListDocumentChunks(ctx context.Context, documentID string) ([]domain.KnowledgeChunk, error)
	// SearchKnowledge 召回可检索的分块：文档为 indexed，所属商家营业中（或平台资料），关联的商品（如有）公开可见。
	SearchKnowledge(ctx context.Context, q KnowledgeQuery) ([]KnowledgeHit, error)

	// 购物车。所有方法都按 accountID 限定，访问不到其他账户的购物车项（返回 ErrNotFound）。
	// ListCartLines 按加入时间（同一毫秒加入的按商品、规格 ID）顺序返回购物车项及其商品、规格、店铺的当前信息（商品或规格可能已失效）。
	ListCartLines(ctx context.Context, accountID string) ([]CartLine, error)
	// AddCartItem 原子加购：在事务中锁定账户行和 (账户, 商品, 规格) 对应的购物车行，并在同一事务中重新读取商品、店铺和规格的
	// 当前状态（MySQL 对这些行加共享锁，商家并发修改商品要等本事务结束，或在本事务读取前已提交）。fn 收到这一状态
	// （line.Quantity 为当前数量，没有该行时为 0）和购物车现有行数，返回新的数量；fn 出错时不做任何修改。
	// 同一账户的加购串行执行：不会产生重复行、丢失数量或死锁。账户不存在返回 ErrNotFound。
	AddCartItem(ctx context.Context, accountID, productID, skuID string, fn func(line CartLine, lines int) (int, error)) (domain.CartItem, error)
	// UpdateCartItem 锁定本人的购物车项，并在同一事务中重新读取（共享锁）其商品、店铺和规格的当前状态，交给 fn 修改数量和选中状态。
	UpdateCartItem(ctx context.Context, accountID, cartItemID string, fn func(item *domain.CartItem, line CartLine) error) (domain.CartItem, error)
	DeleteCartItem(ctx context.Context, accountID, cartItemID string) error

	// 优惠券。ListClaimableCoupons 返回 at 时刻有效（active、在有效期内、店铺券的店铺营业中）的券，按 created_at、coupon_id 倒序。
	ListClaimableCoupons(ctx context.Context, at time.Time, page Page) ([]domain.Coupon, int, error)
	// CountClaimed 返回账户对每张券已领取的张数。
	CountClaimed(ctx context.Context, accountID string, couponIDs []string) (map[string]int, error)
	// ListUserCoupons 返回账户的券（含券的定义），按领取时间、user_coupon_id 倒序。状态按 at 计算：未使用但已过期的为 expired。
	ListUserCoupons(ctx context.Context, q UserCouponQuery) ([]OwnedCoupon, int, error)
	// ClaimCoupon 在事务中锁定券行后检查有效期、总量和每人限领，写入领取记录并增加已领数量。
	// 券不存在 ErrNotFound；不可领 ErrCouponUnavailable / ErrCouponSoldOut / ErrCouponLimitReached。
	ClaimCoupon(ctx context.Context, accountID, couponID string, at time.Time) (OwnedCoupon, error)

	// 订单。
	// Checkout 结算下单，全部在一个事务内：锁定账户行（与加购串行）→ 按 (账户, 幂等键) 查找已成功的结算，有则直接返回
	// 那次的订单（Replayed）→ 锁定已选中的购物车项、商品（排他锁，按 ID 顺序）、店铺（共享锁）、规格（排他锁）和账户未使用的券 →
	// 交给 fn 计价并给出方案 → 用 ValidateCheckoutPlan 校验 → 写订单、订单项、支付单（pending，到期时间为方案的 PaymentDeadline）→
	// 扣减规格库存并重算商品库存 → 标记券已使用 → 删除已结算的购物车项 → 记录结算请求。fn 或任何一步出错时整体回滚，什么都不写。
	// fn 收到的 ctx 属于这个事务：计价要读的数据（分类、有效活动）和“当前时间”都应在 fn 里读取，与加锁后的购物车、库存、券一致，
	// 支付期限也从这里开始计算，不受排队等锁的时间影响。
	Checkout(ctx context.Context, accountID, idempotencyKey string, fn func(ctx context.Context, st CheckoutState) (CheckoutPlan, error)) (CheckoutResult, error)
	// GetOrder 返回订单详情（含最新支付单和评价 ID），不检查归属。
	GetOrder(ctx context.Context, orderID string) (OrderDetail, error)
	ListOrders(ctx context.Context, q OrderQuery) ([]OrderDetail, int, error)
	// UpdateOrder 锁定订单（同一次结算的订单先锁结算请求行）和最新支付单，交给 fn 修改状态和时间后写回；修改须通过 CheckOrderUpdate。
	// 订单变为 cancelled 时在同一事务内：支付单改为 closed（由 fn 设置）、把订单项数量加回仍存在的规格并重算商品库存、退回只用在本订单上的
	// 店铺券；平台券在同一次结算的订单全部取消后退回。订单不存在返回 ErrNotFound；fn 出错时不做任何修改。
	UpdateOrder(ctx context.Context, orderID string, fn func(o *domain.Order, p *domain.Payment) error) (OrderDetail, error)
	// ListExpiredOrderIDs 返回 at 时刻已过支付期限仍待支付的订单 ID，按期限先后，最多 limit 个。
	ListExpiredOrderIDs(ctx context.Context, at time.Time, limit int) ([]string, error)
	// CreateReview 为订单项写评价：锁定订单（共享锁）后交给 fn 检查订单状态并填写评价内容。订单不存在或不属于 accountID 返回 ErrNotFound，
	// 订单项不属于该订单返回 ErrOrderItemNotFound，同一订单项已评价返回 ErrConflict（键 KeyReviewOrderItem）。
	// 评价的 ID、订单、订单项、商品、规格、账户和时间由这里填写。
	CreateReview(ctx context.Context, accountID, orderID, orderItemID string, fn func(o domain.Order, item domain.OrderItem) (domain.ProductReview, error)) (domain.ProductReview, error)

	// ApplySeed 在一个事务内写入开发种子：按主键“不存在才插入”，可重复执行；违反其他唯一键时整体回滚并返回 ErrConflict。
	ApplySeed(ctx context.Context, data SeedData) (SeedResult, error)
}

// SeedAccount 是带密码哈希的种子账户。
type SeedAccount struct {
	domain.Account
	PasswordHash string
}

// SeedData 是一份完整的开发数据集；Product.SKUs 与 Order.Items 随父记录一起写入。
type SeedData struct {
	Accounts    []SeedAccount
	Merchants   []domain.Merchant
	Categories  []domain.Category
	Products    []domain.Product
	Documents   []domain.KnowledgeDocument
	Chunks      []domain.KnowledgeChunk
	Promotions  []domain.PromotionRule
	Coupons     []domain.Coupon
	UserCoupons []domain.UserCoupon
	Orders      []domain.Order
	Payments    []domain.Payment
	Reviews     []domain.ProductReview
}

// SeedResult 记录每张表本次新插入的行数；重复执行时应全部为 0。
type SeedResult map[string]int

func (r SeedResult) Total() int {
	n := 0
	for _, v := range r {
		n += v
	}
	return n
}

// ValidateNewAccount 是两种实现共用的输入校验。
func ValidateNewAccount(in NewAccount) error {
	switch {
	case in.Username == "":
		return fmt.Errorf("%w: username 不能为空", ErrInvalid)
	case in.PasswordHash == "":
		return fmt.Errorf("%w: password_hash 不能为空", ErrInvalid)
	case !in.Role.Valid():
		return fmt.Errorf("%w: role %q 不合法", ErrInvalid, in.Role)
	case in.Role == domain.RoleMerchant && in.MerchantID == "":
		return fmt.Errorf("%w: 商家账户必须关联 merchant_id", ErrInvalid)
	case in.Role != domain.RoleMerchant && in.MerchantID != "":
		return fmt.Errorf("%w: 只有商家账户可以关联 merchant_id", ErrInvalid)
	}
	return nil
}

// ValidatePromotion 是两种实现写入促销规则前共用的校验：折扣率必须在 [0, 1]。
func ValidatePromotion(p domain.PromotionRule) error {
	if err := p.DiscountRate.Validate(); err != nil {
		return fmt.Errorf("%w: 促销 %s: %v", ErrInvalid, p.PromotionID, err)
	}
	return nil
}

// ValidateStoredFile 是两种实现写入文件元数据前共用的校验：一条记录必须完整描述一个已写入的对象。
func ValidateStoredFile(f domain.StoredFile) error {
	switch {
	case f.AccountID == "":
		return fmt.Errorf("%w: 文件缺少 account_id", ErrInvalid)
	case f.ObjectKey == "":
		return fmt.Errorf("%w: 文件缺少 object_key", ErrInvalid)
	case f.MimeType == "":
		return fmt.Errorf("%w: 文件缺少 mime_type", ErrInvalid)
	case f.SizeBytes <= 0:
		return fmt.Errorf("%w: 文件大小必须大于 0", ErrInvalid)
	case !isSHA256Hex(f.ContentHash):
		return fmt.Errorf("%w: content_hash 必须是 64 位小写 hex", ErrInvalid)
	case f.StorageProvider == "":
		return fmt.Errorf("%w: 文件缺少 storage_provider", ErrInvalid)
	}
	return nil
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidateDocument 是两种实现写入知识文档前共用的校验。
func ValidateDocument(d domain.KnowledgeDocument) error {
	switch {
	case d.Title == "":
		return fmt.Errorf("%w: 文档标题不能为空", ErrInvalid)
	case d.DocType == "":
		return fmt.Errorf("%w: 文档类型不能为空", ErrInvalid)
	case !isSHA256Hex(d.ContentHash):
		return fmt.Errorf("%w: content_hash 必须是 64 位小写 hex", ErrInvalid)
	case !d.Status.Valid():
		return fmt.Errorf("%w: 文档状态 %q 不合法", ErrInvalid, d.Status)
	}
	return nil
}

// CheckDocumentTransition 校验状态变化（相同状态视为未变化）。
func CheckDocumentTransition(from, to domain.DocumentStatus) error {
	if from == to {
		return nil
	}
	if err := from.CanTransitionTo(to); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// PrepareChunks 是两种实现写入分块前共用的处理：按顺序编号，补齐 ID、文档、商家、商品、来源和时间，并校验内容非空。
func PrepareChunks(d domain.KnowledgeDocument, chunks []domain.KnowledgeChunk, now time.Time) ([]domain.KnowledgeChunk, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("%w: 文档至少要有一个分块", ErrInvalid)
	}
	out := make([]domain.KnowledgeChunk, len(chunks))
	for i, c := range chunks {
		if c.Content == "" || c.Title == "" {
			return nil, fmt.Errorf("%w: 第 %d 个分块缺少标题或内容", ErrInvalid, i)
		}
		c.ChunkID = domain.NewID(domain.PrefixChunk)
		c.DocumentID, c.MerchantID, c.ProductID, c.ChunkIndex, c.CreatedAt = d.DocumentID, d.MerchantID, d.ProductID, i, now
		if c.Source == "" {
			c.Source = d.Title
		}
		out[i] = c
	}
	return out, nil
}

// EffectiveCouponStatus 按时间计算用户券的状态：未使用但券已到期（at ≥ end_at）的视为 expired。
func EffectiveCouponStatus(stored string, c domain.Coupon, at time.Time) string {
	if stored == domain.UserCouponUnused && !at.Before(c.EndAt) {
		return domain.UserCouponExpired
	}
	return stored
}

// CheckClaim 是两种实现领券时共用的判断：券 active、在 [start_at, end_at) 内、店铺券的店铺营业中，
// 总量（total_count 为 0 表示不限）和每人限领都还有余量。owned 是该账户已领取的张数。
func CheckClaim(c domain.Coupon, merchantStatus domain.EntityStatus, owned int, at time.Time) error {
	switch {
	case c.Status != domain.StatusActive || at.Before(c.StartAt) || !at.Before(c.EndAt):
		return ErrCouponUnavailable
	case c.MerchantID != "" && merchantStatus != domain.StatusActive:
		return ErrCouponUnavailable
	case c.TotalCount > 0 && c.ClaimedCount >= c.TotalCount:
		return ErrCouponSoldOut
	case owned >= max(c.PerUserLimit, 1):
		return ErrCouponLimitReached
	}
	return nil
}
