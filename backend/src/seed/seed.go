// Package seed 定义本地开发用的演示数据。所有 ID 固定，配合 Store.ApplySeed 的“不存在才插入”可重复执行。
// 数据不含任何真实个人信息；账号名与上游不同。
package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// DevPassword 是所有演示账号的初始密码，仅用于本地开发。
const DevPassword = "BlinkDev#2026"

// 演示账号。
const (
	AdminUsername     = "blink_admin"
	MerchantUsername  = "blink_merchant"
	Merchant2Username = "blink_merchant2"
	UserUsername      = "blink_user"
	User2Username     = "blink_user2"

	AdminID     = "acct_seed_admin"
	MerchantID  = "acct_seed_merchant"
	Merchant2ID = "acct_seed_merchant2"
	UserID      = "acct_seed_user"
	User2ID     = "acct_seed_user2"

	DigitalMerchant = "m_seed_digital"
	HomeMerchant    = "m_seed_home"
)

// HashFunc 把明文密码转成 bcrypt 哈希。
type HashFunc func(password string) (string, error)

var (
	base       = time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC)
	validFrom  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	validUntil = time.Date(2027, 12, 31, 15, 59, 59, 0, time.UTC)
)

func at(hours int) time.Time { return base.Add(time.Duration(hours) * time.Hour) }

func ptr(t time.Time) *time.Time { return &t }

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// 演示图片随后端内嵌发布（backend/assets/catalog），无需外网。
func image(slug string) string { return "/api/v1/assets/catalog/products/" + slug + ".png" }
func logo(slug string) string  { return "/api/v1/assets/catalog/merchants/" + slug + ".png" }

// Dev 构建完整的开发数据集。hash 用于生成密码哈希，测试中可传入低成本 bcrypt。
func Dev(hash HashFunc) (store.SeedData, error) {
	pw, err := hash(DevPassword)
	if err != nil {
		return store.SeedData{}, err
	}
	var d store.SeedData

	account := func(id, username, name string, role domain.Role, merchantID string) store.SeedAccount {
		return store.SeedAccount{PasswordHash: pw, Account: domain.Account{
			AccountID: id, Username: username, DisplayName: name, Role: role, MerchantID: merchantID,
			Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base,
		}}
	}
	d.Accounts = []store.SeedAccount{
		account(AdminID, AdminUsername, "Blink 平台管理员", domain.RoleAdmin, ""),
		account(MerchantID, MerchantUsername, "Blink 数码运营", domain.RoleMerchant, DigitalMerchant),
		account(Merchant2ID, Merchant2Username, "Blink 家居运营", domain.RoleMerchant, HomeMerchant),
		account(UserID, UserUsername, "演示用户", domain.RoleUser, ""),
		account(User2ID, User2Username, "演示用户二", domain.RoleUser, ""),
	}

	d.Merchants = []domain.Merchant{
		{MerchantID: DigitalMerchant, Name: "Blink 数码旗舰店", LogoURL: logo("logo-digital"), Description: "主营手机、耳机和办公外设。",
			ServicePhone: "400-800-0001", Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base},
		{MerchantID: HomeMerchant, Name: "Blink 家居生活馆", LogoURL: logo("logo-home"), Description: "主营照明和桌面收纳。",
			ServicePhone: "400-800-0002", Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base},
	}

	cat := func(id, parent, name string, order int) domain.Category {
		return domain.Category{CategoryID: id, ParentID: parent, Name: name, SortOrder: order, CreatedAt: base, UpdatedAt: base}
	}
	d.Categories = []domain.Category{
		cat("c_digital", "", "数码", 10),
		cat("c_phone", "c_digital", "手机", 10),
		cat("c_audio", "c_digital", "耳机音箱", 20),
		cat("c_office", "", "电脑办公", 20),
		cat("c_mouse", "c_office", "鼠标", 10),
		cat("c_keyboard", "c_office", "键盘", 20),
		cat("c_home", "", "家居", 30),
		cat("c_lamp", "c_home", "台灯照明", 10),
	}

	d.Products = products()

	d.Documents, d.Chunks = knowledge()

	d.Promotions = []domain.PromotionRule{
		{PromotionID: "promo_seed_platform", Name: "平台满 300 减 30", Scope: domain.ScopePlatform, Type: domain.PromotionFullReduction,
			ThresholdAmount: domain.MustMoney("300"), DiscountAmount: domain.MustMoney("30"), Stackable: true,
			StartAt: validFrom, EndAt: validUntil, Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base},
		{PromotionID: "promo_seed_digital", Name: "Blink 数码满 1000 减 80", Scope: domain.ScopeMerchant, MerchantID: DigitalMerchant,
			Type: domain.PromotionFullReduction, ThresholdAmount: domain.MustMoney("1000"), DiscountAmount: domain.MustMoney("80"), Stackable: true,
			StartAt: validFrom, EndAt: validUntil, Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base},
		{PromotionID: "promo_seed_earbuds", Name: "降噪耳机 95 折", Scope: domain.ScopeProduct, MerchantID: DigitalMerchant, ProductID: "p_seed_earbuds",
			Type: domain.PromotionDiscount, DiscountRate: domain.MustRate("0.95"), Stackable: false,
			StartAt: validFrom, EndAt: validUntil, Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base},
		{PromotionID: "promo_seed_expired", Name: "已结束的暑期活动", Scope: domain.ScopePlatform, Type: domain.PromotionFullReduction,
			ThresholdAmount: domain.MustMoney("200"), DiscountAmount: domain.MustMoney("50"), Stackable: true,
			StartAt: validFrom, EndAt: time.Date(2026, 8, 31, 15, 59, 59, 0, time.UTC), Status: domain.StatusInactive, CreatedAt: base, UpdatedAt: base},
	}

	d.Coupons = []domain.Coupon{
		{CouponID: "coupon_seed_platform", Name: "平台满 200 减 20", Scope: domain.ScopePlatform, Type: domain.CouponFixedAmount,
			ThresholdAmount: domain.MustMoney("200"), DiscountAmount: domain.MustMoney("20"), TotalCount: 10000, ClaimedCount: 2, PerUserLimit: 1,
			StartAt: validFrom, EndAt: validUntil, Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base},
		{CouponID: "coupon_seed_digital", Name: "Blink 数码满 500 减 50", Scope: domain.ScopeMerchant, MerchantID: DigitalMerchant,
			Type: domain.CouponFixedAmount, ThresholdAmount: domain.MustMoney("500"), DiscountAmount: domain.MustMoney("50"),
			TotalCount: 500, ClaimedCount: 0, PerUserLimit: 1, StartAt: validFrom, EndAt: validUntil, Status: domain.StatusActive, CreatedAt: base, UpdatedAt: base},
	}

	d.Orders, d.Payments, d.UserCoupons, d.Reviews = orders(d.Products, d.Merchants)
	return d, nil
}

