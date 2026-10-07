package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const merchantPromotionsPath = "/api/v1/merchant/promotions"

func previewLines(t *testing.T, ts *testServer, tok string) map[string]string {
	t.Helper()
	rec := ts.call(t, http.MethodGet, cartPath+"/discount-preview?user_coupon_ids=", tok, nil)
	expectStatus(t, rec, http.StatusOK, "")
	out := map[string]string{}
	for _, l := range decodeBody[discountPreview](t, rec).Lines {
		out[l.ID] = l.Amount.String()
	}
	return out
}

// TestMerchantPromotionFlow：家居店创建全店满减 → 公开可见并参与购物车计价 → 修改金额 → 停用后不再参与；他店看不到也改不了。
func TestMerchantPromotionFlow(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	home := ts.login(t, seed.Merchant2Username, seed.DevPassword).Token
	digital := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
	user := ts.login(t, seed.User2Username, seed.DevPassword).Token
	ts.addToCart(t, user, map[string]any{"product_id": "p_seed_lamp"}) // 249，家居店

	rec := ts.call(t, http.MethodPost, merchantPromotionsPath, home, map[string]any{"name": "  家居满 200 减 25  ", "threshold_amount": "200", "discount_amount": 25})
	expectStatus(t, rec, http.StatusCreated, "")
	p := decodeBody[merchantPromotionView](t, rec)
	if p.PromotionID == "" || p.Name != "家居满 200 减 25" || p.Scope != "merchant" || p.MerchantID != seed.HomeMerchant || p.Type != "full_reduction" ||
		!p.Stackable || p.Status != "active" || p.Description != "满 200 减 25" || !p.StartAt.Equal(testNow) || !p.EndAt.Equal(testNow.AddDate(0, 0, 30)) {
		t.Fatalf("created = %+v", p)
	}
	if lines := previewLines(t, ts, user); lines[p.PromotionID] != "25.00" {
		t.Fatalf("promotion not applied: %v", lines)
	}
	rec = ts.call(t, http.MethodGet, "/api/v1/promotions?product_id=p_seed_lamp", "", nil)
	if !strings.Contains(rec.Body.String(), p.PromotionID) {
		t.Fatalf("public promotions = %s", rec.Body)
	}

	// 他店：列表看不到，修改 404。
	rec = ts.call(t, http.MethodGet, merchantPromotionsPath, digital, nil)
	if strings.Contains(rec.Body.String(), p.PromotionID) {
		t.Fatal("digital merchant sees home promotion")
	}
	expectStatus(t, ts.call(t, http.MethodPatch, merchantPromotionsPath+"/"+p.PromotionID, digital, map[string]any{"status": "inactive"}), http.StatusNotFound, "promotion_not_found")
	expectStatus(t, ts.call(t, http.MethodPatch, merchantPromotionsPath+"/promo_missing", home, map[string]any{"status": "inactive"}), http.StatusNotFound, "promotion_not_found")
	expectStatus(t, ts.call(t, http.MethodGet, merchantPromotionsPath+"/"+p.PromotionID, digital, nil), http.StatusNotFound, "promotion_not_found")
	rec = ts.call(t, http.MethodGet, merchantPromotionsPath+"/"+p.PromotionID, home, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[merchantPromotionView](t, rec); got.PromotionID != p.PromotionID || got.Description != p.Description {
		t.Fatalf("get = %+v", got)
	}

	// 修改金额（其他字段不变），再停用。
	rec = ts.call(t, http.MethodPatch, merchantPromotionsPath+"/"+p.PromotionID, home, map[string]any{"discount_amount": "30.00"})
	expectStatus(t, rec, http.StatusOK, "")
	if u := decodeBody[merchantPromotionView](t, rec); u.DiscountAmount.String() != "30.00" || u.Name != p.Name || u.ThresholdAmount.String() != "200.00" || !u.EndAt.Equal(p.EndAt) {
		t.Fatalf("updated = %+v", u)
	}
	if lines := previewLines(t, ts, user); lines[p.PromotionID] != "30.00" {
		t.Fatalf("updated amount not applied: %v", lines)
	}
	rec = ts.call(t, http.MethodPatch, merchantPromotionsPath+"/"+p.PromotionID, home, map[string]any{"status": "inactive"})
	expectStatus(t, rec, http.StatusOK, "")
	if lines := previewLines(t, ts, user); lines[p.PromotionID] != "" {
		t.Fatalf("inactive promotion still applied: %v", lines)
	}

	// 种子里的促销有效期接近两年（超过新建时的上限），停用、改名不受影响；改时间时才检查时长。
	expectStatus(t, ts.call(t, http.MethodPatch, merchantPromotionsPath+"/promo_seed_digital", digital, map[string]any{"status": "inactive", "name": "数码满减（暂停）"}), http.StatusOK, "")
	expectStatus(t, ts.call(t, http.MethodPatch, merchantPromotionsPath+"/promo_seed_digital", digital, map[string]any{"end_at": testNow.AddDate(2, 0, 0).Format(time.RFC3339)}), http.StatusBadRequest, "invalid_argument")

	// 列表与状态筛选。
	rec = ts.call(t, http.MethodGet, merchantPromotionsPath+"?status=inactive", home, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if list := decodeBody[pageResponse[merchantPromotionView]](t, rec); list.Total != 1 || list.Items[0].PromotionID != p.PromotionID || list.Items[0].Status != "inactive" {
		t.Fatalf("inactive list = %+v", list)
	}
	rec = ts.call(t, http.MethodGet, merchantPromotionsPath+"?status=active", home, nil)
	if list := decodeBody[pageResponse[merchantPromotionView]](t, rec); list.Total != 0 {
		t.Fatalf("active list = %+v", list)
	}
	expectStatus(t, ts.call(t, http.MethodGet, merchantPromotionsPath+"?status=deleted", home, nil), http.StatusBadRequest, "invalid_argument")
	rec = ts.call(t, http.MethodGet, merchantPromotionsPath, digital, nil)
	if list := decodeBody[pageResponse[merchantPromotionView]](t, rec); list.Total != 2 { // 种子：店铺满减 + 耳机折扣（单品）
		t.Fatalf("digital list = %+v", list)
	}
	for _, action := range []string{"promotion.created", "promotion.updated"} {
		if !strings.Contains(ts.logs.String(), `"action":"`+action+`"`) {
			t.Errorf("audit %s missing", action)
		}
	}
	// 用户、管理员不能用商家接口。
	expectStatus(t, ts.call(t, http.MethodGet, merchantPromotionsPath, user, nil), http.StatusForbidden, "forbidden")
}

func TestMerchantPromotionScopesAndValidation(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	digital := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
	create := func(body map[string]any) *merchantPromotionView {
		t.Helper()
		rec := ts.call(t, http.MethodPost, merchantPromotionsPath, digital, body)
		expectStatus(t, rec, http.StatusCreated, "")
		v := decodeBody[merchantPromotionView](t, rec)
		return &v
	}
	start, end := testNow.Add(time.Hour).Format(time.RFC3339), testNow.Add(48*time.Hour).Format(time.RFC3339)
	if v := create(map[string]any{"name": "鼠标立减", "scope": "product", "product_id": "p_seed_mouse", "discount_amount": "10", "stackable": false,
		"start_at": start, "end_at": end}); v.TargetName != "Blink 静音无线鼠标 M2" || v.Stackable || v.Description != "立减 10" || v.StartAt.Format(time.RFC3339) != start {
		t.Fatalf("product promotion = %+v", v)
	}
	if v := create(map[string]any{"name": "手机 9 折", "scope": "category", "category_id": "c_phone", "type": "discount", "discount_rate": "0.9"}); v.TargetName != "手机" ||
		v.Description != "9 折" || v.DiscountRate.String() != "0.9000" || v.DiscountAmount != 0 {
		t.Fatalf("category promotion = %+v", v)
	}

	base := map[string]any{"name": "促销", "discount_amount": "10"}
	with := func(kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	for _, c := range []struct {
		body  map[string]any
		field string
	}{
		{with("name", "  "), "name"},
		{with("name", strings.Repeat("长", 65)), "name"},
		{with("scope", "platform"), "scope"},
		{with("merchant_id", seed.HomeMerchant), "merchant_id"},
		{with("scope", "product"), "product_id"},
		{with("scope", "product", "product_id", "p_seed_lamp"), "product_id"},   // 他店商品
		{with("scope", "product", "product_id", "p_seed_legacy"), "product_id"}, // 已删除
		{with("scope", "category", "category_id", "c_missing"), "category_id"},
		{with("type", "percentage"), "type"},
		{with("discount_amount", "0"), "discount_amount"},
		{with("discount_amount", "-1"), "discount_amount"},
		{with("discount_amount", "1.234"), "discount_amount"},
		{with("threshold_amount", "5"), "discount_amount"}, // 减额超过门槛
		{with("type", "discount", "discount_rate", "1"), "discount_rate"},
		{with("type", "discount", "discount_rate", "0"), "discount_rate"},
		{with("type", "discount", "discount_rate", "abc"), "discount_rate"},
		{with("start_at", "2026-10-01"), "start_at"},
		{with("end_at", testNow.Add(-time.Hour).Format(time.RFC3339)), "end_at"},
		{with("start_at", testNow.Add(-48*time.Hour).Format(time.RFC3339), "end_at", testNow.Add(-time.Hour).Format(time.RFC3339)), "end_at"},
		{with("end_at", testNow.AddDate(0, 0, 400).Format(time.RFC3339)), "end_at"},
		{with("status", "deleted"), "status"},
	} {
		rec := ts.call(t, http.MethodPost, merchantPromotionsPath, digital, c.body)
		expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")
		if e := decodeError(t, rec); e.Field != c.field {
			t.Fatalf("%v: field = %q (%s)", c.body, e.Field, e.Message)
		}
	}
	rec := ts.call(t, http.MethodGet, merchantPromotionsPath, digital, nil)
	if list := decodeBody[pageResponse[merchantPromotionView]](t, rec); list.Total != 4 {
		t.Fatalf("invalid promotions written: total %d", list.Total)
	}

	// 修改时同样校验：把单品促销改成他店商品、把结束时间改到开始之前都被拒绝且不修改。
	list := decodeBody[pageResponse[merchantPromotionView]](t, rec)
	var mouse merchantPromotionView
	for _, p := range list.Items {
		if p.Name == "鼠标立减" {
			mouse = p
		}
	}
	expectStatus(t, ts.call(t, http.MethodPatch, merchantPromotionsPath+"/"+mouse.PromotionID, digital, map[string]any{"product_id": "p_seed_lamp"}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPatch, merchantPromotionsPath+"/"+mouse.PromotionID, digital, map[string]any{"end_at": start}), http.StatusBadRequest, "invalid_argument")
	// 改成全店满减：商品字段被清掉。
	rec = ts.call(t, http.MethodPatch, merchantPromotionsPath+"/"+mouse.PromotionID, digital, map[string]any{"scope": "merchant", "threshold_amount": "100"})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[merchantPromotionView](t, rec); v.Scope != "merchant" || v.ProductID != "" || v.TargetName != "" || v.Description != "满 100 减 10" {
		t.Fatalf("rescoped = %+v", v)
	}
}

func TestMerchantReviewsAndReply(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	digital := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
	home := ts.login(t, seed.Merchant2Username, seed.DevPassword).Token
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token

	// 用户评价已完成订单里的第二件商品，作为待回复的评价。
	rec := ts.call(t, http.MethodPost, ordersPath+"/o_seed_completed/items/o_seed_completed_item2:review", user, map[string]any{"rating": 3, "content": "电池一般"})
	expectStatus(t, rec, http.StatusCreated, "")
	reviewID := decodeBody[myReviewView](t, rec).ReviewID

	rec = ts.call(t, http.MethodGet, "/api/v1/merchant/reviews", digital, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), "account_id") || strings.Contains(rec.Body.String(), seed.UserID) {
		t.Fatalf("merchant review list leaks account id: %s", rec.Body)
	}
	list := decodeBody[pageResponse[merchantReviewView]](t, rec)
	if list.Total != 2 || list.Items[0].ReviewID != reviewID || list.Items[0].ProductName != "Blink Nova 9（停产）" || list.Items[0].ReviewerName != "演***" ||
		list.Items[0].MerchantReply != "" || list.Items[1].ReviewID != "rv_seed_mouse" || list.Items[1].MerchantReply == "" {
		t.Fatalf("list = %+v", list)
	}
	for query, want := range map[string]string{"?replied=false": reviewID, "?replied=true": "rv_seed_mouse"} {
		rec := ts.call(t, http.MethodGet, "/api/v1/merchant/reviews"+query, digital, nil)
		if l := decodeBody[pageResponse[merchantReviewView]](t, rec); l.Total != 1 || l.Items[0].ReviewID != want {
			t.Fatalf("%s = %+v", query, l)
		}
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/merchant/reviews?replied=maybe", digital, nil), http.StatusBadRequest, "invalid_argument")
	rec = ts.call(t, http.MethodGet, "/api/v1/merchant/reviews", home, nil)
	if l := decodeBody[pageResponse[merchantReviewView]](t, rec); l.Total != 0 {
		t.Fatalf("home merchant sees %d reviews", l.Total)
	}

	path := "/api/v1/merchant/reviews/" + reviewID + ":reply"
	for _, body := range []map[string]any{{"reply": "   "}, {"reply": strings.Repeat("谢", 501)}, {}} {
		rec := ts.call(t, http.MethodPost, path, digital, body)
		expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")
		if e := decodeError(t, rec); e.Field != "reply" {
			t.Fatalf("field = %q", e.Field)
		}
	}
	expectStatus(t, ts.call(t, http.MethodPost, path, home, map[string]any{"reply": "谢谢"}), http.StatusNotFound, "review_not_found")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/merchant/reviews/rv_missing:reply", digital, map[string]any{"reply": "谢谢"}), http.StatusNotFound, "review_not_found")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/merchant/reviews/"+reviewID+":delete", digital, map[string]any{"reply": "x"}), http.StatusNotFound, "not_found")

	rec = ts.call(t, http.MethodPost, path, digital, map[string]any{"reply": "  感谢反馈，已安排客服联系您  "})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[merchantReviewView](t, rec); v.MerchantReply != "感谢反馈，已安排客服联系您" || v.MerchantRepliedAt == nil || !v.MerchantRepliedAt.Equal(testNow) {
		t.Fatalf("reply = %+v", v)
	}
	rec = ts.call(t, http.MethodPost, path, digital, map[string]any{"reply": strings.Repeat("谢", 500)})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[merchantReviewView](t, rec); v.MerchantReply != strings.Repeat("谢", 500) {
		t.Fatalf("overwrite = %+v", v)
	}
	// 回复上架商品的评价后，公开评价里能看到新的回复。
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/merchant/reviews/rv_seed_mouse:reply", digital, map[string]any{"reply": "新回复"}), http.StatusOK, "")
	rec = ts.call(t, http.MethodGet, "/api/v1/products/p_seed_mouse/reviews", "", nil)
	expectStatus(t, rec, http.StatusOK, "")
	if public := decodeBody[pageResponse[reviewView]](t, rec); len(public.Items) == 0 || public.Items[0].MerchantReply != "新回复" {
		t.Fatalf("public reviews = %+v", public)
	}
	if !strings.Contains(ts.logs.String(), `"action":"review.replied"`) {
		t.Error("audit review.replied missing")
	}
}

// pausePoint 让请求停在某个位置：到达时关闭 reached，等 resume 关闭后继续。
type pausePoint struct{ reached, resume chan struct{} }

func newPausePoint() *pausePoint {
	return &pausePoint{reached: make(chan struct{}), resume: make(chan struct{})}
}

// promotionDeleteRaceStore 在单品促销锁住商品之后（afterLock），或删除商品已写入、事务还没提交时（inDelete）停住。
type promotionDeleteRaceStore struct {
	store.Store
	afterLock, inDelete *pausePoint
}

func (s *promotionDeleteRaceStore) LockProduct(ctx context.Context, productID string) (domain.Product, error) {
	p, err := s.Store.LockProduct(ctx, productID)
	if s.afterLock != nil {
		close(s.afterLock.reached)
		<-s.afterLock.resume
	}
	return p, err
}

func (s *promotionDeleteRaceStore) UpdateProduct(ctx context.Context, productID string, fn func(p *domain.Product) error) (domain.Product, error) {
	return s.Store.UpdateProduct(ctx, productID, func(p *domain.Product) error {
		if err := fn(p); err != nil || s.inDelete == nil || p.Status != domain.ProductDeleted {
			return err
		}
		close(s.inDelete.reached)
		<-s.inDelete.resume
		return nil
	})
}

// TestProductPromotionRacesWithProductDelete（REV-012）：创建或修改单品促销时，商家并发删除该商品。
// 促销先校验并锁住商品：删除必须等促销提交后才能进行；删除先拿到锁：促销等删除提交后读到 deleted，
// 返回 product_id 字段错误且不写入。内存和 MySQL 两个 Store 结果一致。
func TestProductPromotionRacesWithProductDelete(t *testing.T) {
	for name, newServer := range map[string]func(t *testing.T) *testServer{
		"memory": func(t *testing.T) *testServer { return newTestServer(t, nil, nil, nil) },
		"mysql": func(t *testing.T) *testServer {
			ts, _ := newMySQLTestServer(t, func() time.Time { return testNow })
			return ts
		},
	} {
		t.Run(name, func(t *testing.T) {
			ts := newServer(t)
			raced := &promotionDeleteRaceStore{Store: ts.store}
			ts.store = raced
			tok := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
			for _, c := range []struct {
				name                     string
				productID                string
				updating, promotionFirst bool
			}{
				{"create/promotion first", "p_seed_nova", false, true},
				{"create/delete first", "p_seed_vista", false, false},
				{"update/promotion first", "p_seed_keyboard", true, true},
				{"update/delete first", "p_seed_mouse", true, false},
			} {
				t.Run(c.name, func(t *testing.T) {
					runPromotionDeleteRace(t, ts, raced, tok, c.productID, c.updating, c.promotionFirst)
				})
			}
		})
	}
}

func runPromotionDeleteRace(t *testing.T, ts *testServer, raced *promotionDeleteRaceStore, tok, productID string, updating, promotionFirst bool) {
	method, path := http.MethodPost, merchantPromotionsPath
	body := map[string]any{"name": "单品立减", "scope": "product", "product_id": productID, "discount_amount": "10"}
	var promotionID string
	if updating {
		// 先建一条全店满减，再把它改成这个商品的单品促销。
		rec := ts.call(t, http.MethodPost, merchantPromotionsPath, tok, map[string]any{"name": "单品立减", "threshold_amount": "100", "discount_amount": "10"})
		expectStatus(t, rec, http.StatusCreated, "")
		promotionID = decodeBody[merchantPromotionView](t, rec).PromotionID
		method, path = http.MethodPatch, merchantPromotionsPath+"/"+promotionID
		body = map[string]any{"scope": "product", "product_id": productID}
	}
	async := func(method, path string, body any) <-chan *httptest.ResponseRecorder {
		out := make(chan *httptest.ResponseRecorder, 1)
		go func() { out <- ts.call(t, method, path, tok, body) }()
		return out
	}
	first := newPausePoint()
	// reach 等先发的请求停在暂停点；没经过暂停点就结束（例如没有锁商品）直接失败。
	reach := func(done <-chan *httptest.ResponseRecorder) {
		t.Helper()
		select {
		case <-first.reached:
		case rec := <-done:
			t.Fatalf("request finished without reaching the pause point: %d %s", rec.Code, rec.Body)
		case <-time.After(5 * time.Second):
			t.Fatal("pause point not reached")
		}
	}
	var promotionDone, deleteDone, firstDone, secondDone <-chan *httptest.ResponseRecorder
	if promotionFirst {
		raced.afterLock, raced.inDelete = first, nil
		promotionDone = async(method, path, body)
		reach(promotionDone)
		deleteDone = async(http.MethodDelete, merchantProducts+"/"+productID, nil)
		firstDone, secondDone = promotionDone, deleteDone
	} else {
		raced.afterLock, raced.inDelete = nil, first
		deleteDone = async(http.MethodDelete, merchantProducts+"/"+productID, nil)
		reach(deleteDone)
		promotionDone = async(method, path, body)
		firstDone, secondDone = deleteDone, promotionDone
	}
	// 后到的请求必须等先拿到锁的事务提交；失败时先放行，不能让暂停的事务一直占着锁。
	select {
	case rec := <-secondDone:
		close(first.resume)
		<-firstDone
		t.Fatalf("second request did not wait: %d %s", rec.Code, rec.Body)
	case <-time.After(300 * time.Millisecond):
	}
	close(first.resume)
	promotionRec, deleteRec := <-promotionDone, <-deleteDone
	expectStatus(t, deleteRec, http.StatusOK, "")

	if promotionFirst {
		want := http.StatusCreated
		if updating {
			want = http.StatusOK
		}
		expectStatus(t, promotionRec, want, "")
		if v := decodeBody[merchantPromotionView](t, promotionRec); v.Scope != "product" || v.ProductID != productID {
			t.Fatalf("promotion = %+v", v)
		}
		return
	}
	expectStatus(t, promotionRec, http.StatusBadRequest, "invalid_argument")
	if e := decodeError(t, promotionRec); e.Field != "product_id" {
		t.Fatalf("field = %q (%s)", e.Field, e.Message)
	}
	rec := ts.call(t, http.MethodGet, merchantPromotionsPath+"?page_size=100", tok, nil)
	for _, p := range decodeBody[pageResponse[merchantPromotionView]](t, rec).Items {
		if p.ProductID == productID {
			t.Fatalf("promotion written for deleted product: %+v", p)
		}
		if p.PromotionID == promotionID && p.Scope != "merchant" {
			t.Fatalf("promotion changed: %+v", p)
		}
	}
}
