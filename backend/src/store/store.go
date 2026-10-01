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
	SearchVisibleProducts(ctx context.Context, q ProductSearch) ([]CatalogProduct, int, error)
	GetVisibleProduct(ctx context.Context, productID string) (CatalogProduct, error)
	// ListVisibleReviews 只返回 visible 评价，按 created_at、review_id 倒序。
	ListVisibleReviews(ctx context.Context, productID string, page Page) ([]PublicReview, int, error)
	// ListActivePromotions 只返回 active、在有效期内且所属商家（如有）为 active 的促销，按 created_at、promotion_id 倒序。
	ListActivePromotions(ctx context.Context, q PromotionQuery) ([]domain.PromotionRule, int, error)

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
