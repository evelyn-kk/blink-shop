package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

var (
	ErrProductUnderReview = &APIError{Status: http.StatusConflict, Code: "product_under_review", Message: "商品正在风控审核，不能自行上下架"}
	ErrMerchantProductNF  = &APIError{Status: http.StatusNotFound, Code: "product_not_found", Message: "商品不存在或已删除"}
)

// fieldError 是带字段定位的 400。
func fieldError(field, message string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: message, Field: field}
}

// 字段上限。金额上限与 DECIMAL(10,2) 一致。
const (
	maxProductName  = 128
	maxBrand        = 64
	maxImages       = 9
	maxURL          = 512
	maxSKUs         = 20
	maxSKUName      = 64
	maxSpecs        = 10
	maxStock        = 1_000_000
	maxListItems    = 10
	maxAttributes   = 30
	maxReason       = 200
	maxDescription  = 5000
	newProductOrder = 1000 // 商家新建商品排在演示商品之后
)

var maxMoney = domain.MustMoney("99999999.99")

// ---------- 请求 ----------

// productInput 是创建/修改商品的请求。字段缺省（nil）表示不修改；创建时缺省取默认值。
// 价格和库存有两种写法：skus 列表（推荐），或 price + stock_quantity 只维护默认规格（兼容上游）。
type productInput struct {
	MerchantID      *string          `json:"merchant_id"` // 只能是自己的店铺或不传
	Name            *string          `json:"name"`
	Brand           *string          `json:"brand"`
	CategoryID      *string          `json:"category_id"`
	ImageURL        *string          `json:"image_url"`
	ImageURLs       *[]string        `json:"image_urls"`
	Price           json.RawMessage  `json:"price"`
	MarketPrice     json.RawMessage  `json:"market_price"`
	StockQuantity   *int             `json:"stock_quantity"`
	StockStatus     *string          `json:"stock_status"` // 上游字段，忽略：库存状态由数量推导
	Tags            *[]string        `json:"tags"`
	SellingPoints   *[]string        `json:"selling_points"`
	RiskNotes       *[]string        `json:"risk_notes"`
	SuitableFor     *[]string        `json:"suitable_for"`
	NotSuitableFor  *[]string        `json:"not_suitable_for"`
	Attributes      *[]attributeView `json:"attributes"`
	RecommendReason *string          `json:"recommend_reason"`
	Description     *string          `json:"description"`
	Status          *string          `json:"status"`
	SKUs            *[]skuInput      `json:"skus"`
}

type skuInput struct {
	SkuID         string            `json:"sku_id"`
	SkuName       string            `json:"sku_name"`
	Price         json.RawMessage   `json:"price"`
	StockQuantity *int              `json:"stock_quantity"`
	Specs         map[string]string `json:"specs"`
	IsDefault     bool              `json:"is_default"`
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

// parseMoney 解析金额字段（字符串 "12.30" 或数字），要求 (0, 99999999.99]；allowZero 时允许 0。
func parseMoney(field string, raw json.RawMessage, allowZero bool) (domain.Money, error) {
	var m domain.Money
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, fieldError(field, "金额格式不正确，最多两位小数且不能为负")
	}
	if m == 0 && !allowZero {
		return 0, fieldError(field, "金额必须大于 0")
	}
	if m > maxMoney {
		return 0, fieldError(field, "金额不能超过 99999999.99")
	}
	return m, nil
}

func checkText(field, value string, required bool, max int) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fieldError(field, "不能为空")
	}
	if utf8.RuneCountInString(value) > max {
		return "", fieldError(field, fmt.Sprintf("最多 %d 个字符", max))
	}
	for _, c := range value {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			return "", fieldError(field, "不能包含控制字符")
		}
	}
	return value, nil
}

// checkList 去掉空白项和重复项后校验条数和长度。
func checkList(field string, items []string, maxItems, maxLen int) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for i, it := range items {
		v, err := checkText(fmt.Sprintf("%s[%d]", field, i), it, false, maxLen)
		if err != nil {
			return nil, err
		}
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) > maxItems {
		return nil, fieldError(field, fmt.Sprintf("最多 %d 项", maxItems))
	}
	return out, nil
}

func checkStock(field string, n int) error {
	if n < 0 || n > maxStock {
		return fieldError(field, "库存必须是 0 到 1000000 之间的整数")
	}
	return nil
}

