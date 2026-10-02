package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

const merchantProducts = "/api/v1/merchant/products"

// fullProduct 是一个完整的创建请求，测试按需改字段。
func fullProduct() map[string]any {
	return map[string]any{
		"name": "Blink 轻薄键盘 K3", "brand": "Blink", "category_id": "c_keyboard",
		"image_urls":     []string{"/api/v1/assets/catalog/products/p_seed_keyboard.png", "https://cdn.example.com/k3.jpg"},
		"market_price":   "299",
		"tags":           []string{"轻薄", "静音", " ", "轻薄"},
		"selling_points": []string{"矮轴", "三模"}, "risk_notes": []string{}, "suitable_for": []string{"办公"},
		"not_suitable_for": []string{"电竞"},
		"attributes":       []map[string]string{{"key": "键数", "value": "84", "unit": "键"}, {"key": "", "value": "", "unit": ""}},
		"recommend_reason": "安静轻薄", "description": "适合办公的轻薄键盘。", "status": "active",
		"skus": []map[string]any{
			{"sku_name": "K3 白色", "price": "199", "stock_quantity": 30, "specs": map[string]string{"颜色": "白"}},
			{"sku_name": "K3 黑色", "price": 219.5, "stock_quantity": 5, "specs": map[string]string{"颜色": "黑"}, "is_default": true},
		},
	}
}

