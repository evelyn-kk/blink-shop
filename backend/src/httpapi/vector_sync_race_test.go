package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

const (
	mouseImg = "/api/v1/assets/catalog/products/p_seed_mouse.png"
	lampImg  = "/api/v1/assets/catalog/products/p_seed_lamp.png"
)

// gate 让第一个同步任务在“已读取快照、还没写索引”时停住：armed 后第一次写入发 entered 信号并等待 release。
type gate struct {
	mu       sync.Mutex
	armed    bool
	entered  chan struct{}
	release  chan struct{}
	inFlight int // 正在写入的任务数（检查同一商品没有并发写）
	maxSeen  int
}

func newGate() *gate { return &gate{entered: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) arm() {
	g.mu.Lock()
	g.armed = true
	g.mu.Unlock()
}

func (g *gate) pass() {
	g.mu.Lock()
	g.inFlight++
	g.maxSeen = max(g.maxSeen, g.inFlight)
	block := g.armed
	g.armed = false
	g.mu.Unlock()
	if block {
		close(g.entered)
		<-g.release
	}
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
}

type passer interface{ pass() }

// gatedTextIndex 是商品文本向量索引：记录每个商品最后写入的文本，写入前过 gate。
type gatedTextIndex struct {
	g    passer
	mu   sync.Mutex
	text map[string]string
}

func (x *gatedTextIndex) UpsertProducts(_ context.Context, items []rag.IndexedProduct) error {
	x.g.pass()
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, it := range items {
		x.text[it.ProductID] = it.Text
	}
	return nil
}

func (x *gatedTextIndex) DeleteProducts(_ context.Context, ids []string) error {
	x.g.pass()
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, id := range ids {
		delete(x.text, id)
	}
	return nil
}

func (x *gatedTextIndex) SearchProducts(context.Context, string, int) ([]rag.ProductHit, error) {
	return nil, nil
}

func (x *gatedTextIndex) get(id string) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	t, ok := x.text[id]
	return t, ok
}

// imageURLs 返回商品图索引里某个商品的图片地址（排序后）。
func imageURLs(t *testing.T, idx *imagesearch.MemoryIndex, id string) []string {
	t.Helper()
	hits, err := idx.Search(context.Background(), make([]float32, (imagevector.Local{}).Dim()), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range hits {
		if h.ProductID == id {
			out = append(out, h.ImageURL)
		}
	}
	sort.Strings(out)
	return out
}

type raceEnv struct {
	ts     *testServer
	g      *gate
	text   *gatedTextIndex
	images *imagesearch.MemoryIndex
	tok    string
	id     string
}

func newRaceEnv(t *testing.T) *raceEnv {
	t.Helper()
	ts := newTestServer(t, nil, nil, nil)
	g := newGate()
	e := &raceEnv{ts: ts, g: g, text: &gatedTextIndex{g: g, text: map[string]string{}}, images: imagesearch.NewMemoryIndex()}
	ts.productIndex = e.text
	ts.imageSearch = imagesearch.New(imagevector.Local{}, e.images, imagesearch.NewSource(nil), ts.mem, ts.objects, nil)
	e.tok = ts.merchantToken(t, seed.MerchantUsername)
	in := fullProduct()
	in["name"], in["image_urls"] = "Blink 键盘 初版", []string{mouseImg}
	e.id = createProduct(t, ts, e.tok, in).ProductID
	ts.WaitVectorSync()
	return e
}

func (e *raceEnv) patch(t *testing.T, name, img string) {
	t.Helper()
	expectStatus(t, e.ts.call(t, http.MethodPatch, merchantProducts+"/"+e.id, e.tok, map[string]any{"name": name, "image_url": img, "image_urls": []string{img}}), http.StatusOK, "")
}

// waitEntered 等第一个任务停在 gate 上。
func (e *raceEnv) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-e.g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first sync never reached the index write")
	}
}

