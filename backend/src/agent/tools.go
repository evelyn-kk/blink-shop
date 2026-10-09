package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/risk"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// Deps 是规则运行器和工具层的依赖。
type Deps struct {
	Store store.Store
	// Shop 是购物车、结算、订单的业务层，与 HTTP 接口共用同一份校验和金额计算。
	Shop *shop.Service
	// Retriever 是知识检索；nil 表示未配置，search_knowledge 返回 knowledge_unavailable。
	Retriever *rag.Retriever
	// Risk 在规划前检查用户输入；nil 表示不检查。
	Risk risk.Checker
	// Logger 记录工具审计（写操作）和内部错误；nil 表示丢弃。
	Logger *slog.Logger
	Now    func() time.Time
}

// 工具名。
const (
	ToolSearchProducts  = "search_products"
	ToolSearchKnowledge = "search_knowledge"
	ToolGetCart         = "get_cart"
	ToolAddCartItem     = "add_cart_item"
	ToolUpdateCartItem  = "update_cart_item"
	ToolDeleteCartItem  = "delete_cart_item"
	ToolPreviewDiscount = "preview_discount"
	ToolCheckout        = "checkout"
	ToolListOrders      = "list_orders"
	ToolGetOrder        = "get_order"
	ToolPayOrder        = "pay_order"
	ToolCancelOrder     = "cancel_order"
	ToolConfirmReceipt  = "confirm_receipt"
	ToolListCoupons     = "list_coupons"
	ToolListUserCoupons = "list_user_coupons"
	ToolClaimCoupon     = "claim_coupon"
	ToolListPromotions  = "list_promotions"
	ToolListReviews     = "list_reviews"
	ToolCreateReview    = "create_review"
)

// 工具层错误码（除业务层错误码外）。
const (
	CodeToolNotFound      = "tool_not_found"
	CodeToolNotAllowed    = "tool_not_allowed"
	CodeUnauthorized      = "unauthorized"
	CodeInvalidArgument   = "invalid_argument"
	CodeProductNotTrusted = "product_not_in_evidence"
	CodeUnavailable       = "knowledge_unavailable"
	CodeInternal          = "internal_error"
)

// ToolError 是工具层拒绝调用的原因（可展示）。
type ToolError struct {
	Code    string
	Field   string
	Message string
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

// ToolContext 是一次工具调用的上下文：当前用户、运行和意图，以及本轮可信的商品来源。
type ToolContext struct {
	AccountID string
	SessionID string
	RunID     string
	Intent    Intent
	// Evidence 是本轮“可见”的商品 ID：本次运行里搜索到的，加上会话里上一张商品卡列出的。
	// add_cart_item 只接受这些 product_id，模型或规则都不能凭空加购一个没给用户看过的商品。
	Evidence map[string]bool
}

// Observation 是工具调用的结果（给规则回答或模型观察）。
type Observation struct {
	Tool     string        `json:"tool"`
	OK       bool          `json:"ok"`
	Code     string        `json:"code,omitempty"`
	Field    string        `json:"field,omitempty"`
	Message  string        `json:"message,omitempty"`
	Data     any           `json:"data,omitempty"`
	Duration time.Duration `json:"-"`
}

// Tool 是一个可调用的能力。Write 为 true 表示会改变数据（审计记录）。
type Tool struct {
	Name        string
	Description string
	Write       bool
	Schema      *Schema
	Run         func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error)
}

// Registry 是工具注册表：按名字查找、按意图限制可用工具、校验参数后执行。
type Registry struct {
	deps   Deps
	tools  map[string]*Tool
	names  []string
	policy map[Intent][]string
}

// NewRegistry 注册全部工具和默认的意图白名单。
func NewRegistry(deps Deps) *Registry {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	r := &Registry{deps: deps, tools: map[string]*Tool{}, policy: DefaultPolicy()}
	for _, t := range r.shopTools() {
		r.tools[t.Name] = t
		r.names = append(r.names, t.Name)
	}
	sort.Strings(r.names)
	return r
}

// Tools 按名字排序返回全部工具。
func (r *Registry) Tools() []*Tool {
	out := make([]*Tool, 0, len(r.names))
	for _, n := range r.names {
		out = append(out, r.tools[n])
	}
	return out
}

// Allowed 返回意图允许的工具（按名字排序）。
func (r *Registry) Allowed(intent Intent) []string {
	out := append([]string{}, r.policy[intent]...)
	sort.Strings(out)
	return out
}

func (r *Registry) allowed(intent Intent, tool string) bool {
	for _, t := range r.policy[intent] {
		if t == tool {
			return true
		}
	}
	return false
}