func with(base map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == nil {
			delete(out, kv[i].(string))
			continue
		}
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func (ts *testServer) merchantToken(t *testing.T, username string) string {
	t.Helper()
	return ts.login(t, username, seed.DevPassword).Token
}

func createProduct(t *testing.T, ts *testServer, tok string, body any) merchantProductView {
	t.Helper()
	rec := ts.call(t, http.MethodPost, merchantProducts, tok, body)
	expectStatus(t, rec, http.StatusCreated, "")
	return decodeBody[merchantProductView](t, rec)
}

func TestCreateMerchantProduct(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	p := createProduct(t, ts, tok, fullProduct())

	if p.MerchantID != seed.DigitalMerchant || p.Status != domain.ProductActive || p.Price.String() != "219.50" ||
		p.MarketPrice.String() != "299.00" || p.StockQuantity != 35 || p.StockStatus != domain.StockInStock {
		t.Fatalf("product = %+v", p)
	}
	// 空白和重复的标签去掉，空参数行忽略，主图取第一张。
	if !reflect.DeepEqual(p.Tags, []string{"轻薄", "静音"}) || len(p.Attributes) != 1 ||
		p.ImageURL != "/api/v1/assets/catalog/products/p_seed_keyboard.png" || len(p.ImageURLs) != 2 {
		t.Fatalf("normalized fields = %+v", p)
	}
	if len(p.SKUs) != 2 || p.SKUs[0].SkuName != "K3 黑色" || !p.SKUs[0].IsDefault || p.SKUs[0].StockStatus != domain.StockLow {
		t.Fatalf("skus = %+v", p.SKUs)
	}
	// 立即出现在公开目录和详情中。
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/"+p.ProductID, "", nil), 200, "")
	if got := itemIDs(t, getPage(t, ts, "/api/v1/products?category_id=c_keyboard"), "product_id"); !reflect.DeepEqual(got, []string{"p_seed_keyboard", p.ProductID}) {
		t.Fatalf("public list = %v", got)
	}
	if !strings.Contains(ts.logs.String(), `"action":"product.created"`) {
		t.Error("missing audit log")
	}
	// merchant_id 可以是自己的店铺。
	createProduct(t, ts, tok, with(fullProduct(), "merchant_id", seed.DigitalMerchant))
}

// TestCreateSimpleProduct：兼容上游的写法（price + stock_quantity，无 skus），生成默认规格；stock_status 被忽略。
func TestCreateSimpleProduct(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	p := createProduct(t, ts, tok, map[string]any{
		"name": "简单商品", "category_id": "c_mouse", "price": "59.9", "stock_quantity": 0, "stock_status": "in_stock",
	})
	if len(p.SKUs) != 1 || p.SKUs[0].SkuName != "默认款" || p.Price.String() != "59.90" || p.MarketPrice != p.Price ||
		p.StockStatus != domain.StockOutOfStock || p.Brand != "" || p.Tags == nil || p.Attributes == nil || p.ImageURLs == nil {
		t.Fatalf("simple product = %+v", p)
	}
}

func TestCreateMerchantProductValidation(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	base := fullProduct()
	sku := func(kv ...any) []map[string]any {
		s := map[string]any{"sku_name": "规格", "price": "10", "stock_quantity": 1}
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(s, kv[i].(string))
			} else {
				s[kv[i].(string)] = kv[i+1]
			}
		}
		return []map[string]any{s}
	}
	cases := []struct {
		name  string
		body  any
		field string
	}{
		{"other merchant", with(base, "merchant_id", seed.HomeMerchant), "merchant_id"},
		{"missing name", with(base, "name", nil), "name"},
		{"blank name", with(base, "name", "   "), "name"},
		{"long name", with(base, "name", strings.Repeat("长", 129)), "name"},
		{"control char", with(base, "name", "a\u0000b"), "name"},
		{"missing category", with(base, "category_id", nil), "category_id"},
		{"unknown category", with(base, "category_id", "c_nope"), "category_id"},
		{"no price no skus", with(base, "skus", nil), "price"},
		{"both modes", with(base, "price", "10"), "skus"},
		{"empty skus", with(base, "skus", []any{}), "skus"},
		{"too many skus", with(base, "skus", func() []map[string]any {
			out := []map[string]any{}
			for i := 0; i < 21; i++ {
				out = append(out, sku()[0])
			}
			return out
		}()), "skus"},
		{"two defaults", with(base, "skus", append(sku("is_default", true), sku("is_default", true)...)), "skus"},
		{"sku name missing", with(base, "skus", sku("sku_name", nil)), "skus[0].sku_name"},
		{"sku price missing", with(base, "skus", sku("price", nil)), "skus[0].price"},
		{"sku price text", with(base, "skus", sku("price", "abc")), "skus[0].price"},
		{"sku price negative", with(base, "skus", sku("price", "-1")), "skus[0].price"},
		{"sku price zero", with(base, "skus", sku("price", 0)), "skus[0].price"},
		{"sku price 3 decimals", with(base, "skus", sku("price", "1.234")), "skus[0].price"},
		{"sku price too big", with(base, "skus", sku("price", "100000000")), "skus[0].price"},
		{"sku stock missing", with(base, "skus", sku("stock_quantity", nil)), "skus[0].stock_quantity"},
		{"sku stock negative", with(base, "skus", sku("stock_quantity", -1)), "skus[0].stock_quantity"},
		{"sku stock too big", with(base, "skus", sku("stock_quantity", 1000001)), "skus[0].stock_quantity"},
		{"sku stock not int", with(base, "skus", sku("stock_quantity", 1.5)), "skus.stock_quantity"},
		{"sku id on create", with(base, "skus", sku("sku_id", "sku_seed_mouse_gray")), "skus[0].sku_id"},
		{"too many specs", with(base, "skus", sku("specs", func() map[string]string {
			m := map[string]string{}
			for _, k := range "abcdefghijk" {
				m[string(k)] = "v"
			}
			return m
		}())), "skus[0].specs"},
		{"simple price invalid", with(base, "skus", nil, "price", "abc"), "price"},
		{"simple stock negative", with(base, "skus", nil, "price", "10", "stock_quantity", -5), "stock_quantity"},
		{"market below price", with(base, "market_price", "100"), "market_price"},
		{"market invalid", with(base, "market_price", "-1"), "market_price"},
		{"http image", with(base, "image_urls", []string{"http://example.com/a.jpg"}), "image_urls[0]"},
		{"javascript image", with(base, "image_url", "javascript:alert(1)"), "image_url"},
		{"data image", with(base, "image_url", "data:image/png;base64,AAAA"), "image_url"},
		{"userinfo image", with(base, "image_url", "https://user:pw@example.com/a.jpg"), "image_url"},
		{"missing asset", with(base, "image_url", "/api/v1/assets/catalog/products/nope.png"), "image_url"},
		{"asset dir", with(base, "image_url", "/api/v1/assets/catalog"), "image_url"},
		{"too many images", with(base, "image_urls", strings.Split(strings.Repeat("https://e.com/a.jpg,", 9)+"https://e.com/b.jpg,"+strings.Repeat("https://e.com/c.jpg,", 1)+"https://e.com/d.jpg,https://e.com/e.jpg,https://e.com/f.jpg,https://e.com/g.jpg,https://e.com/h.jpg,https://e.com/i.jpg,https://e.com/j.jpg", ",")), "image_urls"},
		{"too many tags", with(base, "tags", strings.Split("1,2,3,4,5,6,7,8,9,10,11", ",")), "tags"},
		{"long tag", with(base, "tags", []string{strings.Repeat("长", 17)}), "tags[0]"},
		{"attribute without value", with(base, "attributes", []map[string]string{{"key": "颜色"}}), "attributes[0]"},
		{"status deleted", with(base, "status", "deleted"), "status"},
		{"status risk", with(base, "status", "risk"), "status"},
		{"long description", with(base, "description", strings.Repeat("字", 5001)), "description"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := ts.call(t, http.MethodPost, merchantProducts, tok, c.body)
			expectStatus(t, rec, 400, "invalid_argument")
			if got := decodeError(t, rec); got.Field != c.field {
				t.Fatalf("field = %q, want %q (message %q)", got.Field, c.field, got.Message)
			}
		})
	}
	// 所有失败请求都没有写入商品。
	if total := getMerchantPage(t, ts, tok, "").Total; total != 7 {
		t.Fatalf("merchant products total = %d, want 7 (seed only)", total)
	}
	bad := httptest.NewRequest(http.MethodPost, merchantProducts, strings.NewReader(`{"name":`))
	bad.Header.Set("Content-Type", "application/json")
	bad.Header.Set("Authorization", "Bearer "+tok)
	expectStatus(t, ts.do(bad), 400, "invalid_json")
}

