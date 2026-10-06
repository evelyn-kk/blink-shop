package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ConfigAdmin 是管理端读取和修改配置的入口，由 configcenter.Admin 实现。
type ConfigAdmin interface {
	List(ctx context.Context) []configcenter.Entry
	Set(ctx context.Context, name, value string) (before, after configcenter.Entry, err error)
}

const (
	maxReasonRunes = 200
	secretMask     = "******"
)

var (
	ErrAccountNotFound  = &APIError{Status: http.StatusNotFound, Code: "account_not_found", Message: "账号不存在"}
	ErrMerchantNotFound = &APIError{Status: http.StatusNotFound, Code: "merchant_not_found", Message: "店铺不存在"}
	ErrCannotChangeSelf = &APIError{Status: http.StatusConflict, Code: "cannot_change_self", Message: "不能修改自己账号的状态"}
	ErrStatusUnchanged  = &APIError{Status: http.StatusConflict, Code: "status_unchanged", Message: "已经是这个状态"}
	ErrConfigNotFound   = &APIError{Status: http.StatusNotFound, Code: "config_not_found", Message: "配置项不存在"}
	ErrConfigReadOnly   = &APIError{Status: http.StatusConflict, Code: "config_read_only", Message: "该配置只在启动时读取，不能在运行中修改"}
	ErrConfigByEnv      = &APIError{Status: http.StatusConflict, Code: "config_overridden_by_env", Message: "该配置由环境变量指定，动态修改不会生效"}
	ErrConfigsDisabled  = &APIError{Status: http.StatusServiceUnavailable, Code: "config_admin_unavailable", Message: "配置管理未启用"}
)

var statusText = map[string]string{
	"active": "正常", "inactive": "停用", "risk": "风控", "deleted": "已删除", "visible": "显示", "hidden": "隐藏",
}

func transitionConflict(from, to string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: "invalid_status_transition",
		Message: fmt.Sprintf("不能从“%s”改为“%s”", statusText[from], statusText[to])}
}

// statusChange 是管理员修改状态的请求。改为非正常状态（停用、风控、隐藏）时必须填写原因，写入审计。
type statusChange struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func readStatusChange(r *http.Request, allowed []string, reasonFree string) (statusChange, error) {
	var in statusChange
	if err := decodeJSON(r, &in); err != nil {
		return in, err
	}
	in.Status, in.Reason = strings.TrimSpace(in.Status), strings.TrimSpace(in.Reason)
	if !contains(allowed, in.Status) {
		return in, fieldError("status", "状态只能是 "+strings.Join(allowed, "、"))
	}
	if n := utf8.RuneCountInString(in.Reason); n > maxReasonRunes || (n == 0 && in.Status != reasonFree) {
		return in, fieldError("reason", fmt.Sprintf("请填写原因（最多 %d 个字），会记入操作审计", maxReasonRunes))
	}
	return in, nil
}

// changeWithAudit 在一个事务内执行状态修改并写审计记录：两者一起成功或一起回滚。change 返回修改前后的值。
func (s *Server) changeWithAudit(r *http.Request, action, targetType, targetID, reason string, change func(ctx context.Context) (string, string, error)) error {
	acc, _ := accountFromContext(r.Context())
	name := acc.DisplayName
	if name == "" {
		name = acc.Username
	}
	var before, after string
	err := s.store.WithTx(r.Context(), func(ctx context.Context) error {
		var err error
		if before, after, err = change(ctx); err != nil {
			return err
		}
		_, err = s.store.InsertAuditLog(ctx, store.AuditLog{OperatorID: acc.AccountID, OperatorName: name, Action: action, TargetType: targetType,
			TargetID: targetID, BeforeValue: before, AfterValue: after, Reason: reason, RequestID: requestIDFromContext(r.Context())})
		return err
	})
	if err == nil {
		s.audit(r, action, acc.AccountID, "target_type", targetType, "target_id", targetID, "before", before, "after", after)
	}
	return err
}

// writeChangeError 输出状态修改的错误：fn 返回的 APIError 原样输出，不存在用 notFound，非法迁移 409。
func (s *Server) writeChangeError(w http.ResponseWriter, r *http.Request, err error, notFound *APIError, from, to string) {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, notFound)
	case errors.Is(err, store.ErrInvalid):
		writeError(w, transitionConflict(from, to))
	default:
		s.storeFailed(w, r, "admin status change", err)
	}
}