// checkImageURL 只接受内嵌资源地址（必须存在）或 https 地址。上传文件的归属校验在 3.1 接入私有文件后补充。
func checkImageURL(field, raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", nil
	}
	if len(u) > maxURL {
		return "", fieldError(field, "图片地址过长")
	}
	if name, ok := strings.CutPrefix(u, "/api/v1/assets/"); ok {
		if info, err := fs.Stat(assets.FS, name); err != nil || info.IsDir() || !fs.ValidPath(name) {
			return "", fieldError(field, "图片不存在")
		}
		return u, nil
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || strings.ContainsAny(u, " \t\n\"'<>") {
		return "", fieldError(field, "图片地址必须是 https 链接或平台内图片")
	}
	return u, nil
}

// ---------- 应用请求到商品 ----------

// applyProductInput 把请求合并到 p（创建时 p 为空白商品），校验所有字段。categories 是全部分类 ID。
func applyProductInput(p *domain.Product, in productInput, creating bool, categories map[string]bool) error {
	if in.Name != nil || creating {
		v, err := checkText("name", deref(in.Name), true, maxProductName)
		if err != nil {
			return err
		}
		p.Name = v
	}
	if in.Brand != nil {
		v, err := checkText("brand", *in.Brand, false, maxBrand)
		if err != nil {
			return err
		}
		p.Brand = v
	}
	if in.CategoryID != nil || creating {
		id := strings.TrimSpace(deref(in.CategoryID))
		if id == "" {
			return fieldError("category_id", "请选择分类")
		}
		if !categories[id] {
			return fieldError("category_id", "分类不存在")
		}
		p.CategoryID = id
	}
	if err := applyImages(p, in); err != nil {
		return err
	}
	lists := []struct {
		field    string
		in       *[]string
		dst      *[]string
		maxItems int
		maxLen   int
	}{
		{"tags", in.Tags, &p.Tags, maxListItems, 16},
		{"selling_points", in.SellingPoints, &p.SellingPoints, maxListItems, 64},
		{"risk_notes", in.RiskNotes, &p.RiskNotes, maxListItems, 64},
		{"suitable_for", in.SuitableFor, &p.SuitableFor, maxListItems, 32},
		{"not_suitable_for", in.NotSuitableFor, &p.NotSuitableFor, maxListItems, 32},
	}
	for _, l := range lists {
		if l.in == nil {
			if *l.dst == nil {
				*l.dst = []string{}
			}
			continue
		}
		v, err := checkList(l.field, *l.in, l.maxItems, l.maxLen)
		if err != nil {
			return err
		}
		*l.dst = v
	}
	if in.Attributes != nil {
		attrs, err := checkAttributes(*in.Attributes)
		if err != nil {
			return err
		}
		p.Attributes = attrs
	} else if p.Attributes == nil {
		p.Attributes = []domain.ProductAttribute{}
	}
	if in.RecommendReason != nil {
		v, err := checkText("recommend_reason", *in.RecommendReason, false, maxReason)
		if err != nil {
			return err
		}
		p.RecommendReason = v
	}
	if in.Description != nil {
		v, err := checkText("description", *in.Description, false, maxDescription)
		if err != nil {
			return err
		}
		p.Description = v
	}
	if in.Status != nil {
		st := domain.ProductStatus(strings.TrimSpace(*in.Status))
		if st != domain.ProductActive && st != domain.ProductInactive {
			return fieldError("status", "状态只能是 active（上架）或 inactive（下架）")
		}
		if st != p.Status {
			if p.Status == domain.ProductRisk {
				return ErrProductUnderReview
			}
			p.Status = st
		}
	} else if creating {
		p.Status = domain.ProductActive
	}
	if err := applySKUs(p, in, creating); err != nil {
		return err
	}
	return applyMarketPrice(p, in, creating)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func applyImages(p *domain.Product, in productInput) error {
	if in.ImageURLs != nil {
		if len(*in.ImageURLs) > maxImages {
			return fieldError("image_urls", "最多 9 张图片")
		}
		urls := []string{}
		for i, raw := range *in.ImageURLs {
			u, err := checkImageURL(fmt.Sprintf("image_urls[%d]", i), raw)
			if err != nil {
				return err
			}
			if u != "" && !containsString(urls, u) {
				urls = append(urls, u)
			}
		}
		p.ImageURLs = urls
	} else if p.ImageURLs == nil {
		p.ImageURLs = []string{}
	}
	if in.ImageURL != nil {
		u, err := checkImageURL("image_url", *in.ImageURL)
		if err != nil {
			return err
		}
		p.ImageURL = u
	}
	// 主图必须在图片列表中：没有主图时取第一张，主图不在列表中时放到最前面。
	switch {
	case p.ImageURL == "" && len(p.ImageURLs) > 0:
		p.ImageURL = p.ImageURLs[0]
	case p.ImageURL != "" && !containsString(p.ImageURLs, p.ImageURL):
		if in.ImageURLs != nil && in.ImageURL == nil {
			// 只改了图片列表且旧主图被移除：主图跟随新列表。
			p.ImageURL = ""
			if len(p.ImageURLs) > 0 {
				p.ImageURL = p.ImageURLs[0]
			}
		} else {
			p.ImageURLs = append([]string{p.ImageURL}, p.ImageURLs...)
		}
	}
	if len(p.ImageURLs) > maxImages {
		return fieldError("image_urls", "最多 9 张图片")
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, it := range list {
		if it == v {
			return true
		}
	}
	return false
}

func checkAttributes(in []attributeView) ([]domain.ProductAttribute, error) {
	if len(in) > maxAttributes {
		return nil, fieldError("attributes", "最多 30 个参数")
	}
	out := []domain.ProductAttribute{}
	for i, a := range in {
		key, err := checkText(fmt.Sprintf("attributes[%d].key", i), a.Key, false, 16)
		if err != nil {
			return nil, err
		}
		value, err := checkText(fmt.Sprintf("attributes[%d].value", i), a.Value, false, 64)
		if err != nil {
			return nil, err
		}
		unit, err := checkText(fmt.Sprintf("attributes[%d].unit", i), a.Unit, false, 8)
		if err != nil {
			return nil, err
		}
		if key == "" && value == "" && unit == "" {
			continue // 空行忽略
		}
		if key == "" || value == "" {
			return nil, fieldError(fmt.Sprintf("attributes[%d]", i), "参数名和参数值都不能为空")
		}
		out = append(out, domain.ProductAttribute{Key: key, Value: value, Unit: unit})
	}
	return out, nil
}

func applySKUs(p *domain.Product, in productInput, creating bool) error {
	simple := present(in.Price) || in.StockQuantity != nil
	if in.SKUs != nil && simple {
		return fieldError("skus", "skus 与 price / stock_quantity 只能二选一")
	}
	switch {
	case in.SKUs != nil:
		skus, err := checkSKUs(*in.SKUs, p.SKUs, creating)
		if err != nil {
			return err
		}
		p.SKUs = skus
	case simple || creating:
		// 只维护默认规格：创建时新建“默认款”，修改时更新现有默认规格的价格和库存。
		idx := -1
		for i := range p.SKUs {
			if p.SKUs[i].IsDefault {
				idx = i
			}
		}
		if idx < 0 {
			p.SKUs = append(p.SKUs, domain.ProductSKU{SkuName: "默认款", IsDefault: true, Specs: map[string]string{}})
			idx = len(p.SKUs) - 1
		}
		if present(in.Price) {
			m, err := parseMoney("price", in.Price, false)
			if err != nil {
				return err
			}
			p.SKUs[idx].Price = m
		} else if creating {
			return fieldError("price", "请填写价格，或提供 skus")
		}
		if in.StockQuantity != nil {
			if err := checkStock("stock_quantity", *in.StockQuantity); err != nil {
				return err
			}
			p.SKUs[idx].StockQuantity = *in.StockQuantity
		}
	}
	return nil
}

// checkSKUs 校验并转换整个 SKU 列表：修改时带 sku_id 的必须是本商品已有的规格，不带的视为新增；
// 不在列表里的规格会被删除。没有任何规格标为默认时第一个为默认。
func checkSKUs(in []skuInput, existing []domain.ProductSKU, creating bool) ([]domain.ProductSKU, error) {
	if len(in) == 0 {
		return nil, fieldError("skus", "至少需要一个规格")
	}
	if len(in) > maxSKUs {
		return nil, fieldError("skus", "最多 20 个规格")
	}
	old := map[string]domain.ProductSKU{}
	for _, s := range existing {
		old[s.SkuID] = s
	}
	out := make([]domain.ProductSKU, 0, len(in))
	seen := map[string]bool{}
	defaults := 0
	for i, s := range in {
		f := func(name string) string { return fmt.Sprintf("skus[%d].%s", i, name) }
		sku := domain.ProductSKU{}
		if id := strings.TrimSpace(s.SkuID); id != "" {
			prev, ok := old[id]
			if creating || !ok {
				return nil, fieldError(f("sku_id"), "规格不属于这个商品")
			}
			if seen[id] {
				return nil, fieldError(f("sku_id"), "规格重复")
			}
			seen[id] = true
			sku = prev
		}
		name, err := checkText(f("sku_name"), s.SkuName, true, maxSKUName)
		if err != nil {
			return nil, err
		}
		sku.SkuName = name
		if !present(s.Price) {
			return nil, fieldError(f("price"), "请填写价格")
		}
		if sku.Price, err = parseMoney(f("price"), s.Price, false); err != nil {
			return nil, err
		}
		if s.StockQuantity == nil {
			return nil, fieldError(f("stock_quantity"), "请填写库存")
		}
		if err := checkStock(f("stock_quantity"), *s.StockQuantity); err != nil {
			return nil, err
		}
		sku.StockQuantity = *s.StockQuantity
		if len(s.Specs) > maxSpecs {
			return nil, fieldError(f("specs"), "最多 10 个规格属性")
		}
		sku.Specs = map[string]string{}
		for k, v := range s.Specs {
			key, err := checkText(f("specs"), k, true, 16)
			if err != nil {
				return nil, err
			}
			val, err := checkText(f("specs"), v, true, 32)
			if err != nil {
				return nil, err
			}
			sku.Specs[key] = val
		}
		sku.IsDefault = s.IsDefault
		if s.IsDefault {
			defaults++
		}
		out = append(out, sku)
	}
	switch defaults {
	case 0:
		out[0].IsDefault = true
	case 1:
	default:
		return nil, fieldError("skus", "只能有一个默认规格")
	}
	return out, nil
}

// applyMarketPrice：市场价可为 0（不展示划线价）；非 0 时不能低于售价（默认规格价格）。创建时缺省等于售价。
func applyMarketPrice(p *domain.Product, in productInput, creating bool) error {
	price := p.Price
	for _, s := range p.SKUs {
		if s.IsDefault {
			price = s.Price
		}
	}
	switch {
	case present(in.MarketPrice):
		m, err := parseMoney("market_price", in.MarketPrice, true)
		if err != nil {
			return err
		}
		p.MarketPrice = m
	case creating:
		p.MarketPrice = price
	}
	if p.MarketPrice != 0 && p.MarketPrice < price {
		return fieldError("market_price", "市场价不能低于售价")
	}
	return nil
}

// ---------- 响应 ----------

// merchantProductView 是商家后台看到的商品：比公开结构多状态、全部规格和时间。
type merchantProductView struct {
	ProductID       string               `json:"product_id"`
	MerchantID      string               `json:"merchant_id"`
	CategoryID      string               `json:"category_id"`
	Name            string               `json:"name"`
	Brand           string               `json:"brand"`
	ImageURL        string               `json:"image_url"`
	ImageURLs       []string             `json:"image_urls"`
	Price           domain.Money         `json:"price"`
	MarketPrice     domain.Money         `json:"market_price"`
	StockQuantity   int                  `json:"stock_quantity"`
	StockStatus     domain.StockStatus   `json:"stock_status"`
	Tags            []string             `json:"tags"`
	SellingPoints   []string             `json:"selling_points"`
	RecommendReason string               `json:"recommend_reason"`
	RiskNotes       []string             `json:"risk_notes"`
	Attributes      []attributeView      `json:"attributes"`
	SuitableFor     []string             `json:"suitable_for"`
	NotSuitableFor  []string             `json:"not_suitable_for"`
	Description     string               `json:"description"`
	Status          domain.ProductStatus `json:"status"`
	SKUs            []skuView            `json:"skus"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

func toMerchantProductView(p domain.Product) merchantProductView {
	d := toProductDetail(store.CatalogProduct{Product: p})
	skus := make([]skuView, 0, len(p.SKUs))
	for _, s := range p.SKUs {
		skus = append(skus, toSKUView(s))
	}
	return merchantProductView{
		ProductID: p.ProductID, MerchantID: p.MerchantID, CategoryID: p.CategoryID, Name: p.Name, Brand: p.Brand,
		ImageURL: p.ImageURL, ImageURLs: d.ImageURLs, Price: p.Price, MarketPrice: p.MarketPrice,
		StockQuantity: p.StockQuantity, StockStatus: p.StockStatus, Tags: d.Tags, SellingPoints: d.SellingPoints,
		RecommendReason: p.RecommendReason, RiskNotes: d.RiskNotes, Attributes: d.Attributes, SuitableFor: d.SuitableFor,
		NotSuitableFor: d.NotSuitableFor, Description: p.Description, Status: p.Status, SKUs: skus,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type productDeletedResponse struct {
	ProductID string               `json:"product_id"`
	Status    domain.ProductStatus `json:"status"`
}

// ---------- handlers ----------

func (s *Server) categorySet(r *http.Request) (map[string]bool, error) {
	cats, err := s.store.ListCategories(r.Context())
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(cats))
	for _, c := range cats {
		set[c.CategoryID] = true
	}
	return set, nil
}

func (s *Server) handleListMerchantProducts(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	q := store.MerchantProductQuery{
		MerchantID: acc.MerchantID,
		Status:     domain.ProductStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
		Keyword:    strings.TrimSpace(r.URL.Query().Get("keyword")),
		Page:       readPage(r),
	}
	switch q.Status {
	case "", domain.ProductActive, domain.ProductInactive, domain.ProductRisk:
	default:
		writeError(w, fieldError("status", "状态只能是 active、inactive 或 risk"))
		return
	}
	if utf8.RuneCountInString(q.Keyword) > maxKeywordRunes {
		writeError(w, fieldError("keyword", "关键词最多 64 个字符"))
		return
	}
	items, total, err := s.store.ListMerchantProducts(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list merchant products", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, toMerchantProductView))
}

// ownProduct 读取当前商家的商品：不存在或已删除 404，属于其他商家 403（与 RBAC 矩阵一致）。
func ownProduct(acc domain.Account, p domain.Product) error {
	if p.Status == domain.ProductDeleted {
		return ErrMerchantProductNF
	}
	return requireMerchantOwner(acc, p.MerchantID)
}

func (s *Server) handleGetMerchantProduct(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	p, err := s.store.GetProduct(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrMerchantProductNF)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "get merchant product", err)
		return
	}
	if err := ownProduct(acc, p); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMerchantProductView(p))
}

func (s *Server) handleCreateMerchantProduct(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in productInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.MerchantID != nil && strings.TrimSpace(*in.MerchantID) != "" && strings.TrimSpace(*in.MerchantID) != acc.MerchantID {
		writeError(w, fieldError("merchant_id", "不能为其他商家创建商品"))
		return
	}
	cats, err := s.categorySet(r)
	if err != nil {
		s.storeFailed(w, r, "list categories", err)
		return
	}
	p := domain.Product{MerchantID: acc.MerchantID, SortOrder: newProductOrder}
	if err := applyProductInput(&p, in, true, cats); err != nil {
		writeError(w, err)
		return
	}
	created, err := s.store.CreateProduct(r.Context(), p)
	if err != nil {
		s.storeFailed(w, r, "create product", err)
		return
	}
	s.audit(r, "product.created", acc.AccountID, "merchant_id", acc.MerchantID, "product_id", created.ProductID)
	writeJSON(w, http.StatusCreated, toMerchantProductView(created))
}

func (s *Server) handleUpdateMerchantProduct(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in productInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.MerchantID != nil && strings.TrimSpace(*in.MerchantID) != "" && strings.TrimSpace(*in.MerchantID) != acc.MerchantID {
		writeError(w, fieldError("merchant_id", "不能把商品转给其他商家"))
		return
	}
	cats, err := s.categorySet(r)
	if err != nil {
		s.storeFailed(w, r, "list categories", err)
		return
	}
	// 归属、状态和字段校验都在行锁内完成，避免与并发的删除或修改交错。
	updated, err := s.store.UpdateProduct(r.Context(), r.PathValue("id"), func(p *domain.Product) error {
		if err := ownProduct(acc, *p); err != nil {
			return err
		}
		return applyProductInput(p, in, false, cats)
	})
	if s.writeProductMutation(w, r, err) {
		s.audit(r, "product.updated", acc.AccountID, "merchant_id", acc.MerchantID, "product_id", updated.ProductID,
			"status", string(updated.Status))
		writeJSON(w, http.StatusOK, toMerchantProductView(updated))
	}
}

// handleDeleteMerchantProduct 软删：状态改为 deleted（终态），公开目录和商家列表都不再出现；订单快照不受影响。
func (s *Server) handleDeleteMerchantProduct(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id := r.PathValue("id")
	_, err := s.store.UpdateProduct(r.Context(), id, func(p *domain.Product) error {
		if err := ownProduct(acc, *p); err != nil {
			return err
		}
		p.Status = domain.ProductDeleted
		return nil
	})
	if s.writeProductMutation(w, r, err) {
		s.audit(r, "product.deleted", acc.AccountID, "merchant_id", acc.MerchantID, "product_id", id)
		writeJSON(w, http.StatusOK, productDeletedResponse{ProductID: id, Status: domain.ProductDeleted})
	}
}

// writeProductMutation 处理写操作的错误，成功时返回 true（由调用方写响应）。
func (s *Server) writeProductMutation(w http.ResponseWriter, r *http.Request, err error) bool {
	var apiErr *APIError
	switch {
	case err == nil:
		return true
	case errors.As(err, &apiErr):
		writeError(w, apiErr)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrMerchantProductNF)
	default:
		s.storeFailed(w, r, "update product", err)
	}
	return false
}
