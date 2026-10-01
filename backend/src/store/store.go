// Package store 定义业务层唯一依赖的数据访问边界。
// handler、Agent 等只面向 Store 接口；MySQL 实现在 mysqlstore，测试用内存实现在 memstore，
// 两者由 storetest 中同一套用例约束行为一致。
package store

import (
	"context"
	"errors"
	"fmt"

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

	// 目录（只读部分；写操作随 2.x 节点补充）。
	ListCategories(ctx context.Context) ([]domain.Category, error)
	GetProduct(ctx context.Context, productID string) (domain.Product, error)

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