func readEnum[T ~string](r *http.Request, key string, allowed ...string) (T, error) {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v != "" && !contains(allowed, v) {
		return "", fieldError(key, key+" 只能是 "+strings.Join(allowed, "、"))
	}
	return T(v), nil
}

// ---------- 账号 ----------

type adminAccountView struct {
	AccountID   string              `json:"account_id"`
	Username    string              `json:"username"`
	DisplayName string              `json:"display_name"`
	Role        domain.Role         `json:"role"`
	MerchantID  string              `json:"merchant_id"`
	Status      domain.EntityStatus `json:"status"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func toAdminAccountView(a domain.Account) adminAccountView {
	return adminAccountView{AccountID: a.AccountID, Username: a.Username, DisplayName: a.DisplayName, Role: a.Role, MerchantID: a.MerchantID,
		Status: a.Status, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}

// handleListAdminAccounts 列出账号（不含已注销），可按角色、状态、账号或显示名筛选；不返回手机号和邮箱。
func (s *Server) handleListAdminAccounts(w http.ResponseWriter, r *http.Request) {
	role, err := readEnum[domain.Role](r, "role", "user", "merchant", "admin")
	if err != nil {
		writeError(w, err)
		return
	}
	status, err := readEnum[domain.EntityStatus](r, "status", "active", "inactive", "risk")
	if err != nil {
		writeError(w, err)
		return
	}
	q := store.AccountQuery{Role: role, Status: status, Keyword: strings.TrimSpace(r.URL.Query().Get("keyword")), Page: readPage(r)}
	items, total, err := s.store.ListAccounts(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list accounts", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toAdminAccountView))
}

// handleUpdateAdminAccount 修改账号状态：active / inactive / risk。不能改自己；停用和风控要写原因。
// 停用的账号只能查看自己的会话和退出，风控账号只读（见 RBAC）；状态在每个请求时检查，已有 token 立即受影响。
func (s *Server) handleUpdateAdminAccount(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	in, err := readStatusChange(r, []string{"active", "inactive", "risk"}, "active")
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if id == acc.AccountID {
		writeError(w, ErrCannotChangeSelf)
		return
	}
	var from string
	var updated domain.Account
	err = s.changeWithAudit(r, "account.status_changed", "account", id, in.Reason, func(ctx context.Context) (string, string, error) {
		var err error
		updated, err = s.store.UpdateAccountStatus(ctx, id, func(a *domain.Account) error {
			from = string(a.Status)
			if from == in.Status {
				return ErrStatusUnchanged
			}
			a.Status = domain.EntityStatus(in.Status)
			return nil
		})
		return from, in.Status, err
	})
	if err != nil {
		s.writeChangeError(w, r, err, ErrAccountNotFound, from, in.Status)
		return
	}
	writeJSON(w, http.StatusOK, toAdminAccountView(updated))
}

// ---------- 店铺 ----------

type adminMerchantView struct {
	merchantView
	ServicePhone string              `json:"service_phone"`
	Status       domain.EntityStatus `json:"status"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

func toAdminMerchantView(m domain.Merchant) adminMerchantView {
	return adminMerchantView{merchantView: toMerchantView(m), ServicePhone: m.ServicePhone, Status: m.Status, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (s *Server) handleListAdminMerchants(w http.ResponseWriter, r *http.Request) {
	status, err := readEnum[domain.EntityStatus](r, "status", "active", "inactive", "risk")
	if err != nil {
		writeError(w, err)
		return
	}
	q := store.MerchantQuery{Status: status, Keyword: strings.TrimSpace(r.URL.Query().Get("keyword")), Page: readPage(r)}
	items, total, err := s.store.ListMerchants(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list merchants", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toAdminMerchantView))
}

// handleUpdateAdminMerchant 修改店铺状态。店铺非正常状态时，它的商品、促销、店铺券和知识资料都不再对外可见（由各查询的可见性条件保证）。
func (s *Server) handleUpdateAdminMerchant(w http.ResponseWriter, r *http.Request) {
	in, err := readStatusChange(r, []string{"active", "inactive", "risk"}, "active")
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	var from string
	var updated domain.Merchant
	err = s.changeWithAudit(r, "merchant.status_changed", "merchant", id, in.Reason, func(ctx context.Context) (string, string, error) {
		var err error
		updated, err = s.store.UpdateMerchantStatus(ctx, id, func(m *domain.Merchant) error {
			from = string(m.Status)
			if from == in.Status {
				return ErrStatusUnchanged
			}
			m.Status = domain.EntityStatus(in.Status)
			return nil
		})
		return from, in.Status, err
	})
	if err != nil {
		s.writeChangeError(w, r, err, ErrMerchantNotFound, from, in.Status)
		return
	}
	writeJSON(w, http.StatusOK, toAdminMerchantView(updated))
}

// ---------- 商品 ----------

type adminProductView struct {
	ProductID     string               `json:"product_id"`
	MerchantID    string               `json:"merchant_id"`
	MerchantName  string               `json:"merchant_name"`
	CategoryID    string               `json:"category_id"`
	Name          string               `json:"name"`
	ImageURL      string               `json:"image_url"`
	Price         domain.Money         `json:"price"`
	StockQuantity int                  `json:"stock_quantity"`
	StockStatus   domain.StockStatus   `json:"stock_status"`
	Status        domain.ProductStatus `json:"status"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

func toAdminProductView(p store.CatalogProduct) adminProductView {
	return adminProductView{ProductID: p.ProductID, MerchantID: p.MerchantID, MerchantName: p.MerchantName, CategoryID: p.CategoryID, Name: p.Name,
		ImageURL: p.ImageURL, Price: p.Price, StockQuantity: p.StockQuantity, StockStatus: p.StockStatus, Status: p.Status, UpdatedAt: p.UpdatedAt}
}

func (s *Server) handleListAdminProducts(w http.ResponseWriter, r *http.Request) {
	status, err := readEnum[domain.ProductStatus](r, "status", "active", "inactive", "risk", "deleted")
	if err != nil {
		writeError(w, err)
		return
	}
	q := store.AdminProductQuery{MerchantID: strings.TrimSpace(r.URL.Query().Get("merchant_id")), Status: status,
		Keyword: strings.TrimSpace(r.URL.Query().Get("keyword")), Page: readPage(r)}
	items, total, err := s.store.ListAllProducts(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list products", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toAdminProductView))
}

// handleUpdateAdminProduct 修改商品状态：上架、下架、风控。删除由商家操作；已删除的商品不能再改（状态机终态）。
func (s *Server) handleUpdateAdminProduct(w http.ResponseWriter, r *http.Request) {
	in, err := readStatusChange(r, []string{"active", "inactive", "risk"}, "active")
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	var from string
	var updated domain.Product
	err = s.changeWithAudit(r, "product.status_changed", "product", id, in.Reason, func(ctx context.Context) (string, string, error) {
		var err error
		updated, err = s.store.UpdateProduct(ctx, id, func(p *domain.Product) error {
			from = string(p.Status)
			if from == in.Status {
				return ErrStatusUnchanged
			}
			if err := p.Status.CanTransitionTo(domain.ProductStatus(in.Status)); err != nil {
				return transitionConflict(from, in.Status)
			}
			p.Status = domain.ProductStatus(in.Status)
			return nil
		})
		return from, in.Status, err
	})
	if err != nil {
		s.writeChangeError(w, r, err, ErrProductNotFound, from, in.Status)
		return
	}
	merchant, _ := s.store.GetMerchant(r.Context(), updated.MerchantID)
	writeJSON(w, http.StatusOK, toAdminProductView(store.CatalogProduct{Product: updated, MerchantName: merchant.Name}))
}

// ---------- 促销 ----------

func (s *Server) handleListAdminPromotions(w http.ResponseWriter, r *http.Request) {
	status, err := readEnum[domain.EntityStatus](r, "status", "active", "inactive")
	if err != nil {
		writeError(w, err)
		return
	}
	q := store.PromotionListQuery{MerchantID: strings.TrimSpace(r.URL.Query().Get("merchant_id")), Status: status, Page: readPage(r)}
	items, total, err := s.store.ListPromotions(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list promotions", err)
		return
	}
	names, err := s.promotionTargets(r.Context(), items)
	if err != nil {
		s.storeFailed(w, r, "load promotion targets", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, func(p domain.PromotionRule) merchantPromotionView { return toMerchantPromotionView(p, names) }))
}

// handleUpdateAdminPromotion 管理员只能停用或启用促销（与上游一致），不能改规则；停用要写原因。
func (s *Server) handleUpdateAdminPromotion(w http.ResponseWriter, r *http.Request) {
	in, err := readStatusChange(r, []string{"active", "inactive"}, "active")
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	var from string
	var updated domain.PromotionRule
	err = s.changeWithAudit(r, "promotion.status_changed", "promotion", id, in.Reason, func(ctx context.Context) (string, string, error) {
		var err error
		updated, err = s.store.UpdatePromotion(ctx, id, func(_ context.Context, p *domain.PromotionRule) error {
			from = string(p.Status)
			if from == in.Status {
				return ErrStatusUnchanged
			}
			p.Status = domain.EntityStatus(in.Status)
			return nil
		})
		return from, in.Status, err
	})
	if err != nil {
		s.writeChangeError(w, r, err, ErrPromotionNotFound, from, in.Status)
		return
	}
	s.writePromotion(w, r, http.StatusOK, updated)
}

// ---------- 评价 ----------

type adminReviewView struct {
	merchantReviewView
	AccountID string    `json:"account_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toAdminReviewView(r store.MerchantReview) adminReviewView {
	return adminReviewView{merchantReviewView: toMerchantReviewView(r), AccountID: r.AccountID, UpdatedAt: r.UpdatedAt}
}

func (s *Server) handleListAdminReviews(w http.ResponseWriter, r *http.Request) {
	status, err := readEnum[string](r, "status", domain.ReviewVisible, domain.ReviewHidden)
	if err != nil {
		writeError(w, err)
		return
	}
	q := store.AdminReviewQuery{Status: status, ProductID: strings.TrimSpace(r.URL.Query().Get("product_id")), Page: readPage(r)}
	items, total, err := s.store.ListAllReviews(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list reviews", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toAdminReviewView))
}

// handleUpdateAdminReview 显示或隐藏评价（上游还有 deleted，这里的数据模型只有 visible/hidden，隐藏即可）。隐藏要写原因。
func (s *Server) handleUpdateAdminReview(w http.ResponseWriter, r *http.Request) {
	in, err := readStatusChange(r, []string{domain.ReviewVisible, domain.ReviewHidden}, domain.ReviewVisible)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	var from string
	var updated store.MerchantReview
	err = s.changeWithAudit(r, "review.status_changed", "review", id, in.Reason, func(ctx context.Context) (string, string, error) {
		var err error
		updated, err = s.store.UpdateReviewStatus(ctx, id, func(rv *domain.ProductReview) error {
			from = rv.Status
			if from == in.Status {
				return ErrStatusUnchanged
			}
			rv.Status = in.Status
			return nil
		})
		return from, in.Status, err
	})
	if err != nil {
		s.writeChangeError(w, r, err, ErrReviewNotFound, from, in.Status)
		return
	}
	writeJSON(w, http.StatusOK, toAdminReviewView(updated))
}

// ---------- 配置 ----------

type configView struct {
	Key         string `json:"key"`
	Env         string `json:"env"`
	Description string `json:"description"`
	// Value 是当前生效的值；密钥类只返回掩码（有值为 ******，未设置为空串），任何接口都不返回明文。
	Value    string `json:"value"`
	Source   string `json:"source"`
	Secret   bool   `json:"secret"`
	Editable bool   `json:"editable"`
}

func toConfigView(e configcenter.Entry) configView {
	v := configView{Key: e.Key.Name, Env: e.Key.Env, Description: e.Description, Value: e.Value, Source: e.Source, Secret: e.Key.Secret,
		Editable: e.Runtime && !e.Key.Secret && e.Source != "env"}
	if e.Key.Secret {
		v.Value = maskSecret(e.Value)
	}
	return v
}

func maskSecret(v string) string {
	if v == "" {
		return ""
	}
	return secretMask
}

func (s *Server) handleListAdminConfigs(w http.ResponseWriter, r *http.Request) {
	if s.configs == nil {
		writeError(w, ErrConfigsDisabled)
		return
	}
	items := []configView{}
	for _, e := range s.configs.List(r.Context()) {
		items = append(items, toConfigView(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type configUpdate struct {
	Value *string `json:"value"`
}

// handleUpdateAdminConfig 修改运行中可调的配置（写入动态配置，立即生效）；空值删除动态配置、退回默认值。
// 只在启动时读取的配置和密钥不能改（409），被环境变量指定的也不能改（环境变量优先）。修改写审计。
func (s *Server) handleUpdateAdminConfig(w http.ResponseWriter, r *http.Request) {
	if s.configs == nil {
		writeError(w, ErrConfigsDisabled)
		return
	}
	var in configUpdate
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.Value == nil {
		writeError(w, fieldError("value", "请提供 value（空串表示恢复默认值）"))
		return
	}
	key := r.PathValue("key")
	// 配置本身不在数据库里：先写配置再写审计；审计写入失败时把配置改回去。
	before, after, err := s.configs.Set(r.Context(), key, *in.Value)
	switch {
	case errors.Is(err, configcenter.ErrUnknownKey):
		writeError(w, ErrConfigNotFound)
		return
	case errors.Is(err, configcenter.ErrNotRuntime):
		writeError(w, ErrConfigReadOnly)
		return
	case errors.Is(err, configcenter.ErrOverriddenByEnv):
		writeError(w, ErrConfigByEnv)
		return
	case err != nil:
		writeError(w, fieldError("value", err.Error()))
		return
	}
	beforeValue := before.Value
	if before.Source == "default" {
		beforeValue = "（默认）" + before.Value
	}
	auditErr := s.changeWithAudit(r, "config.updated", "config", key, "", func(context.Context) (string, string, error) {
		return truncateRunes(beforeValue, 500), truncateRunes(after.Value, 500), nil
	})
	if auditErr != nil {
		restore := before.Value
		if before.Source == "default" {
			restore = ""
		}
		_, _, _ = s.configs.Set(r.Context(), key, restore)
		s.storeFailed(w, r, "audit config update", auditErr)
		return
	}
	writeJSON(w, http.StatusOK, toConfigView(after))
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---------- 风控概览与审计 ----------

type riskOverview struct {
	Accounts      map[string]int `json:"accounts"`
	Merchants     map[string]int `json:"merchants"`
	Products      map[string]int `json:"products"`
	BlockedWords  []string       `json:"blocked_words"`
	WordsSource   string         `json:"blocked_words_source"`
	WordsEditable bool           `json:"blocked_words_editable"`
}

// handleRiskOverview 风控页的汇总：账号、店铺、商品（不含已删除）按状态的数量，以及当前的风险词。
func (s *Server) handleRiskOverview(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.CountStatuses(r.Context())
	if err != nil {
		s.storeFailed(w, r, "count statuses", err)
		return
	}
	out := riskOverview{Accounts: counts.Accounts, Merchants: counts.Merchants, Products: counts.Products, BlockedWords: []string{}}
	for _, m := range []map[string]int{out.Accounts, out.Merchants, out.Products} {
		for _, st := range []string{"active", "inactive", "risk"} {
			m[st] += 0
		}
	}
	if s.configs != nil {
		for _, e := range s.configs.List(r.Context()) {
			if e.Key.Name != configcenter.KeyRiskBlockedWords.Name {
				continue
			}
			for _, word := range strings.Split(e.Value, ",") {
				if word = strings.TrimSpace(word); word != "" {
					out.BlockedWords = append(out.BlockedWords, word)
				}
			}
			out.WordsSource, out.WordsEditable = e.Source, toConfigView(e).Editable
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type auditView struct {
	AuditID      string    `json:"audit_id"`
	OperatorID   string    `json:"operator_id"`
	OperatorName string    `json:"operator_name"`
	Action       string    `json:"action"`
	TargetType   string    `json:"target_type"`
	TargetID     string    `json:"target_id"`
	BeforeValue  string    `json:"before_value"`
	AfterValue   string    `json:"after_value"`
	Reason       string    `json:"reason"`
	RequestID    string    `json:"request_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func toAuditView(l store.AuditLog) auditView {
	return auditView{AuditID: l.AuditID, OperatorID: l.OperatorID, OperatorName: l.OperatorName, Action: l.Action, TargetType: l.TargetType,
		TargetID: l.TargetID, BeforeValue: l.BeforeValue, AfterValue: l.AfterValue, Reason: l.Reason, RequestID: l.RequestID, CreatedAt: l.CreatedAt}
}

// handleListAuditLogs 查询管理员操作审计，可按对象（类型 + ID）或操作人筛选。
func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	targetType, err := readEnum[string](r, "target_type", "account", "merchant", "product", "promotion", "review", "config")
	if err != nil {
		writeError(w, err)
		return
	}
	q := store.AuditQuery{TargetType: targetType, TargetID: strings.TrimSpace(r.URL.Query().Get("target_id")),
		OperatorID: strings.TrimSpace(r.URL.Query().Get("operator_id")), Page: readPage(r)}
	items, total, err := s.store.ListAuditLogs(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list audit logs", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toAuditView))
}
