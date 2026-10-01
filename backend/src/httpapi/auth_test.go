package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// call 发送 JSON 请求；token 为空时不带 Authorization。
func (ts *testServer) call(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return ts.do(req)
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v; body=%s", v, err, rec.Body)
	}
	return v
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body)
	}
	if code != "" {
		if got := decodeError(t, rec); got.Code != code {
			t.Fatalf("code = %q, want %q (message %q)", got.Code, code, got.Message)
		}
	}
}

func (ts *testServer) login(t *testing.T, username, password string) sessionResponse {
	t.Helper()
	rec := ts.call(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": username, "password": password})
	expectStatus(t, rec, http.StatusOK, "")
	return decodeBody[sessionResponse](t, rec)
}

// addAccount 直接写入一个指定状态的账户（管理员改状态的接口在 5.3），返回其 ID。
func (ts *testServer) addAccount(t *testing.T, username, passwordHash string, role domain.Role, merchantID string, status domain.EntityStatus) string {
	t.Helper()
	id := "acct_t_" + username
	_, err := ts.mem.ApplySeed(context.Background(), store.SeedData{Accounts: []store.SeedAccount{{
		PasswordHash: passwordHash,
		Account: domain.Account{AccountID: id, Username: username, DisplayName: username, Role: role,
			MerchantID: merchantID, Status: status},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// tokenFor 绕过登录直接给账户发 token，用于 inactive/risk 账户（它们不能登录）。
func (ts *testServer) tokenFor(t *testing.T, accountID string) string {
	t.Helper()
	token, _, err := ts.mem.CreateAuthToken(context.Background(), accountID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func fastHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := storetest.FastHash(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRegisterLoginMeLogout(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)

	rec := ts.call(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"username": "new_user", "password": "Passw0rd!", "display_name": "  新用户  ",
	})
	expectStatus(t, rec, http.StatusCreated, "")
	reg := decodeBody[sessionResponse](t, rec)
	if reg.Token == "" || reg.TokenType != "Bearer" || reg.Account.Role != domain.RoleUser ||
		reg.Account.DisplayName != "新用户" || reg.Account.Status != domain.StatusActive {
		t.Fatalf("register response: %+v", reg)
	}
	if d := time.Until(reg.ExpiresAt); d < 23*time.Hour || d > 24*time.Hour {
		t.Fatalf("expires_at = %v, want about 24h from now", reg.ExpiresAt)
	}

	me := ts.call(t, http.MethodGet, "/api/v1/auth/me", reg.Token, nil)
	expectStatus(t, me, http.StatusOK, "")
	if got := decodeBody[domain.Account](t, me); got.AccountID != reg.Account.AccountID || got.Username != "new_user" {
		t.Fatalf("me = %+v", got)
	}

	// 登录得到另一个 token；登出只撤销当前 token。
	second := ts.login(t, "new_user", "Passw0rd!")
	if second.Token == reg.Token {
		t.Fatal("login reused the register token")
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/logout", reg.Token, nil), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/auth/me", reg.Token, nil), http.StatusUnauthorized, "unauthorized")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/auth/me", second.Token, nil), http.StatusOK, "")
	// 登出后再用同一个 token 登出：已失效，401。
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/logout", reg.Token, nil), http.StatusUnauthorized, "unauthorized")

	// 用户名大小写不敏感（与 MySQL 排序规则一致）。
	ts.login(t, "NEW_USER", "Passw0rd!")
}

func TestRegisterValidation(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	cases := []struct {
		name   string
		body   map[string]string
		status int
		code   string
	}{
		{"empty", map[string]string{}, 400, "invalid_argument"},
		{"short username", map[string]string{"username": "ab", "password": "Passw0rd!"}, 400, "invalid_argument"},
		{"long username", map[string]string{"username": strings.Repeat("a", 33), "password": "Passw0rd!"}, 400, "invalid_argument"},
		{"bad chars", map[string]string{"username": "bad user", "password": "Passw0rd!"}, 400, "invalid_argument"},
		{"chinese username", map[string]string{"username": "用户名用户", "password": "Passw0rd!"}, 400, "invalid_argument"},
		{"short password", map[string]string{"username": "okname", "password": "1234567"}, 400, "invalid_argument"},
		{"long password", map[string]string{"username": "okname", "password": strings.Repeat("p", 65)}, 400, "invalid_argument"},
		{"password over 72 bytes", map[string]string{"username": "okname", "password": strings.Repeat("密", 25)}, 400, "invalid_argument"},
		{"long display name", map[string]string{"username": "okname", "password": "Passw0rd!", "display_name": strings.Repeat("名", 33)}, 400, "invalid_argument"},
		{"control char name", map[string]string{"username": "okname", "password": "Passw0rd!", "display_name": "a\u0007b"}, 400, "invalid_argument"},
		{"duplicate", map[string]string{"username": seed.UserUsername, "password": "Passw0rd!"}, 409, "username_exists"},
		{"duplicate other case", map[string]string{"username": strings.ToUpper(seed.UserUsername), "password": "Passw0rd!"}, 409, "username_exists"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/register", "", c.body), c.status, c.code)
		})
	}
	// 显示名留空时用用户名。
	rec := ts.call(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{"username": "noname", "password": "Passw0rd!"})
	expectStatus(t, rec, http.StatusCreated, "")
	if got := decodeBody[sessionResponse](t, rec); got.Account.DisplayName != "noname" {
		t.Fatalf("display_name = %q", got.Account.DisplayName)
	}
}

func TestLoginFailures(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	hash := fastHash(t, "Passw0rd!")
	ts.addAccount(t, "off_user", hash, domain.RoleUser, "", domain.StatusInactive)
	ts.addAccount(t, "risk_user", hash, domain.RoleUser, "", domain.StatusRisk)
	deleted := ts.addAccount(t, "gone_user", hash, domain.RoleUser, "", domain.StatusActive)
	if err := ts.mem.SoftDeleteAccount(context.Background(), deleted); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, username, password string
		status                   int
		code                     string
	}{
		{"wrong password", seed.UserUsername, "wrong-password", 401, "invalid_credential"},
		{"unknown user", "nobody_here", "Passw0rd!", 401, "invalid_credential"},
		{"deleted user", "gone_user", "Passw0rd!", 401, "invalid_credential"},
		{"empty password", seed.UserUsername, "", 400, "invalid_argument"},
		{"empty username", "  ", "Passw0rd!", 400, "invalid_argument"},
		// 密码错误时不透露账户状态。
		{"inactive wrong password", "off_user", "wrong-password", 401, "invalid_credential"},
		{"inactive", "off_user", "Passw0rd!", 403, "account_inactive"},
		{"risk", "risk_user", "Passw0rd!", 403, "account_risk"},
	}
	var unknownMsg, wrongMsg string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := ts.call(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": c.username, "password": c.password})
			expectStatus(t, rec, c.status, c.code)
			switch c.name {
			case "wrong password":
				wrongMsg = decodeError(t, rec).Message
			case "unknown user":
				unknownMsg = decodeError(t, rec).Message
			}
		})
	}
	if wrongMsg == "" || wrongMsg != unknownMsg {
		t.Fatalf("用户不存在与密码错误的提示应一致: %q vs %q", unknownMsg, wrongMsg)
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/login", "", "not an object"), 400, "")
}

func TestTokenRejected(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	valid := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	cases := []struct {
		name, header, code string
	}{
		{"no header", "", "unauthorized"},
		{"forged", "Bearer forged-token-value", "unauthorized"},
		{"token hash instead of token", "Bearer " + store.HashAuthToken(valid), "unauthorized"},
		{"wrong scheme", "Basic " + valid, "unauthorized"},
		{"missing scheme", valid, "unauthorized"},
		{"empty bearer", "Bearer ", "unauthorized"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := ts.do(req)
			expectStatus(t, rec, http.StatusUnauthorized, c.code)
			wantMsg := ErrTokenInvalid.Message
			if c.header == "" {
				wantMsg = ErrUnauthorized.Message
			}
			if got := decodeError(t, rec).Message; got != wantMsg {
				t.Errorf("message = %q, want %q", got, wantMsg)
			}
		})
	}
	// scheme 不区分大小写。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "bearer "+valid)
	expectStatus(t, ts.do(req), http.StatusOK, "")
	// 公开接口带失效 token 照常访问。
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/health", "forged", nil), http.StatusOK, "")
}

func TestTokenExpires(t *testing.T) {
	ts := newTestServer(t, map[string]string{"AUTH_TOKEN_TTL": "1h"}, nil, nil)
	now := time.Now().UTC()
	ts.mem.SetClock(func() time.Time { return now })
	sess := ts.login(t, seed.UserUsername, seed.DevPassword)
	if !sess.ExpiresAt.Equal(now.Truncate(time.Millisecond).Add(time.Hour)) {
		t.Fatalf("expires_at = %v", sess.ExpiresAt)
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/auth/me", sess.Token, nil), http.StatusOK, "")
	now = now.Add(time.Hour)
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/auth/me", sess.Token, nil), http.StatusUnauthorized, "unauthorized")
}

func TestLoginRateLimitPerUsername(t *testing.T) {
	ts := newTestServer(t, map[string]string{"LOGIN_ATTEMPTS_PER_MINUTE": "2"}, nil, nil)
	attempt := func(username string) *httptest.ResponseRecorder {
		return ts.call(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": username, "password": "wrong-password"})
	}
	expectStatus(t, attempt(seed.UserUsername), 401, "invalid_credential")
	expectStatus(t, attempt(strings.ToUpper(seed.UserUsername)), 401, "invalid_credential")
	rec := attempt(seed.UserUsername)
	expectStatus(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
	// 其他用户名不受影响。
	expectStatus(t, attempt(seed.User2Username), 401, "invalid_credential")
}

func TestLegacyPasswordUpgraded(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	sum := sha256.Sum256([]byte("Legacy#2026"))
	id := ts.addAccount(t, "legacy_user", hex.EncodeToString(sum[:]), domain.RoleUser, "", domain.StatusActive)

	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "legacy_user", "password": "wrong"}), 401, "invalid_credential")
	if _, hash, _ := ts.mem.GetAccountByUsername(context.Background(), "legacy_user"); isBcrypt(hash) {
		t.Fatal("密码错误时不应升级哈希")
	}
	ts.login(t, "legacy_user", "Legacy#2026")
	_, hash, _ := ts.mem.GetAccountByUsername(context.Background(), "legacy_user")
	if !isBcrypt(hash) {
		t.Fatalf("登录成功后应升级为 bcrypt，当前 %q", hash[:8])
	}
	ts.login(t, "legacy_user", "Legacy#2026")
	if !strings.Contains(ts.logs.String(), `"action":"account.password_rehashed","account_id":"`+id+`"`) {
		t.Error("missing rehash audit log")
	}
}

func TestPasswordHasher(t *testing.T) {
	low, _ := storetest.FastHash("pw") // cost 4
	p := newPasswordHasher(5)
	if !p.needsRehash(low) || p.needsRehash(mustHash(t, p, "pw")) || !p.needsRehash(strings.Repeat("a", 64)) {
		t.Fatal("needsRehash")
	}
	sum := sha256.Sum256([]byte("pw"))
	legacy := hex.EncodeToString(sum[:])
	for hash, want := range map[string]bool{low: true, legacy: true, strings.ToUpper(legacy): true, "": false, "plain-pw": false, "pw": false} {
		if got := p.verify(hash, "pw"); got != want {
			t.Errorf("verify(%q) = %v, want %v", hash, got, want)
		}
	}
	if p.verify(low, "PW") {
		t.Error("wrong password accepted")
	}
}

func mustHash(t *testing.T, p *passwordHasher, pw string) string {
	t.Helper()
	h, err := p.hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestAccountStatusAccess 覆盖 inactive/risk 账户的访问范围；状态在每次请求时重新读取，管理员改状态立即生效。
func TestAccountStatusAccess(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	ts.handle("POST /api/v1/cart/items", accessUser, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	ts.handle("GET /api/v1/cart", accessUser, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	off := ts.tokenFor(t, ts.addAccount(t, "off_user", "h", domain.RoleUser, "", domain.StatusInactive))
	risk := ts.tokenFor(t, ts.addAccount(t, "risk_user", "h", domain.RoleUser, "", domain.StatusRisk))

	cases := []struct {
		method, path string
		offWant      int
		offCode      string
		riskWant     int
		riskCode     string
	}{
		{"GET", "/api/v1/auth/me", 200, "", 200, ""},
		{"GET", "/api/v1/account/profile", 403, "account_inactive", 200, ""},
		{"PATCH", "/api/v1/account/profile", 403, "account_inactive", 403, "account_risk"},
		{"PATCH", "/api/v1/account/contact", 403, "account_inactive", 403, "account_risk"},
		{"DELETE", "/api/v1/account", 403, "account_inactive", 403, "account_risk"},
		{"POST", "/api/v1/uploads/avatar", 403, "account_inactive", 403, "account_risk"},
		{"GET", "/api/v1/cart", 403, "account_inactive", 204, ""},
		{"POST", "/api/v1/cart/items", 403, "account_inactive", 403, "account_risk"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			body := map[string]string{"display_name": "x"}
			expectStatus(t, ts.call(t, c.method, c.path, off, body), c.offWant, c.offCode)
			expectStatus(t, ts.call(t, c.method, c.path, risk, body), c.riskWant, c.riskCode)
		})
	}
	// 登出始终可以。
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/logout", off, nil), 200, "")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/logout", risk, nil), 200, "")
}

func TestProfileAndContact(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token

	rec := ts.call(t, http.MethodPatch, "/api/v1/account/profile", tok, map[string]string{"display_name": " 小蓝 "})
	expectStatus(t, rec, 200, "")
	if got := decodeBody[domain.Account](t, rec); got.DisplayName != "小蓝" {
		t.Fatalf("display_name = %q", got.DisplayName)
	}
	rec = ts.call(t, http.MethodPatch, "/api/v1/account/profile", tok, map[string]string{"nickname": "昵称兼容"})
	expectStatus(t, rec, 200, "")
	if got := decodeBody[domain.Account](t, rec); got.DisplayName != "昵称兼容" {
		t.Fatalf("nickname fallback = %q", got.DisplayName)
	}
	for name, body := range map[string]map[string]string{
		"empty":           {},
		"blank":           {"display_name": "  "},
		"too long":        {"display_name": strings.Repeat("长", 25)},
		"foreign avatar":  {"avatar_url": "/api/v1/uploads/avatar/acct_seed_user2_0123456789abcdef.png"},
		"external avatar": {"avatar_url": "https://evil.example/a.png"},
		"missing avatar":  {"avatar_url": "/api/v1/uploads/avatar/acct_seed_user_0123456789abcdef.png"},
	} {
		t.Run("profile "+name, func(t *testing.T) {
			expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/account/profile", tok, body), 400, "invalid_argument")
		})
	}
	got := decodeBody[domain.Account](t, ts.call(t, http.MethodGet, "/api/v1/account/profile", tok, nil))
	if got.DisplayName != "昵称兼容" || got.AvatarURL != "" {
		t.Fatalf("profile after rejected updates: %+v", got)
	}

	rec = ts.call(t, http.MethodPatch, "/api/v1/account/contact", tok, map[string]string{"phone": "+86 138-0000-0000", "email": "blue@example.com"})
	expectStatus(t, rec, 200, "")
	// 只传 email 时手机号不变；空串清空。
	rec = ts.call(t, http.MethodPatch, "/api/v1/account/contact", tok, map[string]string{"email": ""})
	expectStatus(t, rec, 200, "")
	if got := decodeBody[domain.Account](t, rec); got.Phone != "+86 138-0000-0000" || got.Email != "" {
		t.Fatalf("contact = %q / %q", got.Phone, got.Email)
	}
	for name, body := range map[string]map[string]string{
		"nothing":       {},
		"bad email":     {"email": "not-an-email"},
		"display email": {"email": "Blue <blue@example.com>"},
		"spaced email":  {"email": "a b@example.com"},
		"bad phone":     {"phone": "138abc"},
		"long phone":    {"phone": strings.Repeat("1", 33)},
		"long email":    {"email": strings.Repeat("a", 120) + "@example.com"},
	} {
		t.Run("contact "+name, func(t *testing.T) {
			expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/account/contact", tok, body), 400, "invalid_argument")
		})
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/account/profile", "", map[string]string{"display_name": "x"}), 401, "unauthorized")
}

func TestDeleteAccount(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	first := ts.login(t, seed.User2Username, seed.DevPassword).Token
	second := ts.login(t, seed.User2Username, seed.DevPassword).Token

	expectStatus(t, ts.call(t, http.MethodDelete, "/api/v1/account", first, nil), 200, "")
	for _, tok := range []string{first, second} {
		expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/auth/me", tok, nil), 401, "unauthorized")
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": seed.User2Username, "password": seed.DevPassword}), 401, "invalid_credential")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/auth/register", "",
		map[string]string{"username": seed.User2Username, "password": seed.DevPassword}), 409, "username_exists")
	// 其他账户不受影响。
	ts.login(t, seed.UserUsername, seed.DevPassword)
}

// TestNoSecretsInResponsesOrLogs：响应和日志里不出现密码、密码哈希和 token 明文。
func TestNoSecretsInResponsesOrLogs(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	const password = "S3cret-Passw0rd"
	rec := ts.call(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{"username": "secret_user", "password": password})
	expectStatus(t, rec, 201, "")
	token := decodeBody[sessionResponse](t, rec).Token
	_, hash, _ := ts.mem.GetAccountByUsername(context.Background(), "secret_user")

	var bodies strings.Builder
	bodies.WriteString(rec.Body.String())
	for _, r := range []*httptest.ResponseRecorder{
		ts.call(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "secret_user", "password": password}),
		ts.call(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "secret_user", "password": password + "x"}),
		ts.call(t, http.MethodGet, "/api/v1/auth/me", token, nil),
		ts.call(t, http.MethodGet, "/api/v1/account/profile", token, nil),
		ts.call(t, http.MethodPatch, "/api/v1/account/contact", token, map[string]string{"phone": "13900001111", "email": "pii@example.com"}),
		ts.call(t, http.MethodPost, "/api/v1/auth/logout", token, nil),
	} {
		bodies.WriteString(r.Body.String())
	}
	logs := ts.logs.String()
	for _, secret := range []string{password, hash, token, store.HashAuthToken(token)} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain secret %q", secret[:6])
		}
	}
	// 登录响应必然含本次新 token，这里只检查密码与哈希。
	for _, secret := range []string{password, hash, "password"} {
		if strings.Contains(bodies.String(), secret) {
			t.Errorf("responses contain %q", secret[:6])
		}
	}
	// 联系方式属于个人信息，审计日志只记录改了哪些字段。
	for _, pii := range []string{"13900001111", "pii@example.com"} {
		if strings.Contains(logs, pii) {
			t.Errorf("logs contain PII %q", pii)
		}
	}
	if !strings.Contains(logs, `"msg":"http request"`) || !strings.Contains(logs, `"account_id":"acct_`) {
		t.Error("access log should carry account_id for authenticated requests")
	}
}
