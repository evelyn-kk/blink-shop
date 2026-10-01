package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadRequest(t *testing.T, token, field string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("note", "ignored")
	if field != "" {
		fw, err := mw.CreateFormFile(field, "avatar.txt") // 扩展名故意写错：只看内容
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads/avatar", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestAvatarUploadAndUse(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	img := pngBytes(t)

	rec := ts.do(uploadRequest(t, tok, "file", img))
	expectStatus(t, rec, http.StatusOK, "")
	up := decodeBody[avatarResponse](t, rec)
	if !strings.HasPrefix(up.URL, avatarURLPrefix+seed.UserID+"_") || !strings.HasSuffix(up.URL, ".png") ||
		up.MimeType != "image/png" || up.Size != len(img) {
		t.Fatalf("upload response: %+v", up)
	}

	// 公开读取，不需要登录。
	get := ts.do(httptest.NewRequest(http.MethodGet, up.URL, nil))
	expectStatus(t, get, http.StatusOK, "")
	if !bytes.Equal(get.Body.Bytes(), img) || get.Header().Get("Content-Type") != "image/png" ||
		get.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET avatar: headers=%v len=%d", get.Header(), get.Body.Len())
	}

	// 自己上传的头像可以设为资料头像；别人用同一个 URL 不行。
	rec = ts.call(t, http.MethodPatch, "/api/v1/account/profile", tok, map[string]string{"avatar_url": up.URL})
	expectStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[domain.Account](t, rec); got.AvatarURL != up.URL {
		t.Fatalf("avatar_url = %q", got.AvatarURL)
	}
	other := ts.login(t, seed.User2Username, seed.DevPassword).Token
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/account/profile", other, map[string]string{"avatar_url": up.URL}), 400, "invalid_argument")
}

func TestAvatarUploadRejected(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	img := pngBytes(t)
	big := append(append([]byte{}, img...), bytes.Repeat([]byte{0}, avatarMaxBytes)...)

	expectStatus(t, ts.do(uploadRequest(t, "", "file", img)), 401, "unauthorized")
	expectStatus(t, ts.do(uploadRequest(t, tok, "file", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))), 400, "invalid_argument")
	expectStatus(t, ts.do(uploadRequest(t, tok, "file", []byte("plain text"))), 400, "invalid_argument")
	expectStatus(t, ts.do(uploadRequest(t, tok, "other", img)), 400, "invalid_argument")
	expectStatus(t, ts.do(uploadRequest(t, tok, "", nil)), 400, "invalid_argument")
	expectStatus(t, ts.do(uploadRequest(t, tok, "file", nil)), 400, "invalid_argument")
	expectStatus(t, ts.do(uploadRequest(t, tok, "file", big)), 413, "payload_too_large")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/uploads/avatar", tok, map[string]string{"file": "x"}), 415, "unsupported_media_type")

	entries, _ := os.ReadDir(ts.avatars.dir())
	if len(entries) != 0 {
		t.Fatalf("rejected uploads left %d files", len(entries))
	}
}

func TestAvatarGetRejectsBadNames(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	// 目录外放一个文件，确认无法通过路径穿越读到。
	secret := filepath.Join(filepath.Dir(ts.avatars.dir()), "secret.png")
	if err := os.WriteFile(secret, pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/uploads/avatar/acct_seed_user_0123456789abcdef.png", // 合法但不存在
		"/api/v1/uploads/avatar/..%2Fsecret.png",
		"/api/v1/uploads/avatar/secret.png",
		"/api/v1/uploads/avatar/acct_x_0123456789abcdef.svg",
		"/api/v1/uploads/avatar/acct_x_0123456789ABCDEF.png",
	} {
		rec := ts.do(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestAvatarOwnership(t *testing.T) {
	d := avatarDir{root: t.TempDir()}
	for _, name := range []string{"acct_a_0123456789abcdef.png", "acct_a_b_0123456789abcdef.png"} {
		if err := d.save(name, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		url, account string
		want         bool
	}{
		{avatarURLPrefix + "acct_a_0123456789abcdef.png", "acct_a", true},
		{avatarURLPrefix + "acct_a_b_0123456789abcdef.png", "acct_a_b", true},
		{avatarURLPrefix + "acct_a_b_0123456789abcdef.png", "acct_a", false}, // acct_a 不能认领 acct_a_b 的文件
		{avatarURLPrefix + "acct_a_0123456789abcdef.png", "acct_b", false},
		{"https://cdn.example" + avatarURLPrefix + "acct_a_0123456789abcdef.png", "acct_a", false},
		{avatarURLPrefix + "acct_a_fedcba9876543210.png", "acct_a", false}, // 不存在
	}
	for _, c := range cases {
		if got := d.ownedBy(c.url, c.account); got != c.want {
			t.Errorf("ownedBy(%q, %q) = %v, want %v", c.url, c.account, got, c.want)
		}
	}
	if err := d.save("acct_a_0123456789abcdef.png", []byte("y")); err == nil {
		t.Error("同名文件不应被覆盖")
	}
}
