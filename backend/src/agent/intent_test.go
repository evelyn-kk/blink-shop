package agent

import (
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		text   string
		intent Intent
		action string
	}{
		{"你好", IntentGuide, "greeting"},
		{"你是谁？", IntentGuide, "greeting"},
		{"你能做什么", IntentGuide, "greeting"},
		{"打开购物车页面", IntentNavigation, ""},
		{"带我去我的订单", IntentNavigation, ""},
		{"推荐一款通勤降噪耳机", IntentProductSearch, ""},
		{"3000 以内拍照好的手机", IntentProductSearch, ""},
		{"有没有 500 块左右的无线鼠标", IntentProductSearch, ""},
		{"想买个台灯给孩子学习用", IntentProductSearch, ""},
		{"不要 Blink 牌的耳机", IntentProductSearch, ""},
		{"对比 Blink Nova 12 和 Blink Vista Pro", IntentProductCompare, ""},
		{"Nova 12 和 Vista Pro 哪个好", IntentProductCompare, ""},
		{"把第一个加入购物车", IntentCart, "add"},
		{"帮我把刚才推荐的耳机加购", IntentCart, "add"},
		{"把 Blink 静音无线鼠标 M2 加到购物车，要两件", IntentCart, "add"},
		{"看看我的购物车", IntentCart, "view"},
		{"购物车里有什么", IntentCart, "view"},
		{"删掉购物车里的第一个", IntentCart, "remove"},
		{"购物车里的鼠标不要了", IntentCart, "remove"},
		{"把购物车里的鼠标改成 3 件", IntentCart, "update"},
		{"取消选中购物车里的耳机", IntentCart, "unselect"},
		{"清空购物车", IntentCart, "clear"},
		{"结算", IntentCheckout, ""},
		{"帮我下单", IntentCheckout, ""},
		{"去结算购物车里的东西", IntentCheckout, ""},
		{"支付订单", IntentOrder, "pay"},
		{"付款 BS202609201000100", IntentOrder, "pay"},
		{"取消那个待支付的订单", IntentOrder, "cancel"},
		{"确认收货", IntentOrder, "confirm"},
		{"我的订单", IntentOrder, "list"},
		{"待支付的订单", IntentOrder, "list"},
		{"我的快递到哪了", IntentOrder, "list"},
		{"有什么优惠券", IntentCoupon, "list"},
		{"帮我领券", IntentCoupon, "claim"},
		{"我的优惠券", IntentCoupon, "mine"},
		{"有什么促销活动", IntentCoupon, "promotions"},
		{"Blink Nova 12 的评价怎么样", IntentReview, "list"},
		{"给鼠标打五星，评价：很好用", IntentReview, "create"},
		{"七天无理由怎么退", IntentKnowledge, ""},
		{"耳机保修多久", IntentKnowledge, ""},
		{"鼠标怎么连接电脑", IntentKnowledge, ""},
		{"今天天气怎么样", IntentNonGuide, ""},
		{"帮我写一段 Python 代码", IntentNonGuide, ""},
		{"讲个笑话", IntentNonGuide, ""},
		{"拍照找同款", IntentImageSearch, ""},
		{"嗯嗯好的", IntentGuide, "fallback"},
	}
	for _, c := range cases {
		p := Classify(c.text, false)
		if p.Intent != c.intent || (c.action != "" && p.Action != c.action) {
			t.Errorf("%q: got %s/%s (%s), want %s/%s", c.text, p.Intent, p.Action, p.Rule, c.intent, c.action)
		}
	}
	if p := Classify("看看这张图", true); p.Intent != IntentImageSearch {
		t.Errorf("image attachment: %+v", p)
	}
}

func TestClassifySlots(t *testing.T) {
	p := Classify("3000 以内拍照好的手机，不要 Vista", false)
	if p.Budget != domain.MustMoney("3000") || len(p.Exclude) != 1 || p.Exclude[0] != "Vista" || p.Query != "拍照 手机" {
		t.Errorf("budget/exclude: %+v", p)
	}
	p = Classify("预算三千左右的手机", false)
	if p.Budget != domain.MustMoney("3450") {
		t.Errorf("左右 budget: %s", p.Budget)
	}
	p = Classify("两千五以内的手机", false)
	if p.Budget != domain.MustMoney("2500") {
		t.Errorf("两千五: %s", p.Budget)
	}
	p = Classify("把第二个加入购物车，要 3 件", false)
	if p.Ordinal != 2 || p.Quantity != 3 || p.Query != "" {
		t.Errorf("ordinal/quantity: %+v", p)
	}
	p = Classify("把最后一个加入购物车", false)
	if p.Ordinal != -1 {
		t.Errorf("last: %+v", p)
	}
	p = Classify("把刚才那个加购", false)
	if !p.Reference || p.Ordinal != 0 {
		t.Errorf("reference: %+v", p)
	}
	p = Classify("对比 Blink Nova 12 和 Blink Vista Pro", false)
	if len(p.Names) != 2 || p.Names[0] != "blink nova 12" || p.Names[1] != "blink vista pro" {
		t.Errorf("compare names: %+v", p.Names)
	}
	p = Classify("给鼠标打五星，评价：很好用，很安静", false)
	if p.Rating != 5 || p.Content != "很好用，很安静" || p.Query != "鼠标" {
		t.Errorf("review: %+v", p)
	}
	p = Classify("我的待发货订单", false)
	if p.OrderStatus != domain.OrderPaid {
		t.Errorf("order status: %+v", p)
	}
	p = Classify("支付订单 BS202609201000100", false)
	if p.OrderRef != "BS202609201000100" {
		t.Errorf("order ref: %+v", p)
	}
	p = Classify("推荐一款通勤降噪耳机", false)
	if p.Query != "通勤降噪耳机" {
		t.Errorf("query: %q", p.Query)
	}
}

func TestChineseNumber(t *testing.T) {
	for s, want := range map[string]int{"三千": 3000, "两千五": 2500, "一万二": 12000, "十五": 15, "二十": 20, "五百": 500, "3000": 3000, "1.5": 2, "一千零五十": 1050, "千": 1000} {
		if got, ok := ChineseNumber(s); !ok || got != want {
			t.Errorf("%s: got %d %v, want %d", s, got, ok, want)
		}
	}
	if _, ok := ChineseNumber("abc"); ok {
		t.Error("abc should fail")
	}
}

func TestQueryTerms(t *testing.T) {
	for text, want := range map[string]string{
		"帮我推荐一款适合通勤的降噪耳机":   "通勤 降噪耳机",
		"3000 以内拍照好的手机":     "拍照 手机",
		"Blink Nova 12 多少钱": "blink nova 12",
		"有没有便宜点的无线鼠标":       "无线鼠标",
		"你好":                "",
	} {
		if got := joinTerms(QueryTerms(text)); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func joinTerms(terms []string) string {
	out := ""
	for i, t := range terms {
		if i > 0 {
			out += " "
		}
		out += t
	}
	return out
}
