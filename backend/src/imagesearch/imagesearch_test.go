package imagesearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

func TestSourceOnlyReadsAllowedImages(t *testing.T) {
	ctx := context.Background()
	s := NewSource([]string{"cdn.example.com", " IMG.Example.org "})
	if data, err := s.Load(ctx, "/api/v1/assets/catalog/products/p_seed_mouse.png"); err != nil || len(data) == 0 {
		t.Fatalf("asset: %v", err)
	}
	for _, u := range []string{
		"/api/v1/assets/../../etc/passwd",
		"/api/v1/assets/catalog/products/missing.png",
		"http://cdn.example.com/a.png",       // 不是 https
		"https://other.example.com/a.png",    // 不在白名单
		"https://cdn.example.com:8443/a.png", // 非默认端口
		"https://user:pw@cdn.example.com/a",  // 带凭证
		"https://cdn.example.com.evil.io/a",  // 后缀伪装
		"/api/v1/files/file_abc",             // 用户私有文件不作为商品图读取
		"file:///etc/passwd",
	} {
		if _, err := s.Load(ctx, u); err == nil {
			t.Errorf("%s: should be refused", u)
		}
	}
	if _, err := s.Load(ctx, "https://other.example.com/a.png"); !errors.Is(err, ErrSourceNotAllowed) {
		t.Errorf("not allowlisted: %v", err)
	}
	// 白名单域名被解析到回环 / 内网 / 元数据地址时，在建立连接前拒绝（DNS 重绑定）
	for addr, blocked := range map[string]bool{"127.0.0.1:443": true, "10.1.2.3:443": true, "192.168.0.8:443": true, "169.254.169.254:443": true,
		"[::1]:443": true, "100.64.1.1:443": true, "[::ffff:127.0.0.1]:443": true, "0.0.0.0:443": true, "93.184.216.34:443": false} {
		if err := denyPrivate("tcp", addr, nil); (err != nil) != blocked {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

type testEnv struct {
	mem     *memstore.Store
	objects *objectstore.Memory
	index   *MemoryIndex
	svc     *Service
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	mem := memstore.New()
	if _, err := mem.ApplySeed(context.Background(), storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	e := &testEnv{mem: mem, objects: objectstore.NewMemory(), index: NewMemoryIndex()}
	e.svc = New(imagevector.Local{}, e.index, NewSource(nil), mem, e.objects, nil)
	return e
}

func (e *testEnv) file(t *testing.T, account, mime string, data []byte) string {
	t.Helper()
	key := "uploads/t/" + domain.NewID("o")
	if err := e.objects.Put(context.Background(), key, bytes.NewReader(data), int64(len(data)), mime); err != nil {
		t.Fatal(err)
	}
	f, err := e.mem.CreateStoredFile(context.Background(), domain.StoredFile{AccountID: account, ObjectKey: key, MimeType: mime, SizeBytes: int64(len(data)),
		ContentHash: fmt.Sprintf("%x", sha256.Sum256(data)), StorageProvider: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	return f.FileID
}

func asset(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(assets.FS, "catalog/products/"+name+".png")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBootstrapAndSync(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	if New(nil, e.index, NewSource(nil), e.mem, nil, nil) != nil {
		t.Fatal("nil embedder should disable")
	}
	st, err := e.svc.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 只索引在售商品（6 件，每件 2 张图）；下架、风控、删除的不进索引
	if st.Products != 6 || st.Images != 12 || st.Failed != 0 || strings.Join(e.index.Products(), ",") != "p_seed_earbuds,p_seed_keyboard,p_seed_lamp,p_seed_mouse,p_seed_nova,p_seed_vista" {
		t.Fatalf("bootstrap: %+v %v", st, e.index.Products())
	}
	// 外部图片不在白名单：跳过，不下载
	if _, err := e.mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error {
		p.ImageURLs = []string{"https://cdn.example.com/m.png", "/api/v1/assets/catalog/products/p_seed_mouse-2.png"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if st, err := e.svc.Sync(ctx, "p_seed_mouse"); err != nil || st.Images != 2 || st.Skipped != 1 {
		t.Fatalf("sync: %+v %v", st, err)
	}
	// 下架后同步：从索引删除；不存在的商品同样删除
	if _, err := e.mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error { p.Status = domain.ProductInactive; return nil }); err != nil {
		t.Fatal(err)
	}
	if st, err := e.svc.Sync(ctx, "p_seed_mouse", "p_missing"); err != nil || st.Removed != 2 {
		t.Fatalf("sync removed: %+v %v", st, err)
	}
	for _, id := range e.index.Products() {
		if id == "p_seed_mouse" {
			t.Fatal("inactive product still indexed")
		}
	}
}

func TestSearchFile(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	if _, err := e.svc.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	mine := e.file(t, "acc_a", "image/png", asset(t, "p_seed_keyboard"))
	res, err := e.svc.SearchFile(ctx, "acc_a", mine, 3)
	if err != nil || res.Status != StatusMatched || res.Items[0].Product.ProductID != "p_seed_keyboard" || res.Items[0].Level != LevelMatch || len(res.Items) > 3 {
		t.Fatalf("search: %+v %v", res, err)
	}
	cases := map[string]struct {
		account, file string
		want          error
	}{
		"other account": {"acc_b", mine, ErrFileNotFound},
		"missing":       {"acc_a", "file_missing", ErrFileNotFound},
		"pdf":           {"acc_a", e.file(t, "acc_a", "application/pdf", []byte("%PDF-1.4")), ErrNotImage},
		"broken":        {"acc_a", e.file(t, "acc_a", "image/png", []byte("\x89PNG\r\n\x1a\nxx")), ErrInvalidImage},
	}
	for name, c := range cases {
		if _, err := e.svc.SearchFile(ctx, c.account, c.file, 3); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 对象丢了：按文件不存在；对象存储故障：ErrStorage；没有接对象存储：ErrStorage
	gone := e.file(t, "acc_a", "image/png", asset(t, "p_seed_mouse"))
	f, _ := e.mem.GetStoredFile(ctx, gone)
	_ = e.objects.Delete(ctx, f.ObjectKey)
	if _, err := e.svc.SearchFile(ctx, "acc_a", gone, 3); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("object gone: %v", err)
	}
	e.objects.SetFailGet(errors.New("down"))
	if _, err := e.svc.SearchFile(ctx, "acc_a", mine, 3); !errors.Is(err, ErrStorage) {
		t.Errorf("storage down: %v", err)
	}
	e.objects.SetFailGet(nil)
	noObjects := New(imagevector.Local{}, e.index, NewSource(nil), e.mem, nil, nil)
	if _, err := noObjects.SearchFile(ctx, "acc_a", mine, 3); !errors.Is(err, ErrStorage) {
		t.Errorf("no object store: %v", err)
	}
	// 索引故障
	broken := New(imagevector.Local{}, failingIndex{}, NewSource(nil), e.mem, e.objects, nil)
	if _, err := broken.SearchFile(ctx, "acc_a", mine, 3); !errors.Is(err, ErrIndex) {
		t.Errorf("index down: %v", err)
	}
	// 索引过期：已下架的商品命中了也不返回，计入 Dropped
	if _, err := e.mem.UpdateProduct(ctx, "p_seed_keyboard", func(p *domain.Product) error { p.Status = domain.ProductRisk; return nil }); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.SearchFile(ctx, "acc_a", mine, 3)
	if err != nil || res.Dropped == 0 {
		t.Fatalf("stale: %+v %v", res, err)
	}
	for _, m := range res.Items {
		if m.Product.ProductID == "p_seed_keyboard" {
			t.Fatal("risk product returned")
		}
	}
}

type failingIndex struct{}

func (failingIndex) ReplaceProduct(context.Context, string, []IndexedImage) error {
	return errors.New("down")
}
func (failingIndex) DeleteProducts(context.Context, []string) error { return errors.New("down") }
func (failingIndex) Search(context.Context, []float32, int) ([]Hit, error) {
	return nil, errors.New("down")
}

// replaceHook 在第一次写入后执行 fn（模拟写入期间商品又被改）。
type replaceHook struct {
	*MemoryIndex
	fn func()
}

func (h *replaceHook) ReplaceProduct(ctx context.Context, id string, images []IndexedImage) error {
	err := h.MemoryIndex.ReplaceProduct(ctx, id, images)
	if h.fn != nil {
		fn := h.fn
		h.fn = nil
		fn()
	}
	return err
}

// Sync 写完复查：写入期间商品换了图片或被下架，按最新状态重写 / 删除，不留旧图。
func TestSyncRechecksAfterWrite(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	hook := &replaceHook{MemoryIndex: e.index}
	svc := New(imagevector.Local{}, hook, NewSource(nil), e.mem, nil, nil)
	lamp := "/api/v1/assets/catalog/products/p_seed_lamp.png"
	hook.fn = func() {
		_, _ = e.mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error {
			p.ImageURL, p.ImageURLs = lamp, []string{lamp}
			return nil
		})
	}
	if _, err := svc.Sync(ctx, "p_seed_mouse"); err != nil {
		t.Fatal(err)
	}
	vec, _ := imagevector.Local{}.Embed(ctx, asset(t, "p_seed_lamp"))
	hits, _ := e.index.Search(ctx, vec, 10)
	var urls []string
	for _, h := range hits {
		if h.ProductID == "p_seed_mouse" {
			urls = append(urls, h.ImageURL)
		}
	}
	if len(urls) != 1 || urls[0] != lamp {
		t.Fatalf("after recheck: %v", urls)
	}
	hook.fn = func() {
		_, _ = e.mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error { p.Status = domain.ProductInactive; return nil })
	}
	if _, err := svc.Sync(ctx, "p_seed_mouse"); err != nil {
		t.Fatal(err)
	}
	for _, id := range e.index.Products() {
		if id == "p_seed_mouse" {
			t.Fatal("inactive product still indexed")
		}
	}
}

// 写入期间商品连续换了 6 次图（超过以前的 3 轮上限）后不再变：最终索引是最后一次的图。
func TestSyncKeepsLastChangeAfterManyRounds(t *testing.T) {
	old := RecheckDelay
	RecheckDelay = func(int) time.Duration { return 0 }
	t.Cleanup(func() { RecheckDelay = old })
	ctx := context.Background()
	e := newTestEnv(t)
	imgs := []string{"/api/v1/assets/catalog/products/p_seed_lamp.png", "/api/v1/assets/catalog/products/p_seed_keyboard.png"}
	hook := &replaceHook{MemoryIndex: e.index}
	svc := New(imagevector.Local{}, hook, NewSource(nil), e.mem, nil, nil)
	changes := 0
	var next func()
	next = func() {
		if changes == 6 {
			return
		}
		changes++
		u := imgs[changes%2]
		_, _ = e.mem.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error { p.ImageURL, p.ImageURLs = u, []string{u}; return nil })
		hook.fn = next
	}
	hook.fn = next
	if _, err := svc.Sync(ctx, "p_seed_mouse"); err != nil {
		t.Fatal(err)
	}
	want := imgs[6%2]
	hits, _ := e.index.Search(ctx, make([]float32, (imagevector.Local{}).Dim()), 100)
	var got []string
	for _, h := range hits {
		if h.ProductID == "p_seed_mouse" {
			got = append(got, h.ImageURL)
		}
	}
	if changes != 6 || len(got) != 1 || got[0] != want {
		t.Fatalf("changes=%d images=%v want %s", changes, got, want)
	}
}
