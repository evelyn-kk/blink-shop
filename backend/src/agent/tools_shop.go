package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/pricing"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
	// searchRecall 是关键词召回的候选数，重排后再截断。
	searchRecall = 50
)

// ---------- 商品 ----------

// SKUCard 是商品卡里的规格。
type SKUCard struct {
	SkuID         string             `json:"sku_id"`
	SkuName       string             `json:"sku_name"`
	Price         domain.Money       `json:"price"`
	StockQuantity int                `json:"stock_quantity"`
	StockStatus   domain.StockStatus `json:"stock_status"`
	IsDefault     bool               `json:"is_default"`
}

// ProductCard 是工具返回、也是 product_list / comparison 块里的商品：字段全部来自商品库。
type ProductCard struct {
	ProductID       string                    `json:"product_id"`
	Name            string                    `json:"name"`
	Brand           string                    `json:"brand"`
	ImageURL        string                    `json:"image_url"`
	Price           domain.Money              `json:"price"`
	MarketPrice     domain.Money              `json:"market_price"`
	StockQuantity   int                       `json:"stock_quantity"`
	StockStatus     domain.StockStatus        `json:"stock_status"`
	MerchantID      string                    `json:"merchant_id"`
	MerchantName    string                    `json:"merchant_name"`
	CategoryID      string                    `json:"category_id"`
	CategoryName    string                    `json:"category_name"`
	Tags            []string                  `json:"tags"`
	SellingPoints   []string                  `json:"selling_points"`
	RecommendReason string                    `json:"recommend_reason"`
	RiskNotes       []string                  `json:"risk_notes"`
	SuitableFor     []string                  `json:"suitable_for"`
	NotSuitableFor  []string                  `json:"not_suitable_for"`
	Attributes      []domain.ProductAttribute `json:"attributes"`
	SKUs            []SKUCard                 `json:"skus"`
}

func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toProductCard(p store.CatalogProduct, categoryName string) ProductCard {
	c := ProductCard{ProductID: p.ProductID, Name: p.Name, Brand: p.Brand, ImageURL: p.ImageURL, Price: p.Price, MarketPrice: p.MarketPrice,
		StockQuantity: p.StockQuantity, StockStatus: p.StockStatus, MerchantID: p.MerchantID, MerchantName: p.MerchantName, CategoryID: p.CategoryID,
		CategoryName: categoryName, Tags: strs(p.Tags), SellingPoints: strs(p.SellingPoints), RecommendReason: p.RecommendReason,
		RiskNotes: strs(p.RiskNotes), SuitableFor: strs(p.SuitableFor), NotSuitableFor: strs(p.NotSuitableFor), Attributes: p.Attributes, SKUs: []SKUCard{}}
	if c.Attributes == nil {
		c.Attributes = []domain.ProductAttribute{}
	}
	for _, s := range p.SKUs {
		c.SKUs = append(c.SKUs, SKUCard{SkuID: s.SkuID, SkuName: s.SkuName, Price: s.Price, StockQuantity: s.StockQuantity, StockStatus: s.StockStatus, IsDefault: s.IsDefault})
	}
	return c
}

