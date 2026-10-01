package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// 认证与授权相关错误。
var (
	ErrTokenInvalid      = &APIError{http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录"}
	ErrInvalidCredential = &APIError{http.StatusUnauthorized, "invalid_credential", "账号或密码错误"}
	ErrAccountInactive   = &APIError{http.StatusForbidden, "account_inactive", "账号已停用，请联系平台处理"}
	ErrAccountRisk       = &APIError{http.StatusForbidden, "account_risk", "账号存在风险，暂时只能浏览"}
	ErrLoginInactive     = &APIError{http.StatusForbidden, "account_inactive", "账号已停用，无法登录"}
	ErrLoginRisk         = &APIError{http.StatusForbidden, "account_risk", "账号存在风险，暂时无法登录"}
	ErrRoleForbidden     = &APIError{http.StatusForbidden, "forbidden", "当前账号无权访问"}
	ErrNotResourceOwner  = &APIError{http.StatusForbidden, "forbidden", "不能操作其他商家的资源"}
	ErrUsernameExists    = &APIError{http.StatusConflict, "username_exists", "账号已存在"}
	ErrLoginRateLimited  = &APIError{http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后再试"}
)

// access 是一条路由的访问规则：谁能访问，以及非 active 账户能否访问。
//
// 账户状态规则（docs/09 1.2）：
//   - active：按角色访问。
//   - inactive：只能访问 allowDisabled 的路由（me、logout），其余 403 account_inactive。
//   - risk：可以读（GET/HEAD）和访问 allowDisabled 的路由，任何写操作 403 account_risk。
//   - 已注销（软删）：token 已全部删除，等同未登录。
//
// inactive/risk 账户不能新登录，见 handleLogin。
type access struct {
	name          string // 用于 RBAC 矩阵测试和文档
	public        bool
	roles         []domain.Role
	allowDisabled bool
}

var allRoles = []domain.Role{domain.RoleUser, domain.RoleMerchant, domain.RoleAdmin}

var (
	accessPublic   = access{name: "public", public: true}
	accessSession  = access{name: "session", roles: allRoles, allowDisabled: true} // 查看/结束自己的会话
	accessAccount  = access{name: "account", roles: allRoles}
	accessUser     = access{name: "user", roles: []domain.Role{domain.RoleUser}}
	accessMerchant = access{name: "merchant", roles: []domain.Role{domain.RoleMerchant}}
	accessAdmin    = access{name: "admin", roles: []domain.Role{domain.RoleAdmin}}
)

// handle 注册路由并绑定访问规则。所有业务路由都必须通过它注册，不能直接用 s.mux。
func (s *Server) handle(pattern string, a access, h http.HandlerFunc) {
	if _, dup := s.routeAccess[pattern]; dup {
		panic("duplicate route " + pattern)
	}
	s.routeAccess[pattern] = a
	s.mux.Handle(pattern, s.authorize(a, h))
}

// authorize 在路由命中后按访问规则检查当前账户，通过才调用 handler。
func (s *Server) authorize(a access, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.public {
			next(w, r)
			return
		}
		acc, ok := accountFromContext(r.Context())
		if !ok {
			writeError(w, unauthorizedError(r))
			return
		}
		if err := checkStatus(acc, a, r.Method); err != nil {
			writeError(w, err)
			return
		}
		if !roleAllowed(acc, a.roles) {
			writeError(w, ErrRoleForbidden)
			return
		}
		next(w, r)
	}
}

func checkStatus(acc domain.Account, a access, method string) error {
	if a.allowDisabled {
		return nil
	}
	switch acc.Status {
	case domain.StatusActive:
		return nil
	case domain.StatusRisk:
		if method == http.MethodGet || method == http.MethodHead {
			return nil
		}
		return ErrAccountRisk
	default: // inactive 以及任何未知状态都按停用处理
		return ErrAccountInactive
	}
}

func roleAllowed(acc domain.Account, roles []domain.Role) bool {
	for _, role := range roles {
		if acc.Role != role {
			continue
		}
		// 商家账户必须关联店铺，否则无法做归属校验。
		return role != domain.RoleMerchant || acc.MerchantID != ""
	}
	return false
}

// requireMerchantOwner 校验商家资源归属：商家只能操作自己店铺的资源，管理员不受限，其他角色一律拒绝。
// merchant/admin 共用的写接口在角色检查通过后还必须调用它。
func requireMerchantOwner(acc domain.Account, resourceMerchantID string) error {
	switch {
	case acc.Role == domain.RoleAdmin:
		return nil
	case acc.Role == domain.RoleMerchant && acc.MerchantID != "" && acc.MerchantID == resourceMerchantID:
		return nil
	case acc.Role == domain.RoleMerchant:
		return ErrNotResourceOwner
	default:
		return ErrRoleForbidden
	}
}

// unauthorizedError：带了 token 但无效时提示“登录已失效”，没带时提示“请先登录”。
func unauthorizedError(r *http.Request) error {
	if r.Header.Get("Authorization") != "" {
		return ErrTokenInvalid
	}
	return ErrUnauthorized
}

func accountFromContext(ctx context.Context) (domain.Account, bool) {
	acc, ok := ctx.Value(ctxAccount).(domain.Account)
	return acc, ok
}

// bearerToken 解析 `Authorization: Bearer <token>`（scheme 不区分大小写）；格式不对返回空串。
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 256 || strings.ContainsAny(token, " \t") {
		return ""
	}
	return token
}

// withAuth 解析 Bearer token 并把账户放进 context。这里只做识别，不拒绝请求：
// 公开接口带过期 token 也照常访问；是否需要登录、角色和状态由路由上的 access 决定。
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		acc, err := s.store.GetAccountByToken(r.Context(), token)
		switch {
		case err == nil:
			if st, _ := r.Context().Value(ctxState).(*requestState); st != nil {
				st.accountID = acc.AccountID
			}
			r = r.WithContext(context.WithValue(r.Context(), ctxAccount, acc))
		case errors.Is(err, store.ErrNotFound):
			// 伪造、过期或已撤销：按未登录处理。
		default:
			s.logger.ErrorContext(r.Context(), "auth lookup failed",
				"request_id", requestIDFromContext(r.Context()), "error", err)
			writeError(w, ErrInternal)
			return
		}
		next.ServeHTTP(w, r)
	})
}
