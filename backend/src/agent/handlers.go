package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/shop"
)

// handle 按意图分派。每个处理器只用工具返回的数据说话；需要用户补充信息时提问并停下，不做写操作。
func (s *session) handle(ctx context.Context) error {
	switch s.plan.Intent {
	case IntentGuide:
		s.handleGuide(ctx)
	case IntentImageSearch:
		s.handleImage()
	case IntentNavigation:
		s.handleNavigation()
	case IntentNonGuide:
		s.handleNonGuide()
	case IntentKnowledge:
		s.handleKnowledge(ctx)
	case IntentProductSearch:
		s.handleProductSearch(ctx)
	case IntentProductCompare:
		s.handleCompare(ctx)
	case IntentCart:
		s.handleCart(ctx)
	case IntentCheckout:
		s.handleCheckout(ctx)
	case IntentOrder:
		s.handleOrder(ctx)
	case IntentCoupon:
		s.handleCoupon(ctx)
	case IntentReview:
		s.handleReview(ctx)
	default:
		s.handleNonGuide()
	}
	return ctx.Err()
}

// ---------- 导购 / 非导购 / 导航 ----------

func (s *session) handleGuide(ctx context.Context) {
	if s.plan.Action == "fallback" && s.plan.Query != "" {
		// 没有明确意图：先当商品需求搜一次，有可靠命中就按推荐回答，否则按非导购说明边界。
		res, ok := s.search(ctx, s.plan.Query, s.plan.Budget, s.plan.Exclude, defaultSearchLimit)
		if ok && res.Relevance == RelevanceOK {
			s.plan.Intent = IntentProductSearch
			s.presentProducts(res)
			return
		}
		s.handleNonGuide()
		return
	}
	s.say("你好，我是 Blink 导购助手。我可以帮你：推荐和对比商品（比如“3000 以内拍照好的手机”）、查看和修改购物车、结算下单、查询订单和支付、领取优惠券，以及解答售后和使用问题。")
	s.say("直接告诉我预算、用途或想买的品类就可以。")
	s.followups("推荐一款通勤降噪耳机", "3000 以内拍照好的手机", "有什么优惠券")
}

func (s *session) handleImage() {
	s.say("图片找同款的能力还在接入中（计划在图像检索上线后开放），现在我还不能识别图片内容。你可以用文字描述想找的商品，比如品类、品牌、颜色或用途，我按文字帮你找。")
	s.followups("推荐一款降噪耳机", "3000 以内的手机", "看看我的购物车")
}

func (s *session) handleNavigation() {
	labels := map[string]string{TargetCart: "去购物车", TargetOrders: "去我的订单", TargetProducts: "去商品列表", TargetCoupons: "去优惠券",
		TargetSessions: "查看历史会话", TargetSettings: "去设置"}
	label := labels[s.plan.Target]
	if label == "" {
		label = "打开页面"
	}
	s.say("好的，点下面的按钮就可以" + label + "。")
	s.block(blockAction(s.plan.Target, label, nil))
	s.followups("看看我的购物车", "我的订单", "推荐一款通勤降噪耳机")
}

func (s *session) handleNonGuide() {
	switch s.plan.Topic {
	case "weather", "news", "finance", "math", "chat", "coding":
		s.say("这个问题不在我的能力范围里——我是 Blink 商城的导购助手，只能帮你处理购物相关的事。")
	default:
		s.say("我暂时没有理解你要买什么。我是 Blink 商城的导购助手，可以帮你推荐和对比商品、管理购物车、下单支付、查订单和领券。")
	}
	s.say("可以试试告诉我想买的品类、预算或用途，比如“推荐一款 500 以内的无线鼠标”。")
	s.followups("推荐一款 500 以内的无线鼠标", "3000 以内拍照好的手机", "有什么优惠券")
}

// ---------- 知识 ----------

func (s *session) handleKnowledge(ctx context.Context) {
	obs := s.call(ctx, "查找相关资料", ToolSearchKnowledge, map[string]any{"query": s.plan.Query, "limit": 3})
	if !obs.OK {
		if obs.Code == CodeUnavailable {
			s.say("知识库检索暂时不可用，我没法给出有依据的回答。你可以稍后再问，或联系店铺客服。")
		} else {
			s.say("查找资料时出了点问题：" + obs.Message)
		}
		return
	}
	res := decode[KnowledgeResult](obs.Data)
	if len(res.Citations) == 0 {
		s.say("资料库里没有找到和这个问题直接相关的内容，我不能凭空回答。你可以换个说法，或者联系店铺客服确认。")
		s.followups("七天无理由怎么退", "手机保修多久", "推荐一款手机")
		return
	}
	s.say("根据资料库里的内容：")
	for i, c := range res.Citations {
		if i >= 2 {
			break
		}
		s.sayf("《%s》提到：%s", c.DocumentTitle, strings.TrimSpace(c.Snippet))
	}
	s.say("以上内容来自商家或平台上传的资料，具体以店铺最新政策为准。")
	s.block(blockCitations(res.Citations))
	s.followups("保修多久", "运费谁承担", "推荐一款手机")
}

// ---------- 商品搜索与对比 ----------

// search 调用 search_products，失败时已向用户说明；ok 为 false 表示调用失败。
func (s *session) search(ctx context.Context, query string, budget domain.Money, exclude []string, limit int) (ProductSearchResult, bool) {
	return s.searchWith(ctx, query, searchSpec{max: budget, exclude: exclude}, limit)
}

// searchSpec 是搜索的结构化约束。
type searchSpec struct {
	min, max domain.Money
	brands   []string
	exclude  []string
}

// planSpec 取规划里的全部约束（品牌按在售品牌词表识别）。
func (s *session) planSpec(ctx context.Context) searchSpec {
	brands := s.plan.Brands
	if len(brands) == 0 {
		brands = s.r.reg.MentionedBrands(ctx, s.in.Content, s.plan.Exclude)
	}
	return searchSpec{min: s.plan.MinPrice, max: s.plan.Budget, brands: brands, exclude: s.plan.Exclude}
}