// text 是用来判断相关性和排除的全部文本（小写）。
func (c ProductCard) text() string {
	parts := []string{c.Name, c.Brand, c.CategoryName, c.RecommendReason}
	parts = append(parts, c.Tags...)
	parts = append(parts, c.SellingPoints...)
	parts = append(parts, c.SuitableFor...)
	for _, a := range c.Attributes {
		parts = append(parts, a.Key, a.Value)
	}
	for _, s := range c.SKUs {
		parts = append(parts, s.SkuName)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// ProductSearchResult 是 search_products 的结果。Relevance：ok（有可靠命中）、weak（只有弱命中，不应当作推荐）、none（没有命中）。
type ProductSearchResult struct {
	Query     string        `json:"query"`
	Terms     []string      `json:"terms"`
	Products  []ProductCard `json:"products"`
	Total     int           `json:"total"`
	Relevance string        `json:"relevance"`
	MaxPrice  *domain.Money `json:"max_price,omitempty"`
	Excluded  []string      `json:"excluded,omitempty"`
	Filtered  int           `json:"filtered"` // 因预算或排除词去掉的候选数
}

const (
	RelevanceOK   = "ok"
	RelevanceWeak = "weak"
	RelevanceNone = "none"
)

func (r *Registry) categoryNames(ctx context.Context) (map[string]string, error) {
	cats, err := r.deps.Store.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(cats))
	for _, c := range cats {
		names[c.CategoryID] = c.Name
	}
	return names, nil
}

// searchProducts 关键词召回 → 预算/排除词过滤 → 按相关性重排 → 截断。
func (r *Registry) searchProducts(ctx context.Context, args map[string]any) (ProductSearchResult, error) {
	query := argString(args, "query")
	limit := argInt(args, "limit", defaultSearchLimit)
	out := ProductSearchResult{Query: query, Terms: QueryTerms(query), Products: []ProductCard{}, Relevance: RelevanceNone}
	if f, ok := argFloat(args, "max_price"); ok {
		m := domain.Money(f*100 + 0.5)
		out.MaxPrice = &m
	}
	for _, ex := range argStrings(args, "exclude") {
		if ex = strings.ToLower(ex); ex != "" {
			out.Excluded = append(out.Excluded, ex)
		}
	}
	names, err := r.categoryNames(ctx)
	if err != nil {
		return out, err
	}
	// 显式商品 ID：直接取这件商品（不存在或不可见就是没有结果）。
	if pid := argString(args, "product_id"); pid != "" {
		p, err := r.deps.Store.GetVisibleProduct(ctx, pid)
		if errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out.Products, out.Total, out.Relevance = []ProductCard{toProductCard(p, names[p.CategoryID])}, 1, RelevanceOK
		return out, nil
	}
	keyword := strings.Join(out.Terms, " ")
	if keyword == "" {
		keyword = query
	}
	items, _, err := r.deps.Store.SearchVisibleProducts(ctx, store.ProductSearch{Keyword: keyword, CategoryID: argString(args, "category_id"),
		Page: store.Page{Page: 1, PageSize: searchRecall}})
	if err != nil {
		return out, err
	}
	type scored struct {
		card   ProductCard
		score  float64
		strong bool
		index  int
	}
	var cands []scored
	for i, p := range items {
		card := toProductCard(p, names[p.CategoryID])
		text := card.text()
		if out.MaxPrice != nil && card.Price > *out.MaxPrice {
			out.Filtered++
			continue
		}
		if excludedBy(text, out.Excluded) {
			out.Filtered++
			continue
		}
		score, strong := relevance(out.Terms, strings.ToLower(card.Name), text)
		if score <= 0 {
			continue
		}
		cands = append(cands, scored{card: card, score: score, strong: strong, index: i})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].strong != cands[j].strong {
			return cands[i].strong
		}
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].card.Price != cands[j].card.Price {
			return cands[i].card.Price < cands[j].card.Price
		}
		return cands[i].index < cands[j].index
	})
	out.Total = len(cands)
	for i, c := range cands {
		if i == 0 {
			out.Relevance = RelevanceWeak
			if c.strong {
				out.Relevance = RelevanceOK
			}
		}
		if i < limit {
			out.Products = append(out.Products, c.card)
		}
	}
	return out, nil
}

func excludedBy(text string, excluded []string) bool {
	for _, ex := range excluded {
		if strings.Contains(text, ex) {
			return true
		}
	}
	return false
}

// relevance 给候选打分：每个检索词按它的二元组在商品文本中的覆盖率计分（整词出现在名称里加分）。
// strong 表示至少有一个词整体出现在文本里，或某个 ≥3 字的词有一半以上二元组命中——弱于此的只算“相近”。
func relevance(terms []string, name, text string) (float64, bool) {
	score, strong := 0.0, false
	for _, term := range terms {
		term = strings.ToLower(term)
		if strings.Contains(text, term) {
			score += 2
			if strings.Contains(name, term) {
				score += 1
			}
			strong = true
			continue
		}
		grams := bigrams(term)
		if len(grams) == 0 {
			continue
		}
		hit := 0
		for _, g := range grams {
			if strings.Contains(text, g) {
				hit++
			}
		}
		if hit == 0 {
			continue
		}
		cov := float64(hit) / float64(len(grams))
		score += cov
		if utf8.RuneCountInString(term) >= 3 && cov >= 0.5 {
			strong = true
		}
	}
	return score, strong
}

