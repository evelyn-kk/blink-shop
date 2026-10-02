// Package httpapi 负责 HTTP 路由、中间件和 JSON 输入输出。
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
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
	// AvatarDir 是头像文件目录，不存在时在首次上传时创建。
	AvatarDir string
	// PasswordCost 是 bcrypt 成本，0 表示 bcrypt.DefaultCost；测试可调低以提速。
	PasswordCost int
	// Now 返回当前时间（判断促销是否有效等），nil 表示 time.Now；测试用来固定时间。
	Now func() time.Time
}

type Server struct {
	logger         *slog.Logger
	settings       SettingsProvider
	readiness      []ReadinessCheck
	store          store.Store
	avatars        avatarDir
	passwords      *passwordHasher
	now            func() time.Time
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
		avatars:        avatarDir{root: opts.AvatarDir},
		passwords:      newPasswordHasher(opts.PasswordCost),
		now:            opts.Now,
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
	s.routes()
	return s
}

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