func (s *session) searchWith(ctx context.Context, query string, spec searchSpec, limit int) (ProductSearchResult, bool) {
	args := map[string]any{"query": query, "limit": limit}
	if spec.max > 0 {
		args["max_price"] = float64(spec.max) / 100
	}
	if spec.min > 0 {
		args["min_price"] = float64(spec.min) / 100
	}
	if len(spec.brands) > 0 {
		args["brands"] = spec.brands
	}
	if len(spec.exclude) > 0 {
		args["exclude"] = spec.exclude
	}
	obs := s.call(ctx, "搜索商品："+query, ToolSearchProducts, args)
	if !obs.OK {
		s.say("搜索商品时出了点问题：" + obs.Message)
		return ProductSearchResult{}, false
	}
	res := decode[ProductSearchResult](obs.Data)
	for _, p := range res.Products {
		s.tc.Evidence[p.ProductID] = true
	}
	return res, true
}

func (s *session) handleProductSearch(ctx context.Context) {
	if s.plan.Action == "ask" || s.plan.Query == "" {
		s.ask("想买哪类商品？告诉我品类、预算或用途，比如“1000 以内的降噪耳机”，我来帮你挑。")
		s.followups("推荐一款通勤降噪耳机", "3000 以内拍照好的手机", "500 以内的无线鼠标")
		return
	}
	res, ok := s.searchWith(ctx, s.plan.Query, s.planSpec(ctx), defaultSearchLimit)
	if !ok {
		return
	}
	s.presentProducts(res)
}

// presentProducts 按相关性给出推荐文案和商品卡；弱命中只作为“相近商品”展示，不当作推荐。
func (s *session) presentProducts(res ProductSearchResult) {
	constraint := ""
	switch {
	case res.MinPrice != nil && res.MaxPrice != nil:
		constraint += "、" + yuan(*res.MinPrice) + "–" + yuan(*res.MaxPrice)
	case res.MinPrice != nil:
		constraint += "、" + yuan(*res.MinPrice) + " 以上"
	case res.MaxPrice != nil:
		constraint += "、" + yuan(*res.MaxPrice) + " 以内"
	}
	if len(res.Brands) > 0 {
		constraint += "、品牌“" + strings.Join(res.Brands, "”“") + "”"
	}
	if len(res.Excluded) > 0 {
		constraint += "、排除“" + strings.Join(res.Excluded, "”“") + "”"
	}
	switch {
	case len(res.Products) == 0 && res.Filtered > 0:
		s.sayf("按“%s”%s没有找到符合条件的在售商品：有 %d 件因为价格、品牌或排除条件没有列出。可以放宽条件再试。", res.Query, constraint, res.Filtered)
		s.followups("放宽预算再推荐", "推荐一款通勤降噪耳机", "有什么优惠活动")
		return
	case len(res.Products) == 0:
		s.sayf("商品库里暂时没有找到和“%s”匹配的在售商品。可以换个品类或品牌说法再试，或者告诉我用途和预算。", res.Query)
		s.followups("推荐一款通勤降噪耳机", "3000 以内拍照好的手机", "有什么优惠活动")
		return
	case res.Relevance == RelevanceWeak && len(res.Conflicts) > 0:
		s.sayf("按“%s”找到的商品和你的用途不太合适：%s 的说明里写了不适合这种用途，所以不推荐。下面列出来仅供了解，建议换个需求再找：",
			res.Query, strings.Join(res.Conflicts, "、"))
		s.block(blockProducts("不太合适的商品", res.Products))
		s.followups("换个说法再找", "推荐一款通勤降噪耳机", "看看我的购物车")
		return
	case res.Relevance == RelevanceWeak:
		s.sayf("没有找到和“%s”完全匹配的商品，下面是一些相近的在售商品，仅供参考：", res.Query)
		s.block(blockProducts("相近商品", res.Products))
		s.followups("换个说法再找", "3000 以内拍照好的手机", "看看我的购物车")
		return
	}
	first := res.Products[0]
	intro := fmt.Sprintf("按“%s”%s找到 %d 件在售商品，优先推荐 %s（%s）", res.Query, constraint, res.Total, first.Name, yuan(first.Price))
	if first.RecommendReason != "" {
		intro += "：" + strings.TrimSuffix(first.RecommendReason, "。")
	}
	s.say(intro + "。")
	if len(first.SellingPoints) > 0 {
		s.say("主要卖点：" + strings.Join(first.SellingPoints, "、") + "。")
	}
	if len(first.RiskNotes) > 0 {
		s.say("注意：" + strings.Join(first.RiskNotes, "；") + "。")
	}
	if len(res.Products) > 1 {
		names := make([]string, 0, len(res.Products)-1)
		for _, p := range res.Products[1:] {
			names = append(names, fmt.Sprintf("%s（%s）", p.Name, yuan(p.Price)))
		}
		s.say("其他可以考虑的：" + strings.Join(names, "、") + "。")
	}
	s.say("价格、库存和卖点都来自商品库，点卡片可以看详情。想加购的话告诉我“把第一个加入购物车”。")
	s.block(blockProducts("为你找到的商品", res.Products))
	fu := []string{"把第一个加入购物车", first.Name + " 的评价怎么样", "有什么优惠券"}
	if len(res.Products) > 1 {
		fu[2] = "对比 " + first.Name + " 和 " + res.Products[1].Name
	}
	s.followups(fu...)
}