func bigrams(term string) []string {
	rs := []rune(term)
	if len(rs) < 2 {
		return nil
	}
	out := make([]string, 0, len(rs)-1)
	for i := 0; i+2 <= len(rs); i++ {
		out = append(out, string(rs[i:i+2]))
	}
	return out
}

// ---------- 知识 ----------

// KnowledgeResult 是 search_knowledge 的结果。
type KnowledgeResult struct {
	Query     string         `json:"query"`
	Mode      string         `json:"mode"`
	Citations []rag.Citation `json:"citations"`
}

func (r *Registry) searchKnowledge(ctx context.Context, args map[string]any) (KnowledgeResult, error) {
	if r.deps.Retriever == nil {
		return KnowledgeResult{}, &ToolError{Code: CodeUnavailable, Message: "知识库检索未配置"}
	}
	q := rag.Query{Text: argString(args, "query"), TopK: argInt(args, "limit", 3)}
	if pid := argString(args, "product_id"); pid != "" {
		q.ProductIDs = []string{pid}
	}
	res, err := r.deps.Retriever.Search(ctx, q)
	if err != nil {
		return KnowledgeResult{}, err
	}
	if res.Citations == nil {
		res.Citations = []rag.Citation{}
	}
	return KnowledgeResult{Query: q.Text, Mode: res.Mode, Citations: res.Citations}, nil
}

// ---------- 促销与评价 ----------

// PromotionCard 是促销活动及其说明。
type PromotionCard struct {
	PromotionID string       `json:"promotion_id"`
	Name        string       `json:"name"`
	Scope       string       `json:"scope"`
	MerchantID  string       `json:"merchant_id"`
	ProductID   string       `json:"product_id"`
	CategoryID  string       `json:"category_id"`
	Type        string       `json:"type"`
	Description string       `json:"description"`
	Threshold   domain.Money `json:"threshold_amount"`
	Stackable   bool         `json:"stackable"`
}

func (r *Registry) listPromotions(ctx context.Context, args map[string]any) (map[string]any, error) {
	q := store.PromotionQuery{At: r.deps.Now(), MerchantID: argString(args, "merchant_id"), Page: store.Page{Page: 1, PageSize: argInt(args, "limit", 10)}}
	if pid := argString(args, "product_id"); pid != "" {
		p, err := r.deps.Store.GetVisibleProduct(ctx, pid)
		if errors.Is(err, store.ErrNotFound) {
			return nil, shop.ErrProductNotFound
		}
		if err != nil {
			return nil, err
		}
		cats, err := r.deps.Store.ListCategories(ctx)
		if err != nil {
			return nil, err
		}
		parent := map[string]string{}
		for _, c := range cats {
			parent[c.CategoryID] = c.ParentID
		}
		q.ProductID, q.MerchantID = p.ProductID, p.MerchantID
		for c := p.CategoryID; c != "" && !containsStr(q.CategoryIDs, c); c = parent[c] {
			q.CategoryIDs = append(q.CategoryIDs, c)
		}
	}
	items, total, err := r.deps.Store.ListActivePromotions(ctx, q)
	if err != nil {
		return nil, err
	}
	cards := make([]PromotionCard, 0, len(items))
	for _, p := range items {
		cards = append(cards, PromotionCard{PromotionID: p.PromotionID, Name: p.Name, Scope: p.Scope, MerchantID: p.MerchantID, ProductID: p.ProductID,
			CategoryID: p.CategoryID, Type: p.Type, Description: pricing.DescribePromotion(p), Threshold: p.ThresholdAmount, Stackable: p.Stackable})
	}
	return map[string]any{"promotions": cards, "total": total}, nil
}

// ReviewCard 是商品评价。
type ReviewCard struct {
	ReviewID      string   `json:"review_id"`
	Rating        int      `json:"rating"`
	Content       string   `json:"content"`
	Tags          []string `json:"tags"`
	ReviewerName  string   `json:"reviewer_name"`
	MerchantReply string   `json:"merchant_reply"`
	CreatedAt     string   `json:"created_at"`
}

