package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

func (ts *testServer) audits(t *testing.T, tok, query string) []auditView {
	t.Helper()
	rec := ts.call(t, http.MethodGet, "/api/v1/admin/audit-logs"+query, tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	return decodeBody[pageResponse[auditView]](t, rec).Items
}

// TestAdminAccountStatus：风控 → 账号只读、停用 → 只能看会话和退出、恢复；原因必填；不能改自己；每次修改都有审计。
func TestAdminAccountStatus(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	path := "/api/v1/admin/accounts/" + seed.UserID

	rec := ts.call(t, http.MethodGet, "/api/v1/admin/accounts?role=user", admin, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), "phone") || strings.Contains(rec.Body.String(), "email") {
		t.Fatalf("account list leaks contact info: %s", rec.Body)
	}
	if list := decodeBody[pageResponse[adminAccountView]](t, rec); list.Total != 2 {
		t.Fatalf("users = %+v", list)
	}
	for query, want := range map[string]int{"?keyword=merchant": 2, "?status=risk": 0, "?role=admin&keyword=ADMIN": 1} {
		rec := ts.call(t, http.MethodGet, "/api/v1/admin/accounts"+query, admin, nil)
		if got := decodeBody[pageResponse[adminAccountView]](t, rec).Total; got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/accounts?role=root", admin, nil), http.StatusBadRequest, "invalid_argument")

	for _, c := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"status": "risk"}, "reason"},
		{map[string]any{"status": "risk", "reason": "   "}, "reason"},
		{map[string]any{"status": "risk", "reason": strings.Repeat("长", 201)}, "reason"},
		{map[string]any{"status": "deleted", "reason": "x"}, "status"},
	} {
		rec := ts.call(t, http.MethodPatch, path, admin, c.body)
		expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")
		if e := decodeError(t, rec); e.Field != c.field {
			t.Fatalf("%v: field %q", c.body, e.Field)
		}
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/accounts/"+seed.AdminID, admin, map[string]any{"status": "inactive", "reason": "x"}), http.StatusConflict, "cannot_change_self")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/accounts/acct_missing", admin, map[string]any{"status": "risk", "reason": "x"}), http.StatusNotFound, "account_not_found")
	expectStatus(t, ts.call(t, http.MethodPatch, path, admin, map[string]any{"status": "active"}), http.StatusConflict, "status_unchanged")

	// 风控：已登录的 token 立即变成只读。
	rec = ts.call(t, http.MethodPatch, path, admin, map[string]any{"status": "risk", "reason": "疑似刷单"})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[adminAccountView](t, rec); v.Status != domain.StatusRisk || v.Username != seed.UserUsername {
		t.Fatalf("risk = %+v", v)
	}
	expectStatus(t, ts.call(t, http.MethodGet, cartPath, user, nil), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodPost, cartPath+"/items", user, map[string]any{"product_id": "p_seed_lamp"}), http.StatusForbidden, "account_risk")
	// 停用：只能看会话和退出。
	expectStatus(t, ts.call(t, http.MethodPatch, path, admin, map[string]any{"status": "inactive", "reason": "用户申请冻结"}), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodGet, cartPath, user, nil), http.StatusForbidden, "account_inactive")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/auth/me", user, nil), http.StatusOK, "")
	// 恢复正常不需要原因。
	expectStatus(t, ts.call(t, http.MethodPatch, path, admin, map[string]any{"status": "active"}), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodPost, cartPath+"/items", user, map[string]any{"product_id": "p_seed_lamp"}), http.StatusOK, "")

	logs := ts.audits(t, admin, "?target_type=account&target_id="+seed.UserID)
	if len(logs) != 3 || logs[0].AfterValue != "active" || logs[1].BeforeValue != "risk" || logs[1].AfterValue != "inactive" ||
		logs[1].Reason != "用户申请冻结" || logs[2].Reason != "疑似刷单" || logs[2].OperatorID != seed.AdminID || logs[2].OperatorName == "" ||
		logs[2].RequestID == "" || logs[2].Action != "account.status_changed" {
		t.Fatalf("audits = %+v", logs)
	}
	// 失败的请求不留审计。
	if all := ts.audits(t, admin, ""); len(all) != 3 {
		t.Fatalf("all audits = %d", len(all))
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/audit-logs?target_type=order", admin, nil), http.StatusBadRequest, "invalid_argument")
}