func (s *session) handleCompare(ctx context.Context) {
	var cards []ProductCard
	seen := map[string]bool{}
	add := func(list []ProductCard, n int) {
		for _, p := range list {
			if n == 0 {
				return
			}
			if !seen[p.ProductID] {
				seen[p.ProductID] = true
				cards = append(cards, p)
				n--
			}
		}
	}
	if len(s.plan.Names) >= 2 {
		for _, name := range s.plan.Names {
			res, ok := s.search(ctx, name, 0, nil, 1)
			if !ok {
				return
			}
			if res.Relevance == RelevanceOK {
				add(res.Products, 1)
			}
		}
	}
	if len(cards) < 2 && (s.plan.Reference || s.plan.Ordinal != 0) && len(s.history.Cards) >= 2 {
		add(s.history.Cards, 3)
	}
	if len(cards) < 2 && s.plan.Query != "" {
		res, ok := s.search(ctx, s.plan.Query, s.plan.Budget, s.plan.Exclude, 3)
		if !ok {
			return
		}
		if res.Relevance == RelevanceOK {
			add(res.Products, 3)
		}
	}
	if len(cards) < 2 {
		s.ask("要对比哪几款商品？告诉我两个以上的商品名或品类，比如“对比 Blink Nova 12 和 Blink Vista Pro”。")
		s.followups("对比 Blink Nova 12 和 Blink Vista Pro", "推荐一款手机", "3000 以内的手机")
		return
	}
	// 对比表里的商品已经给用户看过，可以作为后续加购的来源。
	for _, c := range cards {
		s.tc.Evidence[c.ProductID] = true
	}
	names := make([]string, 0, len(cards))
	for _, c := range cards {
		names = append(names, c.Name)
	}
	cheapest, _ := cards[0], 0
	for _, c := range cards[1:] {
		if c.Price < cheapest.Price {
			cheapest = c
		}
	}
	s.sayf("这是 %s 的对比。价格上 %s 更便宜（%s）。", strings.Join(names, " 和 "), cheapest.Name, yuan(cheapest.Price))
	for _, c := range cards {
		line := c.Name + "："
		if len(c.SuitableFor) > 0 {
			line += "适合" + strings.Join(c.SuitableFor, "、")
		}
		if len(c.NotSuitableFor) > 0 {
			line += "，不太适合" + strings.Join(c.NotSuitableFor, "、")
		}
		if len(c.SellingPoints) > 0 {
			line += "；卖点是" + strings.Join(c.SellingPoints, "、")
		}
		s.say(strings.TrimSuffix(line, "：") + "。")
	}
	s.say("以上参数都来自商品库，详细差异见下表。")
	s.block(blockComparison(cards))
	s.followups("把 "+cards[0].Name+" 加入购物车", cards[0].Name+" 的评价怎么样", "有什么优惠券")
}

// ---------- 购物车 ----------

func (s *session) loadCart(ctx context.Context) (shop.Cart, bool) {
	obs := s.call(ctx, "查看购物车", ToolGetCart, nil)
	if !obs.OK {
		s.say("读取购物车时出了点问题：" + obs.Message)
		return shop.Cart{}, false
	}
	return decode[shop.Cart](obs.Data), true
}

func (s *session) handleCart(ctx context.Context) {
	switch s.plan.Action {
	case "add":
		s.cartAdd(ctx)
	case "remove", "update", "select", "unselect":
		s.cartModify(ctx)
	case "clear":
		s.cartClear(ctx)
	default:
		s.cartView(ctx)
	}
}

func (s *session) cartView(ctx context.Context) {
	cart, ok := s.loadCart(ctx)
	if !ok {
		return
	}
	if len(cart.Items) == 0 {
		s.say("你的购物车还是空的。想买点什么？告诉我品类和预算，我来推荐。")
		s.block(blockCart(cart, nil))
		s.followups("推荐一款通勤降噪耳机", "3000 以内拍照好的手机", "有什么优惠券")
		return
	}
	var hints []shop.Hint
	if obs := s.call(ctx, "试算优惠", ToolPreviewDiscount, nil); obs.OK {
		hints = decode[shop.DiscountPreview](obs.Data).Hints
	}
	s.sayf("购物车里有 %d 种商品，已选中 %d 件，合计 %s，优惠 %s，应付 %s。", cart.Summary.ItemCount, cart.Summary.SelectedCount,
		yuan(cart.Summary.TotalAmount), yuan(cart.Summary.DiscountAmount), yuan(cart.Summary.PayAmount))
	var bad []string
	for _, it := range cart.Items {
		if !it.Available {
			bad = append(bad, it.ProductName+"（"+it.UnavailableReason+"）")
		}
	}
	if len(bad) > 0 {
		s.say("有商品暂时不能购买：" + strings.Join(bad, "、") + "，结算前需要处理。")
	}
	for _, h := range hints {
		s.sayf("再买 %s 可以参加“%s”。", yuan(h.Shortfall), h.Name)
	}
	s.block(blockCart(cart, hints))
	s.followups("去结算", "有什么优惠券", "删掉购物车里的第一个")
}

