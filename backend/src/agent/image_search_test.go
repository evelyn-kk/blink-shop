package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/risk"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// imageEnv 在 env 的基础上接入图片搜索（本地特征 + 内存索引，已为在售商品建好索引）和内存对象存储。
type imageEnv struct {
	*env
	objects *objectstore.Memory
	svc     *imagesearch.Service
}

func newImageEnv(t *testing.T) *imageEnv {
	t.Helper()
	e := newEnv(t)
	objects := objectstore.NewMemory()
	svc := imagesearch.New(imagevector.Local{}, imagesearch.NewMemoryIndex(), imagesearch.NewSource(nil), e.mem, objects, nil)
	if _, err := svc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(e.logs, slog.LevelDebug)
	deps := Deps{Store: e.mem, Shop: e.shop, Retriever: rag.NewRetriever(e.mem, nil, logger), ImageSearch: svc,
		Risk: risk.WordList{Words: func(context.Context) []string { return e.words }}, Logger: logger, Now: e.runner.deps.Now}
	e.runner = NewRuleRunner(deps)
	e.reg = e.runner.Registry()
	return &imageEnv{env: e, objects: objects, svc: svc}
}

// upload 以 account 保存一个文件（对象 + 元数据），返回附件。
func (e *imageEnv) upload(t *testing.T, account, mime string, data []byte) domain.Attachment {
	t.Helper()
	key := "uploads/test/" + domain.NewID("obj")
	if err := e.objects.Put(context.Background(), key, bytes.NewReader(data), int64(len(data)), mime); err != nil {
		t.Fatal(err)
	}
	f, err := e.mem.CreateStoredFile(context.Background(), domain.StoredFile{AccountID: account, ObjectKey: key, MimeType: mime,
		SizeBytes: int64(len(data)), ContentHash: fmt.Sprintf("%x", sha256.Sum256(data)), StorageProvider: "memory", CreatedAt: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Attachment{FileID: f.FileID, MimeType: mime}
}

func photo(t *testing.T, slug string, transforms ...string) []byte {
	t.Helper()
	raw, err := fs.ReadFile(assets.FS, "catalog/products/"+slug+".png")
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := imagevector.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := imagevector.Transform(img, transforms, 3)
	if err != nil {
		t.Fatal(err)
	}
	data, err := imagevector.EncodePNG(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImageSearchFindsProductAndAllowsAddToCart(t *testing.T) {
	e := newImageEnv(t)
	sid := e.newSession(t, seed.UserID)
	att := e.upload(t, seed.UserID, "image/png", photo(t, "p_seed_mouse", "crop:0.85", "jpeg:70"))
	r := e.run(t, seed.UserID, sid, "帮我找一下同款", att)
	b := r.block(BlockProductList)
	if b == nil || b["title"] != "按图片找到的商品" || productIDs(b)[0] != "p_seed_mouse" || !strings.Contains(r.text.String(), "最像的是 Blink 静音无线鼠标 M2") {
		t.Fatalf("image search: %v %q", r.blockTypes(), r.text.String())
	}
	if calls := r.toolCalls(); len(calls) != 1 || calls[0] != ToolSearchImage {
		t.Fatalf("tools: %v", calls)
	}
	rt := r.traceOf("retrieval")
	if rt == nil || rt.Type != "images" || rt.Meta["status"] != imagesearch.StatusMatched || !strings.HasPrefix(rt.Meta["embedder"].(string), "local:") {
		t.Fatalf("retrieval trace: %+v", rt)
	}
	// 按图找到的商品进入可信集：下一轮“把第一个加入购物车”加的是图里的鼠标
	r = e.run(t, seed.UserID, sid, "把第一个加入购物车")
	if !strings.Contains(r.text.String(), "已把 Blink 静音无线鼠标 M2") {
		t.Fatalf("add after image search: %q", r.text.String())
	}
}

func TestImageSearchWithTextConditions(t *testing.T) {
	e := newImageEnv(t)
	sid := e.newSession(t, seed.UserID)
	// 价格条件把图里的手机（3299）过滤掉：说明原因，不推荐
	att := e.upload(t, seed.UserID, "image/png", photo(t, "p_seed_nova", "crop:0.85"))
	r := e.run(t, seed.UserID, sid, "找同款，500 以内", att)
	if r.block(BlockProductList) != nil || !strings.Contains(r.text.String(), "因为价格、品牌或排除条件没有列出") {
		t.Fatalf("price filter: %v %q", r.blockTypes(), r.text.String())
	}
	args := r.traceOf("tool").Meta["args"].(map[string]any)
	if args["max_price"] != 500.0 || args["file_id"] != att.FileID {
		t.Fatalf("tool args: %v", args)
	}
	// 排除词
	r = e.run(t, seed.UserID, sid, "这张图里的手机，不要 Nova", att)
	for _, id := range productIDs(r.block(BlockProductList)) {
		if id == "p_seed_nova" {
			t.Fatalf("excluded product returned: %q", r.text.String())
		}
	}
	// 文字条件和图片对不上（图是鼠标、说要耳机）：不当作推荐，只给外观相近的参考
	mouse := e.upload(t, seed.UserID, "image/png", photo(t, "p_seed_mouse", "crop:0.85"))
	r = e.run(t, seed.UserID, sid, "找一下这样的耳机", mouse)
	if b := r.block(BlockProductList); b != nil && b["title"] == "按图片找到的商品" && productIDs(b)[0] == "p_seed_mouse" {
		t.Fatalf("text mismatch recommended: %v", productIDs(b))
	}
}

func TestImageSearchGuards(t *testing.T) {
	e := newImageEnv(t)
	sid := e.newSession(t, seed.UserID)
	// 说了“拍照找”但没带图：请用户上传，不调用工具
	r := e.run(t, seed.UserID, sid, "拍照找同款")
	if len(r.toolCalls()) != 0 || !strings.Contains(r.text.String(), "请先上传一张商品图片") {
		t.Fatalf("ask upload: %v %q", r.toolCalls(), r.text.String())
	}
	// 附件不是图片
	pdf := e.upload(t, seed.UserID, "application/pdf", []byte("%PDF-1.4"))
	r = e.run(t, seed.UserID, sid, "找同款", pdf)
	if len(r.toolCalls()) != 0 || !strings.Contains(r.text.String(), "不是图片") {
		t.Fatalf("pdf: %v %q", r.toolCalls(), r.text.String())
	}
	// 图片损坏
	bad := e.upload(t, seed.UserID, "image/png", []byte("\x89PNG\r\n\x1a\nbroken"))
	r = e.run(t, seed.UserID, sid, "找同款", bad)
	if r.block(BlockProductList) != nil || !strings.Contains(r.text.String(), "这张图片无法识别") {
		t.Fatalf("broken image: %q", r.text.String())
	}
	// 别人的图片（绕过接口层校验直接塞进附件）：服务层按文件不存在处理，不返回商品
	other := e.upload(t, seed.User2ID, "image/png", photo(t, "p_seed_mouse"))
	r = e.run(t, seed.UserID, sid, "找同款", other)
	if r.block(BlockProductList) != nil {
		t.Fatalf("other user's file searched: %q", r.text.String())
	}
	// 工具只接受本轮附件里的图片
	tc := e.tc(seed.UserID, IntentImageSearch)
	tc.Images = map[string]bool{}
	mine := e.upload(t, seed.UserID, "image/png", photo(t, "p_seed_mouse"))
	e.mustFail(t, tc, ToolSearchImage, map[string]any{"file_id": mine.FileID}, CodeImageNotAttached)
	tc.Images[mine.FileID] = true
	res := e.mustOK(t, tc, ToolSearchImage, map[string]any{"file_id": mine.FileID}).(ImageSearchResult)
	if res.Relevance != RelevanceOK || res.Products[0].ProductID != "p_seed_mouse" {
		t.Fatalf("tool: %+v", res)
	}
	// 图搜意图不能用写工具
	e.mustFail(t, tc, ToolAddCartItem, map[string]any{"product_id": "p_seed_mouse"}, CodeToolNotAllowed)
}

func TestImageSearchHidesUnsellableProducts(t *testing.T) {
	e := newImageEnv(t)
	sid := e.newSession(t, seed.UserID)
	// 索引过期：已下架的音箱仍在索引里，结果里也不能出现
	speaker, err := e.mem.GetProduct(context.Background(), "p_seed_speaker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.IndexProduct(context.Background(), speaker); err != nil {
		t.Fatal(err)
	}
	att := e.upload(t, seed.UserID, "image/png", photo(t, "p_seed_speaker"))
	r := e.run(t, seed.UserID, sid, "找同款", att)
	for _, id := range productIDs(r.block(BlockProductList)) {
		if id == "p_seed_speaker" {
			t.Fatal("inactive product returned")
		}
	}
	if r.traceOf("retrieval").Meta["dropped"] == 0 {
		t.Fatalf("stale hit not counted: %+v", r.traceOf("retrieval").Meta)
	}
}

func TestImageSearchUnavailableFallsBackToText(t *testing.T) {
	e := newEnv(t) // 没有接入图片搜索
	sid := e.newSession(t, seed.UserID)
	img := domain.Attachment{FileID: "file_x", MimeType: "image/png"}
	r := e.run(t, seed.UserID, sid, "看看这张图", img)
	if !strings.Contains(r.text.String(), "图片搜索暂时不可用") || r.block(BlockProductList) != nil {
		t.Fatalf("unavailable: %q", r.text.String())
	}
	// 有文字条件时按文字搜
	r = e.run(t, seed.UserID, sid, "这张图里的降噪耳机", img)
	b := r.block(BlockProductList)
	if b == nil || productIDs(b)[0] != "p_seed_earbuds" || !strings.Contains(r.text.String(), "先按你的文字描述") {
		t.Fatalf("text fallback: %v %q", r.blockTypes(), r.text.String())
	}
}

func TestClassifyImage(t *testing.T) {
	cases := []struct {
		text     string
		img      bool
		action   string
		query    string
		budget   domain.Money
		excluded []string
	}{
		{"帮我找一下同款", true, "", "", 0, nil},
		{"这张图片里的耳机有没有类似的", true, "", "耳机", 0, nil},
		{"拍照找同款", false, "ask_upload", "", 0, nil},
		{"找同款 3000 以内 不要 Nova", true, "", "", 300000, []string{"nova"}},
	}
	for _, c := range cases {
		p := Classify(c.text, c.img)
		if p.Intent != IntentImageSearch || p.Action != c.action || p.Query != c.query || p.Budget != c.budget || strings.Join(lower(p.Exclude), ",") != strings.Join(c.excluded, ",") {
			t.Errorf("%q: %+v", c.text, p)
		}
	}
	// 模型把带图的商品需求判成 product_search：改回图搜；没图却判成图搜：请上传
	if p := imagePlan(Plan{Intent: IntentProductSearch, Query: "耳机"}, Classify("这样的耳机", true), true); p.Intent != IntentImageSearch {
		t.Errorf("image plan: %+v", p)
	}
	if p := imagePlan(Plan{Intent: IntentCart, Action: "view"}, Classify("看看购物车", true), true); p.Intent != IntentCart {
		t.Errorf("cart kept: %+v", p)
	}
	if p := imagePlan(Plan{Intent: IntentImageSearch}, Classify("找同款", false), false); p.Action != "ask_upload" {
		t.Errorf("ask upload: %+v", p)
	}
}

func lower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}
