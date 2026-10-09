package agent

import (
	"regexp"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// Plan 是规则规划的结果：意图、具体动作和从句子里抽出的槽位。
type Plan struct {
	Intent Intent `json:"intent"`
	// Action 细分动作：
	//   guide: greeting / help / fallback；cart: view / add / update / remove / clear / select / unselect；
	//   order: list / detail / pay / cancel / confirm；coupon: list / mine / claim / promotions；review: list / create；
	//   navigation: 目标页面在 Target；其余意图为空。
	Action string `json:"action,omitempty"`
	// Query 是去掉口头语后的商品/知识检索词。
	Query string `json:"query,omitempty"`
	// Budget 是价格上限（0 表示没有）。
	Budget domain.Money `json:"budget,omitempty"`
	// Exclude 是用户明确排除的词。
	Exclude []string `json:"exclude,omitempty"`
	// Ordinal 是“第 N 个”（-1 表示最后一个，0 表示没有）；Reference 表示用“这个/那个/刚才的”指代上文。
	Ordinal   int  `json:"ordinal,omitempty"`
	Reference bool `json:"reference,omitempty"`
	// Quantity 是件数（0 表示没有说）。
	Quantity int `json:"quantity,omitempty"`
	// Names 是对比时提到的多个商品名。
	Names []string `json:"names,omitempty"`
	// OrderStatus / OrderRef 是订单筛选条件。
	OrderStatus domain.OrderStatus `json:"order_status,omitempty"`
	OrderRef    string             `json:"order_ref,omitempty"`
	// Rating / Content 是发表评价的评分和内容。
	Rating  int    `json:"rating,omitempty"`
	Content string `json:"content,omitempty"`
	// Target 是导航目标：products / product_detail / cart / orders / coupons / sessions / settings。
	Target string `json:"target,omitempty"`
	// Rule 是命中的规则名，写入轨迹便于排查。
	Rule string `json:"rule"`
	// Topic 是非导购问题的类型（weather / coding / ...），只用于回答措辞。
	Topic string `json:"topic,omitempty"`
}

var (
	greetings       = []string{"你好", "您好", "嗨", "哈喽", "哈罗", "hello", "hi", "hey", "在吗", "在不在", "你是谁", "你是什么", "你能做什么", "你会什么", "能帮我什么", "可以帮我什么", "帮助", "help", "谢谢", "谢谢你", "感谢", "多谢", "好的", "ok", "好", "嗯", "早上好", "晚上好", "下午好"}
	navVerbs        = []string{"打开", "跳转", "跳到", "进入", "带我去", "切到", "切换到", "转到", "去到"}
	navPages        = map[string]string{"购物车": "cart", "订单": "orders", "我的订单": "orders", "首页": "products", "商品列表": "products", "商品页": "products", "列表页": "products", "优惠券": "coupons", "券包": "coupons", "我的券": "coupons", "会话": "sessions", "历史": "sessions", "聊天记录": "sessions", "设置": "settings"}
	imageWords      = []string{"拍照找", "拍照搜", "找同款", "图片找", "识图", "这张图", "图里", "图片里", "照片里", "看图", "以图搜"}
	cartAddWords    = []string{"加入购物车", "加到购物车", "放进购物车", "放入购物车", "添加到购物车", "加进购物车", "加购物车", "加购", "加车", "放到车里", "加到车里", "放进车里"}
	cartRemoveWords = []string{"删掉", "删除", "移除", "去掉", "拿掉", "移出", "不要了", "清空", "去除"}
	cartQtyWords    = []string{"改成", "改为", "换成", "调成", "变成", "改到", "数量", "减到", "加到"}
	cartSelectWords = []string{"取消选中", "取消勾选", "不选", "别选", "选中", "勾选", "全选"}
	cartWords       = []string{"购物车", "车里", "车内", "加购的", "凑单", "凑够", "试算", "合计", "总价", "应付", "实付", "用券"}
	checkoutWords   = []string{"结算", "下单", "买单", "结账", "提交订单", "去购买", "直接买", "现在买", "立即购买", "购买购物车", "全部买了", "都买了", "买了吧", "下个单"}
	payWords        = []string{"支付", "付款", "付钱", "付一下", "付了", "交钱", "买了它"}
	orderCancel     = []string{"取消订单", "取消那个订单", "取消这个订单", "退单", "不想要这个订单", "撤销订单", "把订单取消"}
	orderConfirm    = []string{"确认收货", "收到货", "签收", "收货了", "已经收到", "确认收到"}
	orderWords      = []string{"订单", "我买的", "物流", "快递", "发货", "到哪了", "到哪儿了", "什么时候到", "买过", "购买记录", "待付款", "待支付", "已发货", "已完成", "已取消", "待发货", "没付款", "未付款", "单子"}
	couponClaim     = []string{"领券", "领取", "领一下", "领了", "帮我领", "都领", "领优惠券", "领张"}
	couponWords     = []string{"优惠券", "券", "红包", "折扣券", "满减券", "代金券"}
	promoWords      = []string{"促销", "活动", "满减", "打折", "折扣", "有什么优惠", "有啥优惠", "优惠活动", "特价", "降价"}
	reviewCreate    = []string{"写评价", "发评价", "发表评价", "发布评价", "提交评价", "写个评价", "评价一下", "给个好评", "打五星", "打5星", "评个分", "我要评价", "我想评价", "评价：", "评价:", "评论：", "评论:"}
	reviewWords     = []string{"评价", "评论", "口碑", "好评", "差评", "评分", "买过的人", "用过的人", "用户反馈", "风评"}
	knowledgeWords  = []string{"退货", "退款", "售后", "保修", "换货", "运费", "发票", "无理由", "政策", "规则", "怎么用", "怎么连", "怎么连接", "怎么配对", "怎么充电", "怎么清洗", "怎么保养", "怎么安装", "支持吗", "能用吗", "可以用吗", "防水吗", "保修期", "质保", "几天", "多久", "什么时候发", "包邮", "配送", "快递费", "七天", "保价", "说明书", "参数", "材质", "续航多久", "怎么办", "可以退", "能退"}
	compareWords    = []string{"对比", "比较", "区别", "差别", "哪个好", "哪款好", "哪个更", "哪款更", "哪个值", "有什么不同", "有啥不同", "差在哪", "怎么选", " vs ", "vs", "pk"}
	searchWords     = []string{"推荐", "想买", "要买", "买个", "买一", "来一", "来个", "找", "有没有", "有什么", "有哪些", "哪款", "哪个", "适合", "预算", "多少钱", "价格", "便宜", "性价比", "好用", "选", "挑", "想要", "需要", "介绍", "看看", "有卖", "卖不卖", "有货", "库存", "型号", "规格", "颜色", "多少", "送", "礼物", "新款", "热销", "爆款"}
	catalogWords    = []string{"手机", "耳机", "音箱", "键盘", "鼠标", "台灯", "灯", "电脑", "笔记本", "平板", "手表", "相机", "充电", "移动电源", "充电宝", "数码", "办公", "家居", "照明", "降噪", "机械", "护眼", "拍照", "续航", "无线", "蓝牙", "nova", "vista", "blink", "k8", "m2", "l1", "s1"}
	nonGuideTopics  = map[string][]string{
		"weather": {"天气", "下雨", "气温", "温度多少"},
		"coding":  {"写代码", "代码", "编程", "脚本", "python", "java", "sql", "bug", "函数", "程序"},
		"chat":    {"笑话", "讲个故事", "写诗", "作文", "翻译", "聊聊天", "唱歌", "星座", "算命"},
		"finance": {"股票", "基金", "彩票", "比特币", "汇率", "贷款"},
		"news":    {"新闻", "政治", "总统", "选举", "战争"},
		"math":    {"等于几", "等于多少", "数学题", "计算一下", "方程"},
		"other":   {"你多大", "你几岁", "几点了", "现在时间", "日期", "星期几", "导航到", "地图", "路线", "外卖", "打车", "订票", "机票", "酒店"},
	}
	splitCompare = regexp.MustCompile(`\s*(?:和|与|跟|还是|、|，|,|vs|VS|对比|比较|比)\s*`)
	referenceRe  = regexp.MustCompile(`这个|那个|这款|那款|这件|那件|它|刚才(?:推荐)?的|上面的|前面的|刚刚的|刚才那|刚刚那|这一个|那一个|这些|那些`)
)

// Classify 用规则判断一句话的意图和槽位。hasImage 表示消息带图片附件。
// 规则按“具体 → 泛化”排列：先看固定短语和明确动作（导航、加购、结算、订单操作），再看领域词，最后才落到商品搜索和非导购。
func Classify(content string, hasImage bool) Plan {
	text := strings.TrimSpace(content)
	lower := strings.ToLower(text)
	plain := compact(text)

	// 1. 打招呼 / 能力询问：整句就是固定短语。
	for _, g := range greetings {
		if plain == compact(g) {
			return Plan{Intent: IntentGuide, Action: "greeting", Rule: "greeting"}
		}
	}
	if plain == "" {
		return Plan{Intent: IntentGuide, Action: "greeting", Rule: "empty"}
	}

	// 2. 图片找货：带图或明确说识图。
	if hasImage || containsAny(lower, imageWords...) {
		return Plan{Intent: IntentImageSearch, Query: strings.Join(QueryTerms(text), " "), Rule: "image"}
	}

	// 3. 页面跳转：导航动词 + 页面名，且没有别的操作动词。
	if containsAny(lower, navVerbs...) && !containsAny(lower, cartAddWords...) && !containsAny(lower, checkoutWords...) {
		if target := navTarget(lower); target != "" {
			return Plan{Intent: IntentNavigation, Target: target, Rule: "navigation"}
		}
	}

	base := Plan{Ordinal: ParseOrdinal(text), Reference: referenceRe.MatchString(text), Quantity: ParseQuantity(text),
		Budget: ParseBudget(text), Exclude: ParseExclusions(text), OrderRef: ParseOrderRef(text)}

	// 4. 购物车操作。
	if containsAny(lower, cartAddWords...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentCart, "add", "cart_add"
		p.Query = productQuery(stripWords(text, cartAddWords, cartWords, []string{"商品", "那款", "这款"}))
		return p
	}
	inCart := containsAny(lower, cartWords...) || ((base.Ordinal != 0 || base.Reference) && containsAny(lower, "商品", "件", "个"))
	if inCart && containsAny(lower, "清空", "全部删", "都删", "全删") {
		return Plan{Intent: IntentCart, Action: "clear", Rule: "cart_clear"}
	}
	if inCart && containsAny(lower, cartRemoveWords...) && !containsAny(lower, orderWords...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentCart, "remove", "cart_remove"
		p.Query = productQuery(stripWords(text, cartRemoveWords, cartWords, []string{"商品", "那款", "这款", "里的", "中的"}))
		return p
	}
	if inCart && containsAny(lower, cartSelectWords...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentCart, "select", "cart_select"
		if containsAny(lower, "取消选中", "取消勾选", "不选", "别选") {
			p.Action = "unselect"
		}
		p.Query = productQuery(stripWords(text, cartSelectWords, cartWords, []string{"商品", "那款", "这款", "里的", "中的"}))
		return p
	}
	if inCart && (containsAny(lower, cartQtyWords...) || base.Quantity > 0) && !containsAny(lower, checkoutWords...) && !containsAny(lower, orderWords...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentCart, "update", "cart_update"
		p.Query = productQuery(stripWords(text, cartQtyWords, cartWords, []string{"商品", "那款", "这款", "里的", "中的"}))
		return p
	}

	// 5. 结算 / 支付 / 订单操作。
	if containsAny(lower, checkoutWords...) && !containsAny(lower, "订单号", "那个订单", "这个订单", "待支付的订单", "待付款的订单") {
		if !(containsAny(lower, orderCancel...) || containsAny(lower, orderConfirm...)) {
			return Plan{Intent: IntentCheckout, Rule: "checkout", OrderRef: base.OrderRef}
		}
	}
	if containsAny(lower, orderCancel...) || (containsAny(lower, "取消", "撤销") && containsAny(lower, orderWords...)) {
		p := base
		p.Intent, p.Action, p.Rule = IntentOrder, "cancel", "order_cancel"
		return p
	}
	if containsAny(lower, orderConfirm...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentOrder, "confirm", "order_confirm"
		return p
	}
	// “待支付的订单”“多久要付款”是在问状态或规则，不是要付钱。
	payText := stripWords(lower, []string{"待支付", "待付款", "未支付", "未付款", "没付款", "没付钱", "支付方式", "怎么支付", "如何支付", "支付时间", "多久要付", "多久付款", "付款时间", "支付期限", "等待支付"})
	if containsAny(payText, payWords...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentOrder, "pay", "order_pay"
		return p
	}
	if base.OrderRef != "" || containsAny(lower, orderWords...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentOrder, "list", "order_list"
		p.OrderStatus = orderStatusOf(lower)
		if base.OrderRef != "" || containsAny(lower, "详情", "明细", "这个订单", "那个订单", "这单", "那单") {
			p.Action = "detail"
		}
		return p
	}
	if inCart && !containsAny(lower, cartAddWords...) {
		return Plan{Intent: IntentCart, Action: "view", Rule: "cart_view"}
	}

	// 6. 优惠券 / 促销。
	if containsAny(lower, couponWords...) || (containsAny(lower, couponClaim...) && !containsAny(lower, "领取方式", "怎么领", "如何领", "在哪领", "哪里领")) {
		p := base
		p.Intent, p.Action, p.Rule = IntentCoupon, "list", "coupon"
		switch {
		case containsAny(lower, "怎么领", "如何领", "在哪领", "哪里领", "领取方式", "怎么用", "如何使用", "使用规则", "能不能用", "能用吗"):
			p.Intent, p.Action, p.Rule = IntentKnowledge, "", "coupon_help"
			p.Query = text
		case containsAny(lower, couponClaim...):
			p.Action, p.Rule = "claim", "coupon_claim"
			p.Query = productQuery(stripWords(text, couponClaim, couponWords, []string{"所有", "全部", "都", "能领的", "可以领的"}))
		case containsAny(lower, "我的", "已领", "领过", "有几张", "有哪些券", "我有"):
			p.Action, p.Rule = "mine", "coupon_mine"
		}
		return p
	}
	if containsAny(lower, promoWords...) {
		return Plan{Intent: IntentCoupon, Action: "promotions", Rule: "promotions", Query: productQuery(stripWords(text, promoWords, nil, nil))}
	}

	// 7. 评价。
	if containsAny(lower, reviewCreate...) || (containsAny(lower, reviewWords...) && (ParseRating(text) > 0 || containsAny(lower, "我要", "我想", "帮我写", "给他", "给它"))) {
		p := base
		p.Intent, p.Action, p.Rule = IntentReview, "create", "review_create"
		p.Rating = ParseRating(text)
		p.Content, p.Query = reviewContent(text)
		return p
	}
	if containsAny(lower, reviewWords...) {
		p := base
		p.Intent, p.Action, p.Rule = IntentReview, "list", "review_list"
		p.Query = productQuery(stripWords(text, reviewWords, nil, []string{"怎么样", "如何", "看看", "看一下", "的", "有没有"}))
		return p
	}

	// 8. 知识问答：售后、规则、用法。
	if containsAny(lower, knowledgeWords...) && !containsAny(lower, "推荐", "想买", "要买") {
		p := base
		p.Intent, p.Rule, p.Query = IntentKnowledge, "knowledge", text
		return p
	}

	// 9. 对比。
	if containsAny(lower, compareWords...) {
		p := base
		p.Intent, p.Rule = IntentProductCompare, "compare"
		p.Names = compareNames(text)
		p.Query = productQuery(text)
		return p
	}

	// 10. 非导购话题。
	for topic, words := range nonGuideTopics {
		if containsAny(lower, words...) && !containsAny(lower, catalogWords...) {
			return Plan{Intent: IntentNonGuide, Topic: topic, Rule: "non_guide_topic"}
		}
	}

	// 11. 商品搜索：有购物动词或品类词。
	if containsAny(lower, searchWords...) || containsAny(lower, catalogWords...) || base.Budget > 0 || len(base.Exclude) > 0 {
		p := base
		p.Intent, p.Rule = IntentProductSearch, "product_search"
		p.Query = productQuery(text)
		if p.Query == "" {
			p.Action = "ask"
		}
		return p
	}

	// 12. 兜底：先试着当商品需求搜一次，搜不到再按非导购回答。
	p := base
	p.Intent, p.Action, p.Rule = IntentGuide, "fallback", "fallback"
	p.Query = productQuery(text)
	return p
}

func navTarget(lower string) string {
	for page, target := range navPages {
		if strings.Contains(lower, page) {
			return target
		}
	}
	return ""
}

func orderStatusOf(lower string) domain.OrderStatus {
	switch {
	case containsAny(lower, "待付款", "待支付", "没付款", "未付款", "没付钱", "还没付", "未支付", "等待支付"):
		return domain.OrderPendingPayment
	case containsAny(lower, "待发货", "已付款", "付过款", "付了款", "等发货", "还没发货", "没发货", "什么时候发"):
		return domain.OrderPaid
	case containsAny(lower, "已发货", "在路上", "物流", "快递", "到哪", "什么时候到", "运输"):
		return domain.OrderShipped
	case containsAny(lower, "已完成", "完成的", "收货的", "买过的", "历史订单"):
		return domain.OrderCompleted
	case containsAny(lower, "已取消", "取消的", "关闭的"):
		return domain.OrderCancelled
	}
	return ""
}

// stripWords 去掉动作词后再提取商品词。
func stripWords(text string, groups ...[]string) string {
	for _, g := range groups {
		for _, w := range g {
			text = strings.ReplaceAll(text, w, " ")
		}
	}
	return text
}

// productQuery 把句子压成检索词串（空格分隔）。
func productQuery(text string) string {
	text = ordinalPattern.ReplaceAllString(text, " ")
	text = quantityPattern.ReplaceAllString(text, " ")
	text = referenceRe.ReplaceAllString(text, " ")
	return strings.Join(QueryTerms(text), " ")
}

// compareNames 从对比句里拆出候选商品名：按“和/与/跟/还是/vs/、”切开，去掉动作词和口头语。
func compareNames(text string) []string {
	cleaned := text
	for _, w := range compareWords {
		if strings.TrimSpace(w) != "" && w != "比" {
			cleaned = strings.ReplaceAll(cleaned, w, " ")
		}
	}
	cleaned = strings.NewReplacer("一下", " ", "帮我", " ", "给我", " ", "我想", " ", "请", " ", "哪个", " ", "哪款", " ", "好", " ", "更", " ", "值得买", " ", "买", " ", "选", " ", "推荐", " ").Replace(cleaned)
	var out []string
	for _, part := range splitCompare.Split(cleaned, -1) {
		terms := QueryTerms(part)
		if len(terms) == 0 {
			continue
		}
		name := strings.Join(terms, " ")
		if !containsStr(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// reviewContent 从“给鼠标打五星，评价：很好用”里拆出评价正文和商品词。
func reviewContent(text string) (content, product string) {
	for _, sep := range []string{"评价：", "评价:", "评论：", "评论:", "内容：", "内容:", "说：", "说:", "，", ","} {
		if i := strings.Index(text, sep); i >= 0 {
			head, tail := text[:i], strings.TrimSpace(text[i+len(sep):])
			if tail != "" && ParseRating(tail) == 0 {
				return tail, productQuery(stripWords(head, reviewCreate, reviewWords, []string{"给", "打", "星", "分", "颗", "我要", "我想", "帮我", "写", "这个", "那个"}))
			}
		}
	}
	return "", productQuery(stripWords(ratingPattern.ReplaceAllString(text, " "), reviewCreate, reviewWords, []string{"给", "打", "我要", "我想", "帮我", "写", "这个", "那个"}))
}