// resolveProduct 按用户的说法确定一个商品：显式 ID → 上一张商品卡的序号/指代 → 按名称搜索（唯一可靠命中才算）。
// 返回 nil 表示已向用户提问。
func (s *session) resolveProduct(ctx context.Context, purpose string) *ProductCard {
	if ids := ParseProductIDs(s.in.Content); len(ids) == 1 {
		obs := s.call(ctx, "查看商品 "+ids[0], ToolSearchProducts, map[string]any{"query": ids[0], "product_id": ids[0], "limit": 1})
		if !obs.OK {
			s.say("查找商品时出了点问题：" + obs.Message)
			return nil
		}
		res := decode[ProductSearchResult](obs.Data)
		if len(res.Products) != 1 {
			s.ask("没有找到 ID 为 " + ids[0] + " 的在售商品。")
			return nil
		}
		s.tc.Evidence[res.Products[0].ProductID] = true
		return &res.Products[0]
	}
	if s.plan.Ordinal != 0 || (s.plan.Reference && s.plan.Query == "") {
		if len(s.history.Cards) == 0 {
			s.ask("我还没有给你推荐过商品，不确定你指的是哪一件，所以没有" + purpose + "。先告诉我想买什么，我推荐后你再说“把第一个加入购物车”。")
			return nil
		}
		idx := s.plan.Ordinal
		switch {
		case idx == -1:
			idx = len(s.history.Cards)
		case idx == 0 && len(s.history.Cards) == 1:
			idx = 1
		case idx == 0:
			s.ask(fmt.Sprintf("刚才给你看了 %d 件商品，你指的是哪一个？可以说“第一个”或直接说商品名。", len(s.history.Cards)))
			s.block(blockProducts("刚才的商品", s.history.Cards))
			return nil
		}
		if idx < 1 || idx > len(s.history.Cards) {
			s.ask(fmt.Sprintf("刚才只给你看了 %d 件商品，没有第 %d 个。", len(s.history.Cards), idx))
			s.block(blockProducts("刚才的商品", s.history.Cards))
			return nil
		}
		card := s.history.Cards[idx-1]
		// 用最新的商品数据（价格、库存可能已变化）。
		obs := s.call(ctx, "查看商品："+card.Name, ToolSearchProducts, map[string]any{"query": card.Name, "product_id": card.ProductID, "limit": 1})
		if !obs.OK {
			s.say("查找商品时出了点问题：" + obs.Message)
			return nil
		}
		if res := decode[ProductSearchResult](obs.Data); len(res.Products) == 1 {
			return &res.Products[0]
		}
		s.ask(card.Name + " 现在已经不在售了，没有" + purpose + "。")
		return nil
	}
	if s.plan.Query == "" {
		s.ask("要" + purpose + "哪件商品？告诉我商品名，或者先让我推荐几款。")
		return nil
	}
	res, ok := s.search(ctx, s.plan.Query, 0, nil, 5)
	if !ok {
		return nil
	}
	if len(res.Products) == 0 || res.Relevance != RelevanceOK {
		s.ask(fmt.Sprintf("没有找到和“%s”匹配的在售商品，没有%s。换个名字试试？", s.plan.Query, purpose))
		return nil
	}
	if len(res.Products) == 1 || nameMatches(res.Products[0], s.plan.Query) {
		return &res.Products[0]
	}
	s.ask(fmt.Sprintf("“%s”匹配到 %d 件商品，你指的是哪一个？可以说“第一个”或说完整的商品名。", s.plan.Query, len(res.Products)))
	s.block(blockProducts("匹配到的商品", res.Products))
	return nil
}

// nameMatches 判断候选是否明确就是用户说的那件：检索词全部出现在名称里，或名称（去空格）包含整段查询。
func nameMatches(p ProductCard, query string) bool {
	name := strings.ToLower(strings.ReplaceAll(p.Name, " ", ""))
	q := strings.ToLower(strings.ReplaceAll(query, " ", ""))
	if q != "" && strings.Contains(name, q) {
		return true
	}
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return false
	}
	for _, t := range terms {
		if !strings.Contains(name, strings.ReplaceAll(t, " ", "")) {
			return false
		}
	}
	return true
}

func (s *session) cartAdd(ctx context.Context) {
	card := s.resolveProduct(ctx, "加入购物车")
	if card == nil {
		s.followups("推荐一款通勤降噪耳机", "看看我的购物车", "3000 以内的手机")
		return
	}
	qty := s.plan.Quantity
	if qty <= 0 {
		qty = 1
	}
	obs := s.call(ctx, "加入购物车："+card.Name, ToolAddCartItem, map[string]any{"product_id": card.ProductID, "quantity": qty})
	if !obs.OK {
		s.sayf("%s 没有加入购物车：%s", card.Name, obs.Message)
		s.followups("看看我的购物车", "推荐类似的商品", "有什么优惠券")
		return
	}
	data := decode[struct {
		Quantity int       `json:"quantity"`
		Cart     shop.Cart `json:"cart"`
	}](obs.Data)
	s.sayf("已把 %s × %d 加入购物车（现在这件共 %d 件）。购物车共 %d 种商品，应付 %s。", card.Name, qty, data.Quantity, data.Cart.Summary.ItemCount, yuan(data.Cart.Summary.PayAmount))
	s.block(blockCart(data.Cart, nil))
	s.block(blockAction(TargetCart, "去购物车", nil))
	s.followups("去结算", "有什么优惠券", "再推荐几款")
}

// resolveCartItem 在购物车里确定一项：序号 → 唯一一项 → 按名称匹配（唯一才算）。返回 nil 表示已提问。
func (s *session) resolveCartItem(cart shop.Cart, purpose string) *shop.CartItem {
	if len(cart.Items) == 0 {
		s.ask("购物车是空的，没有可以" + purpose + "的商品。")
		return nil
	}
	if s.plan.Ordinal != 0 {
		idx := s.plan.Ordinal
		if idx == -1 {
			idx = len(cart.Items)
		}
		if idx < 1 || idx > len(cart.Items) {
			s.ask(fmt.Sprintf("购物车里只有 %d 种商品，没有第 %d 个。", len(cart.Items), idx))
			s.block(blockCart(cart, nil))
			return nil
		}
		return &cart.Items[idx-1]
	}
	if s.plan.Query != "" {
		var hits []*shop.CartItem
		terms := strings.Fields(strings.ToLower(s.plan.Query))
		for i := range cart.Items {
			text := strings.ToLower(cart.Items[i].ProductName + " " + cart.Items[i].SkuName)
			all := true
			for _, t := range terms {
				if !strings.Contains(text, t) {
					all = false
				}
			}
			if all {
				hits = append(hits, &cart.Items[i])
			}
		}
		if len(hits) == 1 {
			return hits[0]
		}
		if len(hits) > 1 {
			s.ask(fmt.Sprintf("购物车里有 %d 种商品都匹配“%s”，你指的是哪一个？可以说“第几个”。", len(hits), s.plan.Query))
			s.block(blockCart(cart, nil))
			return nil
		}
		s.ask(fmt.Sprintf("购物车里没有找到“%s”。", s.plan.Query))
		s.block(blockCart(cart, nil))
		return nil
	}
	if len(cart.Items) == 1 {
		return &cart.Items[0]
	}
	s.ask(fmt.Sprintf("购物车里有 %d 种商品，要%s哪一个？可以说“第一个”或商品名。", len(cart.Items), purpose))
	s.block(blockCart(cart, nil))
	return nil
}