// 第一个同步读到旧商品（名称“旧版”、鼠标图）后停住；期间商家改成“新版”、台灯图并触发第二次同步；
// 放开第一个后，文本向量和商品图向量最终都是第二次修改的内容，检索返回的 matched_image_url 也是新图。
func TestVectorSyncLatestWinsAfterReorderedUpdates(t *testing.T) {
	e := newRaceEnv(t)
	e.g.arm()
	e.patch(t, "Blink 键盘 旧版", mouseImg)
	e.waitEntered(t)
	e.patch(t, "Blink 键盘 新版", lampImg)
	// 第二次同步不能和第一个并发写：此时新内容还没进索引
	time.Sleep(100 * time.Millisecond)
	if txt, _ := e.text.get(e.id); containsAll(txt, "新版") {
		t.Fatal("second sync ran concurrently with the first")
	}
	close(e.g.release)
	e.ts.WaitVectorSync()

	if txt, _ := e.text.get(e.id); !containsAll(txt, "Blink 键盘 新版") || containsAll(txt, "旧版") {
		t.Fatalf("text index: %q", txt)
	}
	if got := imageURLs(t, e.images, e.id); len(got) != 1 || got[0] != lampImg {
		t.Fatalf("image index: %v", got)
	}
	if e.g.maxSeen != 1 {
		t.Fatalf("concurrent writers for one product: %d", e.g.maxSeen)
	}
	// 用台灯照片检索：这个商品的命中图是新图，不会返回旧的鼠标图
	user := e.ts.login(t, seed.UserUsername, seed.DevPassword).Token
	photo := e.ts.upload(t, user, productPhoto(t, "p_seed_lamp", "crop:0.9"))
	res := decodeBody[searchImageResponse](t, e.ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID, "top_k": 10}))
	found := false
	for _, it := range res.Items {
		if it.Product.ProductID == e.id {
			found = true
			if it.MatchedImageURL != lampImg {
				t.Fatalf("matched_image_url = %s", it.MatchedImageURL)
			}
		}
	}
	if !found {
		t.Fatalf("updated product not found by its new image: %+v", res.Items)
	}
}