// DefaultPolicy 是意图 → 可用工具的白名单。导航和非导购不能调用任何工具；只有购物车意图能改购物车，
// 只有订单意图能支付/取消/收货；写操作工具不会出现在推荐、对比等只读意图里。
func DefaultPolicy() map[Intent][]string {
	return map[Intent][]string{
		IntentGuide:          {ToolSearchProducts, ToolSearchKnowledge, ToolListPromotions},
		IntentProductSearch:  {ToolSearchProducts, ToolSearchKnowledge, ToolListPromotions, ToolListReviews},
		IntentProductCompare: {ToolSearchProducts, ToolSearchKnowledge, ToolListReviews},
		IntentKnowledge:      {ToolSearchKnowledge, ToolSearchProducts},
		IntentImageSearch:    {},
		IntentCart:           {ToolGetCart, ToolAddCartItem, ToolUpdateCartItem, ToolDeleteCartItem, ToolPreviewDiscount, ToolSearchProducts},
		IntentCheckout:       {ToolGetCart, ToolPreviewDiscount, ToolCheckout},
		IntentOrder:          {ToolListOrders, ToolGetOrder, ToolPayOrder, ToolCancelOrder, ToolConfirmReceipt},
		IntentCoupon:         {ToolListCoupons, ToolListUserCoupons, ToolClaimCoupon, ToolListPromotions, ToolGetCart, ToolPreviewDiscount},
		IntentReview:         {ToolListReviews, ToolCreateReview, ToolListOrders, ToolGetOrder, ToolSearchProducts},
		IntentNavigation:     {},
		IntentNonGuide:       {},
	}
}

// Call 执行一次工具调用：工具存在 → 意图允许 → 有当前用户 → 参数符合 schema → 写操作的商品来源可信 → 执行。
// 任何一步失败都返回 OK=false 的观察，不会执行工具；业务层拒绝（库存不足、状态不允许等）同样以观察返回。
func (r *Registry) Call(ctx context.Context, tc *ToolContext, name string, args map[string]any) Observation {
	start := r.deps.Now()
	obs := r.call(ctx, tc, name, args)
	obs.Tool, obs.Duration = name, r.deps.Now().Sub(start)
	return obs
}

func (r *Registry) call(ctx context.Context, tc *ToolContext, name string, args map[string]any) Observation {
	t, ok := r.tools[name]
	if !ok {
		return Observation{Code: CodeToolNotFound, Message: "没有这个工具：" + name}
	}
	if !r.allowed(tc.Intent, name) {
		return Observation{Code: CodeToolNotAllowed, Message: fmt.Sprintf("当前意图 %s 不允许使用 %s", tc.Intent, name)}
	}
	if tc.AccountID == "" {
		return Observation{Code: CodeUnauthorized, Message: "没有当前用户"}
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := t.Schema.Validate(args); err != nil {
		var ae *ArgError
		errors.As(err, &ae)
		return Observation{Code: CodeInvalidArgument, Field: ae.Field, Message: ae.Message}
	}
	if name == ToolAddCartItem {
		if pid := argString(args, "product_id"); !tc.Evidence[pid] {
			return Observation{Code: CodeProductNotTrusted, Field: "product_id", Message: "只能加购本轮给你看过的商品"}
		}
	}
	data, err := t.Run(ctx, tc, args)
	if err != nil {
		return r.failure(ctx, tc, t, args, err)
	}
	if t.Write {
		r.deps.Logger.InfoContext(ctx, "audit", "action", "agent.tool", "tool", name, "account_id", tc.AccountID, "run_id", tc.RunID,
			"session_id", tc.SessionID, "args", SanitizeArgs(args))
	}
	return Observation{OK: true, Data: data}
}

func (r *Registry) failure(ctx context.Context, tc *ToolContext, t *Tool, args map[string]any, err error) Observation {
	var se *shop.Error
	var te *ToolError
	switch {
	case errors.As(err, &se):
		return Observation{Code: se.Code, Field: se.Field, Message: se.Message}
	case errors.As(err, &te):
		return Observation{Code: te.Code, Field: te.Field, Message: te.Message}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return Observation{Code: "cancelled", Message: "已停止"}
	}
	r.deps.Logger.ErrorContext(ctx, "agent tool failed", "tool", t.Name, "run_id", tc.RunID, "account_id", tc.AccountID, "error", err)
	return Observation{Code: CodeInternal, Message: "服务暂时不可用，请稍后再试"}
}

// SanitizeArgs 返回可写入审计和轨迹的参数：字符串截断到 80 个字，数组只保留长度，避免把长文本写进日志。
func SanitizeArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		switch x := v.(type) {
		case string:
			if utf8.RuneCountInString(x) > 80 {
				x = string([]rune(x)[:80]) + "…"
			}
			out[k] = x
		case []any:
			out[k] = fmt.Sprintf("[%d items]", len(x))
		case []string:
			out[k] = fmt.Sprintf("[%d items]", len(x))
		default:
			out[k] = v
		}
	}
	return out
}

// ExportTools 输出工具清单（名字、说明、是否写操作、参数 schema、允许的意图），供文档和契约测试使用。
func (r *Registry) ExportTools() []map[string]any {
	intentsOf := map[string][]string{}
	for intent, tools := range r.policy {
		for _, t := range tools {
			intentsOf[t] = append(intentsOf[t], string(intent))
		}
	}
	out := make([]map[string]any, 0, len(r.names))
	for _, t := range r.Tools() {
		intents := append([]string{}, intentsOf[t.Name]...)
		sort.Strings(intents)
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "write": t.Write,
			"parameters": t.Schema.Export(), "intents": intents})
	}
	return out
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}