func getMerchantPage(t *testing.T, ts *testServer, tok, query string) rawPage {
	t.Helper()
	rec := ts.call(t, http.MethodGet, merchantProducts+query, tok, nil)
	expectStatus(t, rec, 200, "")
	return decodeBody[rawPage](t, rec)
}

func TestUpdateMerchantProduct(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	p := createProduct(t, ts, tok, fullProduct())
	path := merchantProducts + "/" + p.ProductID
	patch := func(body any) merchantProductView {
		t.Helper()
		rec := ts.call(t, http.MethodPatch, path, tok, body)
		expectStatus(t, rec, 200, "")
		return decodeBody[merchantProductView](t, rec)
	}

	// 只改名字：其他字段（参数、适用人群、规格）原样保留（上游会清空它们）。
	renamed := patch(map[string]any{"name": "改名后的键盘"})
	if renamed.Name != "改名后的键盘" || !reflect.DeepEqual(renamed.Attributes, p.Attributes) ||
		!reflect.DeepEqual(renamed.SuitableFor, p.SuitableFor) || !reflect.DeepEqual(renamed.SKUs, p.SKUs) {
		t.Fatalf("partial update lost fields: %+v", renamed)
	}
	// 下架：公开不可见；再上架：重新可见。
	if patch(map[string]any{"status": "inactive"}).Status != domain.ProductInactive {
		t.Fatal("not inactive")
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/"+p.ProductID, "", nil), 404, "product_not_found")
	patch(map[string]any{"status": "active"})
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/"+p.ProductID, "", nil), 200, "")

	// 替换规格：修改黑色（按 sku_id）、删掉白色、新增灰色并设为默认。
	black, white := p.SKUs[0], p.SKUs[1]
	updated := patch(map[string]any{"skus": []map[string]any{
		{"sku_id": black.SkuID, "sku_name": "K3 黑色 新版", "price": "209", "stock_quantity": 0},
		{"sku_name": "K3 灰色", "price": "229", "stock_quantity": 12, "is_default": true},
	}})
	if len(updated.SKUs) != 2 || updated.SKUs[0].SkuName != "K3 灰色" || updated.SKUs[1].SkuID != black.SkuID ||
		updated.Price.String() != "229.00" || updated.StockQuantity != 12 {
		t.Fatalf("sku update = %+v", updated)
	}
	for _, s := range updated.SKUs {
		if s.SkuID == white.SkuID {
			t.Fatal("removed SKU still present")
		}
	}
	// 只改价格：作用于默认规格。
	if got := patch(map[string]any{"price": "239", "stock_quantity": 3}); got.SKUs[0].Price.String() != "239.00" || got.StockQuantity != 3 {
		t.Fatalf("simple patch = %+v", got)
	}
	// 只改图片列表：主图跟随新列表。
	if got := patch(map[string]any{"image_urls": []string{"https://cdn.example.com/new.jpg"}}); got.ImageURL != "https://cdn.example.com/new.jpg" {
		t.Fatalf("image = %+v", got)
	}

	fails := []struct {
		name  string
		body  any
		field string
	}{
		{"market below price", map[string]any{"market_price": "100"}, "market_price"},
		{"foreign sku id", map[string]any{"skus": []map[string]any{{"sku_id": "sku_seed_mouse_gray", "sku_name": "x", "price": "1", "stock_quantity": 1}}}, "skus[0].sku_id"},
		{"duplicate sku id", map[string]any{"skus": []map[string]any{
			{"sku_id": black.SkuID, "sku_name": "x", "price": "300", "stock_quantity": 1},
			{"sku_id": black.SkuID, "sku_name": "y", "price": "300", "stock_quantity": 1}}}, "skus[1].sku_id"},
		{"blank name", map[string]any{"name": ""}, "name"},
		{"other merchant", map[string]any{"merchant_id": seed.HomeMerchant}, "merchant_id"},
	}
	before := ts.call(t, http.MethodGet, path, tok, nil).Body.String()
	for _, c := range fails {
		rec := ts.call(t, http.MethodPatch, path, tok, c.body)
		expectStatus(t, rec, 400, "invalid_argument")
		if got := decodeError(t, rec).Field; got != c.field {
			t.Errorf("%s: field = %q, want %q", c.name, got, c.field)
		}
	}
	if after := ts.call(t, http.MethodGet, path, tok, nil).Body.String(); after != before {
		t.Fatal("rejected PATCH changed the product")
	}
	// 空 PATCH 不改动任何东西。
	if got := patch(map[string]any{}); got.Name != "改名后的键盘" {
		t.Fatalf("empty patch = %+v", got)
	}
}

func TestRiskProductStatusLocked(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	path := merchantProducts + "/p_seed_powerbank"
	for _, st := range []string{"active", "inactive"} {
		expectStatus(t, ts.call(t, http.MethodPatch, path, tok, map[string]any{"status": st}), 409, "product_under_review")
	}
	// 风控中的商品可以改资料，状态保持 risk；带上相同状态不算修改。
	rec := ts.call(t, http.MethodPatch, path, tok, map[string]any{"description": "补充说明"})
	expectStatus(t, rec, 200, "")
	if got := decodeBody[merchantProductView](t, rec); got.Status != domain.ProductRisk || got.Description != "补充说明" {
		t.Fatalf("risk product = %+v", got)
	}
}

func TestMerchantProductOwnership(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	own := ts.merchantToken(t, seed.MerchantUsername)
	other := ts.merchantToken(t, seed.Merchant2Username)
	user := ts.merchantToken(t, seed.UserUsername)
	admin := ts.merchantToken(t, seed.AdminUsername)
	path := merchantProducts + "/p_seed_nova"
	before := ts.call(t, http.MethodGet, path, own, nil).Body.String()

	// 商家 B 读、改、删商家 A 的商品：403（与 RBAC 矩阵一致）。
	expectStatus(t, ts.call(t, http.MethodGet, path, other, nil), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodPatch, path, other, map[string]any{"name": "被篡改"}), 403, "forbidden")
	expectStatus(t, ts.call(t, http.MethodDelete, path, other, nil), 403, "forbidden")
	// 普通用户和管理员不能用商家接口（管理员的商品治理在 5.3 的 /admin/products）。
	for _, tok := range []string{user, admin} {
		expectStatus(t, ts.call(t, http.MethodPatch, path, tok, map[string]any{"name": "x"}), 403, "forbidden")
		expectStatus(t, ts.call(t, http.MethodPost, merchantProducts, tok, fullProduct()), 403, "forbidden")
	}
	expectStatus(t, ts.call(t, http.MethodGet, merchantProducts, "", nil), 401, "unauthorized")
	if after := ts.call(t, http.MethodGet, path, own, nil).Body.String(); after != before {
		t.Fatal("product changed by another merchant")
	}
	// 不存在和已删除：404。
	for _, id := range []string{"p_missing", "p_seed_legacy"} {
		expectStatus(t, ts.call(t, http.MethodGet, merchantProducts+"/"+id, own, nil), 404, "product_not_found")
		expectStatus(t, ts.call(t, http.MethodPatch, merchantProducts+"/"+id, own, map[string]any{"name": "x"}), 404, "product_not_found")
		expectStatus(t, ts.call(t, http.MethodDelete, merchantProducts+"/"+id, own, nil), 404, "product_not_found")
	}
	// 商家 B 的列表里没有商家 A 的商品。
	if got := itemIDs(t, getMerchantPage(t, ts, other, ""), "product_id"); !reflect.DeepEqual(got, []string{"p_seed_lamp"}) {
		t.Fatalf("merchant2 list = %v", got)
	}
}

