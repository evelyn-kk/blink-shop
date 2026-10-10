package agent

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
)

// 工具结果给模型看之前要压缩：去掉图片、属性、时间等对决策没用的字段，截断长文本和长数组，控制 token。
var observationDropKeys = map[string]bool{
	"image_url": true, "image_urls": true, "attributes": true, "skus": true, "category_id": true, "merchant_id": true, "created_at": true,
	"updated_at": true, "claimed_at": true, "used_at": true, "paid_at": true, "shipped_at": true, "completed_at": true, "closed_at": true,
	"expires_at": true, "transaction_no": true, "payment_id": true, "account_id": true, "sku_id": true, "stock_quantity": true,
	"source_url": true, "source": true, "matched_by": true, "score": true, "document_id": true, "merchant_reply": true, "terms": true,
	"lines": true, "merchants": true, "items_amount": true,
}

const (
	observationMaxItems = 8
	observationMaxRunes = 160
)

// observationForModel 把观察结果转成紧凑的 JSON 文本。
func observationForModel(obs Observation) string {
	payload := map[string]any{"tool": obs.Tool, "ok": obs.OK}
	if !obs.OK {
		payload["code"], payload["message"] = obs.Code, obs.Message
		if obs.Field != "" {
			payload["field"] = obs.Field
		}
	} else {
		payload["data"] = prune(toJSONValue(obs.Data), 0)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"tool":%q,"ok":false,"message":"observation marshal failed"}`, obs.Tool)
	}
	return string(b)
}

func toJSONValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func prune(v any, depth int) any {
	if depth > 6 {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if observationDropKeys[k] {
				continue
			}
			out[k] = prune(val, depth+1)
		}
		return out
	case []any:
		n := len(x)
		if n > observationMaxItems {
			n = observationMaxItems
		}
		out := make([]any, 0, n)
		for _, it := range x[:n] {
			out = append(out, prune(it, depth+1))
		}
		return out
	case string:
		if utf8.RuneCountInString(x) > observationMaxRunes {
			return string([]rune(x)[:observationMaxRunes]) + "…"
		}
		return x
	}
	return v
}

// pendingBlocks 收集工具循环里产生的块：同类块只保留最新一个（购物车、订单、券），商品卡按 ID 去重累加；
// 最终回答时一起输出；回退到规则处理时整体丢弃，避免和规则回答的卡片重复。
type pendingBlocks struct {
	products   []ProductCard
	seen       map[string]bool
	citations  []rag.Citation
	cart       *shop.Cart
	orders     []shop.Order
	hasOrders  bool
	coupons    map[string]any
	promotions []PromotionCard
	reviews    *ReviewsResult
	actions    []map[string]any
}

func newPendingBlocks() *pendingBlocks {
	return &pendingBlocks{seen: map[string]bool{}}
}

// absorb 根据工具结果更新待输出的块，并返回本次出现的商品 ID（加入可信集）。
func (p *pendingBlocks) absorb(tool string, data any) []string {
	var ids []string
	switch tool {
	case ToolSearchProducts:
		res := decode[ProductSearchResult](data)
		if res.Relevance == RelevanceNone {
			return nil
		}
		for _, card := range res.Products {
			ids = append(ids, card.ProductID)
			if !p.seen[card.ProductID] {
				p.seen[card.ProductID] = true
				p.products = append(p.products, card)
			}
		}
	case ToolSearchKnowledge:
		res := decode[KnowledgeResult](data)
		p.citations = append(p.citations, res.Citations...)
	case ToolGetCart, ToolUpdateCartItem, ToolDeleteCartItem:
		cart := decode[shop.Cart](data)
		p.cart = &cart
	case ToolAddCartItem:
		var out struct {
			Cart shop.Cart `json:"cart"`
		}
		out = decode[struct {
			Cart shop.Cart `json:"cart"`
		}](data)
		p.cart = &out.Cart
		p.action(TargetCart, "去购物车", nil)
	case ToolCheckout:
		res := decode[shop.CheckoutResult](data)
		p.orders, p.hasOrders = res.Orders, true
		p.action(TargetOrders, "去我的订单", nil)
	case ToolListOrders:
		out := decode[struct {
			Orders []shop.Order `json:"orders"`
		}](data)
		p.orders, p.hasOrders = out.Orders, true
	case ToolGetOrder, ToolPayOrder, ToolCancelOrder, ToolConfirmReceipt:
		o := decode[shop.Order](data)
		p.orders, p.hasOrders = []shop.Order{o}, true
	case ToolListCoupons:
		out := decode[struct {
			Coupons []shop.AvailableCoupon `json:"coupons"`
		}](data)
		p.coupon("available", out.Coupons)
	case ToolListUserCoupons:
		out := decode[struct {
			Coupons []shop.UserCoupon `json:"coupons"`
		}](data)
		p.coupon("mine", out.Coupons)
	case ToolListPromotions:
		out := decode[struct {
			Promotions []PromotionCard `json:"promotions"`
		}](data)
		p.promotions = out.Promotions
	case ToolListReviews:
		res := decode[ReviewsResult](data)
		p.reviews = &res
	}
	return ids
}

func (p *pendingBlocks) coupon(key string, v any) {
	if p.coupons == nil {
		p.coupons = map[string]any{}
	}
	p.coupons[key] = v
}

func (p *pendingBlocks) action(target, label string, params map[string]string) {
	for _, a := range p.actions {
		if a["target"] == target {
			return
		}
	}
	p.actions = append(p.actions, blockAction(target, label, params))
}

// emit 按固定顺序输出全部块。
func (p *pendingBlocks) emit(s *session) {
	if len(p.products) > 0 {
		s.block(blockProducts("为你找到的商品", p.products))
	}
	if len(p.citations) > 0 {
		s.block(blockCitations(p.citations))
	}
	if p.cart != nil {
		s.block(blockCart(*p.cart, nil))
	}
	if p.hasOrders {
		s.block(blockOrders(p.orders))
	}
	if p.coupons != nil {
		var available []shop.AvailableCoupon
		var mine []shop.UserCoupon
		if v, ok := p.coupons["available"].([]shop.AvailableCoupon); ok {
			available = v
		}
		if v, ok := p.coupons["mine"].([]shop.UserCoupon); ok {
			mine = v
		}
		s.block(blockCoupons(available, mine))
	}
	if len(p.promotions) > 0 {
		s.block(blockPromotions(p.promotions))
	}
	if p.reviews != nil {
		s.block(blockReviews(*p.reviews))
	}
	for _, a := range p.actions {
		s.block(a)
	}
}