// products 至少 6 个商品，覆盖：可售、库存紧张、无货、下架、风控、已删除，以及跨商家。
func products() []domain.Product {
	type skuDef struct {
		id, name, price string
		stock           int
		specs           map[string]string
	}
	mk := func(id, merchant, category, name, brand, market string, status domain.ProductStatus, order int,
		tags, points, suitable, unsuitable, risks []string, attrs []domain.ProductAttribute, reason, desc string, skus ...skuDef) domain.Product {
		p := domain.Product{
			ProductID: id, MerchantID: merchant, CategoryID: category, Name: name, Brand: brand,
			ImageURL: image(id), ImageURLs: []string{image(id), image(id + "-2")},
			MarketPrice: domain.MustMoney(market), Tags: tags, SellingPoints: points, RecommendReason: reason,
			RiskNotes: risks, Attributes: attrs, SuitableFor: suitable, NotSuitableFor: unsuitable,
			Description: desc, Status: status, SortOrder: order, CreatedAt: base, UpdatedAt: base,
		}
		for i, s := range skus {
			sku := domain.ProductSKU{
				SkuID: s.id, ProductID: id, SkuName: s.name, Price: domain.MustMoney(s.price), StockQuantity: s.stock,
				StockStatus: domain.StockStatusOf(s.stock), Specs: s.specs, IsDefault: i == 0, CreatedAt: base, UpdatedAt: base,
			}
			p.SKUs = append(p.SKUs, sku)
			p.StockQuantity += s.stock
			if i == 0 {
				p.Price = sku.Price
			}
		}
		p.StockStatus = domain.StockStatusOf(p.StockQuantity)
		return p
	}
	none := []string{}
	attr := func(kv ...string) []domain.ProductAttribute {
		var out []domain.ProductAttribute
		for i := 0; i+2 < len(kv); i += 3 {
			out = append(out, domain.ProductAttribute{Key: kv[i], Value: kv[i+1], Unit: kv[i+2]})
		}
		return out
	}

	return []domain.Product{
		mk("p_seed_nova", DigitalMerchant, "c_phone", "Blink Nova 12", "Blink", "3299", domain.ProductActive, 10,
			[]string{"拍照", "轻薄", "长续航"}, []string{"5000 万像素主摄", "5000mAh 电池", "6.5 英寸 OLED"},
			[]string{"日常拍照", "学生", "预算三千左右"}, []string{"重度手游"}, none,
			attr("屏幕", "6.5 英寸 OLED", "", "重量", "186", "g", "电池", "5000", "mAh"),
			"拍照和续航均衡，2999 元起。", "面向日常拍照和长续航需求的轻薄手机。",
			skuDef{"sku_seed_nova_128", "Blink Nova 12 8+128G", "2999", 50, map[string]string{"存储": "128GB", "颜色": "曜石黑"}},
			skuDef{"sku_seed_nova_256", "Blink Nova 12 8+256G", "3299", 30, map[string]string{"存储": "256GB", "颜色": "曜石黑"}}),
		mk("p_seed_vista", DigitalMerchant, "c_phone", "Blink Vista Pro", "Blink", "4599", domain.ProductActive, 20,
			[]string{"影像旗舰", "长焦"}, []string{"3 倍光学长焦", "夜景增强"},
			[]string{"旅行拍照", "人像"}, []string{"预算三千以内"}, none,
			attr("屏幕", "6.7 英寸 LTPO", "", "重量", "205", "g"),
			"长焦和夜景更强，适合重视影像的用户。", "影像旗舰手机。",
			skuDef{"sku_seed_vista_256", "Blink Vista Pro 12+256G", "4299", 25, map[string]string{"存储": "256GB"}}),
		mk("p_seed_earbuds", DigitalMerchant, "c_audio", "Blink Air 降噪耳机", "Blink", "699", domain.ProductActive, 30,
			[]string{"降噪", "通勤"}, []string{"主动降噪", "30 小时续航"},
			[]string{"通勤", "办公"}, []string{"运动防水场景"}, []string{"不支持游泳佩戴"},
			attr("续航", "30", "小时", "防水", "IPX4", ""),
			"通勤降噪性价比高。", "入耳式主动降噪蓝牙耳机。",
			skuDef{"sku_seed_earbuds_white", "Blink Air 白色", "599", 5, map[string]string{"颜色": "白色"}}),
		mk("p_seed_mouse", DigitalMerchant, "c_mouse", "Blink 静音无线鼠标 M2", "Blink", "159", domain.ProductActive, 40,
			[]string{"静音", "无线", "办公"}, []string{"静音微动", "双模连接"},
			[]string{"办公", "宿舍", "图书馆"}, []string{"高强度电竞"}, []string{"不适合高强度电竞"},
			attr("连接", "2.4G + 蓝牙", "", "重量", "85", "g"),
			"安静办公首选。", "适合安静环境的无线鼠标。",
			skuDef{"sku_seed_mouse_gray", "Blink M2 深空灰", "129", 150, map[string]string{"颜色": "深空灰"}}),
		mk("p_seed_keyboard", DigitalMerchant, "c_keyboard", "Blink 机械键盘 K8", "Blink", "399", domain.ProductActive, 50,
			[]string{"机械键盘", "热插拔"}, []string{"热插拔轴体", "三模连接"},
			[]string{"程序员", "文字工作者"}, []string{"需要静音的环境"}, none,
			attr("轴体", "茶轴", "", "键数", "84", "键"),
			"手感扎实，目前缺货。", "84 键三模机械键盘（演示无货状态）。",
			skuDef{"sku_seed_keyboard_brown", "Blink K8 茶轴", "349", 0, map[string]string{"轴体": "茶轴"}}),
		mk("p_seed_lamp", HomeMerchant, "c_lamp", "Blink 护眼台灯 L1", "Blink Home", "299", domain.ProductActive, 60,
			[]string{"护眼", "学习"}, []string{"无频闪", "色温可调"},
			[]string{"学生", "夜间阅读"}, []string{}, none,
			attr("功率", "12", "W", "色温", "2700-6500", "K"),
			"无频闪、可调色温。", "适合学习和阅读的护眼台灯。",
			skuDef{"sku_seed_lamp_white", "Blink L1 白色", "249", 60, map[string]string{"颜色": "白色"}}),
		mk("p_seed_speaker", DigitalMerchant, "c_audio", "Blink 便携音箱 S1", "Blink", "299", domain.ProductInactive, 70,
			[]string{"便携"}, []string{"IPX7 防水"}, []string{"户外"}, []string{}, none, nil,
			"已下架。", "演示下架商品。",
			skuDef{"sku_seed_speaker_black", "Blink S1 黑色", "199", 40, map[string]string{"颜色": "黑色"}}),
		mk("p_seed_powerbank", DigitalMerchant, "c_digital", "Blink 快充移动电源", "Blink", "199", domain.ProductRisk, 80,
			[]string{"快充"}, []string{"20000mAh"}, []string{"出差"}, []string{}, []string{"风控审核中"}, nil,
			"风控审核中，暂停售卖。", "演示风控商品。",
			skuDef{"sku_seed_powerbank", "Blink 移动电源 20000mAh", "149", 100, map[string]string{"容量": "20000mAh"}}),
		mk("p_seed_legacy", DigitalMerchant, "c_phone", "Blink Nova 9（停产）", "Blink", "1999", domain.ProductDeleted, 90,
			[]string{"停产"}, []string{}, []string{}, []string{}, none, nil,
			"已删除。", "演示已删除商品，订单快照仍可引用。",
			skuDef{"sku_seed_legacy", "Blink Nova 9 6+128G", "1599", 0, map[string]string{"存储": "128GB"}}),
	}
}