func TestDeleteMerchantProduct(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	path := merchantProducts + "/p_seed_mouse"
	rec := ts.call(t, http.MethodDelete, path, tok, nil)
	expectStatus(t, rec, 200, "")
	if got := decodeBody[productDeletedResponse](t, rec); got.ProductID != "p_seed_mouse" || got.Status != domain.ProductDeleted {
		t.Fatalf("delete response = %+v", got)
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_seed_mouse", "", nil), 404, "product_not_found")
	if strings.Contains(ts.call(t, http.MethodGet, "/api/v1/products?keyword=mouse", "", nil).Body.String(), "p_seed_mouse") {
		t.Fatal("deleted product still in public search")
	}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		expectStatus(t, ts.call(t, method, path, tok, map[string]any{"name": "x"}), 404, "product_not_found")
	}
	if strings.Contains(strings.Join(itemIDs(t, getMerchantPage(t, ts, tok, ""), "product_id"), ","), "p_seed_mouse") {
		t.Fatal("deleted product still in merchant list")
	}
	// 数据仍在（软删），状态为 deleted。
	if p, err := ts.mem.GetProduct(t.Context(), "p_seed_mouse"); err != nil || p.Status != domain.ProductDeleted {
		t.Fatalf("stored = %+v, %v", p.Status, err)
	}
	if !strings.Contains(ts.logs.String(), `"action":"product.deleted"`) {
		t.Error("missing audit log")
	}
}

