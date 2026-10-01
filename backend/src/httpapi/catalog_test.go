package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// jsonKeys 返回 JSON 对象的键（排序后），用于锁定公开 DTO 的字段集合。
func jsonKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not an object: %s", raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sorted(keys ...string) []string {
	sort.Strings(keys)
	return keys
}

var (
	cardKeys = []string{"product_id", "sku_id", "merchant_id", "merchant_name", "category_id", "name", "brand", "image_url",
		"price", "market_price", "stock_status", "tags", "selling_points", "recommend_reason", "risk_notes"}
	detailKeys = append(append([]string{}, cardKeys...),
		"image_urls", "stock_quantity", "attributes", "suitable_for", "not_suitable_for", "description")
)

type rawPage struct {
	Items    []json.RawMessage `json:"items"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Total    int               `json:"total"`
}

func getPage(t *testing.T, ts *testServer, path string) rawPage {
	t.Helper()
	rec := ts.call(t, http.MethodGet, path, "", nil)
	expectStatus(t, rec, http.StatusOK, "")
	return decodeBody[rawPage](t, rec)
}

func itemIDs(t *testing.T, p rawPage, key string) []string {
	t.Helper()
	out := []string{}
	for _, raw := range p.Items {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		out = append(out, m[key].(string))
	}
	return out
}

func TestCategoryTree(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	rec := ts.call(t, http.MethodGet, "/api/v1/categories/tree", "", nil)
	expectStatus(t, rec, 200, "")
	got := decodeBody[categoryTreeResponse](t, rec)
	var shape []string
	for _, root := range got.Items {
		var kids []string
		for _, c := range root.Children {
			kids = append(kids, c.CategoryID)
			if c.Children == nil || len(c.Children) != 0 || c.ParentID != root.CategoryID {
				t.Errorf("leaf %s: %+v", c.CategoryID, c)
			}
		}
		shape = append(shape, root.CategoryID+":"+strings.Join(kids, ","))
	}
	want := []string{"c_digital:c_phone,c_audio", "c_office:c_mouse,c_keyboard", "c_home:c_lamp"}
	if !reflect.DeepEqual(shape, want) {
		t.Fatalf("tree = %v, want %v", shape, want)
	}
	if !strings.Contains(rec.Body.String(), `"children":[]`) {
		t.Error("叶子分类的 children 应为 []，不是 null 或缺省")
	}
}

func TestBuildCategoryTreeDropsOrphans(t *testing.T) {
	tree := buildCategoryTree([]domain.Category{
		{CategoryID: "a", Name: "A"}, {CategoryID: "x", ParentID: "missing"}, {CategoryID: "a1", ParentID: "a"},
	})
	if len(tree) != 1 || tree[0].CategoryID != "a" || len(tree[0].Children) != 1 {
		t.Fatalf("tree = %+v", tree)
	}
	if empty := buildCategoryTree(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty tree = %#v", empty)
	}
}

func TestMerchantsList(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	p := getPage(t, ts, "/api/v1/merchants")
	if p.Total != 2 || len(p.Items) != 2 || p.Page != 1 || p.PageSize != 10 {
		t.Fatalf("page = %+v", p)
	}
	if got, want := jsonKeys(t, p.Items[0]), sorted("merchant_id", "name", "logo_url", "description", "service_phone"); !reflect.DeepEqual(got, want) {
		t.Fatalf("merchant keys = %v", got)
	}
}

func TestProductListAndPaging(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	cases := []struct {
		query          string
		page, pageSize int
		ids            []string
	}{
		{"", 1, 10, []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}},
		{"?page=2&page_size=4", 2, 4, []string{"p_seed_keyboard", "p_seed_lamp"}},
		{"?page=3&page_size=4", 3, 4, []string{}},
		// 非法或越界的分页参数取默认值/边界值（与上游一致）。
		{"?page=0&page_size=0", 1, 1, []string{"p_seed_nova"}},
		{"?page=-3&page_size=-1", 1, 1, []string{"p_seed_nova"}},
		{"?page=abc&page_size=x", 1, 10, []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}},
		{"?page_size=1000", 1, 100, []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}},
		{"?page=999999999&page_size=10", maxPage, 10, []string{}},
		{"?category_id=c_office", 1, 10, []string{"p_seed_mouse", "p_seed_keyboard"}},
		{"?keyword=%E8%80%B3%E6%9C%BA", 1, 10, []string{"p_seed_earbuds"}}, // 耳机
		{"?keyword=blink&category_id=c_phone&page_size=1", 1, 1, []string{"p_seed_nova"}},
		{"?keyword=%E4%B8%8D%E5%AD%98%E5%9C%A8%E7%9A%84", 1, 10, []string{}}, // 不存在的
		{"?keyword=%20%20&category_id=", 1, 10, []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			p := getPage(t, ts, "/api/v1/products"+c.query)
			if got := itemIDs(t, p, "product_id"); !reflect.DeepEqual(got, c.ids) {
				t.Fatalf("ids = %v, want %v", got, c.ids)
			}
			if p.Page != c.page || p.PageSize != c.pageSize {
				t.Fatalf("page/page_size = %d/%d, want %d/%d", p.Page, p.PageSize, c.page, c.pageSize)
			}
			if p.Items == nil {
				t.Fatal("items 应为 []")
			}
		})
	}
	totals := map[string]int{"": 6, "?category_id=c_office&page_size=1": 2, "?keyword=zzzz": 0}
	for q, want := range totals {
		if got := getPage(t, ts, "/api/v1/products"+q).Total; got != want {
			t.Errorf("%s total = %d, want %d", q, got, want)
		}
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products?keyword="+strings.Repeat("a", 65), "", nil), 400, "invalid_argument")
}

func TestProductCardContract(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	p := getPage(t, ts, "/api/v1/products?keyword=nova")
	if len(p.Items) != 1 {
		t.Fatalf("items = %d", len(p.Items))
	}
	if got := jsonKeys(t, p.Items[0]); !reflect.DeepEqual(got, sorted(cardKeys...)) {
		t.Fatalf("card keys = %v\nwant %v", got, sorted(cardKeys...))
	}
	card := decodeJSONAs[productCard](t, p.Items[0])
	if card.SkuID != "sku_seed_nova_128" || card.MerchantName != "Blink 数码旗舰店" || card.Price.String() != "2999.00" ||
		card.StockStatus != domain.StockInStock || card.RiskNotes == nil {
		t.Fatalf("card = %+v", card)
	}
}

func decodeJSONAs[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestProductDetail(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	rec := ts.call(t, http.MethodGet, "/api/v1/products/p_seed_earbuds", "", nil)
	expectStatus(t, rec, 200, "")
	if got := jsonKeys(t, rec.Body.Bytes()); !reflect.DeepEqual(got, sorted(detailKeys...)) {
		t.Fatalf("detail keys = %v", got)
	}
	d := decodeBody[productDetail](t, rec)
	if d.StockStatus != domain.StockLow || d.StockQuantity != 5 || len(d.Attributes) != 2 || d.Attributes[1].Unit != "" ||
		!reflect.DeepEqual(d.RiskNotes, []string{"不支持游泳佩戴"}) || len(d.ImageURLs) != 2 {
		t.Fatalf("detail = %+v", d)
	}
	// 无货但上架的商品可以看详情。
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/p_seed_keyboard", "", nil), 200, "")
	for _, id := range []string{"p_seed_speaker", "p_seed_powerbank", "p_seed_legacy", "p_missing"} {
		for _, suffix := range []string{"", "/skus", "/reviews"} {
			expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/products/"+id+suffix, "", nil), 404, "product_not_found")
		}
		expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/promotions?product_id="+id, "", nil), 404, "product_not_found")
	}
}

// TestEmptyArraysAreStable：商品的可选列表字段为空时输出 []，不是 null。
func TestEmptyArraysAreStable(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	_, err := ts.mem.ApplySeed(context.Background(), store.SeedData{Products: []domain.Product{{
		ProductID: "p_bare", MerchantID: "m_seed_home", CategoryID: "c_lamp", Name: "光秃秃的商品",
		Price: domain.MustMoney("1"), Status: domain.ProductActive, StockStatus: domain.StockOutOfStock, SortOrder: 999,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := ts.call(t, http.MethodGet, "/api/v1/products/p_bare", "", nil)
	expectStatus(t, rec, 200, "")
	body := rec.Body.String()
	for _, field := range []string{"tags", "selling_points", "risk_notes", "image_urls", "attributes", "suitable_for", "not_suitable_for"} {
		if !strings.Contains(body, `"`+field+`":[]`) {
			t.Errorf("%s 应为 []: %s", field, body)
		}
	}
	if !strings.Contains(body, `"sku_id":""`) {
		t.Errorf("没有 SKU 时 sku_id 应为空串: %s", body)
	}
	skus := getPage(t, ts, "/api/v1/products/p_bare/skus")
	if skus.Total != 0 || skus.Items == nil {
		t.Fatalf("skus = %+v", skus)
	}
}

func TestProductSKUs(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	p := getPage(t, ts, "/api/v1/products/p_seed_nova/skus")
	if p.Total != 2 || !reflect.DeepEqual(itemIDs(t, p, "sku_id"), []string{"sku_seed_nova_128", "sku_seed_nova_256"}) {
		t.Fatalf("skus = %+v", p)
	}
	want := sorted("sku_id", "product_id", "sku_name", "price", "stock_quantity", "stock_status", "specs", "is_default")
	if got := jsonKeys(t, p.Items[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("sku keys = %v", got)
	}
	sku := decodeJSONAs[skuView](t, p.Items[0])
	if !sku.IsDefault || sku.Specs["存储"] != "128GB" || sku.Price.String() != "2999.00" {
		t.Fatalf("sku = %+v", sku)
	}
	second := getPage(t, ts, "/api/v1/products/p_seed_nova/skus?page=2&page_size=1")
	if second.Total != 2 || !reflect.DeepEqual(itemIDs(t, second, "sku_id"), []string{"sku_seed_nova_256"}) {
		t.Fatalf("page 2 = %+v", second)
	}
	if beyond := getPage(t, ts, "/api/v1/products/p_seed_nova/skus?page=5"); beyond.Total != 2 || len(beyond.Items) != 0 {
		t.Fatalf("beyond = %+v", beyond)
	}
}

func TestProductReviews(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	p := getPage(t, ts, "/api/v1/products/p_seed_mouse/reviews")
	if p.Total != 1 || len(p.Items) != 1 {
		t.Fatalf("reviews = %+v", p)
	}
	want := sorted("review_id", "product_id", "sku_id", "reviewer_name", "rating", "content", "tags", "merchant_reply",
		"merchant_replied_at", "created_at")
	if got := jsonKeys(t, p.Items[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("review keys = %v", got)
	}
	r := decodeJSONAs[reviewView](t, p.Items[0])
	if r.ReviewerName != "演***" || r.Rating != 5 || r.MerchantReply == "" || r.MerchantRepliedAt == nil {
		t.Fatalf("review = %+v", r)
	}
	// 公开评价不暴露评价人的账户 ID、用户名或订单。
	for _, leak := range []string{"acct_seed_user", "blink_user", "o_seed_", "oi_seed_"} {
		if strings.Contains(string(p.Items[0]), leak) {
			t.Errorf("review leaks %q", leak)
		}
	}
	if none := getPage(t, ts, "/api/v1/products/p_seed_lamp/reviews"); none.Total != 0 || none.Items == nil {
		t.Fatalf("no reviews = %+v", none)
	}
}

func TestMaskName(t *testing.T) {
	for in, want := range map[string]string{"演示用户": "演***", "A": "A***", "": "匿名用户", "  ": "匿名用户", "Bob": "B***"} {
		if got := maskName(in); got != want {
			t.Errorf("maskName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromotions(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	all := getPage(t, ts, "/api/v1/promotions")
	// 已结束的暑期活动不出现。
	if got := itemIDs(t, all, "promotion_id"); !reflect.DeepEqual(got, []string{"promo_seed_platform", "promo_seed_earbuds", "promo_seed_digital"}) {
		t.Fatalf("promotions = %v", got)
	}
	want := sorted("promotion_id", "name", "scope", "merchant_id", "product_id", "category_id", "type", "threshold_amount",
		"discount_amount", "discount_rate", "stackable", "start_at", "end_at")
	if got := jsonKeys(t, all.Items[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("promotion keys = %v", got)
	}
	earbuds := getPage(t, ts, "/api/v1/promotions?product_id=p_seed_earbuds")
	if got := itemIDs(t, earbuds, "promotion_id"); !reflect.DeepEqual(got, []string{"promo_seed_platform", "promo_seed_earbuds", "promo_seed_digital"}) {
		t.Fatalf("earbuds promotions = %v", got)
	}
	lamp := getPage(t, ts, "/api/v1/promotions?product_id=p_seed_lamp")
	if got := itemIDs(t, lamp, "promotion_id"); !reflect.DeepEqual(got, []string{"promo_seed_platform"}) {
		t.Fatalf("lamp promotions = %v", got)
	}
	// 服务端时间移到活动结束后，列表为空。
	ts.now = func() time.Time { return time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC) }
	if later := getPage(t, ts, "/api/v1/promotions"); later.Total != 0 {
		t.Fatalf("after end: %+v", later)
	}
}

func TestCatalogIsPublic(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	for _, path := range []string{"/api/v1/categories/tree", "/api/v1/merchants", "/api/v1/products", "/api/v1/products/p_seed_nova",
		"/api/v1/products/p_seed_nova/skus", "/api/v1/products/p_seed_nova/reviews", "/api/v1/promotions"} {
		expectStatus(t, ts.call(t, http.MethodGet, path, "", nil), 200, "")
		expectStatus(t, ts.call(t, http.MethodGet, path, "expired-token", nil), 200, "")
	}
	// 写方法不存在。
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/products", "", map[string]string{}), 405, "method_not_allowed")
}

func TestAssets(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	rec := ts.do(httptest.NewRequest(http.MethodGet, "/api/v1/assets/catalog/products/p_seed_nova.png", nil))
	expectStatus(t, rec, 200, "")
	if rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() < 1000 || !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Fatalf("asset: %v len %d", rec.Header(), rec.Body.Len())
	}
	for _, path := range []string{
		"/api/v1/assets/catalog",           // 目录不列出
		"/api/v1/assets/catalog/products/", // 目录不列出
		"/api/v1/assets/catalog/missing.png",
		"/api/v1/assets/embed.go", // 只内嵌了 catalog
	} {
		got := ts.do(httptest.NewRequest(http.MethodGet, path, nil))
		if got.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, got.Code)
		}
	}
}