// 修改没有经过本进程的同步入口（例如另一个 API 实例或 cmd/vectorindex），而且“别人”已经把新快照写进索引后，
// 旧任务才写完：写完复查发现内容变了，按最新状态重写，旧快照不会留下。
func TestVectorSyncRechecksAfterWrite(t *testing.T) {
	e := newRaceEnv(t)
	e.g.arm()
	e.patch(t, "Blink 键盘 旧版", mouseImg)
	e.waitEntered(t)
	ctx := context.Background()
	if _, err := e.ts.mem.UpdateProduct(ctx, e.id, func(p *domain.Product) error {
		p.Name, p.ImageURL, p.ImageURLs = "Blink 键盘 新版", lampImg, []string{lampImg}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 另一个写入者先写完了新快照
	cur, err := e.ts.store.GetVisibleProduct(ctx, e.id)
	if err != nil {
		t.Fatal(err)
	}
	e.text.mu.Lock()
	e.text.text[e.id] = rag.ProductText(cur, "键盘")
	e.text.mu.Unlock()
	if _, err := e.ts.imageSearch.IndexProduct(ctx, cur.Product); err != nil {
		t.Fatal(err)
	}
	close(e.g.release)
	e.ts.WaitVectorSync()
	if txt, _ := e.text.get(e.id); !containsAll(txt, "新版") || containsAll(txt, "旧版") {
		t.Fatalf("text index: %q", txt)
	}
	if got := imageURLs(t, e.images, e.id); len(got) != 1 || got[0] != lampImg {
		t.Fatalf("image index: %v", got)
	}
}

// 旧的“可见”任务落后于下架 / 删除：最终两个索引里都没有这个商品。
func TestVectorSyncStaleVisibleTaskAfterRemoval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(t *testing.T, e *raceEnv)
	}{
		{"delete", func(t *testing.T, e *raceEnv) {
			expectStatus(t, e.ts.call(t, http.MethodDelete, merchantProducts+"/"+e.id, e.tok, nil), http.StatusOK, "")
		}},
		{"admin risk", func(t *testing.T, e *raceEnv) {
			admin := e.ts.login(t, seed.AdminUsername, seed.DevPassword).Token
			expectStatus(t, e.ts.call(t, http.MethodPatch, "/api/v1/admin/products/"+e.id, admin, map[string]any{"status": "risk", "reason": "测试"}), http.StatusOK, "")
		}},
		{"store only", func(t *testing.T, e *raceEnv) {
			// 不经过同步入口（没有 dirty 标记），靠写完复查发现
			if _, err := e.ts.mem.UpdateProduct(context.Background(), e.id, func(p *domain.Product) error {
				p.Status = domain.ProductInactive
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRaceEnv(t)
			e.g.arm()
			e.patch(t, "Blink 键盘 旧版", mouseImg)
			e.waitEntered(t)
			tc.remove(t, e)
			close(e.g.release)
			e.ts.WaitVectorSync()
			if txt, ok := e.text.get(e.id); ok {
				t.Fatalf("text index still has removed product: %q", txt)
			}
			if got := imageURLs(t, e.images, e.id); len(got) != 0 {
				t.Fatalf("image index still has removed product: %v", got)
			}
		})
	}
}

// stepGate 让每一轮写入都停住，由测试逐轮放行（覆盖超过任意固定轮数的连续变更）。
type stepGate struct {
	mu      sync.Mutex
	on      bool
	entered chan struct{}
	release chan struct{}
}

func (g *stepGate) pass() {
	g.mu.Lock()
	on := g.on
	g.mu.Unlock()
	if on {
		g.entered <- struct{}{}
		<-g.release
	}
}

func (g *stepGate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sync round never reached the index write")
	}
}

// stop 关掉逐轮放行并放开当前停住的一轮。
func (g *stepGate) stop() {
	g.mu.Lock()
	g.on = false
	g.mu.Unlock()
	g.release <- struct{}{}
}

// 每轮写入都停住，连续 8 次修改（超过以前的 5 轮上限）后不再修改：最终索引是最后一次修改，而不是倒数一次；
// 最后一次是删除 / 下架时两个索引都没有这个商品。
func TestVectorSyncKeepsLastChangeAfterManyRounds(t *testing.T) {
	old := syncDelay
	syncDelay = func(int) time.Duration { return 0 }
	t.Cleanup(func() { syncDelay = old })
	const updates = 8
	for _, last := range []string{"update", "delete", "inactive"} {
		t.Run(last, func(t *testing.T) {
			e := newRaceEnv(t)
			g := &stepGate{on: true, entered: make(chan struct{}), release: make(chan struct{})}
			e.text.g = g
			img := func(k int) string {
				if k%2 == 0 {
					return lampImg
				}
				return mouseImg
			}
			e.patch(t, "Blink 键盘 第1版", img(1))
			for k := 2; k <= updates; k++ {
				g.waitEntered(t) // 第 k-1 轮读到第 k-1 版后停住
				if k == updates && last == "delete" {
					expectStatus(t, e.ts.call(t, http.MethodDelete, merchantProducts+"/"+e.id, e.tok, nil), http.StatusOK, "")
				} else if k == updates && last == "inactive" {
					expectStatus(t, e.ts.call(t, http.MethodPatch, merchantProducts+"/"+e.id, e.tok, map[string]any{"status": "inactive"}), http.StatusOK, "")
				} else {
					e.patch(t, fmt.Sprintf("Blink 键盘 第%d版", k), img(k))
				}
				g.release <- struct{}{}
			}
			g.waitEntered(t) // 按最后一次修改写入的那一轮
			g.stop()
			e.ts.WaitVectorSync()

			txt, ok := e.text.get(e.id)
			urls := imageURLs(t, e.images, e.id)
			if last != "update" {
				if ok || len(urls) != 0 {
					t.Fatalf("removed product still indexed: %q %v", txt, urls)
				}
				return
			}
			if !containsAll(txt, fmt.Sprintf("第%d版", updates)) {
				t.Fatalf("text index = %q, want 第%d版", txt, updates)
			}
			if len(urls) != 1 || urls[0] != img(updates) {
				t.Fatalf("image index = %v, want %s", urls, img(updates))
			}
			user := e.ts.login(t, seed.UserUsername, seed.DevPassword).Token
			photo := e.ts.upload(t, user, productPhoto(t, "p_seed_lamp", "crop:0.9"))
			res := decodeBody[searchImageResponse](t, e.ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID, "top_k": 10}))
			for _, it := range res.Items {
				if it.Product.ProductID == e.id && it.MatchedImageURL != img(updates) {
					t.Fatalf("matched_image_url = %s", it.MatchedImageURL)
				}
			}
		})
	}
}
