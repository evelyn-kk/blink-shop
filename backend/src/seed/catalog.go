package seed

import (
	"fmt"
	"math/rand/v2"
	"strconv"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// 大目录生成器：给压测和检索评测造 N 件商品（分类 × 品牌 × 系列 × 型号），同一个 seed 结果完全相同。
// 只挂在开发种子的两个商家和叶子分类下，所以要先写入开发种子；ID 前缀 p_gen_ / sku_gen_，不会和种子商品冲突。

type genCategory struct {
	id, merchant string
	nouns        []string // 品类名词，组成商品名
	brands       []string
	series       []string
	tags         []string
	points       []string
	suitable     []string
	unsuitable   []string
	specKey      string
	specs        []string
	minPrice     int // 元
	maxPrice     int
}

var genCategories = []genCategory{
	{id: "c_phone", merchant: DigitalMerchant, nouns: []string{"手机", "5G 手机", "拍照手机", "游戏手机"},
		brands: []string{"Blink", "星河", "Orion", "Nimbus", "极光", "Vela"}, series: []string{"Nova", "Vista", "Lite", "Max", "Neo", "Air", "Ultra", "Mini"},
		tags: []string{"拍照", "长续航", "轻薄", "快充", "大屏", "5G", "高刷屏", "影像"}, points: []string{"5000 万像素主摄", "5000mAh 电池", "120Hz 高刷屏", "67W 快充", "IP68 防水", "3 倍长焦"},
		suitable: []string{"学生", "日常拍照", "商务", "长辈"}, unsuitable: []string{"重度手游", "专业摄影"}, specKey: "存储", specs: []string{"128GB", "256GB", "512GB"}, minPrice: 699, maxPrice: 6999},
	{id: "c_audio", merchant: DigitalMerchant, nouns: []string{"降噪耳机", "蓝牙耳机", "头戴耳机", "蓝牙音箱", "运动耳机"},
		brands: []string{"Blink", "声海", "Sonic", "Echo", "回响", "Pulse"}, series: []string{"Air", "Buds", "Pro", "Beat", "Free", "Studio", "Go"},
		tags: []string{"降噪", "通勤", "运动", "防水", "低延迟", "长续航", "HiFi"}, points: []string{"主动降噪", "30 小时续航", "IPX5 防水", "LDAC 高清", "双设备连接", "空间音频"},
		suitable: []string{"通勤", "办公", "跑步", "学生"}, unsuitable: []string{"游泳", "录音棚监听"}, specKey: "颜色", specs: []string{"白色", "黑色", "蓝色"}, minPrice: 59, maxPrice: 2999},
	{id: "c_mouse", merchant: DigitalMerchant, nouns: []string{"无线鼠标", "游戏鼠标", "静音鼠标", "蓝牙鼠标"},
		brands: []string{"Blink", "灵动", "Swift", "Raven", "迅点"}, series: []string{"M", "G", "S", "Pro", "Lite"},
		tags: []string{"静音", "无线", "电竞", "轻量化", "人体工学", "办公"}, points: []string{"静音微动", "双模连接", "26000 DPI", "60g 轻量化", "一节电池用 18 个月"},
		suitable: []string{"办公", "宿舍", "电竞", "设计"}, unsuitable: []string{"高强度电竞", "左手用户"}, specKey: "颜色", specs: []string{"深空灰", "白色", "粉色"}, minPrice: 39, maxPrice: 899},
	{id: "c_keyboard", merchant: DigitalMerchant, nouns: []string{"机械键盘", "无线键盘", "静音键盘", "游戏键盘"},
		brands: []string{"Blink", "键客", "Keyron", "Tactile", "青轴社"}, series: []string{"K", "TK", "Pro", "Mini", "75"},
		tags: []string{"机械键盘", "热插拔", "三模", "RGB", "静音", "Mac 适配"}, points: []string{"热插拔轴体", "三模连接", "Gasket 结构", "PBT 键帽", "4000mAh 电池"},
		suitable: []string{"程序员", "文字工作者", "游戏玩家"}, unsuitable: []string{"需要静音的环境"}, specKey: "轴体", specs: []string{"红轴", "茶轴", "青轴"}, minPrice: 99, maxPrice: 1599},
	{id: "c_lamp", merchant: HomeMerchant, nouns: []string{"护眼台灯", "落地灯", "床头灯", "LED 台灯", "阅读灯"},
		brands: []string{"Blink Home", "暖光", "Lumi", "明月", "Halo"}, series: []string{"L", "Read", "Soft", "Desk", "Glow"},
		tags: []string{"护眼", "无频闪", "调光", "学习", "阅读", "夜灯"}, points: []string{"全光谱 LED", "Ra97 高显色", "无级调光", "RG0 无蓝光危害", "定时关灯"},
		suitable: []string{"学生", "阅读", "办公", "卧室"}, unsuitable: []string{"户外"}, specKey: "颜色", specs: []string{"白色", "黑色", "木纹"}, minPrice: 49, maxPrice: 1299},
}

// Catalog 生成 n 件商品（含 1–3 个规格）。约 95% 在售，其余下架或风控（检验可见性过滤）。
func Catalog(n int, seed uint64) []domain.Product {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	pickN := func(xs []string, k int) []string {
		idx := r.Perm(len(xs))
		out := make([]string, 0, k)
		for _, i := range idx[:min(k, len(xs))] {
			out = append(out, xs[i])
		}
		return out
	}
	out := make([]domain.Product, 0, n)
	for i := 1; i <= n; i++ {
		c := genCategories[r.IntN(len(genCategories))]
		brand, series, noun := pick(c.brands), pick(c.series), pick(c.nouns)
		model := series + " " + strconv.Itoa(1+r.IntN(30))
		if r.IntN(3) == 0 {
			model += pick([]string{" Pro", " Max", " SE", " 青春版", " 2026 款"})
		}
		id := fmt.Sprintf("p_gen_%06d", i)
		price := c.minPrice + r.IntN(c.maxPrice-c.minPrice+1)
		status := domain.ProductActive
		switch v := r.IntN(100); {
		case v < 3:
			status = domain.ProductInactive
		case v < 5:
			status = domain.ProductRisk
		}
		p := domain.Product{
			ProductID: id, MerchantID: c.merchant, CategoryID: c.id, Name: brand + " " + model + " " + noun, Brand: brand,
			ImageURLs: []string{}, MarketPrice: domain.Money(int64(price) * 120), Tags: pickN(c.tags, 2+r.IntN(2)),
			SellingPoints: pickN(c.points, 2+r.IntN(2)), SuitableFor: pickN(c.suitable, 1+r.IntN(2)), NotSuitableFor: pickN(c.unsuitable, r.IntN(2)),
			RiskNotes: []string{}, Attributes: []domain.ProductAttribute{}, RecommendReason: noun + "，" + pick(c.points) + "。",
			Description: brand + " " + model + "，" + pick(c.tags) + "向的" + noun + "。", Status: status, SortOrder: r.IntN(1000),
			CreatedAt: base, UpdatedAt: base,
		}
		for j, spec := range pickN(c.specs, 1+r.IntN(3)) {
			stock := r.IntN(200)
			if r.IntN(10) == 0 {
				stock = 0
			}
			p.SKUs = append(p.SKUs, domain.ProductSKU{
				SkuID: fmt.Sprintf("sku_gen_%06d_%d", i, j), ProductID: id, SkuName: p.Name + " " + spec,
				Price: domain.Money(int64(price+j*100) * 100), StockQuantity: stock, StockStatus: domain.StockStatusOf(stock),
				Specs: map[string]string{c.specKey: spec}, IsDefault: j == 0, CreatedAt: base, UpdatedAt: base,
			})
		}
		if err := p.SyncFromSKUs(); err != nil {
			panic(err) // 生成的数据总是合法的；这里失败说明模板写错了
		}
		out = append(out, p)
	}
	return out
}
