package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/pricing"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const (
	maxPromotionNameRunes = 64
	// defaultPromotionDays 是不填结束时间时的有效期（与上游一致：开始后 30 天）。
	defaultPromotionDays = 30
	// maxPromotionDays 是一个促销最长的有效期。
	maxPromotionDays = 366
	maxReplyRunes    = 500
)

var (
	ErrPromotionNotFound = &APIError{Status: http.StatusNotFound, Code: "promotion_not_found", Message: "促销不存在"}
	ErrReviewNotFound    = &APIError{Status: http.StatusNotFound, Code: "review_not_found", Message: "评价不存在"}
)

// ---------- 促销 ----------

type merchantPromotionView struct {
	promotionView
	// TargetName 是作用对象的名称：单品促销为商品名，品类促销为分类名，店铺促销为空。
	TargetName  string              `json:"target_name"`
	Description string              `json:"description"`
	Status      domain.EntityStatus `json:"status"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// promotionInput 是创建和修改促销的请求；修改时只改出现的字段。金额是字符串或数字（两位小数），折扣率是实付比例（0.95 = 9.5 折）。
type promotionInput struct {
	Name            *string         `json:"name"`
	Scope           *string         `json:"scope"`
	MerchantID      *string         `json:"merchant_id"` // 只能是自己的店铺或不传
	ProductID       *string         `json:"product_id"`
	CategoryID      *string         `json:"category_id"`
	Type            *string         `json:"type"`
	ThresholdAmount json.RawMessage `json:"threshold_amount"`
	DiscountAmount  json.RawMessage `json:"discount_amount"`
	DiscountRate    json.RawMessage `json:"discount_rate"`
	Stackable       *bool           `json:"stackable"`
	StartAt         *string         `json:"start_at"`
	EndAt           *string         `json:"end_at"`
	Status          *string         `json:"status"`
}

func (s *Server) promotionTargets(ctx context.Context, items []domain.PromotionRule) (map[string]string, error) {
	names := map[string]string{}
	needCats := false
	for _, p := range items {
		switch p.Scope {
		case domain.ScopeProduct:
			if _, ok := names[p.ProductID]; ok {
				continue
			}
			prod, err := s.store.GetProduct(ctx, p.ProductID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			names[p.ProductID] = prod.Name
		case domain.ScopeCategory:
			needCats = true
		}
	}
	if needCats {
		cats, err := s.store.ListCategories(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range cats {
			names[c.CategoryID] = c.Name
		}
	}
	return names, nil
}

func toMerchantPromotionView(p domain.PromotionRule, names map[string]string) merchantPromotionView {
	v := merchantPromotionView{promotionView: toPromotionView(p), Description: pricing.DescribePromotion(p), Status: p.Status,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	switch p.Scope {
	case domain.ScopeProduct:
		v.TargetName = names[p.ProductID]
	case domain.ScopeCategory:
		v.TargetName = names[p.CategoryID]
	}
	return v
}

func (s *Server) writePromotion(w http.ResponseWriter, r *http.Request, status int, p domain.PromotionRule) {
	names, err := s.promotionTargets(r.Context(), []domain.PromotionRule{p})
	if err != nil {
		s.storeFailed(w, r, "load promotion targets", err)
		return
	}
	writeJSON(w, status, toMerchantPromotionView(p, names))
}

func (s *Server) handleListMerchantPromotions(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	status := domain.EntityStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != domain.StatusActive && status != domain.StatusInactive {
		writeError(w, fieldError("status", "状态只能是 active 或 inactive"))
		return
	}
	page := readPage(r)
	items, total, err := s.store.ListPromotions(r.Context(), store.PromotionListQuery{MerchantID: acc.MerchantID, Status: status, Page: page})
	if err != nil {
		s.storeFailed(w, r, "list promotions", err)
		return
	}
	names, err := s.promotionTargets(r.Context(), items)
	if err != nil {
		s.storeFailed(w, r, "load promotion targets", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, page, total, func(p domain.PromotionRule) merchantPromotionView { return toMerchantPromotionView(p, names) }))
}

// handleGetMerchantPromotion 本店促销详情（编辑页用；上游没有这个接口）。其他店铺的 404。
func (s *Server) handleGetMerchantPromotion(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	p, err := s.store.GetPromotion(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.MerchantID != acc.MerchantID) {
		writeError(w, ErrPromotionNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "get promotion", err)
		return
	}
	s.writePromotion(w, r, http.StatusOK, p)
}

// handleCreateMerchantPromotion 商家创建本店促销：范围为店铺（默认）、本店的单品或某个品类（只作用于本店商品）。
func (s *Server) handleCreateMerchantPromotion(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in promotionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	now := s.now().UTC()
	p := domain.PromotionRule{Scope: domain.ScopeMerchant, MerchantID: acc.MerchantID, Type: domain.PromotionFullReduction, Stackable: true,
		StartAt: now, Status: domain.StatusActive}
	if err := s.applyPromotionInput(r.Context(), &p, in, acc.MerchantID, true, now); err != nil {
		writeError(w, err)
		return
	}
	created, err := s.store.CreatePromotion(r.Context(), p)
	if err != nil {
		s.storeFailed(w, r, "create promotion", err)
		return
	}
	s.audit(r, "promotion.created", acc.AccountID, "promotion_id", created.PromotionID, "merchant_id", acc.MerchantID)
	s.writePromotion(w, r, http.StatusCreated, created)
}

// handleUpdateMerchantPromotion 修改本店促销（只改出现的字段），停用/启用也用它（status）。没有删除：停用即可。
// 其他店铺的促销按不存在处理（404，与上游一致）。
func (s *Server) handleUpdateMerchantPromotion(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in promotionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	now := s.now().UTC()
	updated, err := s.store.UpdatePromotion(r.Context(), id, func(ctx context.Context, p *domain.PromotionRule) error {
		if p.MerchantID != acc.MerchantID {
			return ErrPromotionNotFound
		}
		return s.applyPromotionInput(ctx, p, in, acc.MerchantID, false, now)
	})
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr)
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrPromotionNotFound)
		return
	case err != nil:
		s.storeFailed(w, r, "update promotion", err)
		return
	}
	s.audit(r, "promotion.updated", acc.AccountID, "promotion_id", id, "status", updated.Status)
	s.writePromotion(w, r, http.StatusOK, updated)
}

// applyPromotionInput 把请求中出现的字段合并到 p 并校验整条促销；错误定位到字段。
func (s *Server) applyPromotionInput(ctx context.Context, p *domain.PromotionRule, in promotionInput, merchantID string, creating bool, now time.Time) error {
	if in.MerchantID != nil && strings.TrimSpace(*in.MerchantID) != merchantID {
		return fieldError("merchant_id", "只能为自己的店铺创建促销")
	}
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if n := utf8.RuneCountInString(p.Name); n == 0 || n > maxPromotionNameRunes {
		return fieldError("name", fmt.Sprintf("请填写促销名称（最多 %d 个字）", maxPromotionNameRunes))
	}
	if in.Scope != nil {
		p.Scope = strings.TrimSpace(*in.Scope)
	}
	if in.ProductID != nil {
		p.ProductID = strings.TrimSpace(*in.ProductID)
	}
	if in.CategoryID != nil {
		p.CategoryID = strings.TrimSpace(*in.CategoryID)
	}
	switch p.Scope {
	case domain.ScopeMerchant:
		p.ProductID, p.CategoryID = "", ""
	case domain.ScopeProduct:
		p.CategoryID = ""
		if p.ProductID == "" {
			return fieldError("product_id", "请选择商品")
		}
		prod, err := s.store.GetProduct(ctx, p.ProductID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (prod.MerchantID != merchantID || prod.Status == domain.ProductDeleted)) {
			return fieldError("product_id", "只能选择本店的商品")
		}
		if err != nil {
			return err
		}
	case domain.ScopeCategory:
		p.ProductID = ""
		if p.CategoryID == "" {
			return fieldError("category_id", "请选择分类")
		}
		cats, err := s.store.ListCategories(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, c := range cats {
			found = found || c.CategoryID == p.CategoryID
		}
		if !found {
			return fieldError("category_id", "分类不存在")
		}
	default:
		return fieldError("scope", "范围只能是 merchant（全店）、product（单品）或 category（品类）")
	}
	p.MerchantID = merchantID

	if in.Type != nil {
		p.Type = strings.TrimSpace(*in.Type)
	}
	if present(in.ThresholdAmount) {
		m, err := parseMoney("threshold_amount", in.ThresholdAmount, true)
		if err != nil {
			return err
		}
		p.ThresholdAmount = m
	}
	if present(in.DiscountAmount) {
		m, err := parseMoney("discount_amount", in.DiscountAmount, true)
		if err != nil {
			return err
		}
		p.DiscountAmount = m
	}
	if present(in.DiscountRate) {
		var rate domain.Rate
		if err := json.Unmarshal(in.DiscountRate, &rate); err != nil {
			return fieldError("discount_rate", "折扣率格式不正确，例如 0.95 表示 9.5 折")
		}
		p.DiscountRate = rate
	}
	switch p.Type {
	case domain.PromotionFullReduction:
		p.DiscountRate = 0
		if p.DiscountAmount <= 0 {
			return fieldError("discount_amount", "请填写减免金额")
		}
		if p.ThresholdAmount > 0 && p.DiscountAmount > p.ThresholdAmount {
			return fieldError("discount_amount", "减免金额不能超过门槛")
		}
	case domain.PromotionDiscount:
		p.DiscountAmount = 0
		if p.DiscountRate <= 0 || p.DiscountRate >= domain.MustRate("1") {
			return fieldError("discount_rate", "折扣率必须大于 0 小于 1，例如 0.95 表示 9.5 折")
		}
	default:
		return fieldError("type", "类型只能是 full_reduction（满减）或 discount（折扣）")
	}
	if in.Stackable != nil {
		p.Stackable = *in.Stackable
	}
	if in.StartAt != nil {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*in.StartAt))
		if err != nil {
			return fieldError("start_at", "开始时间格式不正确（RFC 3339，例如 2026-10-01T00:00:00+08:00）")
		}
		p.StartAt = t.UTC()
	}
	switch {
	case in.EndAt != nil:
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*in.EndAt))
		if err != nil {
			return fieldError("end_at", "结束时间格式不正确（RFC 3339，例如 2026-10-31T23:59:59+08:00）")
		}
		p.EndAt = t.UTC()
	case creating:
		p.EndAt = p.StartAt.AddDate(0, 0, defaultPromotionDays)
	}
	switch {
	case !p.EndAt.After(p.StartAt):
		return fieldError("end_at", "结束时间必须晚于开始时间")
	// 时长上限只在创建或改了时间时检查：停用、改名等不受历史数据（例如种子里跨年的促销）影响。
	case (creating || in.StartAt != nil || in.EndAt != nil) && p.EndAt.Sub(p.StartAt) > maxPromotionDays*24*time.Hour:
		return fieldError("end_at", fmt.Sprintf("一个促销最长 %d 天", maxPromotionDays))
	case creating && !p.EndAt.After(now):
		return fieldError("end_at", "结束时间必须晚于现在")
	}
	if in.Status != nil {
		p.Status = domain.EntityStatus(strings.TrimSpace(*in.Status))
	}
	if p.Status != domain.StatusActive && p.Status != domain.StatusInactive {
		return fieldError("status", "状态只能是 active（启用）或 inactive（停用）")
	}
	return nil
}

// ---------- 评价 ----------

type merchantReviewView struct {
	ReviewID          string     `json:"review_id"`
	OrderID           string     `json:"order_id"`
	ProductID         string     `json:"product_id"`
	ProductName       string     `json:"product_name"`
	SkuID             string     `json:"sku_id"`
	ReviewerName      string     `json:"reviewer_name"` // 脱敏后的显示名，不返回账户 ID
	Rating            int        `json:"rating"`
	Content           string     `json:"content"`
	Tags              []string   `json:"tags"`
	Status            string     `json:"status"`
	MerchantReply     string     `json:"merchant_reply"`
	MerchantRepliedAt *time.Time `json:"merchant_replied_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

func toMerchantReviewView(r store.MerchantReview) merchantReviewView {
	return merchantReviewView{ReviewID: r.ReviewID, OrderID: r.OrderID, ProductID: r.ProductID, ProductName: r.ProductName, SkuID: r.SkuID,
		ReviewerName: maskName(r.ReviewerName), Rating: r.Rating, Content: r.Content, Tags: strs(r.Tags), Status: r.Status,
		MerchantReply: r.MerchantReply, MerchantRepliedAt: r.MerchantRepliedAt, CreatedAt: r.CreatedAt}
}

// handleListMerchantReviews 本店商品收到的评价（含平台隐藏的），可按是否已回复筛选。
func (s *Server) handleListMerchantReviews(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	q := store.MerchantReviewQuery{MerchantID: acc.MerchantID, Page: readPage(r)}
	switch strings.TrimSpace(r.URL.Query().Get("replied")) {
	case "":
	case "true":
		q.Replied = new(bool)
		*q.Replied = true
	case "false":
		q.Replied = new(bool)
	default:
		writeError(w, fieldError("replied", "replied 只能是 true 或 false"))
		return
	}
	items, total, err := s.store.ListMerchantReviews(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list merchant reviews", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toMerchantReviewView))
}

type replyRequest struct {
	Reply string `json:"reply"`
}

// handleMerchantReviewAction 处理 POST /merchant/reviews/{id}:reply：回复或修改回复。其他店铺的评价 404（与上游一致）。
// 上游会把超长回复静默截断、空回复报 404，这里都返回 400 并指出字段。
func (s *Server) handleMerchantReviewAction(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id, ok := strings.CutSuffix(r.PathValue("action"), ":reply")
	if !ok || id == "" {
		writeError(w, ErrNotFound)
		return
	}
	var in replyRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	reply := strings.TrimSpace(in.Reply)
	if n := utf8.RuneCountInString(reply); n == 0 || n > maxReplyRunes {
		writeError(w, fieldError("reply", fmt.Sprintf("回复内容需要 1 到 %d 个字", maxReplyRunes)))
		return
	}
	got, err := s.store.ReplyReview(r.Context(), acc.MerchantID, id, reply, s.now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrReviewNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "reply review", err)
		return
	}
	s.audit(r, "review.replied", acc.AccountID, "review_id", id, "merchant_id", acc.MerchantID)
	writeJSON(w, http.StatusOK, toMerchantReviewView(got))
}
