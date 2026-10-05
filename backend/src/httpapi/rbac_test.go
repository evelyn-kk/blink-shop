package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// expectedRoutes 是当前全部路由的访问规则，必须与 backend/README.md 的 RBAC 矩阵一致。
// 新增路由时同时更新这里和 README；漏写会让 TestRouteAccessTable 失败。
var expectedRoutes = map[string]string{
	"GET /api/v1/health":                            "public",
	"GET /api/v1/ready":                             "public",
	"POST /api/v1/auth/register":                    "public",
	"POST /api/v1/auth/login":                       "public",
	"POST /api/v1/auth/logout":                      "session",
	"GET /api/v1/auth/me":                           "session",
	"GET /api/v1/account/profile":                   "account",
	"PATCH /api/v1/account/profile":                 "account",
	"PATCH /api/v1/account/contact":                 "account",
	"DELETE /api/v1/account":                        "account",
	"POST /api/v1/uploads/avatar":                   "account",
	"GET /api/v1/uploads/avatar/{name}":             "public",
	"GET /api/v1/assets/{path...}":                  "public",
	"POST /api/v1/files":                            "account",
	"GET /api/v1/files/{id}":                        "account",
	"GET /api/v1/categories/tree":                   "public",
	"GET /api/v1/merchants":                         "public",
	"GET /api/v1/products":                          "public",
	"GET /api/v1/products/{id}":                     "public",
	"GET /api/v1/products/{id}/skus":                "public",
	"GET /api/v1/products/{id}/reviews":             "public",
	"GET /api/v1/promotions":                        "public",
	"GET /api/v1/merchant/products":                 "merchant",
	"POST /api/v1/merchant/products":                "merchant",
	"GET /api/v1/merchant/products/{id}":            "merchant",
	"PATCH /api/v1/merchant/products/{id}":          "merchant",
	"DELETE /api/v1/merchant/products/{id}":         "merchant",
	"GET /api/v1/cart":                              "user",
	"GET /api/v1/cart/discount-preview":             "user",
	"POST /api/v1/cart/items":                       "user",
	"PATCH /api/v1/cart/items/{id}":                 "user",
	"DELETE /api/v1/cart/items/{id}":                "user",
	"GET /api/v1/coupons/available":                 "user",
	"GET /api/v1/coupons/mine":                      "user",
	"POST /api/v1/coupons/{action}":                 "user",
	"GET /api/v1/orders":                            "user",
	"POST /api/v1/orders:checkout":                  "user",
	"GET /api/v1/orders/{id}":                       "user",
	"POST /api/v1/orders/{action}":                  "user",
	"POST /api/v1/orders/{id}/items/{action}":       "user",
	"GET /api/v1/merchant/promotions":               "merchant",
	"POST /api/v1/merchant/promotions":              "merchant",
	"GET /api/v1/merchant/promotions/{id}":          "merchant",
	"PATCH /api/v1/merchant/promotions/{id}":        "merchant",
	"GET /api/v1/merchant/reviews":                  "merchant",
	"POST /api/v1/merchant/reviews/{action}":        "merchant",
	"GET /api/v1/merchant/orders":                   "merchant",
	"GET /api/v1/merchant/orders/{id}":              "merchant",
	"PATCH /api/v1/merchant/orders/{id}":            "merchant",
	"GET /api/v1/admin/orders":                      "admin",
	"GET /api/v1/admin/orders/{id}":                 "admin",
	"PATCH /api/v1/admin/orders/{id}":               "admin",
	"GET /api/v1/merchant/documents":                "merchant",
	"POST /api/v1/merchant/documents":               "merchant",
	"GET /api/v1/merchant/documents/{id}":           "merchant",
	"POST /api/v1/merchant/unstructured-ingestions": "merchant",
	"GET /api/v1/admin/documents":                   "admin",
	"GET /api/v1/admin/documents/{id}":              "admin",
	"POST /api/v1/admin/unstructured-ingestions":    "admin",
}

