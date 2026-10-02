package domain

import (
	"errors"
	"time"
)

// ---------- 身份 ----------

type Role string

const (
	RoleUser     Role = "user"
	RoleMerchant Role = "merchant"
	RoleAdmin    Role = "admin"
)

func (r Role) Valid() bool { return r == RoleUser || r == RoleMerchant || r == RoleAdmin }

// Account 不含密码哈希；哈希只在 Store 与认证模块之间传递，永远不进入 JSON。
type Account struct {
	AccountID   string       `json:"account_id"`
	Username    string       `json:"username"`
	DisplayName string       `json:"display_name"`
	AvatarURL   string       `json:"avatar_url"`
	Phone       string       `json:"phone"`
	Email       string       `json:"email"`
	Role        Role         `json:"role"`
	MerchantID  string       `json:"merchant_id,omitempty"`
	Status      EntityStatus `json:"status"`
	DeletedAt   *time.Time   `json:"-"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// AuthToken 只保存 token 的 SHA-256 摘要。
type AuthToken struct {
	TokenHash string    `json:"-"`
	AccountID string    `json:"account_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ---------- 目录 ----------

type Merchant struct {
	MerchantID   string       `json:"merchant_id"`
	Name         string       `json:"name"`
	LogoURL      string       `json:"logo_url"`
	Description  string       `json:"description"`
	ServicePhone string       `json:"service_phone"`
	Status       EntityStatus `json:"status"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// Category 两级分类；ParentID 为空表示一级分类。
type Category struct {
	CategoryID string     `json:"category_id"`
	ParentID   string     `json:"parent_id"`
	Name       string     `json:"name"`
	SortOrder  int        `json:"sort_order"`
	Children   []Category `json:"children,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type ProductAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

type Product struct {
	ProductID       string             `json:"product_id"`
	MerchantID      string             `json:"merchant_id"`
	CategoryID      string             `json:"category_id"`
	Name            string             `json:"name"`
	Brand           string             `json:"brand"`
	ImageURL        string             `json:"image_url"`
	ImageURLs       []string           `json:"image_urls"`
	Price           Money              `json:"price"`
	MarketPrice     Money              `json:"market_price"`
	StockQuantity   int                `json:"stock_quantity"`
	StockStatus     StockStatus        `json:"stock_status"`
	Tags            []string           `json:"tags"`
	SellingPoints   []string           `json:"selling_points"`
	RecommendReason string             `json:"recommend_reason"`
	RiskNotes       []string           `json:"risk_notes"`
	Attributes      []ProductAttribute `json:"attributes"`
	SuitableFor     []string           `json:"suitable_for"`
	NotSuitableFor  []string           `json:"not_suitable_for"`
	Description     string             `json:"description"`
	Status          ProductStatus      `json:"status"`
	SortOrder       int                `json:"sort_order"`
	SKUs            []ProductSKU       `json:"skus,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// Sellable 商品处于上架状态且有库存。
func (p Product) Sellable() bool { return p.Status == ProductActive && p.StockQuantity > 0 }

// ErrSKUInvariant 表示 SKU 列表不满足“至少一个、恰好一个默认、属于本商品”。
var ErrSKUInvariant = errors.New("商品必须有至少一个 SKU，且恰好一个默认 SKU")

// SyncFromSKUs 按 SKU 重新计算商品的派生字段：售价 = 默认 SKU 价格，库存 = 各 SKU 库存之和，
// 库存状态由数量推导（商品和每个 SKU 都是）。写库前调用，保证这些字段不会与 SKU 不一致。
func (p *Product) SyncFromSKUs() error {
	defaults, total := 0, 0
	for i := range p.SKUs {
		sku := &p.SKUs[i]
		if sku.ProductID != p.ProductID {
			return ErrSKUInvariant
		}
		sku.StockStatus = StockStatusOf(sku.StockQuantity)
		total += sku.StockQuantity
		if sku.IsDefault {
			defaults++
			p.Price = sku.Price
		}
	}
	if len(p.SKUs) == 0 || defaults != 1 {
		return ErrSKUInvariant
	}
	p.StockQuantity = total
	p.StockStatus = StockStatusOf(total)
	return nil
}

// ProductSKU 是库存粒度：(product_id, sku_id)。
type ProductSKU struct {
	SkuID         string            `json:"sku_id"`
	ProductID     string            `json:"product_id"`
	SkuName       string            `json:"sku_name"`
	Price         Money             `json:"price"`
	StockQuantity int               `json:"stock_quantity"`
	StockStatus   StockStatus       `json:"stock_status"`
	Specs         map[string]string `json:"specs"`
	IsDefault     bool              `json:"is_default"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// ---------- 文件与知识 ----------

type StoredFile struct {
	FileID          string    `json:"file_id"`
	AccountID       string    `json:"account_id"`
	ObjectKey       string    `json:"-"`
	MimeType        string    `json:"mime_type"`
	SizeBytes       int64     `json:"size_bytes"`
	ContentHash     string    `json:"content_hash"`
	StorageProvider string    `json:"storage_provider"`
	SourceURL       string    `json:"source_url,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type KnowledgeDocument struct {
	DocumentID  string         `json:"document_id"`
	MerchantID  string         `json:"merchant_id"`
	Title       string         `json:"title"`
	DocType     string         `json:"doc_type"`
	Content     string         `json:"content,omitempty"`
	Status      DocumentStatus `json:"status"`
	ChunkCount  int            `json:"chunk_count"`
	SourceURL   string         `json:"source_url,omitempty"`
	ContentHash string         `json:"content_hash"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	ErrorReason string         `json:"error_reason,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type KnowledgeChunk struct {
	ChunkID    string    `json:"chunk_id"`
	DocumentID string    `json:"document_id"`
	MerchantID string    `json:"merchant_id"`
	ProductID  string    `json:"product_id,omitempty"`
	ChunkIndex int       `json:"chunk_index"`
	Title      string    `json:"title"`
	Content    string    `json:"content"`
	Source     string    `json:"source,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ---------- 交易 ----------

type CartItem struct {
	CartItemID string    `json:"cart_item_id"`
	AccountID  string    `json:"account_id"`
	ProductID  string    `json:"product_id"`
	SkuID      string    `json:"sku_id"`
	Quantity   int       `json:"quantity"`
	Selected   bool      `json:"selected"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CheckoutRequest 记录一次结算的幂等键与结果，重复请求返回首次成功的订单。
type CheckoutRequest struct {
	RequestID      string    `json:"request_id"`
	AccountID      string    `json:"account_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Status         string    `json:"status"`
	OrderIDs       []string  `json:"order_ids"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Order struct {
	OrderID           string      `json:"order_id"`
	OrderNo           string      `json:"order_no"`
	AccountID         string      `json:"account_id"`
	MerchantID        string      `json:"merchant_id"`
	CheckoutRequestID string      `json:"checkout_request_id,omitempty"`
	Status            OrderStatus `json:"status"`
	TotalAmount       Money       `json:"total_amount"`
	DiscountAmount    Money       `json:"discount_amount"`
	PayAmount         Money       `json:"pay_amount"`
	PaymentDeadlineAt *time.Time  `json:"payment_deadline_at,omitempty"`
	PaidAt            *time.Time  `json:"paid_at,omitempty"`
	ShippedAt         *time.Time  `json:"shipped_at,omitempty"`
	CompletedAt       *time.Time  `json:"completed_at,omitempty"`
	ClosedAt          *time.Time  `json:"closed_at,omitempty"`
	CancelReason      string      `json:"cancel_reason,omitempty"`
	Items             []OrderItem `json:"items,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

// OrderItem 冻结下单时的商品名称、图片和价格。
type OrderItem struct {
	OrderItemID  string    `json:"order_item_id"`
	OrderID      string    `json:"order_id"`
	ProductID    string    `json:"product_id"`
	SkuID        string    `json:"sku_id"`
	Name         string    `json:"name"`
	SkuName      string    `json:"sku_name"`
	ImageURL     string    `json:"image_url"`
	Price        Money     `json:"price"`
	Quantity     int       `json:"quantity"`
	MerchantID   string    `json:"merchant_id"`
	MerchantName string    `json:"merchant_name"`
	CreatedAt    time.Time `json:"created_at"`
}

type Payment struct {
	PaymentID     string        `json:"payment_id"`
	OrderID       string        `json:"order_id"`
	AccountID     string        `json:"account_id"`
	Amount        Money         `json:"amount"`
	Status        PaymentStatus `json:"status"`
	Method        string        `json:"method"`
	TransactionNo string        `json:"transaction_no,omitempty"`
	ExpiresAt     time.Time     `json:"expires_at"`
	PaidAt        *time.Time    `json:"paid_at,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// ---------- 营销与评价 ----------

// 促销/券的作用范围与类型。
const (
	ScopePlatform = "platform"
	ScopeMerchant = "merchant"
	ScopeProduct  = "product"
	ScopeCategory = "category"

	PromotionFullReduction = "full_reduction"
	PromotionDiscount      = "discount"
	CouponFixedAmount      = "fixed_amount"

	UserCouponUnused  = "unused"
	UserCouponUsed    = "used"
	UserCouponExpired = "expired"

	ReviewVisible = "visible"
	ReviewHidden  = "hidden"
)

type PromotionRule struct {
	PromotionID     string       `json:"promotion_id"`
	Name            string       `json:"name"`
	Scope           string       `json:"scope"`
	MerchantID      string       `json:"merchant_id,omitempty"`
	ProductID       string       `json:"product_id,omitempty"`
	CategoryID      string       `json:"category_id,omitempty"`
	Type            string       `json:"type"`
	ThresholdAmount Money        `json:"threshold_amount"`
	DiscountAmount  Money        `json:"discount_amount"`
	DiscountRate    Rate         `json:"discount_rate"`
	Stackable       bool         `json:"stackable"`
	StartAt         time.Time    `json:"start_at"`
	EndAt           time.Time    `json:"end_at"`
	Status          EntityStatus `json:"status"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

type Coupon struct {
	CouponID        string       `json:"coupon_id"`
	Name            string       `json:"name"`
	Scope           string       `json:"scope"`
	MerchantID      string       `json:"merchant_id,omitempty"`
	Type            string       `json:"type"`
	ThresholdAmount Money        `json:"threshold_amount"`
	DiscountAmount  Money        `json:"discount_amount"`
	TotalCount      int          `json:"total_count"`
	ClaimedCount    int          `json:"claimed_count"`
	PerUserLimit    int          `json:"per_user_limit"`
	StartAt         time.Time    `json:"start_at"`
	EndAt           time.Time    `json:"end_at"`
	Status          EntityStatus `json:"status"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

type UserCoupon struct {
	UserCouponID string     `json:"user_coupon_id"`
	CouponID     string     `json:"coupon_id"`
	AccountID    string     `json:"account_id"`
	Status       string     `json:"status"`
	OrderID      string     `json:"order_id,omitempty"`
	ClaimedAt    time.Time  `json:"claimed_at"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
}

type ProductReview struct {
	ReviewID          string     `json:"review_id"`
	OrderID           string     `json:"order_id"`
	OrderItemID       string     `json:"order_item_id"`
	ProductID         string     `json:"product_id"`
	SkuID             string     `json:"sku_id"`
	AccountID         string     `json:"account_id"`
	Rating            int        `json:"rating"`
	Content           string     `json:"content"`
	Tags              []string   `json:"tags"`
	Status            string     `json:"status"`
	MerchantReply     string     `json:"merchant_reply,omitempty"`
	MerchantRepliedAt *time.Time `json:"merchant_replied_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// ---------- 导购会话 ----------

type ChatSession struct {
	SessionID     string     `json:"session_id"`
	AccountID     string     `json:"account_id"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary,omitempty"`
	MessageCount  int        `json:"message_count"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
	PinnedAt      *time.Time `json:"pinned_at,omitempty"`
	DeletedAt     *time.Time `json:"-"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Attachment struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
	URL      string `json:"url,omitempty"`
}

// UserMessage 的 (account_id, session_id, client_message_id) 唯一，保证流式请求幂等。
type UserMessage struct {
	MessageID       string       `json:"message_id"`
	SessionID       string       `json:"session_id"`
	AccountID       string       `json:"account_id"`
	ClientMessageID string       `json:"client_message_id"`
	Content         string       `json:"content"`
	Attachments     []Attachment `json:"attachments"`
	CreatedAt       time.Time    `json:"created_at"`
}

// AgentRun 的 (account_id, message_id) 唯一：一条用户消息只对应一次运行。
type AgentRun struct {
	RunID          string           `json:"run_id"`
	SessionID      string           `json:"session_id"`
	MessageID      string           `json:"message_id"`
	AccountID      string           `json:"account_id"`
	TraceID        string           `json:"trace_id"`
	Status         RunStatus        `json:"status"`
	Content        string           `json:"content,omitempty"`
	Blocks         []map[string]any `json:"blocks,omitempty"`
	Followups      []string         `json:"followups,omitempty"`
	PromptVersions map[string]int   `json:"prompt_versions,omitempty"`
	ErrorCode      string           `json:"error_code,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type AgentTraceEvent struct {
	TraceEventID string         `json:"trace_event_id"`
	RunID        string         `json:"run_id"`
	TraceID      string         `json:"trace_id"`
	AccountID    string         `json:"account_id"`
	Stage        string         `json:"stage"`
	EventType    string         `json:"event_type"`
	Model        string         `json:"model,omitempty"`
	Status       string         `json:"status"`
	DurationMS   int64          `json:"duration_ms"`
	Error        string         `json:"error,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// ---------- 治理 ----------

const (
	PromptDraft     = "draft"
	PromptActive    = "active"
	PromptArchived  = "archived"
	ConfigValueText = "string"
)

// AgentPrompt 的 (prompt_key, version) 唯一；同一 key 同时只有一个 active 版本。
type AgentPrompt struct {
	PromptID    string     `json:"prompt_id"`
	PromptKey   string     `json:"prompt_key"`
	Title       string     `json:"title"`
	Content     string     `json:"content"`
	Status      string     `json:"status"`
	Version     int        `json:"version"`
	Description string     `json:"description,omitempty"`
	CreatedBy   string     `json:"created_by"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type AgentPromptPublishRecord struct {
	RecordID    string    `json:"record_id"`
	PromptKey   string    `json:"prompt_key"`
	PromptID    string    `json:"prompt_id"`
	Version     int       `json:"version"`
	PublishedBy string    `json:"published_by"`
	Target      string    `json:"target"`
	Result      string    `json:"result"`
	CreatedAt   time.Time `json:"created_at"`
}

// AppConfig 是动态配置项；Secret 为 true 时 Value 在列表/日志/trace 中永远是掩码。
type AppConfig struct {
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	ValueType   string    `json:"value_type"`
	Secret      bool      `json:"secret"`
	Description string    `json:"description,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SecretMask 是密钥类配置对外展示的固定值。
const SecretMask = "******"

// Masked 返回可对外展示的配置：密钥类只保留是否已设置。
func (c AppConfig) Masked() AppConfig {
	if c.Secret && c.Value != "" {
		c.Value = SecretMask
	}
	return c
}
