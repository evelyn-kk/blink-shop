// Package httpapi 负责 HTTP 路由、中间件和 JSON 输入输出。
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/ingest"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/risk"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// SettingsProvider 返回当前生效的 HTTP 配置；由 configcenter.HTTPSettingsProvider 实现。
type SettingsProvider interface {
	Current(ctx context.Context) configcenter.HTTPSettings
}

// ReadinessCheck 是 /ready 检查的一项依赖，Check 返回 nil 表示可用。
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// Options 是 Server 的全部依赖。
type Options struct {
	Logger    *slog.Logger
	Settings  SettingsProvider
	Readiness []ReadinessCheck
	Store     store.Store
	// ObjectStore 保存私有上传文件；nil 表示未配置，文件接口返回 object_storage_unavailable。
	ObjectStore objectstore.Store
	// VectorIndex 是知识分块的向量索引；nil 表示只用关键词检索。
	VectorIndex rag.VectorIndex
	// ProductIndex 是商品向量索引；nil 表示商品只用关键词召回。商品增改删和上下架后异步同步。
	ProductIndex rag.ProductIndex
	// ImageSearch 是图片找商品；nil 表示图搜不可用（接口返回 503，导购如实说明）。商品图随商品变更异步重建。
	ImageSearch *imagesearch.Service
	// Fetcher 抓取知识采集的 URL；nil 时使用带 SSRF 防护的默认实现。测试可替换。
	Fetcher ingest.URLFetcher
	// AvatarDir 是头像文件目录，不存在时在首次上传时创建。
	AvatarDir string
	// PasswordCost 是 bcrypt 成本，0 表示 bcrypt.DefaultCost；测试可调低以提速。
	PasswordCost int
	// Configs 是管理端的配置读写入口；nil 表示未启用，配置接口返回 503。
	Configs ConfigAdmin
	// Now 返回当前时间（判断促销是否有效等），nil 表示 time.Now；测试用来固定时间。
	Now func() time.Time
	// PaymentTimeout 是下单后的支付期限，0 表示 DefaultPaymentTimeout（30 分钟）。
	PaymentTimeout time.Duration
	// AgentRunner 执行导购运行；nil 表示默认的规则运行器（agent.RuleRunner：风险词检查、规则规划、工具白名单）。测试可替换。
	AgentRunner agent.Runner
	// RiskWords 返回当前生效的导购风险词（configcenter.RiskBlockedWords）；nil 表示用 risk.blocked_words 的默认词表。
	RiskWords func(ctx context.Context) []string
	// LLM 是模型调用入口；nil 表示没有配置模型，导购只走规则。
	LLM llm.Provider
	// AgentSettings 返回当前的模型开关、模型名、轮数和工具白名单（configcenter.AgentSettingsNow）；nil 表示默认值。
	AgentSettings func(ctx context.Context) agent.ModelSettings
	// AgentRunTimeout 是一次运行的最长时间，0 表示 DefaultAgentRunTimeout。
	AgentRunTimeout time.Duration
	// SSEHeartbeat 是流式响应的心跳间隔，0 表示 DefaultSSEHeartbeat。
	SSEHeartbeat time.Duration
}

type Server struct {
	logger         *slog.Logger
	settings       SettingsProvider
	readiness      []ReadinessCheck
	store          store.Store
	objects        objectstore.Store
	ingestor       *ingest.Service
	avatars        avatarDir
	passwords      *passwordHasher
	now            func() time.Time
	paymentTimeout time.Duration
	runner         agent.Runner
	productIndex   rag.ProductIndex
	imageSearch    *imagesearch.Service
	vectorSync     sync.WaitGroup
	syncs          productSyncs
	runTimeout     time.Duration
	heartbeat      time.Duration
	runs           *runRegistry
	configs        ConfigAdmin
	mux            *http.ServeMux
	routeAccess    map[string]access // 路由 pattern → 访问规则，RBAC 矩阵测试据此核对
	ipLimiter      *rateLimiter
	accountLimiter *rateLimiter
	loginLimiter   *rateLimiter
}

const readinessTimeout = 2 * time.Second