func knowledge() ([]domain.KnowledgeDocument, []domain.KnowledgeChunk) {
	type chunk struct{ product, title, content string }
	type docDef struct {
		id, title, docType string
		chunks             []chunk
	}
	defs := []docDef{
		{"doc_seed_nova", "Blink Nova 12 商品说明", "product_detail", []chunk{
			{"p_seed_nova", "Nova 12 拍照", "Blink Nova 12 采用 5000 万像素主摄，支持人像和夜景模式，128GB 版本售价 2999 元。"},
			{"p_seed_nova", "Nova 12 续航", "Blink Nova 12 内置 5000mAh 电池，支持 33W 快充，正常使用可坚持一天半。"},
		}},
		{"doc_seed_after_sales", "Blink 数码售后政策", "policy", []chunk{
			{"", "七天无理由", "签收后 7 天内，商品完好可申请无理由退货，运费由买家承担。"},
			{"", "保修", "手机和耳机享受一年官方保修，人为损坏不在保修范围内。"},
		}},
		{"doc_seed_mouse", "Blink 静音鼠标 M2 使用指南", "product_detail", []chunk{
			{"p_seed_mouse", "M2 连接", "Blink M2 支持 2.4G 接收器和蓝牙双模，可在两台设备间一键切换。"},
		}},
	}
	var docs []domain.KnowledgeDocument
	var chunks []domain.KnowledgeChunk
	for _, def := range defs {
		content := ""
		for i, c := range def.chunks {
			content += c.content + "\n"
			chunks = append(chunks, domain.KnowledgeChunk{
				ChunkID: def.id + "_" + string(rune('a'+i)), DocumentID: def.id, MerchantID: DigitalMerchant, ProductID: c.product,
				ChunkIndex: i, Title: c.title, Content: c.content, Source: def.title, CreatedAt: base,
			})
		}
		docs = append(docs, domain.KnowledgeDocument{
			DocumentID: def.id, MerchantID: DigitalMerchant, Title: def.title, DocType: def.docType, Content: content,
			Status: domain.DocIndexed, ChunkCount: len(def.chunks), ContentHash: hashOf(content),
			Metadata: map[string]any{"seed": true}, CreatedAt: base, UpdatedAt: base,
		})
	}
	return docs, chunks
}