func TestRouteAccessTable(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	got := map[string]string{}
	for pattern, a := range ts.routeAccess {
		got[pattern] = a.name
	}
	var problems []string
	for pattern, want := range expectedRoutes {
		if got[pattern] != want {
			problems = append(problems, pattern+": got "+got[pattern]+", want "+want)
		}
	}
	for pattern := range got {
		if _, ok := expectedRoutes[pattern]; !ok {
			problems = append(problems, pattern+": 未列入 RBAC 矩阵")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// TestRoutePrefixRoles：按路径前缀约定的角色（docs/03），防止把 admin/merchant 接口误注册成宽松规则。
func TestRoutePrefixRoles(t *testing.T) {
	prefixRules := []struct {
		prefix string
		want   string
	}{
		{"/api/v1/admin/", "admin"},
		{"/api/v1/merchant/", "merchant"},
		{"/api/v1/cart", "user"},
		{"/api/v1/orders", "user"},
		{"/api/v1/coupons/", "user"},
		{"/api/v1/agent/", "user"},
		{"/api/v1/speech/", "user"},
		{"/api/v1/account", "account"},
		{"/api/v1/files", "account"},
	}
	ts := newTestServer(t, nil, nil, nil)
	for pattern, a := range ts.routeAccess {
		_, path, _ := strings.Cut(pattern, " ")
		for _, rule := range prefixRules {
			if strings.HasPrefix(path, rule.prefix) && a.name != rule.want {
				t.Errorf("%s 使用 %s，按前缀应为 %s", pattern, a.name, rule.want)
			}
		}
	}
}

// TestRBACMatrix 用探针路由逐条验证角色矩阵和商家资源归属。真实的 cart/merchant/admin 接口在后续节点加入，
// 它们复用同样的 access 规则与 requireMerchantOwner。
func TestRBACMatrix(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	ts.handle("GET /api/v1/admin/probe", accessAdmin, ok)
	ts.handle("POST /api/v1/cart/probe", accessUser, ok)
	ts.handle("GET /api/v1/merchant/probe", accessMerchant, ok)
	ts.handle("PATCH /api/v1/merchant/shops/{merchant}/probe", accessMerchant, func(w http.ResponseWriter, r *http.Request) {
		acc, _ := accountFromContext(r.Context())
		if err := requireMerchantOwner(acc, r.PathValue("merchant")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	tokens := map[string]string{"anonymous": ""}
	for role, username := range map[string]string{
		"user": seed.UserUsername, "merchant": seed.MerchantUsername, "admin": seed.AdminUsername,
	} {
		tokens[role] = ts.login(t, username, seed.DevPassword).Token
	}
	// 没有关联店铺的商家账户（数据异常）不能通过商家规则。
	tokens["merchant_no_shop"] = ts.tokenFor(t, ts.addAccount(t, "orphan_merchant", "h", domain.RoleMerchant, "", domain.StatusActive))
	tokens["inactive_user"] = ts.tokenFor(t, ts.addAccount(t, "off_user", "h", domain.RoleUser, "", domain.StatusInactive))
	tokens["risk_user"] = ts.tokenFor(t, ts.addAccount(t, "risk_user", "h", domain.RoleUser, "", domain.StatusRisk))

	own := "/api/v1/merchant/shops/" + seed.DigitalMerchant + "/probe"
	other := "/api/v1/merchant/shops/" + seed.HomeMerchant + "/probe"
	matrix := []struct {
		who, method, path string
		status            int
		code              string
	}{
		{"anonymous", "GET", "/api/v1/admin/probe", 401, "unauthorized"},
		{"user", "GET", "/api/v1/admin/probe", 403, "forbidden"},
		{"merchant", "GET", "/api/v1/admin/probe", 403, "forbidden"},
		{"admin", "GET", "/api/v1/admin/probe", 204, ""},

		{"anonymous", "POST", "/api/v1/cart/probe", 401, "unauthorized"},
		{"user", "POST", "/api/v1/cart/probe", 204, ""},
		{"merchant", "POST", "/api/v1/cart/probe", 403, "forbidden"},
		{"admin", "POST", "/api/v1/cart/probe", 403, "forbidden"},
		{"inactive_user", "POST", "/api/v1/cart/probe", 403, "account_inactive"},
		{"risk_user", "POST", "/api/v1/cart/probe", 403, "account_risk"},

		{"user", "GET", "/api/v1/merchant/probe", 403, "forbidden"},
		{"admin", "GET", "/api/v1/merchant/probe", 403, "forbidden"},
		{"merchant", "GET", "/api/v1/merchant/probe", 204, ""},
		{"merchant_no_shop", "GET", "/api/v1/merchant/probe", 403, "forbidden"},

		{"merchant", "PATCH", own, 204, ""},
		{"merchant", "PATCH", other, 403, "forbidden"},
		{"user", "PATCH", own, 403, "forbidden"},
	}
	for _, m := range matrix {
		t.Run(m.who+" "+m.method+" "+m.path, func(t *testing.T) {
			expectStatus(t, ts.call(t, m.method, m.path, tokens[m.who], nil), m.status, m.code)
		})
	}
}

func TestRequireMerchantOwner(t *testing.T) {
	merchant := domain.Account{Role: domain.RoleMerchant, MerchantID: "m_a"}
	cases := []struct {
		acc   domain.Account
		owner string
		ok    bool
	}{
		{merchant, "m_a", true},
		{merchant, "m_b", false},
		{merchant, "", false},
		{domain.Account{Role: domain.RoleMerchant}, "", false},
		{domain.Account{Role: domain.RoleAdmin}, "m_b", true},
		{domain.Account{Role: domain.RoleUser}, "m_a", false},
	}
	for _, c := range cases {
		if err := requireMerchantOwner(c.acc, c.owner); (err == nil) != c.ok {
			t.Errorf("requireMerchantOwner(%+v, %q) = %v, want ok=%v", c.acc, c.owner, err, c.ok)
		}
	}
}
