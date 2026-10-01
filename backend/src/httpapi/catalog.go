package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

var ErrProductNotFound = &APIError{http.StatusNotFound, "product_not_found", "商品不存在或已下架"}

// ---------- 分页 ----------

const (
	defaultPageSize = 10
	maxPageSize     = 100
	maxPage         = 100000
)

// readPage 读取 page / page_size（与上游一致）：缺省或不是整数时用默认值，超出范围时取边界值，不报错。
func readPage(r *http.Request) store.Page {
	return store.Page{
		Page:     queryInt(r, "page", 1, 1, maxPage),
		PageSize: queryInt(r, "page_size", defaultPageSize, 1, maxPageSize),
	}
}

func queryInt(r *http.Request, key string, fallback, lo, hi int) int {
	v, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		return fallback
	}
	return min(max(v, lo), hi)
}

// pageResponse 是列表响应，见 openapi.yaml#/components/schemas/Page。
type pageResponse[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

func newPage[S, T any](items []S, page store.Page, total int, convert func(S) T) pageResponse[T] {
	out := make([]T, 0, len(items))
	for _, it := range items {
		out = append(out, convert(it))
	}
	return pageResponse[T]{Items: out, Page: page.Page, PageSize: page.PageSize, Total: total}
}

// ---------- 公开 DTO ----------
// 公开接口只输出这里列出的字段：不含商品/商家状态、排序值、内部时间戳，评价不含订单和账户 ID。
// 数组字段永远是 []、对象字段永远是 {}，客户端不需要判空。

type categoryNode struct {
	CategoryID string         `json:"category_id"`
	ParentID   string         `json:"parent_id"`
	Name       string         `json:"name"`
	Children   []categoryNode `json:"children"`
}

type merchantView struct {
	MerchantID   string `json:"merchant_id"`
	Name         string `json:"name"`
	LogoURL      string `json:"logo_url"`
	Description  string `json:"description"`
	ServicePhone string `json:"service_phone"`
}

type productCard struct {
	ProductID       string             `json:"product_id"`
	SkuID           string             `json:"sku_id"` // 默认 SKU；没有 SKU 时为空串
	MerchantID      string             `json:"merchant_id"`
	MerchantName    string             `json:"merchant_name"`
	CategoryID      string             `json:"category_id"`
	Name            string             `json:"name"`
	Brand           string             `json:"brand"`
	ImageURL        string             `json:"image_url"`
	Price           domain.Money       `json:"price"`
	MarketPrice     domain.Money       `json:"market_price"`
	StockStatus     domain.StockStatus `json:"stock_status"`
	Tags            []string           `json:"tags"`
	SellingPoints   []string           `json:"selling_points"`
	RecommendReason string             `json:"recommend_reason"`
	RiskNotes       []string           `json:"risk_notes"`
}

type productDetail struct {
	productCard
	ImageURLs      []string        `json:"image_urls"`
	StockQuantity  int             `json:"stock_quantity"`
	Attributes     []attributeView `json:"attributes"`
	SuitableFor    []string        `json:"suitable_for"`
	NotSuitableFor []string        `json:"not_suitable_for"`
	Description    string          `json:"description"`
}

// attributeView 与领域类型不同：unit 始终输出（无单位为空串）。
type attributeView struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type skuView struct {
	SkuID         string             `json:"sku_id"`
	ProductID     string             `json:"product_id"`
	SkuName       string             `json:"sku_name"`
	Price         domain.Money       `json:"price"`
	StockQuantity int                `json:"stock_quantity"`
	StockStatus   domain.StockStatus `json:"stock_status"`
	Specs         map[string]string  `json:"specs"`
	IsDefault     bool               `json:"is_default"`
}

type reviewView struct {
	ReviewID          string     `json:"review_id"`
	ProductID         string     `json:"product_id"`
	SkuID             string     `json:"sku_id"`
	ReviewerName      string     `json:"reviewer_name"` // 脱敏后的显示名
	Rating            int        `json:"rating"`
	Content           string     `json:"content"`
	Tags              []string   `json:"tags"`
	MerchantReply     string     `json:"merchant_reply"`
	MerchantRepliedAt *time.Time `json:"merchant_replied_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

type promotionView struct {
	PromotionID     string       `json:"promotion_id"`
	Name            string       `json:"name"`
	Scope           string       `json:"scope"`
	MerchantID      string       `json:"merchant_id"`
	ProductID       string       `json:"product_id"`
	CategoryID      string       `json:"category_id"`
	Type            string       `json:"type"`
	ThresholdAmount domain.Money `json:"threshold_amount"`
	DiscountAmount  domain.Money `json:"discount_amount"`
	DiscountRate    domain.Rate  `json:"discount_rate"`
	Stackable       bool         `json:"stackable"`
	StartAt         time.Time    `json:"start_at"`
	EndAt           time.Time    `json:"end_at"`
}

func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toMerchantView(m domain.Merchant) merchantView {
	return merchantView{MerchantID: m.MerchantID, Name: m.Name, LogoURL: m.LogoURL, Description: m.Description, ServicePhone: m.ServicePhone}
}

func toProductCard(p store.CatalogProduct) productCard {
	card := productCard{
		ProductID: p.ProductID, MerchantID: p.MerchantID, MerchantName: p.MerchantName, CategoryID: p.CategoryID,
		Name: p.Name, Brand: p.Brand, ImageURL: p.ImageURL, Price: p.Price, MarketPrice: p.MarketPrice,
		StockStatus: p.StockStatus, Tags: strs(p.Tags), SellingPoints: strs(p.SellingPoints),
		RecommendReason: p.RecommendReason, RiskNotes: strs(p.RiskNotes),
	}
	for _, sku := range p.SKUs {
		if sku.IsDefault {
			card.SkuID = sku.SkuID
			break
		}
	}
	return card
}

func toProductDetail(p store.CatalogProduct) productDetail {
	attrs := make([]attributeView, 0, len(p.Attributes))
	for _, a := range p.Attributes {
		attrs = append(attrs, attributeView(a))
	}
	return productDetail{
		productCard: toProductCard(p), ImageURLs: strs(p.ImageURLs), StockQuantity: p.StockQuantity, Attributes: attrs,
		SuitableFor: strs(p.SuitableFor), NotSuitableFor: strs(p.NotSuitableFor), Description: p.Description,
	}
}

func toSKUView(s domain.ProductSKU) skuView {
	specs := s.Specs
	if specs == nil {
		specs = map[string]string{}
	}
	return skuView{SkuID: s.SkuID, ProductID: s.ProductID, SkuName: s.SkuName, Price: s.Price,
		StockQuantity: s.StockQuantity, StockStatus: s.StockStatus, Specs: specs, IsDefault: s.IsDefault}
}

func toReviewView(r store.PublicReview) reviewView {
	return reviewView{ReviewID: r.ReviewID, ProductID: r.ProductID, SkuID: r.SkuID, ReviewerName: maskName(r.ReviewerName),
		Rating: r.Rating, Content: r.Content, Tags: strs(r.Tags), MerchantReply: r.MerchantReply,
		MerchantRepliedAt: r.MerchantRepliedAt, CreatedAt: r.CreatedAt}
}

// maskName 只保留显示名的第一个字符；账户已注销或没有名字时显示“匿名用户”。
func maskName(name string) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) == 0 {
		return "匿名用户"
	}
	return string(runes[0]) + "***"
}

func toPromotionView(p domain.PromotionRule) promotionView {
	return promotionView{PromotionID: p.PromotionID, Name: p.Name, Scope: p.Scope, MerchantID: p.MerchantID,
		ProductID: p.ProductID, CategoryID: p.CategoryID, Type: p.Type, ThresholdAmount: p.ThresholdAmount,
		DiscountAmount: p.DiscountAmount, DiscountRate: p.DiscountRate, Stackable: p.Stackable, StartAt: p.StartAt, EndAt: p.EndAt}
}

// buildCategoryTree 把扁平分类组装成树；输入已按 (parent_id, sort_order, category_id) 排序。父分类不存在的子分类丢弃。
func buildCategoryTree(flat []domain.Category) []categoryNode {
	children := map[string][]domain.Category{}
	for _, c := range flat {
		children[c.ParentID] = append(children[c.ParentID], c)
	}
	var build func(parent string) []categoryNode
	build = func(parent string) []categoryNode {
		out := []categoryNode{}
		for _, c := range children[parent] {
			if c.CategoryID == parent { // 防御自引用
				continue
			}
			out = append(out, categoryNode{CategoryID: c.CategoryID, ParentID: c.ParentID, Name: c.Name, Children: build(c.CategoryID)})
		}
		return out
	}
	return build("")
}

// ---------- handlers ----------

func (s *Server) storeFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.ErrorContext(r.Context(), what+" failed", "request_id", requestIDFromContext(r.Context()), "error", err)
	writeError(w, ErrInternal)
}

type categoryTreeResponse struct {
	Items []categoryNode `json:"items"`
}

func (s *Server) handleCategoryTree(w http.ResponseWriter, r *http.Request) {
	flat, err := s.store.ListCategories(r.Context())
	if err != nil {
		s.storeFailed(w, r, "list categories", err)
		return
	}
	writeJSON(w, http.StatusOK, categoryTreeResponse{Items: buildCategoryTree(flat)})
}

func (s *Server) handleListMerchants(w http.ResponseWriter, r *http.Request) {
	page := readPage(r)
	items, total, err := s.store.ListActiveMerchants(r.Context(), page)
	if err != nil {
		s.storeFailed(w, r, "list merchants", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, page, total, toMerchantView))
}

const maxKeywordRunes = 64

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	q := store.ProductSearch{
		Keyword:    strings.TrimSpace(r.URL.Query().Get("keyword")),
		CategoryID: strings.TrimSpace(r.URL.Query().Get("category_id")),
		Page:       readPage(r),
	}
	if len([]rune(q.Keyword)) > maxKeywordRunes {
		writeError(w, invalidArgument("关键词最多 64 个字符"))
		return
	}
	items, total, err := s.store.SearchVisibleProducts(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "search products", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toProductCard))
}

// visibleProduct 读取公开可见的商品；不存在或不可见时已写出 404。
func (s *Server) visibleProduct(w http.ResponseWriter, r *http.Request, id string) (store.CatalogProduct, bool) {
	p, err := s.store.GetVisibleProduct(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrProductNotFound)
		return store.CatalogProduct{}, false
	case err != nil:
		s.storeFailed(w, r, "get product", err)
		return store.CatalogProduct{}, false
	}
	return p, true
}

func (s *Server) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.visibleProduct(w, r, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, toProductDetail(p))
	}
}

// handleListSKUs 分页返回商品的 SKU（默认 SKU 在前），与上游一致独立于详情接口，客户端可懒加载。
func (s *Server) handleListSKUs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.visibleProduct(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	page := readPage(r)
	start := min(page.Offset(), len(p.SKUs))
	end := min(start+page.PageSize, len(p.SKUs))
	writeJSON(w, http.StatusOK, newPage(p.SKUs[start:end], page, len(p.SKUs), toSKUView))
}

func (s *Server) handleListReviews(w http.ResponseWriter, r *http.Request) {
	p, ok := s.visibleProduct(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	page := readPage(r)
	items, total, err := s.store.ListVisibleReviews(r.Context(), p.ProductID, page)
	if err != nil {
		s.storeFailed(w, r, "list reviews", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, page, total, toReviewView))
}

// handleListPromotions 返回当前有效的促销；带 product_id 时只返回适用于该商品的促销（商品不可见时 404）。
func (s *Server) handleListPromotions(w http.ResponseWriter, r *http.Request) {
	q := store.PromotionQuery{At: s.now(), Page: readPage(r)}
	if id := strings.TrimSpace(r.URL.Query().Get("product_id")); id != "" {
		p, ok := s.visibleProduct(w, r, id)
		if !ok {
			return
		}
		q.ProductID, q.MerchantID = p.ProductID, p.MerchantID
		q.CategoryIDs = []string{p.CategoryID}
		if parent, err := s.parentCategory(r, p.CategoryID); err != nil {
			s.storeFailed(w, r, "list categories", err)
			return
		} else if parent != "" {
			q.CategoryIDs = append(q.CategoryIDs, parent)
		}
	}
	items, total, err := s.store.ListActivePromotions(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list promotions", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toPromotionView))
}

func (s *Server) parentCategory(r *http.Request, categoryID string) (string, error) {
	cats, err := s.store.ListCategories(r.Context())
	if err != nil {
		return "", err
	}
	for _, c := range cats {
		if c.CategoryID == categoryID {
			return c.ParentID, nil
		}
	}
	return "", nil
}

// handleGetAsset 公开读取内嵌静态资源（演示商品图等）。只服务文件，不列目录。
func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(r.PathValue("path"))
	info, err := fs.Stat(assets.FS, name)
	if err != nil || info.IsDir() || !fs.ValidPath(name) {
		writeError(w, &APIError{http.StatusNotFound, "not_found", "资源不存在"})
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFileFS(w, r, assets.FS, name)
}
