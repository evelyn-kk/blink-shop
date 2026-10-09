package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// 结构化块（docs/03、openapi AgentBlock）。所有商品、订单、券、引用都来自工具返回，客户端只渲染不推断。
const (
	BlockProductList = "product_list" // {title, products: [ProductCard]}
	BlockComparison  = "comparison"   // {products: [{product_id, name}], rows: [{label, values: [...]}]}
	BlockCitation    = "citation"     // {citations: [rag.Citation]}
	BlockAction      = "action"       // {action: navigate, target, label, params}
	BlockCart        = "cart"         // shop.Cart + {hints}
	BlockOrderList   = "order_list"   // {orders: [shop.Order]}
	BlockCouponList  = "coupon_list"  // {available: [shop.AvailableCoupon], mine: [shop.UserCoupon]}
	BlockReviewList  = "review_list"  // ReviewsResult
	BlockPromotions  = "promotion_list"
)

// 导航目标（action 块的 target）。
const (
	TargetProducts      = "products"
	TargetProductDetail = "product_detail"
	TargetCart          = "cart"
	TargetOrders        = "orders"
	TargetOrderDetail   = "order_detail"
	TargetCoupons       = "coupons"
	TargetSessions      = "sessions"
	TargetSettings      = "settings"
)

// toJSONMap 把任意值转成 JSON 形态的 map：块在内存和数据库里的形状一致（数字为 float64、金额为字符串）。
func toJSONMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

func block(kind string, body any) map[string]any {
	m := toJSONMap(body)
	m["type"] = kind
	return m
}

func blockProducts(title string, cards []ProductCard) map[string]any {
	if cards == nil {
		cards = []ProductCard{}
	}
	return block(BlockProductList, map[string]any{"title": title, "products": cards})
}

type comparisonRow struct {
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

// blockComparison 把若干商品按统一字段排成对比表；属性行取所有商品属性名的并集。
func blockComparison(cards []ProductCard) map[string]any {
	heads := make([]map[string]string, 0, len(cards))
	for _, c := range cards {
		heads = append(heads, map[string]string{"product_id": c.ProductID, "name": c.Name, "image_url": c.ImageURL})
	}
	row := func(label string, f func(c ProductCard) string) comparisonRow {
		r := comparisonRow{Label: label, Values: make([]string, 0, len(cards))}
		for _, c := range cards {
			r.Values = append(r.Values, f(c))
		}
		return r
	}
	joinOr := func(v []string) string {
		if len(v) == 0 {
			return "—"
		}
		return strings.Join(v, "、")
	}
	rows := []comparisonRow{
		row("价格", func(c ProductCard) string { return yuan(c.Price) }),
		row("划线价", func(c ProductCard) string { return yuan(c.MarketPrice) }),
		row("品牌", func(c ProductCard) string { return c.Brand }),
		row("店铺", func(c ProductCard) string { return c.MerchantName }),
		row("库存", func(c ProductCard) string { return stockText(c.StockStatus, c.StockQuantity) }),
		row("卖点", func(c ProductCard) string { return joinOr(c.SellingPoints) }),
		row("适合", func(c ProductCard) string { return joinOr(c.SuitableFor) }),
		row("不适合", func(c ProductCard) string { return joinOr(c.NotSuitableFor) }),
		row("注意", func(c ProductCard) string { return joinOr(c.RiskNotes) }),
	}
	var keys []string
	for _, c := range cards {
		for _, a := range c.Attributes {
			if !containsStr(keys, a.Key) {
				keys = append(keys, a.Key)
			}
		}
	}
	for _, k := range keys {
		rows = append(rows, row(k, func(c ProductCard) string {
			for _, a := range c.Attributes {
				if a.Key == k {
					return strings.TrimSpace(a.Value + " " + a.Unit)
				}
			}
			return "—"
		}))
	}
	return block(BlockComparison, map[string]any{"products": heads, "rows": rows})
}

func blockCitations(cits []rag.Citation) map[string]any {
	if cits == nil {
		cits = []rag.Citation{}
	}
	return block(BlockCitation, map[string]any{"citations": cits})
}

func blockAction(target, label string, params map[string]string) map[string]any {
	if params == nil {
		params = map[string]string{}
	}
	return block(BlockAction, map[string]any{"action": "navigate", "target": target, "label": label, "params": params})
}

func blockCart(cart shop.Cart, hints []shop.Hint) map[string]any {
	if hints == nil {
		hints = []shop.Hint{}
	}
	m := block(BlockCart, cart)
	m["hints"] = toJSONMap(map[string]any{"hints": hints})["hints"]
	return m
}

func blockOrders(orders []shop.Order) map[string]any {
	if orders == nil {
		orders = []shop.Order{}
	}
	return block(BlockOrderList, map[string]any{"orders": orders})
}

func blockCoupons(available []shop.AvailableCoupon, mine []shop.UserCoupon) map[string]any {
	if available == nil {
		available = []shop.AvailableCoupon{}
	}
	if mine == nil {
		mine = []shop.UserCoupon{}
	}
	return block(BlockCouponList, map[string]any{"available": available, "mine": mine})
}

func blockPromotions(items []PromotionCard) map[string]any {
	if items == nil {
		items = []PromotionCard{}
	}
	return block(BlockPromotions, map[string]any{"promotions": items})
}

func blockReviews(res ReviewsResult) map[string]any { return block(BlockReviewList, res) }

// ---------- 文案辅助 ----------

// yuan 把金额格式化为 ¥2999 / ¥129.5。
func yuan(m domain.Money) string {
	s := m.String()
	s = strings.TrimSuffix(s, ".00")
	if strings.Contains(s, ".") {
		s = strings.TrimSuffix(s, "0")
	}
	return "¥" + s
}

func stockText(status domain.StockStatus, qty int) string {
	switch status {
	case domain.StockOutOfStock:
		return "缺货"
	case domain.StockLow:
		return "仅剩 " + itoa(qty) + " 件"
	}
	return "有货"
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// ---------- 会话上下文 ----------

// history 是会话里之前几轮给用户看过的商品：Cards 是最近一张商品卡/对比表里的商品（按展示顺序），
// Evidence 是全部出现过的商品 ID。“把第一个加购物车”按 Cards 解析；加购只接受 Evidence 里的商品。
type history struct {
	Cards    []ProductCard
	Evidence map[string]bool
}

// loadHistory 读取会话历史里的商品块；当前运行（RunID）排除。读取失败按没有历史处理（不影响回答）。
func loadHistory(ctx context.Context, st store.Store, accountID, sessionID, currentRunID string) history {
	h := history{Evidence: map[string]bool{}}
	turns, err := st.ListChatMessages(ctx, accountID, sessionID)
	if err != nil {
		return h
	}
	for _, t := range turns {
		if t.Run == nil || t.Run.RunID == currentRunID {
			continue
		}
		var cards []ProductCard
		for _, b := range t.Run.Blocks {
			kind, _ := b["type"].(string)
			if kind != BlockProductList && kind != BlockComparison {
				continue
			}
			raw, _ := json.Marshal(b["products"])
			var list []ProductCard
			if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
				cards = list
			}
		}
		if len(cards) > 0 {
			h.Cards = cards
			for _, c := range cards {
				h.Evidence[c.ProductID] = true
			}
		}
	}
	return h
}