func TestAdminMerchantProductPromotionReview(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token

	// 店铺停业：商品、店铺券都不对外；恢复后回来。
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/merchants/"+seed.HomeMerchant, admin, map[string]any{"status": "inactive", "reason": "资质过期"}), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_seed_lamp", "", nil), http.StatusNotFound, "product_not_found")
	rec := ts.call(t, http.MethodGet, "/api/v1/admin/merchants?status=inactive", admin, nil)
	if list := decodeBody[pageResponse[adminMerchantView]](t, rec); list.Total != 1 || list.Items[0].MerchantID != seed.HomeMerchant || list.Items[0].Status != "inactive" {
		t.Fatalf("inactive merchants = %+v", list)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/merchants/"+seed.HomeMerchant, admin, map[string]any{"status": "active"}), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_seed_lamp", "", nil), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/merchants/m_missing", admin, map[string]any{"status": "risk", "reason": "x"}), http.StatusNotFound, "merchant_not_found")

	// 商品：风控 → 不可见；已删除的不能改；不能由管理员删除。
	rec = ts.call(t, http.MethodPatch, "/api/v1/admin/products/p_seed_mouse", admin, map[string]any{"status": "risk", "reason": "图片侵权"})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[adminProductView](t, rec); v.Status != "risk" || v.MerchantName == "" {
		t.Fatalf("product = %+v", v)
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_seed_mouse", "", nil), http.StatusNotFound, "product_not_found")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/products/p_seed_legacy", admin, map[string]any{"status": "active"}), http.StatusConflict, "invalid_status_transition")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/products/p_seed_mouse", admin, map[string]any{"status": "deleted", "reason": "x"}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/products/p_missing", admin, map[string]any{"status": "risk", "reason": "x"}), http.StatusNotFound, "product_not_found")
	for query, want := range map[string]int{"": 9, "?status=risk": 2, "?status=deleted": 1, "?merchant_id=" + seed.HomeMerchant: 1, "?keyword=nova": 2} {
		rec := ts.call(t, http.MethodGet, "/api/v1/admin/products"+query, admin, nil)
		if got := decodeBody[pageResponse[adminProductView]](t, rec).Total; got != want {
			t.Fatalf("products%s = %d, want %d", query, got, want)
		}
	}

	// 促销：只能停用/启用；停用后不再参与公开促销。
	rec = ts.call(t, http.MethodPatch, "/api/v1/admin/promotions/promo_seed_platform", admin, map[string]any{"status": "inactive", "reason": "活动预算用完"})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[merchantPromotionView](t, rec); v.Status != "inactive" || v.Description == "" {
		t.Fatalf("promotion = %+v", v)
	}
	if strings.Contains(ts.call(t, http.MethodGet, "/api/v1/promotions", "", nil).Body.String(), "promo_seed_platform") {
		t.Fatal("inactive promotion still public")
	}
	rec = ts.call(t, http.MethodGet, "/api/v1/admin/promotions?status=inactive", admin, nil)
	if list := decodeBody[pageResponse[merchantPromotionView]](t, rec); list.Total != 2 { // 加上种子里已结束并停用的暑期活动
		t.Fatalf("inactive promotions = %+v", list)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/promotions/promo_missing", admin, map[string]any{"status": "active"}), http.StatusNotFound, "promotion_not_found")

	// 评价：隐藏后公开列表看不到；管理员列表带账户 ID。
	rec = ts.call(t, http.MethodPatch, "/api/v1/admin/reviews/rv_seed_mouse", admin, map[string]any{"status": "hidden", "reason": "含广告"})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[adminReviewView](t, rec); v.Status != "hidden" || v.AccountID != seed.UserID || v.ProductName == "" {
		t.Fatalf("review = %+v", v)
	}
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/reviews/rv_seed_mouse", admin, map[string]any{"status": "deleted", "reason": "x"}), http.StatusBadRequest, "invalid_argument")
	rec = ts.call(t, http.MethodGet, "/api/v1/admin/reviews?status=hidden", admin, nil)
	if list := decodeBody[pageResponse[adminReviewView]](t, rec); list.Total != 1 {
		t.Fatalf("hidden reviews = %+v", list)
	}

	logs := ts.audits(t, admin, "")
	actions := map[string]int{}
	for _, l := range logs {
		actions[l.Action]++
	}
	if actions["merchant.status_changed"] != 2 || actions["product.status_changed"] != 1 || actions["promotion.status_changed"] != 1 || actions["review.status_changed"] != 1 {
		t.Fatalf("audit actions = %v", actions)
	}
	rec = ts.call(t, http.MethodGet, "/api/v1/admin/risk/overview", admin, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if o := decodeBody[riskOverview](t, rec); o.Products["risk"] != 2 || o.Merchants["active"] != 2 || o.Accounts["risk"] != 0 || len(o.BlockedWords) != 4 ||
		o.WordsSource != "default" || !o.WordsEditable {
		t.Fatalf("overview = %+v", o)
	}
}

func TestAdminConfigs(t *testing.T) {
	ts := newTestServer(t, map[string]string{"AI_API_KEY": "sk-live-secret", "RATE_LIMIT_IP_PER_MINUTE": "600"}, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	rec := ts.call(t, http.MethodGet, "/api/v1/admin/configs", admin, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), "sk-live-secret") || strings.Contains(rec.Body.String(), "blink_dev_password") || strings.Contains(rec.Body.String(), "minioadmin") {
		t.Fatalf("config list leaks a secret: %s", rec.Body)
	}
	items := map[string]configView{}
	for _, c := range decodeBody[struct{ Items []configView }](t, rec).Items {
		items[c.Key] = c
	}
	if c := items["ai.api_key"]; c.Value != "******" || !c.Secret || c.Editable || c.Source != "env" {
		t.Fatalf("ai.api_key = %+v", c)
	}
	if c := items["milvus.token"]; c.Value != "" || !c.Secret {
		t.Fatalf("unset secret = %+v", c)
	}
	if c := items["http.rate_limit.ip_per_minute"]; c.Value != "600" || c.Source != "env" || c.Editable {
		t.Fatalf("env config = %+v", c)
	}
	if c := items["risk.blocked_words"]; !c.Editable || c.Source != "default" {
		t.Fatalf("risk words = %+v", c)
	}

	for _, c := range []struct {
		key    string
		body   map[string]any
		status int
		code   string
	}{
		{"ai.api_key", map[string]any{"value": "sk-new"}, 409, "config_read_only"},
		{"mysql.dsn", map[string]any{"value": "x"}, 409, "config_read_only"},
		{"http.rate_limit.ip_per_minute", map[string]any{"value": "10"}, 409, "config_overridden_by_env"},
		{"no.such", map[string]any{"value": "1"}, 404, "config_not_found"},
		{"http.request_timeout", map[string]any{"value": "soon"}, 400, "invalid_argument"},
		{"http.request_timeout", map[string]any{}, 400, "invalid_argument"},
	} {
		expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/"+c.key, admin, c.body), c.status, c.code)
	}
	rec = ts.call(t, http.MethodPatch, "/api/v1/admin/configs/risk.blocked_words", admin, map[string]any{"value": "刷单, 套现"})
	expectStatus(t, rec, http.StatusOK, "")
	if c := decodeBody[configView](t, rec); c.Value != "刷单, 套现" || c.Source != "dynamic" {
		t.Fatalf("updated = %+v", c)
	}
	// 修改按请求读取的 HTTP 配置立即生效：把账号限流调成 2，第三个请求被限流。
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/http.rate_limit.account_per_minute", admin, map[string]any{"value": "2"}), http.StatusOK, "")
	codes := []int{}
	for range 3 {
		codes = append(codes, ts.call(t, http.MethodGet, "/api/v1/admin/configs", admin, nil).Code)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("rate limit not applied: %v", codes)
	}
	ts.accountLimiter = newRateLimiter(time.Minute) // 清空计数，后面的请求不受刚才的限流影响
	logs := ts.audits(t, admin, "?target_type=config")
	if len(logs) != 2 || logs[1].TargetID != "risk.blocked_words" || !strings.HasPrefix(logs[1].BeforeValue, "（默认）") || logs[1].AfterValue != "刷单, 套现" {
		t.Fatalf("config audits = %+v", logs)
	}
	// 普通用户和商家不能调用管理接口。
	for _, u := range []string{seed.UserUsername, seed.MerchantUsername} {
		tok := ts.login(t, u, seed.DevPassword).Token
		expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/configs", tok, nil), http.StatusForbidden, "forbidden")
		expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/accounts/"+seed.User2ID, tok, map[string]any{"status": "risk", "reason": "x"}), http.StatusForbidden, "forbidden")
	}
}