func NewServer(opts Options) *Server {
	s := &Server{
		logger:         opts.Logger,
		settings:       opts.Settings,
		readiness:      opts.Readiness,
		store:          opts.Store,
		objects:        opts.ObjectStore,
		avatars:        avatarDir{root: opts.AvatarDir},
		passwords:      newPasswordHasher(opts.PasswordCost),
		now:            opts.Now,
		paymentTimeout: opts.PaymentTimeout,
		runner:         opts.AgentRunner,
		productIndex:   opts.ProductIndex,
		imageSearch:    opts.ImageSearch,
		runTimeout:     opts.AgentRunTimeout,
		heartbeat:      opts.SSEHeartbeat,
		runs:           newRunRegistry(),
		configs:        opts.Configs,
		mux:            http.NewServeMux(),
		routeAccess:    map[string]access{},
		ipLimiter:      newRateLimiter(time.Minute),
		accountLimiter: newRateLimiter(time.Minute),
		loginLimiter:   newRateLimiter(time.Minute),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.store == nil {
		panic("httpapi: Options.Store is required")
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	if s.runner == nil {
		words := opts.RiskWords
		if words == nil {
			words = func(context.Context) []string { return risk.Split(configcenter.KeyRiskBlockedWords.Default) }
		}
		s.runner = agent.NewRuleRunner(agent.Deps{LLM: opts.LLM, Settings: opts.AgentSettings, Store: s.store, Shop: s.shop(), ProductIndex: opts.ProductIndex,
			ImageSearch: serverImageSearch{s}, Retriever: rag.NewRetriever(s.store, opts.VectorIndex, s.logger), Risk: risk.WordList{Words: words}, Logger: s.logger,
			Now: func() time.Time { return s.now() }})
	}
	if s.runTimeout <= 0 {
		s.runTimeout = DefaultAgentRunTimeout
	}
	if s.heartbeat <= 0 {
		s.heartbeat = DefaultSSEHeartbeat
	}
	if s.paymentTimeout <= 0 {
		s.paymentTimeout = DefaultPaymentTimeout
	}
	fetcher := opts.Fetcher
	if fetcher == nil {
		fetcher = ingest.NewFetcher(ingest.FetcherOptions{})
	}
	s.ingestor = ingest.NewService(s.store, opts.VectorIndex, fetcher, s.logger, s.now)
	s.routes()
	return s
}

// ModelSettingsFrom 把配置中心的 Agent 设置转成 agent 包的类型（cmd/api 和测试共用）。
func ModelSettingsFrom(s configcenter.AgentSettings) agent.ModelSettings {
	return agent.ModelSettings{PlannerEnabled: s.PlannerEnabled, AgentEnabled: s.AgentEnabled, PlannerModel: s.PlannerModel, AgentModel: s.AgentModel,
		Timeout: s.Timeout, MaxToolRounds: s.MaxToolRounds, ToolPolicy: s.ToolPolicy, RerankEnabled: s.RerankEnabled, SummaryEnabled: s.SummaryEnabled,
		MemoryTurns: s.MemoryTurns}
}

// shop 返回购物车、结算、订单的业务层（与导购 Agent 的工具共用同一套规则）。每次按当前的 Store 和时钟构造，
// 测试替换 Store 或时钟后立即生效。
func (s *Server) shop() *shop.Service { return shop.New(s.store, s.now, s.paymentTimeout) }

// routes 注册全部路由。访问规则含义见 auth.go 的 access；RBAC 矩阵见 backend/README.md。
func (s *Server) routes() {
	s.handle("GET /api/v1/health", accessPublic, s.handleHealth)
	s.handle("GET /api/v1/ready", accessPublic, s.handleReady)

	s.handle("POST /api/v1/auth/register", accessPublic, s.handleRegister)
	s.handle("POST /api/v1/auth/login", accessPublic, s.handleLogin)
	s.handle("POST /api/v1/auth/logout", accessSession, s.handleLogout)
	s.handle("GET /api/v1/auth/me", accessSession, s.handleMe)

	s.handle("GET /api/v1/account/profile", accessAccount, s.handleGetProfile)
	s.handle("PATCH /api/v1/account/profile", accessAccount, s.handleUpdateProfile)
	s.handle("PATCH /api/v1/account/contact", accessAccount, s.handleUpdateContact)
	s.handle("DELETE /api/v1/account", accessAccount, s.handleDeleteAccount)

	s.handle("POST /api/v1/uploads/avatar", accessAccount, s.handleUploadAvatar)
	s.handle("GET /api/v1/uploads/avatar/{name}", accessPublic, s.handleGetAvatar)
	s.handle("GET /api/v1/assets/{path...}", accessPublic, s.handleGetAsset)

	s.handle("POST /api/v1/files", accessAccount, s.handleUploadFile)
	s.handle("POST /api/v1/search/image", accessAccount, s.handleSearchImage)
	s.handle("GET /api/v1/files/{id}", accessAccount, s.handleGetFile)

	s.handle("GET /api/v1/categories/tree", accessPublic, s.handleCategoryTree)
	s.handle("GET /api/v1/merchants", accessPublic, s.handleListMerchants)
	s.handle("GET /api/v1/products", accessPublic, s.handleListProducts)
	s.handle("GET /api/v1/products/{id}", accessPublic, s.handleGetProduct)
	s.handle("GET /api/v1/products/{id}/skus", accessPublic, s.handleListSKUs)
	s.handle("GET /api/v1/products/{id}/reviews", accessPublic, s.handleListReviews)
	s.handle("GET /api/v1/promotions", accessPublic, s.handleListPromotions)

	s.handle("GET /api/v1/merchant/products", accessMerchant, s.handleListMerchantProducts)
	s.handle("POST /api/v1/merchant/products", accessMerchant, s.handleCreateMerchantProduct)
	s.handle("GET /api/v1/merchant/products/{id}", accessMerchant, s.handleGetMerchantProduct)
	s.handle("PATCH /api/v1/merchant/products/{id}", accessMerchant, s.handleUpdateMerchantProduct)
	s.handle("DELETE /api/v1/merchant/products/{id}", accessMerchant, s.handleDeleteMerchantProduct)

	s.handle("GET /api/v1/cart", accessUser, s.handleGetCart)
	s.handle("GET /api/v1/cart/discount-preview", accessUser, s.handleDiscountPreview)
	s.handle("POST /api/v1/cart/items", accessUser, s.handleAddCartItem)
	s.handle("PATCH /api/v1/cart/items/{id}", accessUser, s.handleUpdateCartItem)
	s.handle("DELETE /api/v1/cart/items/{id}", accessUser, s.handleDeleteCartItem)
	s.handle("GET /api/v1/coupons/available", accessUser, s.handleListAvailableCoupons)
	s.handle("GET /api/v1/coupons/mine", accessUser, s.handleListMyCoupons)
	s.handle("POST /api/v1/coupons/{action}", accessUser, s.handleCouponAction)

	s.handle("GET /api/v1/agent/sessions", accessUser, s.handleListSessions)
	s.handle("POST /api/v1/agent/sessions", accessUser, s.handleCreateSession)
	s.handle("GET /api/v1/agent/sessions/{id}", accessUser, s.handleGetSession)
	s.handle("PATCH /api/v1/agent/sessions/{id}", accessUser, s.handleUpdateSession)
	s.handle("DELETE /api/v1/agent/sessions/{id}", accessUser, s.handleDeleteSession)
	s.handle("POST /api/v1/agent/sessions/{action}", accessUser, s.handleSessionAction)
	s.handle("POST /api/v1/agent/sessions/{id}/{action}", accessUser, s.handleSessionSubAction)
	s.handle("GET /api/v1/agent/runs/{id}/trace", accessUser, s.handleRunTrace)
	s.handle("POST /api/v1/agent/runs/{action}", accessUser, s.handleRunAction)

	s.handle("GET /api/v1/orders", accessUser, s.handleListMyOrders)
	s.handle("POST /api/v1/orders:checkout", accessUser, s.handleCheckout)
	s.handle("GET /api/v1/orders/{id}", accessUser, s.handleGetMyOrder)
	s.handle("POST /api/v1/orders/{action}", accessUser, s.handleOrderAction)
	s.handle("POST /api/v1/orders/{id}/items/{action}", accessUser, s.handleOrderItemAction)

	s.handle("GET /api/v1/merchant/promotions", accessMerchant, s.handleListMerchantPromotions)
	s.handle("POST /api/v1/merchant/promotions", accessMerchant, s.handleCreateMerchantPromotion)
	s.handle("GET /api/v1/merchant/promotions/{id}", accessMerchant, s.handleGetMerchantPromotion)
	s.handle("PATCH /api/v1/merchant/promotions/{id}", accessMerchant, s.handleUpdateMerchantPromotion)
	s.handle("GET /api/v1/merchant/reviews", accessMerchant, s.handleListMerchantReviews)
	s.handle("POST /api/v1/merchant/reviews/{action}", accessMerchant, s.handleMerchantReviewAction)

	s.handle("GET /api/v1/merchant/orders", accessMerchant, s.handleListMerchantOrders)
	s.handle("GET /api/v1/merchant/orders/{id}", accessMerchant, s.handleGetMerchantOrder)
	s.handle("PATCH /api/v1/merchant/orders/{id}", accessMerchant, s.handleUpdateMerchantOrder)

	s.handle("GET /api/v1/admin/orders", accessAdmin, s.handleListAdminOrders)
	s.handle("GET /api/v1/admin/orders/{id}", accessAdmin, s.handleGetAdminOrder)
	s.handle("PATCH /api/v1/admin/orders/{id}", accessAdmin, s.handleUpdateAdminOrder)

	s.handle("GET /api/v1/merchant/documents", accessMerchant, s.handleListMerchantDocuments)
	s.handle("POST /api/v1/merchant/documents", accessMerchant, s.handleCreateMerchantDocument)
	s.handle("GET /api/v1/merchant/documents/{id}", accessMerchant, s.handleGetMerchantDocument)
	s.handle("POST /api/v1/merchant/unstructured-ingestions", accessMerchant, s.handleMerchantIngestion)

	s.handle("GET /api/v1/admin/accounts", accessAdmin, s.handleListAdminAccounts)
	s.handle("PATCH /api/v1/admin/accounts/{id}", accessAdmin, s.handleUpdateAdminAccount)
	s.handle("GET /api/v1/admin/merchants", accessAdmin, s.handleListAdminMerchants)
	s.handle("PATCH /api/v1/admin/merchants/{id}", accessAdmin, s.handleUpdateAdminMerchant)
	s.handle("GET /api/v1/admin/products", accessAdmin, s.handleListAdminProducts)
	s.handle("PATCH /api/v1/admin/products/{id}", accessAdmin, s.handleUpdateAdminProduct)
	s.handle("GET /api/v1/admin/promotions", accessAdmin, s.handleListAdminPromotions)
	s.handle("PATCH /api/v1/admin/promotions/{id}", accessAdmin, s.handleUpdateAdminPromotion)
	s.handle("GET /api/v1/admin/reviews", accessAdmin, s.handleListAdminReviews)
	s.handle("PATCH /api/v1/admin/reviews/{id}", accessAdmin, s.handleUpdateAdminReview)
	s.handle("GET /api/v1/admin/configs", accessAdmin, s.handleListAdminConfigs)
	s.handle("PATCH /api/v1/admin/configs/{key}", accessAdmin, s.handleUpdateAdminConfig)
	s.handle("GET /api/v1/admin/risk/overview", accessAdmin, s.handleRiskOverview)
	s.handle("GET /api/v1/admin/audit-logs", accessAdmin, s.handleListAuditLogs)

	s.handle("GET /api/v1/admin/documents", accessAdmin, s.handleListAdminDocuments)
	s.handle("GET /api/v1/admin/documents/{id}", accessAdmin, s.handleGetAdminDocument)
	s.handle("POST /api/v1/admin/unstructured-ingestions", accessAdmin, s.handleAdminIngestion)
}

// Handler 返回带完整中间件链的 handler。顺序（外 → 内）：
// request id/配置 → 访问日志 → panic 恢复 → CORS → IP 限流 → 请求体限制 → 超时 → 认证 → 账号限流 → 路由（含访问规则）。
func (s *Server) Handler() http.Handler {
	var h http.Handler = http.HandlerFunc(s.dispatch)
	h = s.withAccountRateLimit(h)
	h = s.withAuth(h)
	h = s.withTimeout(h)
	h = s.withBodyLimit(h)
	h = s.withIPRateLimit(h)
	h = s.withCORS(h)
	h = s.withRecover(h)
	h = s.withAccessLog(h)
	h = s.withRequestContext(h)
	return h
}

// dispatch 交给 ServeMux 路由；未命中的路径和方法统一输出 JSON 错误，而不是 ServeMux 默认的纯文本。
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if h, pattern := s.mux.Handler(r); pattern == "" {
		probe := &statusProbe{header: http.Header{}}
		h.ServeHTTP(probe, r)
		if probe.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", probe.header.Get("Allow"))
			writeError(w, ErrMethodNotAllow)
			return
		}
		writeError(w, ErrNotFound)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// statusProbe 只记录 ServeMux 内置 404/405 handler 写出的状态码和 Allow 头。
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header         { return p.header }
func (p *statusProbe) Write(b []byte) (int, error) { return len(b), nil }
func (p *statusProbe) WriteHeader(status int)      { p.status = status }

type healthResponse struct {
	Status string `json:"status"`
}

// handleHealth 只表示进程存活，不检查任何依赖。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// handleReady 依次检查依赖（当前为 MySQL）；任一不可用返回 503。具体错误只写日志。
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	checks := make(map[string]string, len(s.readiness))
	for _, c := range s.readiness {
		if err := c.Check(ctx); err != nil {
			s.logger.WarnContext(r.Context(), "readiness check failed",
				"request_id", requestIDFromContext(r.Context()), "check", c.Name, "error", err)
			writeError(w, &APIError{Status: http.StatusServiceUnavailable, Code: ErrNotReady.Code, Message: "依赖 " + c.Name + " 不可用"})
			return
		}
		checks[c.Name] = "ok"
	}
	writeJSON(w, http.StatusOK, readyResponse{Status: "ready", Checks: checks})
}
