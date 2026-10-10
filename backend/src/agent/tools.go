package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/risk"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// Deps 是规则运行器和工具层的依赖。
type Deps struct {
	// LLM 是模型调用入口；nil 表示没有配置模型，规划和回答全部走规则。
	LLM llm.Provider
	// Settings 返回当前的模型设置（开关、模型名、轮数上限、工具白名单）；nil 表示默认值。每次运行读取，运行中可改。
	Settings SettingsSource
	Store    store.Store
	// Shop 是购物车、结算、订单的业务层，与 HTTP 接口共用同一份校验和金额计算。
	Shop *shop.Service
	// Retriever 是知识检索；nil 表示未配置，search_knowledge 返回 knowledge_unavailable。
	Retriever *rag.Retriever
	// ProductIndex 是商品向量索引；nil 表示商品只用关键词召回。
	ProductIndex rag.ProductIndex
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
// Private 列出不得留存的自由文本参数（评价正文、取消原因等）：写轨迹和审计时只记长度，不记内容也不记可逆片段。
type Tool struct {
	Name        string
	Description string
	Write       bool
	Private     []string
	Schema      *Schema
	Run         func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error)
}

// Registry 是工具注册表：按名字查找、按意图限制可用工具、校验参数后执行。
// 白名单默认是 DefaultPolicy；配置了 Deps.Settings 时每次调用按 ToolPolicy（JSON）解析，解析结果按原文缓存。
type Registry struct {
	deps   Deps
	tools  map[string]*Tool
	names  []string
	policy map[Intent][]string

	policyMu     sync.Mutex
	policyRaw    string
	policyParsed map[Intent][]string

	vocab catalogVocab
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

// Allowed 返回意图当前允许的工具（按名字排序）。
func (r *Registry) Allowed(ctx context.Context, intent Intent) []string {
	out := append([]string{}, r.policyFor(ctx)[intent]...)
	sort.Strings(out)
	return out
}

func (r *Registry) allowed(ctx context.Context, intent Intent, tool string) bool {
	for _, t := range r.policyFor(ctx)[intent] {
		if t == tool {
			return true
		}
	}
	return false
}

// policyFor 返回当前生效的白名单：没有动态配置或配置为空时是内置默认；否则解析 JSON（结果按原文缓存）。
func (r *Registry) policyFor(ctx context.Context) map[Intent][]string {
	if r.deps.Settings == nil {
		return r.policy
	}
	raw := strings.TrimSpace(r.deps.Settings(ctx).ToolPolicy)
	if raw == "" {
		return r.policy
	}
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	if raw == r.policyRaw && r.policyParsed != nil {
		return r.policyParsed
	}
	parsed, warnings := ParsePolicy(raw, r.tools)
	if parsed == nil {
		r.deps.Logger.Warn("agent tool policy invalid, using default", "warnings", warnings)
		parsed = r.policy
	} else if len(warnings) > 0 {
		r.deps.Logger.Warn("agent tool policy adjusted", "warnings", warnings)
	}
	r.policyRaw, r.policyParsed = raw, parsed
	return parsed
}

// noToolIntents 永远不能调用任何工具，写工具也不能通过配置赋给它们：导航只返回跳转，非导购和图搜只做说明。
var noToolIntents = map[Intent]bool{IntentNavigation: true, IntentNonGuide: true, IntentImageSearch: true}

// ParsePolicy 解析动态配置的白名单：JSON 对象，键是意图名，值是工具名数组。未知意图和未知工具忽略（记入 warnings），
// 无工具意图里的工具被去掉；JSON 不合法时返回 nil。没有出现在配置里的意图沿用内置默认。
func ParsePolicy(raw string, known map[string]*Tool) (map[Intent][]string, []string) {
	var m map[string][]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, []string{"not a JSON object of string arrays: " + err.Error()}
	}
	out := DefaultPolicy()
	var warnings []string
	for name, tools := range m {
		intent := Intent(name)
		if _, ok := out[intent]; !ok {
			warnings = append(warnings, "unknown intent "+name)
			continue
		}
		var list []string
		for _, t := range tools {
			t = strings.TrimSpace(t)
			tool, ok := known[t]
			switch {
			case !ok:
				warnings = append(warnings, "unknown tool "+t+" for "+name)
			case noToolIntents[intent]:
				warnings = append(warnings, "intent "+name+" cannot use tools, dropped "+t)
			case tool.Write && intent == IntentGuide:
				warnings = append(warnings, "intent guide cannot use write tool "+t)
			case containsStr(list, t):
			default:
				list = append(list, t)
			}
		}
		out[intent] = list
	}
	return out, warnings
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
	if !r.allowed(ctx, tc.Intent, name) {
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
			"session_id", tc.SessionID, "args", r.Sanitize(name, args))
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

// Sanitize 返回工具 name 可写入审计和轨迹的参数：先按工具的 Private 列表剔除自由文本（只留长度），再做通用处理（SanitizeArgs）。
// 未注册的工具只做通用处理。
func (r *Registry) Sanitize(name string, args map[string]any) map[string]any {
	t, ok := r.tools[name]
	if !ok {
		return SanitizeArgs(args)
	}
	return SanitizeArgs(redact(args, t.Private))
}

// redact 把 private 列出的参数替换成不含内容的描述：字符串记 rune 数，数组记项数，其他类型记类型名。
func redact(args map[string]any, private []string) map[string]any {
	if len(private) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
		if !containsStr(private, k) || v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			out[k] = map[string]any{"redacted": true, "runes": utf8.RuneCountInString(x)}
		case []any:
			out[k] = map[string]any{"redacted": true, "items": len(x)}
		case []string:
			out[k] = map[string]any{"redacted": true, "items": len(x)}
		default:
			out[k] = map[string]any{"redacted": true}
		}
	}
	return out
}

// SanitizeArgs 返回可写入审计和轨迹的参数：字符串截断到 80 个字，数组只保留长度，避免把长文本写进日志。
// 这只是防止日志膨胀，不是脱敏；不得留存的字段用工具的 Private 列表剔除（见 Registry.Sanitize）。
func SanitizeArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		switch x := v.(type) {
		case string:
			x = RedactPII(x)
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
		private := append([]string{}, t.Private...)
		sort.Strings(private)
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "write": t.Write, "private": private,
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