func (s *session) cartModify(ctx context.Context) {
	cart, ok := s.loadCart(ctx)
	if !ok {
		return
	}
	purpose := map[string]string{"remove": "删除", "update": "修改数量", "select": "选中", "unselect": "取消选中"}[s.plan.Action]
	item := s.resolveCartItem(cart, purpose)
	if item == nil {
		return
	}
	var obs Observation
	switch s.plan.Action {
	case "remove":
		obs = s.call(ctx, "删除购物车项："+item.ProductName, ToolDeleteCartItem, map[string]any{"cart_item_id": item.CartItemID})
	case "update":
		if s.plan.Quantity <= 0 {
			s.ask(fmt.Sprintf("要把 %s 改成几件？现在是 %d 件。", item.ProductName, item.Quantity))
			return
		}
		obs = s.call(ctx, "修改数量："+item.ProductName, ToolUpdateCartItem, map[string]any{"cart_item_id": item.CartItemID, "quantity": s.plan.Quantity})
	default:
		obs = s.call(ctx, purpose+"："+item.ProductName, ToolUpdateCartItem, map[string]any{"cart_item_id": item.CartItemID, "selected": s.plan.Action == "select"})
	}
	if !obs.OK {
		s.sayf("没有%s %s：%s", purpose, item.ProductName, obs.Message)
		s.block(blockCart(cart, nil))
		return
	}
	after := decode[shop.Cart](obs.Data)
	switch s.plan.Action {
	case "remove":
		s.sayf("已从购物车删除 %s。现在还有 %d 种商品，应付 %s。", item.ProductName, after.Summary.ItemCount, yuan(after.Summary.PayAmount))
	case "update":
		s.sayf("已把 %s 改为 %d 件。购物车应付 %s。", item.ProductName, s.plan.Quantity, yuan(after.Summary.PayAmount))
	default:
		s.sayf("已%s %s。购物车已选中 %d 件，应付 %s。", purpose, item.ProductName, after.Summary.SelectedCount, yuan(after.Summary.PayAmount))
	}
	s.block(blockCart(after, nil))
	s.followups("去结算", "看看我的购物车", "有什么优惠券")
}

func (s *session) cartClear(ctx context.Context) {
	cart, ok := s.loadCart(ctx)
	if !ok {
		return
	}
	if len(cart.Items) == 0 {
		s.say("购物车本来就是空的。")
		s.block(blockCart(cart, nil))
		return
	}
	removed := 0
	var last shop.Cart
	for _, it := range cart.Items {
		obs := s.call(ctx, "删除购物车项："+it.ProductName, ToolDeleteCartItem, map[string]any{"cart_item_id": it.CartItemID})
		if obs.OK {
			removed++
			last = decode[shop.Cart](obs.Data)
		}
	}
	s.sayf("已清空购物车，删除了 %d 种商品。", removed)
	if removed > 0 {
		s.block(blockCart(last, nil))
	}
	s.followups("推荐一款通勤降噪耳机", "3000 以内的手机", "我的订单")
}

// ---------- 结算 ----------

func (s *session) handleCheckout(ctx context.Context) {
	cart, ok := s.loadCart(ctx)
	if !ok {
		return
	}
	if cart.Summary.SelectedCount == 0 {
		if len(cart.Items) == 0 {
			s.say("购物车是空的，没有可以结算的商品。先挑点东西吧。")
		} else {
			s.say("购物车里没有选中的可购买商品，没法结算。先去购物车勾选要买的商品，或者告诉我“选中第一个”。")
		}
		s.block(blockCart(cart, nil))
		s.block(blockAction(TargetCart, "去购物车", nil))
		s.followups("看看我的购物车", "推荐一款通勤降噪耳机", "有什么优惠券")
		return
	}
	obs := s.call(ctx, "提交订单", ToolCheckout, nil)
	if !obs.OK {
		s.say("这次没有下单：" + obs.Message)
		s.block(blockCart(cart, nil))
		s.block(blockAction(TargetCart, "去购物车处理", nil))
		s.followups("看看我的购物车", "有什么优惠券", "我的订单")
		return
	}
	res := decode[shop.CheckoutResult](obs.Data)
	var total domain.Money
	for _, o := range res.Orders {
		total += o.PayAmount
	}
	if res.Replayed {
		s.sayf("这次请求之前已经下过单了，直接给你之前的结果：共 %d 个订单。", len(res.Orders))
	} else {
		s.sayf("已为你生成 %d 个订单（按店铺拆单），实付合计 %s。", len(res.Orders), yuan(total))
	}
	for _, o := range res.Orders {
		line := fmt.Sprintf("%s：订单号 %s，%s，实付 %s", o.MerchantName, o.OrderNo, shop.OrderStatusText[o.Status], yuan(o.PayAmount))
		if o.Status == domain.OrderPendingPayment && o.PaymentDeadlineAt != nil {
			line += "，请在 30 分钟内支付，超时会自动关闭"
		}
		s.say(line + "。")
	}
	s.say("说“支付订单”我可以帮你完成（模拟）支付，或者到“我的订单”里操作。")
	s.block(blockOrders(res.Orders))
	s.block(blockAction(TargetOrders, "去我的订单", nil))
	s.followups("支付订单", "我的订单", "推荐一款通勤降噪耳机")
}