// ReviewsResult 是 list_reviews 的结果，带评分摘要。
type ReviewsResult struct {
	ProductID   string       `json:"product_id"`
	ProductName string       `json:"product_name"`
	Total       int          `json:"total"`
	Average     float64      `json:"average_rating"`
	Reviews     []ReviewCard `json:"reviews"`
}

func maskName(name string) string {
	rs := []rune(name)
	if len(rs) <= 1 {
		return "匿名用户"
	}
	return string(rs[0]) + "**"
}

func (r *Registry) listReviews(ctx context.Context, args map[string]any) (ReviewsResult, error) {
	pid := argString(args, "product_id")
	p, err := r.deps.Store.GetVisibleProduct(ctx, pid)
	if errors.Is(err, store.ErrNotFound) {
		return ReviewsResult{}, shop.ErrProductNotFound
	}
	if err != nil {
		return ReviewsResult{}, err
	}
	items, total, err := r.deps.Store.ListVisibleReviews(ctx, pid, store.Page{Page: 1, PageSize: argInt(args, "limit", 5)})
	if err != nil {
		return ReviewsResult{}, err
	}
	out := ReviewsResult{ProductID: pid, ProductName: p.Name, Total: total, Reviews: []ReviewCard{}}
	sum := 0
	for _, rv := range items {
		sum += rv.Rating
		out.Reviews = append(out.Reviews, ReviewCard{ReviewID: rv.ReviewID, Rating: rv.Rating, Content: rv.Content, Tags: strs(rv.Tags),
			ReviewerName: maskName(rv.ReviewerName), MerchantReply: rv.MerchantReply, CreatedAt: rv.CreatedAt.UTC().Format("2006-01-02")})
	}
	if len(items) > 0 {
		out.Average = float64(int(float64(sum)/float64(len(items))*10+0.5)) / 10
	}
	return out, nil
}

// ---------- 注册 ----------