// orders 为 blink_user 生成覆盖每个订单状态的样例，金额按“下单时价格”冻结。
func orders(products []domain.Product, merchants []domain.Merchant) ([]domain.Order, []domain.Payment, []domain.UserCoupon, []domain.ProductReview) {
	productByID := map[string]domain.Product{}
	for _, p := range products {
		productByID[p.ProductID] = p
	}
	merchantName := map[string]string{}
	for _, m := range merchants {
		merchantName[m.MerchantID] = m.Name
	}

	type line struct {
		product, sku string
		qty          int
	}
	type orderDef struct {
		suffix   string
		status   domain.OrderStatus
		discount string
		created  int
		lines    []line
	}
	defs := []orderDef{
		{"pending", domain.OrderPendingPayment, "0", 0, []line{{"p_seed_mouse", "sku_seed_mouse_gray", 2}}},
		{"paid", domain.OrderPaid, "20", 24, []line{{"p_seed_earbuds", "sku_seed_earbuds_white", 1}}},
		{"shipped", domain.OrderShipped, "80", 48, []line{{"p_seed_nova", "sku_seed_nova_128", 1}}},
		{"completed", domain.OrderCompleted, "0", 72, []line{{"p_seed_mouse", "sku_seed_mouse_gray", 1}, {"p_seed_legacy", "sku_seed_legacy", 1}}},
		{"cancelled", domain.OrderCancelled, "0", 96, []line{{"p_seed_vista", "sku_seed_vista_256", 1}}},
	}

	var (
		orders   []domain.Order
		payments []domain.Payment
	)
	for i, def := range defs {
		created := at(def.created)
		o := domain.Order{
			OrderID: "o_seed_" + def.suffix, OrderNo: "BS20260920" + string(rune('1'+i)) + "0001", AccountID: UserID,
			MerchantID: DigitalMerchant, Status: def.status, DiscountAmount: domain.MustMoney(def.discount),
			PaymentDeadlineAt: ptr(created.Add(30 * time.Minute)), CreatedAt: created, UpdatedAt: created,
		}
		for j, l := range def.lines {
			p := productByID[l.product]
			var sku domain.ProductSKU
			for _, s := range p.SKUs {
				if s.SkuID == l.sku {
					sku = s
				}
			}
			o.Items = append(o.Items, domain.OrderItem{
				OrderItemID: o.OrderID + "_item" + string(rune('1'+j)), OrderID: o.OrderID, ProductID: p.ProductID, SkuID: sku.SkuID,
				Name: p.Name, SkuName: sku.SkuName, ImageURL: p.ImageURL, Price: sku.Price, Quantity: l.qty,
				MerchantID: p.MerchantID, MerchantName: merchantName[p.MerchantID], CreatedAt: created,
			})
			o.TotalAmount += sku.Price.Mul(l.qty)
		}
		o.PayAmount = o.TotalAmount - o.DiscountAmount

		pay := domain.Payment{
			PaymentID: "pay_seed_" + def.suffix, OrderID: o.OrderID, AccountID: UserID, Amount: o.PayAmount,
			Status: domain.PaymentPending, Method: "mock", ExpiresAt: created.Add(30 * time.Minute), CreatedAt: created, UpdatedAt: created,
		}
		switch def.status {
		case domain.OrderPaid, domain.OrderShipped, domain.OrderCompleted:
			paid := created.Add(5 * time.Minute)
			o.PaidAt, pay.PaidAt = ptr(paid), ptr(paid)
			pay.Status, pay.TransactionNo = domain.PaymentPaid, "MOCK"+o.OrderNo
			if def.status != domain.OrderPaid {
				o.ShippedAt = ptr(created.Add(6 * time.Hour))
			}
			if def.status == domain.OrderCompleted {
				o.CompletedAt = ptr(created.Add(20 * time.Hour))
			}
		case domain.OrderCancelled:
			o.ClosedAt, o.CancelReason = ptr(created.Add(10*time.Minute)), "不想要了"
			pay.Status = domain.PaymentClosed
		}
		o.UpdatedAt = created.Add(21 * time.Hour)
		orders = append(orders, o)
		payments = append(payments, pay)
	}

	userCoupons := []domain.UserCoupon{
		{UserCouponID: "uc_seed_user_platform_used", CouponID: "coupon_seed_platform", AccountID: UserID,
			Status: domain.UserCouponUsed, OrderID: "o_seed_paid", ClaimedAt: at(-24), UsedAt: ptr(at(24))},
		{UserCouponID: "uc_seed_user2_platform", CouponID: "coupon_seed_platform", AccountID: User2ID,
			Status: domain.UserCouponUnused, ClaimedAt: at(-24)},
	}

	completed := orders[3]
	reviews := []domain.ProductReview{
		{ReviewID: "rv_seed_mouse", OrderID: completed.OrderID, OrderItemID: completed.Items[0].OrderItemID,
			ProductID: completed.Items[0].ProductID, SkuID: completed.Items[0].SkuID, AccountID: UserID, Rating: 5,
			Content: "按键很安静，宿舍用正好。", Tags: []string{"静音", "手感好"}, Status: domain.ReviewVisible,
			MerchantReply: "感谢支持！", MerchantRepliedAt: ptr(at(100)), CreatedAt: at(96), UpdatedAt: at(100)},
	}
	return orders, payments, userCoupons, reviews
}