// ---------- 订单 ----------

func (s *session) listOrders(ctx context.Context, status domain.OrderStatus, orderNo string, limit int) ([]shop.Order, int, bool) {
	args := map[string]any{"limit": limit}
	if status != "" {
		args["status"] = string(status)
	}
	if orderNo != "" {
		args["order_no"] = orderNo
	}
	obs := s.call(ctx, "查询订单", ToolListOrders, args)
	if !obs.OK {
		s.say("查询订单时出了点问题：" + obs.Message)
		return nil, 0, false
	}
	data := decode[struct {
		Orders []shop.Order `json:"orders"`
		Total  int          `json:"total"`
	}](obs.Data)
	return data.Orders, data.Total, true
}

func (s *session) handleOrder(ctx context.Context) {
	switch s.plan.Action {
	case "pay":
		s.orderAction(ctx, domain.OrderPendingPayment, "支付", ToolPayOrder)
	case "cancel":
		s.orderAction(ctx, domain.OrderPendingPayment, "取消", ToolCancelOrder)
	case "confirm":
		s.orderAction(ctx, domain.OrderShipped, "确认收货", ToolConfirmReceipt)
	default:
		s.orderList(ctx)
	}
}

func (s *session) orderList(ctx context.Context) {
	orderNo := ""
	if strings.HasPrefix(s.plan.OrderRef, "BS") {
		orderNo = s.plan.OrderRef
	}
	orders, total, ok := s.listOrders(ctx, s.plan.OrderStatus, orderNo, 5)
	if !ok {
		return
	}
	if strings.HasPrefix(s.plan.OrderRef, "o_") {
		obs := s.call(ctx, "查看订单详情", ToolGetOrder, map[string]any{"order_id": s.plan.OrderRef})
		if !obs.OK {
			s.say("没有找到这个订单：" + obs.Message)
			return
		}
		orders, total = []shop.Order{decode[shop.Order](obs.Data)}, 1
	}
	label := "订单"
	if s.plan.OrderStatus != "" {
		label = shop.OrderStatusText[s.plan.OrderStatus] + "的订单"
	}
	if len(orders) == 0 {
		s.sayf("你目前没有%s。", label)
		s.block(blockOrders(orders))
		s.followups("推荐一款通勤降噪耳机", "看看我的购物车", "有什么优惠券")
		return
	}
	s.sayf("你有 %d 个%s，最近的 %d 个：", total, label, len(orders))
	for _, o := range orders {
		names := make([]string, 0, len(o.Items))
		for _, it := range o.Items {
			names = append(names, fmt.Sprintf("%s × %d", it.Name, it.Quantity))
		}
		s.sayf("订单 %s（%s）：%s，实付 %s，%s。", o.OrderNo, o.MerchantName, strings.Join(names, "、"), yuan(o.PayAmount), shop.OrderStatusText[o.Status])
	}
	s.block(blockOrders(orders))
	s.block(blockAction(TargetOrders, "去我的订单", nil))
	fu := []string{"待支付的订单", "已发货的订单", "推荐一款通勤降噪耳机"}
	for _, o := range orders {
		if o.Status == domain.OrderPendingPayment {
			fu = append([]string{"支付订单 " + o.OrderNo}, fu...)
			break
		}
	}
	s.followups(fu...)
}

// orderAction 对一个订单做支付/取消/收货：只在当前用户、状态允许的订单里找目标；找不到唯一目标就列出来让用户选，不猜。
func (s *session) orderAction(ctx context.Context, status domain.OrderStatus, verb, tool string) {
	orderNo := ""
	if strings.HasPrefix(s.plan.OrderRef, "BS") {
		orderNo = s.plan.OrderRef
	}
	orders, _, ok := s.listOrders(ctx, status, orderNo, 20)
	if !ok {
		return
	}
	var target *shop.Order
	switch {
	case strings.HasPrefix(s.plan.OrderRef, "o_"):
		for i := range orders {
			if orders[i].OrderID == s.plan.OrderRef {
				target = &orders[i]
			}
		}
		if target == nil {
			s.ask(fmt.Sprintf("没有找到可以%s的订单 %s（可能不是你的订单，或状态不允许）。", verb, s.plan.OrderRef))
			return
		}
	case orderNo != "":
		if len(orders) == 0 {
			s.ask(fmt.Sprintf("没有找到订单号 %s 的%s订单。", orderNo, shop.OrderStatusText[status]))
			return
		}
		target = &orders[0]
	case len(orders) == 0:
		s.sayf("你目前没有%s的订单，没有可以%s的。", shop.OrderStatusText[status], verb)
		s.followups("我的订单", "看看我的购物车", "推荐一款通勤降噪耳机")
		return
	case len(orders) == 1:
		target = &orders[0]
	case s.plan.Ordinal != 0:
		idx := s.plan.Ordinal
		if idx == -1 {
			idx = len(orders)
		}
		if idx < 1 || idx > len(orders) {
			s.ask(fmt.Sprintf("只有 %d 个%s的订单，没有第 %d 个。", len(orders), shop.OrderStatusText[status], idx))
			s.block(blockOrders(orders))
			return
		}
		target = &orders[idx-1]
	case containsAny(s.in.Content, "最近", "最新", "刚下的", "刚才的", "刚刚的"):
		target = &orders[0]
	default:
		s.ask(fmt.Sprintf("你有 %d 个%s的订单，要%s哪一个？告诉我订单号，或者说“第一个”“最近的”。", len(orders), shop.OrderStatusText[status], verb))
		s.block(blockOrders(orders))
		return
	}
	args := map[string]any{"order_id": target.OrderID}
	if tool == ToolCancelOrder {
		args["reason"] = "用户通过导购助手取消"
	}
	obs := s.call(ctx, verb+"订单 "+target.OrderNo, tool, args)
	if !obs.OK {
		s.sayf("订单 %s 没有%s：%s", target.OrderNo, verb, obs.Message)
		s.block(blockOrders([]shop.Order{*target}))
		s.followups("我的订单", "待支付的订单", "看看我的购物车")
		return
	}
	after := decode[shop.Order](obs.Data)
	switch tool {
	case ToolPayOrder:
		s.sayf("订单 %s 已完成（模拟）支付，实付 %s，现在是“%s”，等商家发货。", after.OrderNo, yuan(after.PayAmount), shop.OrderStatusText[after.Status])
	case ToolCancelOrder:
		s.sayf("订单 %s 已取消，库存和用掉的券已退回。", after.OrderNo)
	default:
		s.sayf("订单 %s 已确认收货，现在是“%s”。可以给商品写评价了。", after.OrderNo, shop.OrderStatusText[after.Status])
	}
	s.block(blockOrders([]shop.Order{after}))
	s.block(blockAction(TargetOrderDetail, "查看订单", map[string]string{"order_id": after.OrderID}))
	if tool == ToolConfirmReceipt && len(after.Items) > 0 {
		s.followups("给 "+after.Items[0].Name+" 写评价：很好用，五星", "我的订单", "推荐一款通勤降噪耳机")
	} else {
		s.followups("我的订单", "推荐一款通勤降噪耳机", "看看我的购物车")
	}
}