func (r *Registry) shopTools() []*Tool {
	sh := r.deps.Shop
	cartAfter := func(ctx context.Context, tc *ToolContext, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return sh.Cart(ctx, tc.AccountID)
	}
	return []*Tool{
		{Name: ToolSearchProducts, Description: "搜索当前商品库：按关键词召回，按预算和排除词过滤，再按相关性排序。只有 relevance=ok 的结果能当作推荐。",
			Schema: object([]string{"query"}, map[string]*Schema{
				"query":       strLen("商品关键词（品类、品牌、型号、用途）", 1, 100),
				"product_id":  str("直接按商品 ID 取一件商品（用户明确指定时）"),
				"category_id": str("限定分类 ID（含子分类）"),
				"limit":       integer("返回数量", 1, maxSearchLimit, defaultSearchLimit),
				"max_price":   number("价格上限（元）", 0),
				"exclude":     strList("排除词：名称、品牌、标签或卖点包含任一词的商品不返回", 10, 30),
			}),
			Run: func(ctx context.Context, _ *ToolContext, args map[string]any) (any, error) {
				return r.searchProducts(ctx, args)
			}},
		{Name: ToolSearchKnowledge, Description: "检索知识库（商品说明、售后政策、使用指南），返回可引用的分块；回答只能引用返回的片段。",
			Schema: object([]string{"query"}, map[string]*Schema{
				"query":      strLen("要查证的问题", 1, 200),
				"product_id": str("只看某个商品的资料"),
				"limit":      integer("返回数量", 1, rag.MaxTopK, 3),
			}),
			Run: func(ctx context.Context, _ *ToolContext, args map[string]any) (any, error) {
				return r.searchKnowledge(ctx, args)
			}},
		{Name: ToolGetCart, Description: "读取当前用户的购物车（含每项是否可购买、自动选券后的金额）。", Schema: object(nil, map[string]*Schema{}),
			Run: func(ctx context.Context, tc *ToolContext, _ map[string]any) (any, error) {
				return sh.Cart(ctx, tc.AccountID)
			}},
		{Name: ToolAddCartItem, Write: true, Description: "把商品加入当前用户的购物车；product_id 必须是本轮给用户看过的商品。",
			Schema: object([]string{"product_id"}, map[string]*Schema{
				"product_id": strLen("商品 ID", 1, 64),
				"sku_id":     str("规格 ID，空表示默认规格"),
				"quantity":   integer("数量", 1, shop.MaxLineQuantity, 1),
			}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				item, added, err := sh.AddCartItem(ctx, tc.AccountID, shop.AddCartItemInput{ProductID: argString(args, "product_id"), SkuID: argString(args, "sku_id"), Quantity: argIntPtr(args, "quantity")})
				if err != nil {
					return nil, err
				}
				cart, err := sh.Cart(ctx, tc.AccountID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"cart_item_id": item.CartItemID, "product_id": item.ProductID, "sku_id": item.SkuID, "added": added, "quantity": item.Quantity, "cart": cart}, nil
			}},
		{Name: ToolUpdateCartItem, Write: true, Description: "修改当前用户购物车项的数量或选中状态。",
			Schema: object([]string{"cart_item_id"}, map[string]*Schema{
				"cart_item_id": strLen("购物车项 ID", 1, 64),
				"quantity":     integer("数量", 1, shop.MaxLineQuantity, nil),
				"selected":     boolean("是否选中"),
			}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				_, err := sh.UpdateCartItem(ctx, tc.AccountID, argString(args, "cart_item_id"), shop.UpdateCartItemInput{Quantity: argIntPtr(args, "quantity"), Selected: argBoolPtr(args, "selected")})
				return cartAfter(ctx, tc, err)
			}},
		{Name: ToolDeleteCartItem, Write: true, Description: "删除当前用户的购物车项。",
			Schema: object([]string{"cart_item_id"}, map[string]*Schema{"cart_item_id": strLen("购物车项 ID", 1, 64)}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				return cartAfter(ctx, tc, sh.DeleteCartItem(ctx, tc.AccountID, argString(args, "cart_item_id")))
			}},
		{Name: ToolPreviewDiscount, Description: "对当前购物车做优惠试算：不传 user_coupon_ids 自动选最优券，传了只用指定的券。",
			Schema: object(nil, map[string]*Schema{"user_coupon_ids": strList("指定使用的券", shop.MaxCouponChoice, 64)}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				var choice []string
				if _, ok := args["user_coupon_ids"]; ok {
					choice = append([]string{}, argStrings(args, "user_coupon_ids")...)
				}
				return sh.PreviewDiscount(ctx, tc.AccountID, choice)
			}},
		{Name: ToolCheckout, Write: true, Description: "把当前用户购物车里选中的商品下单（按店铺拆单）；同一次运行重复调用不会重复下单。",
			Schema: object(nil, map[string]*Schema{"user_coupon_ids": strList("指定使用的券，不传自动选券", shop.MaxCouponChoice, 64)}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				in := shop.CheckoutInput{IdempotencyKey: "agent-" + tc.RunID}
				if _, ok := args["user_coupon_ids"]; ok {
					in.UserCouponIDs = append([]string{}, argStrings(args, "user_coupon_ids")...)
				}
				return sh.Checkout(ctx, tc.AccountID, in)
			}},
		{Name: ToolListOrders, Description: "查询当前用户的订单（最新的在前），可按状态或订单号筛选。",
			Schema: object(nil, map[string]*Schema{
				"status":   enum("订单状态", "pending_payment", "paid", "shipped", "completed", "cancelled"),
				"order_no": str("订单号"),
				"limit":    integer("返回数量", 1, 20, 5),
			}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				items, total, err := sh.ListOrders(ctx, store.OrderQuery{AccountID: tc.AccountID, Status: domain.OrderStatus(argString(args, "status")),
					OrderNo: argString(args, "order_no"), Page: store.Page{Page: 1, PageSize: argInt(args, "limit", 5)}})
				if err != nil {
					return nil, err
				}
				return map[string]any{"orders": items, "total": total}, nil
			}},
		{Name: ToolGetOrder, Description: "查询当前用户某个订单的详情（含支付单和已评价的订单项）。",
			Schema: object([]string{"order_id"}, map[string]*Schema{"order_id": strLen("订单 ID", 1, 64)}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				return sh.GetMyOrder(ctx, tc.AccountID, argString(args, "order_id"))
			}},
		{Name: ToolPayOrder, Write: true, Description: "模拟支付当前用户的待支付订单。",
			Schema: object([]string{"order_id"}, map[string]*Schema{
				"order_id": strLen("订单 ID", 1, 64),
				"method":   enum("支付方式", "mock_balance", "mock_wechat", "mock_alipay"),
			}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				return sh.PayOrder(ctx, tc.AccountID, argString(args, "order_id"), argString(args, "method"))
			}},
		{Name: ToolCancelOrder, Write: true, Description: "取消当前用户的待支付订单（回补库存、退券）。",
			Schema: object([]string{"order_id"}, map[string]*Schema{
				"order_id": strLen("订单 ID", 1, 64),
				"reason":   strLen("取消原因", 0, shop.MaxCancelReasonRunes),
			}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				return sh.CancelOrder(ctx, tc.AccountID, argString(args, "order_id"), argString(args, "reason"))
			}},
		{Name: ToolConfirmReceipt, Write: true, Description: "确认当前用户已发货订单的收货。",
			Schema: object([]string{"order_id"}, map[string]*Schema{"order_id": strLen("订单 ID", 1, 64)}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				return sh.ConfirmReceipt(ctx, tc.AccountID, argString(args, "order_id"))
			}},
		{Name: ToolListCoupons, Description: "查询当前可领取的优惠券（含本人已领张数和能否再领）。",
			Schema: object(nil, map[string]*Schema{"limit": integer("返回数量", 1, 20, 10)}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				items, total, err := sh.AvailableCoupons(ctx, tc.AccountID, store.Page{Page: 1, PageSize: argInt(args, "limit", 10)})
				if err != nil {
					return nil, err
				}
				return map[string]any{"coupons": items, "total": total}, nil
			}},
		{Name: ToolListUserCoupons, Description: "查询当前用户已领取的优惠券。",
			Schema: object(nil, map[string]*Schema{
				"status": enum("状态", "unused", "used", "expired"),
				"limit":  integer("返回数量", 1, 20, 10),
			}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				items, total, err := sh.MyCoupons(ctx, tc.AccountID, argString(args, "status"), store.Page{Page: 1, PageSize: argInt(args, "limit", 10)})
				if err != nil {
					return nil, err
				}
				return map[string]any{"coupons": items, "total": total}, nil
			}},
		{Name: ToolClaimCoupon, Write: true, Description: "为当前用户领取一张优惠券。",
			Schema: object([]string{"coupon_id"}, map[string]*Schema{"coupon_id": strLen("优惠券 ID", 1, 64)}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				return sh.ClaimCoupon(ctx, tc.AccountID, argString(args, "coupon_id"))
			}},
		{Name: ToolListPromotions, Description: "查询当前有效的促销活动；给 product_id 时只看适用于该商品的活动。",
			Schema: object(nil, map[string]*Schema{
				"product_id":  str("商品 ID"),
				"merchant_id": str("店铺 ID"),
				"limit":       integer("返回数量", 1, 20, 10),
			}),
			Run: func(ctx context.Context, _ *ToolContext, args map[string]any) (any, error) {
				return r.listPromotions(ctx, args)
			}},
		{Name: ToolListReviews, Description: "查询某个商品的可见评价和评分摘要。",
			Schema: object([]string{"product_id"}, map[string]*Schema{
				"product_id": strLen("商品 ID", 1, 64),
				"limit":      integer("返回数量", 1, 20, 5),
			}),
			Run: func(ctx context.Context, _ *ToolContext, args map[string]any) (any, error) {
				return r.listReviews(ctx, args)
			}},
		{Name: ToolCreateReview, Write: true, Description: "评价当前用户已完成订单里的某件商品，每件只能评价一次。",
			Schema: object([]string{"order_id", "order_item_id", "rating", "content"}, map[string]*Schema{
				"order_id":      strLen("订单 ID", 1, 64),
				"order_item_id": strLen("订单项 ID", 1, 64),
				"rating":        integer("评分", 1, 5, nil),
				"content":       strLen("评价内容", 1, shop.MaxReviewRunes),
				"tags":          strList("标签", shop.MaxReviewTags, shop.MaxReviewTagRunes),
			}),
			Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
				return sh.CreateReview(ctx, tc.AccountID, argString(args, "order_id"), argString(args, "order_item_id"),
					shop.ReviewInput{Rating: argIntPtr(args, "rating"), Content: argString(args, "content"), Tags: argStrings(args, "tags")})
			}},
	}
}

func containsStr(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
