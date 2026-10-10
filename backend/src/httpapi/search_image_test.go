package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// newTestImageSearch 用本地特征 + 内存索引为种子在售商品建好图片索引。
func newTestImageSearch(t *testing.T, ts *testServer) *imagesearch.Service {
	t.Helper()
	svc := imagesearch.New(imagevector.Local{}, imagesearch.NewMemoryIndex(), imagesearch.NewSource(nil), ts.mem, ts.objects, nil)
	if _, err := svc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	return svc
}

// productPhoto 把种子商品图按变换生成一张“用户拍的照片”（PNG）。
func productPhoto(t *testing.T, slug string, transforms ...string) []byte {
	t.Helper()
	raw, err := fs.ReadFile(assets.FS, "catalog/products/"+slug+".png")
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := imagevector.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := imagevector.Transform(img, transforms, 7)
	if err != nil {
		t.Fatal(err)
	}
	data, err := imagevector.EncodePNG(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSearchImage(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	ts.Server.imageSearch = newTestImageSearch(t, ts)
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	user2 := ts.login(t, seed.User2Username, seed.DevPassword).Token

	photo := ts.upload(t, user, productPhoto(t, "p_seed_mouse", "crop:0.85", "jpeg:70"))
	rec := ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID, "top_k": 3})
	expectStatus(t, rec, http.StatusOK, "")
	res := decodeBody[searchImageResponse](t, rec)
	if res.Status != imagesearch.StatusMatched || len(res.Items) == 0 || len(res.Items) > 3 || res.Items[0].Product.ProductID != "p_seed_mouse" ||
		res.Items[0].Level != imagesearch.LevelMatch || res.Items[0].MatchedImageURL == "" || res.Embedder == "" {
		t.Fatalf("search: %+v", res)
	}

	// 别人的文件和不存在的文件一样是 404；参数错误 400
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user2, map[string]any{"file_id": photo.FileID}), http.StatusNotFound, "file_not_found")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": "file_missing"}), http.StatusNotFound, "file_not_found")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": "../etc/passwd"}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID, "top_k": 0}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID, "top_k": 21}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", "", map[string]any{"file_id": photo.FileID}), http.StatusUnauthorized, "")

	// PDF 不是图片；头部是 PNG 但内容损坏的文件识别失败
	pdf := ts.upload(t, user, []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF"))
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": pdf.FileID}), http.StatusBadRequest, "unsupported_file_type")
	broken := productPhoto(t, "p_seed_mouse", "scale:0.2")
	bad := ts.upload(t, user, broken[:len(broken)/2])
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": bad.FileID}), http.StatusBadRequest, "invalid_image")

	// 纯色图片没有主体：200，没有结果
	solid, _ := imagevector.Synthetic("solid:#ffffff", 64, 1)
	solidPNG, _ := imagevector.EncodePNG(solid)
	plain := ts.upload(t, user, solidPNG)
	rec = ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": plain.FileID})
	expectStatus(t, rec, http.StatusOK, "")
	if res := decodeBody[searchImageResponse](t, rec); res.Status != imagesearch.StatusNoMatch || len(res.Items) != 0 {
		t.Fatalf("plain image: %+v", res)
	}

	// 下架的商品即使还在索引里（索引过期）也不返回
	if _, err := ts.mem.UpdateProduct(context.Background(), "p_seed_mouse", func(p *domain.Product) error {
		p.Status = domain.ProductInactive
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec = ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID})
	expectStatus(t, rec, http.StatusOK, "")
	for _, it := range decodeBody[searchImageResponse](t, rec).Items {
		if it.Product.ProductID == "p_seed_mouse" {
			t.Fatal("inactive product returned from a stale index")
		}
	}

	// 对象存储里文件丢了：404；对象存储不可用：503；图搜未配置：503
	ts.objects.SetFailGet(errors.New("storage down"))
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID}), http.StatusServiceUnavailable, "object_storage_unavailable")
	ts.objects.SetFailGet(nil)
	ts.Server.imageSearch = nil
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/search/image", user, map[string]any{"file_id": photo.FileID}), http.StatusServiceUnavailable, "image_search_unavailable")
}

// TestProductChangesResyncImageIndex：商家改商品、管理员下架后，商品图索引随之更新。
func TestProductChangesResyncImageIndex(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	idx := imagesearch.NewMemoryIndex()
	ts.Server.imageSearch = imagesearch.New(imagevector.Local{}, idx, imagesearch.NewSource(nil), ts.mem, ts.objects, nil)
	merchant := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token

	rec := ts.call(t, http.MethodPatch, "/api/v1/merchant/products/p_seed_mouse", merchant, map[string]any{"image_urls": []string{"/api/v1/assets/catalog/products/p_seed_mouse.png"}})
	expectStatus(t, rec, http.StatusOK, "")
	ts.WaitVectorSync()
	if got := idx.Products(); len(got) != 1 || got[0] != "p_seed_mouse" {
		t.Fatalf("after update: %v", got)
	}
	rec = ts.call(t, http.MethodPatch, "/api/v1/admin/products/p_seed_mouse", admin, map[string]any{"status": "risk", "reason": "测试"})
	expectStatus(t, rec, http.StatusOK, "")
	ts.WaitVectorSync()
	if got := idx.Products(); len(got) != 0 {
		t.Fatalf("after risk: %v", got)
	}
}

// TestAgentImageAttachment：聊天消息带上传的图片，导购按图找商品；附件只能是本人上传的文件。
func TestAgentImageAttachment(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	base := newHTTPServer(t, ts)
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	user2 := ts.login(t, seed.User2Username, seed.DevPassword).Token
	sid := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, user, map[string]any{})).SessionID
	photo := ts.upload(t, user, productPhoto(t, "p_seed_lamp", "crop:0.85"))
	body := func(id string) map[string]any {
		b := msgBody(id, "帮我找同款")
		b["attachments"] = []map[string]string{{"file_id": photo.FileID}}
		return b
	}

	// 没有配置图片搜索：如实说明
	s, _ := openStream(t, base, user, sid, body("img-1"))
	_, text, blocks := collect(t, s)
	if blockOf(blocks, "product_list") != nil || !strings.Contains(text, "图片搜索暂时不可用") {
		t.Fatalf("unavailable: %q %v", text, blocks)
	}
	// 配置后：按图片找到台灯
	ts.Server.imageSearch = newTestImageSearch(t, ts)
	s, _ = openStream(t, base, user, sid, body("img-2"))
	_, text, blocks = collect(t, s)
	b := blockOf(blocks, "product_list")
	if b == nil || b["title"] != "按图片找到的商品" || !strings.Contains(text, "Blink 护眼台灯 L1") {
		t.Fatalf("image answer: %q %v", text, blocks)
	}
	// 别人上传的文件不能作为附件
	sid2 := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, user2, map[string]any{})).SessionID
	rec := ts.call(t, http.MethodPost, agentSessionsPath+"/"+sid2+"/messages:stream", user2, map[string]any{"client_message_id": "x-1", "content": "找同款",
		"attachments": []map[string]string{{"file_id": photo.FileID}}})
	expectStatus(t, rec, http.StatusBadRequest, "invalid_argument")
}