// ---------- 优惠券 ----------

func (s *session) handleCoupon(ctx context.Context) {
	switch s.plan.Action {
	case "claim":
		s.couponClaim(ctx)
	case "promotions":
		s.promotions(ctx)
	case "mine":
		obs := s.call(ctx, "查看我的优惠券", ToolListUserCoupons, map[string]any{"status": "unused"})
		if !obs.OK {
			s.say("查询优惠券时出了点问题：" + obs.Message)
			return
		}
		mine := decode[struct {
			Coupons []shop.UserCoupon `json:"coupons"`
		}](obs.Data).Coupons
		if len(mine) == 0 {
			s.say("你现在没有可用的优惠券。可以说“帮我领券”，我看看有哪些能领。")
		} else {
			names := make([]string, 0, len(mine))
			for _, c := range mine {
				names = append(names, c.Coupon.Name)
			}
			s.sayf("你有 %d 张可用的优惠券：%s。结算时会自动用上最优的组合。", len(mine), strings.Join(names, "、"))
		}
		s.block(blockCoupons(nil, mine))
		s.followups("帮我领券", "看看购物车能优惠多少", "去结算")
	default:
		s.couponList(ctx)
	}
}

func (s *session) couponList(ctx context.Context) {
	obs := s.call(ctx, "查看可领的优惠券", ToolListCoupons, nil)
	if !obs.OK {
		s.say("查询优惠券时出了点问题：" + obs.Message)
		return
	}
	available := decode[struct {
		Coupons []shop.AvailableCoupon `json:"coupons"`
	}](obs.Data).Coupons
	var mine []shop.UserCoupon
	if obs := s.call(ctx, "查看我的优惠券", ToolListUserCoupons, map[string]any{"status": "unused"}); obs.OK {
		mine = decode[struct {
			Coupons []shop.UserCoupon `json:"coupons"`
		}](obs.Data).Coupons
	}
	var claimable []string
	for _, c := range available {
		if c.CanClaim {
			claimable = append(claimable, fmt.Sprintf("%s（%s）", c.Name, c.Description))
		}
	}
	switch {
	case len(claimable) > 0:
		s.sayf("现在有 %d 张券可以领：%s。说“帮我领券”我就帮你领。", len(claimable), strings.Join(claimable, "、"))
	case len(available) > 0:
		s.say("目前的券你都已经领过了（或已领完）。")
	default:
		s.say("目前没有可领的优惠券。")
	}
	if len(mine) > 0 {
		s.sayf("你手里还有 %d 张未使用的券，结算时会自动用上。", len(mine))
	}
	s.block(blockCoupons(available, mine))
	s.followups("帮我领券", "有什么促销活动", "看看购物车能优惠多少")
}

func (s *session) couponClaim(ctx context.Context) {
	obs := s.call(ctx, "查看可领的优惠券", ToolListCoupons, nil)
	if !obs.OK {
		s.say("查询优惠券时出了点问题：" + obs.Message)
		return
	}
	available := decode[struct {
		Coupons []shop.AvailableCoupon `json:"coupons"`
	}](obs.Data).Coupons
	var targets []shop.AvailableCoupon
	for _, c := range available {
		if !c.CanClaim {
			continue
		}
		if s.plan.Query != "" && !containsAny(strings.ToLower(c.Name+" "+c.Description), strings.Fields(strings.ToLower(s.plan.Query))...) {
			continue
		}
		targets = append(targets, c)
	}
	if len(targets) == 0 {
		if s.plan.Query != "" {
			s.sayf("没有找到和“%s”匹配、还能领的券。", s.plan.Query)
		} else {
			s.say("现在没有可以领的券：要么已经领过，要么已领完。")
		}
		s.block(blockCoupons(available, nil))
		s.followups("我的优惠券", "有什么促销活动", "看看购物车能优惠多少")
		return
	}
	var got, failed []string
	for _, c := range targets {
		obs := s.call(ctx, "领取："+c.Name, ToolClaimCoupon, map[string]any{"coupon_id": c.CouponID})
		if obs.OK {
			got = append(got, c.Name)
		} else {
			failed = append(failed, c.Name+"（"+obs.Message+"）")
		}
	}
	if len(got) > 0 {
		s.sayf("已帮你领到 %d 张券：%s。结算时会自动使用。", len(got), strings.Join(got, "、"))
	}
	if len(failed) > 0 {
		s.say("没领到：" + strings.Join(failed, "、") + "。")
	}
	if obs := s.call(ctx, "查看我的优惠券", ToolListUserCoupons, map[string]any{"status": "unused"}); obs.OK {
		mine := decode[struct {
			Coupons []shop.UserCoupon `json:"coupons"`
		}](obs.Data).Coupons
		s.block(blockCoupons(nil, mine))
	}
	s.followups("看看购物车能优惠多少", "去结算", "推荐一款手机")
}