func TestListMerchantProducts(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	all := getMerchantPage(t, ts, tok, "")
	// 本店未删除的 7 个商品，含下架和风控。
	if all.Total != 7 || len(all.Items) != 7 {
		t.Fatalf("total = %d", all.Total)
	}
	want := sorted("product_id", "merchant_id", "category_id", "name", "brand", "image_url", "image_urls", "price", "market_price",
		"stock_quantity", "stock_status", "tags", "selling_points", "recommend_reason", "risk_notes", "attributes",
		"suitable_for", "not_suitable_for", "description", "status", "skus", "created_at", "updated_at")
	if got := jsonKeys(t, all.Items[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v", got)
	}
	if got := itemIDs(t, getMerchantPage(t, ts, tok, "?status=risk"), "product_id"); !reflect.DeepEqual(got, []string{"p_seed_powerbank"}) {
		t.Fatalf("risk = %v", got)
	}
	if got := getMerchantPage(t, ts, tok, "?keyword=Nova&page_size=1"); got.Total != 1 || got.PageSize != 1 {
		t.Fatalf("keyword = %+v", got)
	}
	expectStatus(t, ts.call(t, http.MethodGet, merchantProducts+"?status=deleted", tok, nil), 400, "invalid_argument")
	// 详情含全部规格。
	rec := ts.call(t, http.MethodGet, merchantProducts+"/p_seed_speaker", tok, nil)
	expectStatus(t, rec, 200, "")
	if got := decodeBody[merchantProductView](t, rec); got.Status != domain.ProductInactive || len(got.SKUs) != 1 {
		t.Fatalf("detail = %+v", got)
	}
}

// TestSingleLineFieldsRejectLineBreaks 覆盖 REV-006：单行字段拒绝换行、制表符、回车和 Unicode 行/段分隔符，
// 创建和修改都一样，且不写入；只有商品介绍允许多行。
func TestSingleLineFieldsRejectLineBreaks(t *testing.T) {
	// 用例会发出一百多个请求，放宽限流，避免被默认的每分钟 120 次拦下。
	ts := newTestServer(t, map[string]string{"RATE_LIMIT_IP_PER_MINUTE": "10000", "RATE_LIMIT_ACCOUNT_PER_MINUTE": "10000"}, nil, nil)
	tok := ts.merchantToken(t, seed.MerchantUsername)
	target := createProduct(t, ts, tok, fullProduct())
	path := merchantProducts + "/" + target.ProductID
	before := ts.call(t, http.MethodGet, path, tok, nil).Body.String()

	skuWith := func(name string, specs map[string]string) []map[string]any {
		return []map[string]any{{"sku_name": name, "price": "10", "stock_quantity": 1, "specs": specs}}
	}
	fields := []struct {
		field string
		body  func(bad string) map[string]any
	}{
		{"name", func(bad string) map[string]any { return map[string]any{"name": "键盘" + bad + "限时促销"} }},
		{"brand", func(bad string) map[string]any { return map[string]any{"brand": "品牌" + bad + "旗舰店"} }},
		{"skus[0].sku_name", func(bad string) map[string]any {
			return map[string]any{"skus": skuWith("白色"+bad+"标准版", nil)}
		}},
		{"skus[0].specs", func(bad string) map[string]any {
			return map[string]any{"skus": skuWith("白色", map[string]string{"颜色": "白" + bad + "黑"})}
		}},
		{"recommend_reason", func(bad string) map[string]any { return map[string]any{"recommend_reason": "好" + bad + "用"} }},
		{"tags[0]", func(bad string) map[string]any { return map[string]any{"tags": []string{"轻" + bad + "薄"}} }},
		{"selling_points[0]", func(bad string) map[string]any { return map[string]any{"selling_points": []string{"a" + bad + "b"}} }},
		{"suitable_for[0]", func(bad string) map[string]any { return map[string]any{"suitable_for": []string{"a" + bad + "b"}} }},
		{"attributes[0].key", func(bad string) map[string]any {
			return map[string]any{"attributes": []map[string]string{{"key": "键" + bad + "数", "value": "84"}}}
		}},
		{"attributes[0].value", func(bad string) map[string]any {
			return map[string]any{"attributes": []map[string]string{{"key": "键数", "value": "8" + bad + "4"}}}
		}},
	}
	breaks := map[string]string{`\n`: "\n", `\t`: "\t", `\r`: "\r", `U+2028`: " ", `U+2029`: " ", `\x00`: "\x00"}

	for _, f := range fields {
		for label, bad := range breaks {
			t.Run(f.field+" "+label, func(t *testing.T) {
				// POST：在完整请求上覆盖该字段。
				create := with(fullProduct())
				for k, v := range f.body(bad) {
					create[k] = v
				}
				rec := ts.call(t, http.MethodPost, merchantProducts, tok, create)
				expectStatus(t, rec, 400, "invalid_argument")
				if got := decodeError(t, rec).Field; got != f.field {
					t.Fatalf("POST field = %q, want %q", got, f.field)
				}
				// PATCH：只提交该字段。
				rec = ts.call(t, http.MethodPatch, path, tok, f.body(bad))
				expectStatus(t, rec, 400, "invalid_argument")
				if got := decodeError(t, rec).Field; got != f.field {
					t.Fatalf("PATCH field = %q, want %q", got, f.field)
				}
			})
		}
	}
	// 以上请求都没有写入：商品数量和目标商品都不变。
	if total := getMerchantPage(t, ts, tok, "").Total; total != 8 {
		t.Fatalf("merchant products total = %d, want 8", total)
	}
	if after := ts.call(t, http.MethodGet, path, tok, nil).Body.String(); after != before {
		t.Fatal("rejected PATCH changed the product")
	}

	// 商品介绍允许多行：保留换行和制表符，\r\n 统一为 \n；其他控制字符仍拒绝。
	rec := ts.call(t, http.MethodPatch, path, tok, map[string]any{"description": "第一行\r\n第二行\n\t缩进"})
	expectStatus(t, rec, 200, "")
	if got := decodeBody[merchantProductView](t, rec).Description; got != "第一行\n第二行\n\t缩进" {
		t.Fatalf("description = %q", got)
	}
	rec = ts.call(t, http.MethodPatch, path, tok, map[string]any{"description": "a\x00b"})
	expectStatus(t, rec, 400, "invalid_argument")
	if got := decodeError(t, rec).Field; got != "description" {
		t.Fatalf("description field = %q", got)
	}
	created := createProduct(t, ts, tok, with(fullProduct(), "description", "多行\n介绍"))
	if created.Description != "多行\n介绍" {
		t.Fatalf("created description = %q", created.Description)
	}
}
