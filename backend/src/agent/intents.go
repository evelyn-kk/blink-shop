package agent

// Intent 是规划出的用户意图；工具白名单按意图划分（见 DefaultPolicy）。
type Intent string

const (
	IntentGuide          Intent = "guide"           // 打招呼、能力说明、泛泛的购物求助
	IntentProductSearch  Intent = "product_search"  // 商品推荐 / 找商品
	IntentProductCompare Intent = "product_compare" // 商品对比
	IntentImageSearch    Intent = "image_search"    // 拍照找货：附图检索相似商品，没带图时请用户上传
	IntentKnowledge      Intent = "knowledge"       // 售后、规则、使用方法等知识问答
	IntentCart           Intent = "cart"            // 查看 / 加购 / 改数量 / 删除 / 选中
	IntentCheckout       Intent = "checkout"        // 结算下单
	IntentOrder          Intent = "order"           // 订单查询 / 支付 / 取消 / 确认收货
	IntentCoupon         Intent = "coupon"          // 优惠券 / 促销活动
	IntentReview         Intent = "review"          // 查看评价 / 发表评价
	IntentNavigation     Intent = "navigation"      // 跳转页面
	IntentNonGuide       Intent = "non_guide"       // 与购物无关
)

// IntentTitle 是展示给用户的意图名称（thinking 步骤）。
func IntentTitle(i Intent) string {
	switch i {
	case IntentGuide:
		return "导购咨询"
	case IntentProductSearch:
		return "商品推荐"
	case IntentProductCompare:
		return "商品对比"
	case IntentImageSearch:
		return "图片找货"
	case IntentKnowledge:
		return "知识问答"
	case IntentCart:
		return "购物车"
	case IntentCheckout:
		return "结算下单"
	case IntentOrder:
		return "订单"
	case IntentCoupon:
		return "优惠券与活动"
	case IntentReview:
		return "商品评价"
	case IntentNavigation:
		return "页面跳转"
	case IntentNonGuide:
		return "非购物问题"
	}
	return string(i)
}