func (s *session) promotions(ctx context.Context) {
	args := map[string]any{}
	if s.plan.Query != "" {
		if res, ok := s.search(ctx, s.plan.Query, 0, nil, 1); ok && res.Relevance == RelevanceOK && len(res.Products) == 1 {
			args["product_id"] = res.Products[0].ProductID
		}
	}
	obs := s.call(ctx, "查看促销活动", ToolListPromotions, args)
	if !obs.OK {
		s.say("查询活动时出了点问题：" + obs.Message)
		return
	}
	items := decode[struct {
		Promotions []PromotionCard `json:"promotions"`
	}](obs.Data).Promotions
	if len(items) == 0 {
		s.say("目前没有进行中的促销活动。")
	} else {
		lines := make([]string, 0, len(items))
		for _, p := range items {
			lines = append(lines, fmt.Sprintf("%s（%s）", p.Name, p.Description))
		}
		s.sayf("进行中的活动有 %d 个：%s。满足门槛时结算会自动享受。", len(items), strings.Join(lines, "、"))
	}
	s.block(blockPromotions(items))
	s.followups("有什么优惠券", "看看购物车能优惠多少", "推荐一款手机")
}

// ---------- 评价 ----------

func (s *session) handleReview(ctx context.Context) {
	if s.plan.Action == "create" {
		s.reviewCreate(ctx)
		return
	}
	card := s.resolveProduct(ctx, "查看评价")
	if card == nil {
		return
	}
	obs := s.call(ctx, "查看评价："+card.Name, ToolListReviews, map[string]any{"product_id": card.ProductID, "limit": 5})
	if !obs.OK {
		s.say("查询评价时出了点问题：" + obs.Message)
		return
	}
	res := decode[ReviewsResult](obs.Data)
	if res.Total == 0 {
		s.sayf("%s 还没有用户评价。", res.ProductName)
	} else {
		s.sayf("%s 有 %d 条评价，平均 %.1f 分。", res.ProductName, res.Total, res.Average)
		for i, r := range res.Reviews {
			if i >= 2 {
				break
			}
			s.sayf("%s（%d 星）：%s", r.ReviewerName, r.Rating, r.Content)
		}
	}
	s.block(blockReviews(res))
	s.block(blockAction(TargetProductDetail, "查看商品", map[string]string{"product_id": card.ProductID}))
	s.followups("把 "+card.Name+" 加入购物车", "推荐类似的商品", "有什么优惠券")
}

// reviewCreate 只能评价自己已完成订单里、还没评价过的商品；目标不唯一就问，不猜。
func (s *session) reviewCreate(ctx context.Context) {
	orders, _, ok := s.listOrders(ctx, domain.OrderCompleted, "", 20)
	if !ok {
		return
	}
	type candidate struct {
		order shop.Order
		item  shop.OrderItem
	}
	var cands []candidate
	terms := strings.Fields(strings.ToLower(s.plan.Query))
	for _, o := range orders {
		// 列表不带评价状态：对每个已完成订单取详情，拿到各订单项是否已评价。
		obs := s.call(ctx, "查看订单 "+o.OrderNo, ToolGetOrder, map[string]any{"order_id": o.OrderID})
		if !obs.OK {
			continue
		}
		o = decode[shop.Order](obs.Data)
		for _, it := range o.Items {
			if it.ReviewID != "" {
				continue
			}
			match := true
			for _, t := range terms {
				if !strings.Contains(strings.ToLower(it.Name), t) {
					match = false
				}
			}
			if match {
				cands = append(cands, candidate{o, it})
			}
		}
	}
	switch {
	case len(orders) == 0:
		s.say("你还没有已完成的订单，确认收货后才能评价。")
		s.followups("我的订单", "已发货的订单", "推荐一款手机")
		return
	case len(cands) == 0:
		s.ask("已完成订单里没有找到还能评价的商品（每件只能评价一次）。")
		s.block(blockOrders(orders))
		return
	case len(cands) > 1:
		names := make([]string, 0, len(cands))
		for _, c := range cands {
			names = append(names, c.item.Name)
		}
		s.ask("可以评价的商品有：" + strings.Join(names, "、") + "。你要评价哪一件？说出商品名就行。")
		return
	}
	target := cands[0]
	if s.plan.Rating == 0 {
		s.ask(fmt.Sprintf("要给 %s 打几星（1–5）？比如“给 %s 打五星，评价：很好用”。", target.item.Name, target.item.Name))
		return
	}
	if s.plan.Content == "" {
		s.ask(fmt.Sprintf("想对 %s 说点什么？比如“评价：%s”。", target.item.Name, "做工不错，用着很顺手"))
		return
	}
	obs := s.call(ctx, "发表评价："+target.item.Name, ToolCreateReview, map[string]any{"order_id": target.order.OrderID,
		"order_item_id": target.item.OrderItemID, "rating": s.plan.Rating, "content": s.plan.Content})
	if !obs.OK {
		s.sayf("评价没有发布：%s", obs.Message)
		return
	}
	review := decode[shop.Review](obs.Data)
	s.sayf("已为 %s 发布 %d 星评价：“%s”。感谢你的反馈！", target.item.Name, review.Rating, review.Content)
	s.block(blockAction(TargetProductDetail, "查看商品", map[string]string{"product_id": review.ProductID}))
	s.followups("推荐一款手机", "我的订单", "有什么优惠券")
}
