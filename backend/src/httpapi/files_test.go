package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"regexp"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// filePart 描述一个 multipart 分段；客户端声明的文件名和类型都可以是假的。
type filePart struct {
	field, filename, contentType string
	data                         []byte
}

func multipartBody(t *testing.T, parts ...filePart) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		disp := fmt.Sprintf(`form-data; name=%q`, p.field)
		if p.filename != "" {
			disp += fmt.Sprintf(`; filename=%q`, p.filename)
		}
		h.Set("Content-Disposition", disp)
		if p.contentType != "" {
			h.Set("Content-Type", p.contentType)
		}
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(p.data)
	}
	_ = mw.Close()
	return &body, mw.FormDataContentType()
}

func fileUploadRequest(t *testing.T, token string, parts ...filePart) *http.Request {
	t.Helper()
	body, ct := multipartBody(t, parts...)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", body)
	req.Header.Set("Content-Type", ct)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func imageFile(data []byte) filePart {
	return filePart{field: "file", filename: "photo.png", contentType: "image/png", data: data}
}

func (ts *testServer) upload(t *testing.T, token string, data []byte) fileView {
	t.Helper()
	rec := ts.do(fileUploadRequest(t, token, imageFile(data)))
	expectStatus(t, rec, http.StatusCreated, "")
	return decodeBody[uploadFileResponse](t, rec).File
}

func (ts *testServer) getFile(token, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	return ts.do(req)
}

// fileCount 返回内存库中的文件记录数（直接看 Store 数据，不经过 API）。
func (ts *testServer) fileCount(t *testing.T, ids ...string) int {
	t.Helper()
	n := 0
	for _, id := range ids {
		if _, err := ts.mem.GetStoredFile(context.Background(), id); err == nil {
			n++
		}
	}
	return n
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	webpBytes = []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00" + strings.Repeat("\x00", 24))
	pdfBytes  = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")
	bmpBytes  = append([]byte("BM"), make([]byte, 60)...)
)

var objectKeyPattern = regexp.MustCompile(`^uploads/\d{4}/\d{2}/[0-9a-f]{32}\.(jpg|png|webp|gif|bmp|pdf)$`)

func TestFileUploadAndDownload(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	owner := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	img := pngBytes(t)
	sum := sha256.Sum256(img)

	// 恶意文件名和伪造的声明类型都不影响结果：类型来自内容，key 与文件名无关。
	rec := ts.do(fileUploadRequest(t, owner,
		filePart{field: "type", data: []byte("image")}, // 上游 Android 会先发一个 type 字段
		filePart{field: "file", filename: `../../etc/passwd<script>.php`, contentType: "text/html", data: img}))
	expectStatus(t, rec, http.StatusCreated, "")
	if strings.Contains(rec.Body.String(), "uploads/") || strings.Contains(rec.Body.String(), "object_key") {
		t.Fatalf("response leaks object key: %s", rec.Body)
	}
	up := decodeBody[uploadFileResponse](t, rec).File
	if !regexp.MustCompile(`^file_[0-9a-f]{24}$`).MatchString(up.FileID) || up.URL != fileURLPrefix+up.FileID ||
		up.MimeType != "image/png" || up.SizeBytes != int64(len(img)) || up.ContentHash != hex.EncodeToString(sum[:]) ||
		!up.CreatedAt.Equal(testNow) {
		t.Fatalf("upload response: %+v", up)
	}

	rowFile, err := ts.mem.GetStoredFile(context.Background(), up.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if rowFile.AccountID != seed.UserID || rowFile.StorageProvider != "minio" || !objectKeyPattern.MatchString(rowFile.ObjectKey) ||
		!strings.HasPrefix(rowFile.ObjectKey, "uploads/2026/10/") || strings.Contains(rowFile.ObjectKey, seed.UserID) {
		t.Fatalf("stored record: %+v", rowFile)
	}
	data, ct, ok := ts.objects.Object(rowFile.ObjectKey)
	if !ok || !bytes.Equal(data, img) || ct != "image/png" {
		t.Fatalf("stored object: ok=%v ct=%q len=%d", ok, ct, len(data))
	}
	if !strings.Contains(ts.logs.String(), `"action":"file.uploaded"`) || strings.Contains(ts.logs.String(), rowFile.ObjectKey) {
		t.Fatalf("audit log missing or leaks object key:\n%s", ts.logs)
	}

	// 上传者本人下载。
	get := ts.getFile(owner, up.URL)
	expectStatus(t, get, http.StatusOK, "")
	h := get.Header()
	if !bytes.Equal(get.Body.Bytes(), img) {
		t.Fatalf("download body differs")
	}
	for k, want := range map[string]string{
		"Content-Type":            "image/png",
		"Content-Length":          fmt.Sprint(len(img)),
		"Content-Disposition":     `inline; filename=` + up.FileID + ".png",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; sandbox",
		"Cache-Control":           "private, max-age=3600",
		"ETag":                    `"` + up.ContentHash + `"`,
	} {
		if got := h.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	// 条件请求命中时不再传输内容。
	notModified := ts.getFile(owner, up.URL, "If-None-Match", `W/"other", "`+up.ContentHash+`"`)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("If-None-Match: %d len=%d", notModified.Code, notModified.Body.Len())
	}
	head := ts.do(withToken(httptest.NewRequest(http.MethodHead, up.URL, nil), owner))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(img)) {
		t.Fatalf("HEAD: %d len=%d headers=%v", head.Code, head.Body.Len(), head.Header())
	}

	// 管理员可以读取任何文件；其他用户和商家不行；未登录 401。
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	if rec := ts.getFile(admin, up.URL); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), img) {
		t.Fatalf("admin download: %d", rec.Code)
	}
	expectStatus(t, ts.getFile(ts.login(t, seed.User2Username, seed.DevPassword).Token, up.URL), 403, "forbidden")
	expectStatus(t, ts.getFile(ts.login(t, seed.MerchantUsername, seed.DevPassword).Token, up.URL), 403, "forbidden")
	expectStatus(t, ts.getFile("", up.URL), 401, "unauthorized")
	// 越权请求即使带上正确的 ETag 也只能拿到 403，不能借 304 探测文件内容。
	other := ts.login(t, seed.User2Username, seed.DevPassword).Token
	expectStatus(t, ts.getFile(other, up.URL, "If-None-Match", `"`+up.ContentHash+`"`), 403, "forbidden")
}

func withToken(r *http.Request, token string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestFileTypesSniffedFromContent(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.MerchantUsername, seed.DevPassword).Token
	cases := []struct {
		name, mime, disposition string
		data                    []byte
	}{
		{"png", "image/png", "inline", pngBytes(t)},
		{"jpeg", "image/jpeg", "inline", jpegBytes(t)},
		{"gif", "image/gif", "inline", gifBytes(t)},
		{"webp", "image/webp", "inline", webpBytes},
		{"pdf", "application/pdf", "attachment", pdfBytes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 声明类型一律写成 application/octet-stream，文件名也不带扩展名。
			rec := ts.do(fileUploadRequest(t, tok, filePart{field: "file", filename: "blob", contentType: "application/octet-stream", data: c.data}))
			expectStatus(t, rec, http.StatusCreated, "")
			up := decodeBody[uploadFileResponse](t, rec).File
			if up.MimeType != c.mime {
				t.Fatalf("mime = %q, want %q", up.MimeType, c.mime)
			}
			get := ts.getFile(tok, up.URL)
			expectStatus(t, get, http.StatusOK, "")
			if get.Header().Get("Content-Type") != c.mime || !strings.HasPrefix(get.Header().Get("Content-Disposition"), c.disposition+";") {
				t.Fatalf("headers: %v", get.Header())
			}
		})
	}
}

func TestFileUploadRejected(t *testing.T) {
	ts := newTestServer(t, map[string]string{"UPLOAD_MAX_BYTES": "1024"}, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	img := pngBytes(t)
	atLimit := append(append([]byte{}, img...), make([]byte, 1024-len(img))...)
	overLimit := append(append([]byte{}, atLimit...), 0)
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")

	cases := []struct {
		name   string
		req    *http.Request
		status int
		code   string
		field  string
	}{
		{"text claimed as png", fileUploadRequest(t, tok, filePart{field: "file", filename: "a.png", contentType: "image/png", data: []byte("plain text, not an image")}), 400, "unsupported_file_type", "file"},
		{"html claimed as jpeg", fileUploadRequest(t, tok, filePart{field: "file", filename: "a.jpg", contentType: "image/jpeg", data: html}), 400, "unsupported_file_type", "file"},
		{"svg", fileUploadRequest(t, tok, filePart{field: "file", filename: "a.svg", contentType: "image/svg+xml", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)}), 400, "unsupported_file_type", "file"},
		{"bmp not in default allow list", fileUploadRequest(t, tok, filePart{field: "file", filename: "a.bmp", data: bmpBytes}), 400, "unsupported_file_type", "file"},
		{"zip", fileUploadRequest(t, tok, filePart{field: "file", filename: "a.zip", data: []byte("PK\x03\x04" + strings.Repeat("\x00", 40))}), 400, "unsupported_file_type", "file"},
		{"empty file", fileUploadRequest(t, tok, filePart{field: "file", filename: "a.png", data: nil}), 400, "invalid_argument", "file"},
		{"no file field", fileUploadRequest(t, tok, filePart{field: "image", filename: "a.png", data: img}), 400, "invalid_argument", "file"},
		{"no parts", fileUploadRequest(t, tok), 400, "invalid_argument", "file"},
		{"over size limit", fileUploadRequest(t, tok, imageFile(overLimit)), 413, "payload_too_large", "file"},
		{"json body", withToken(httptest.NewRequest(http.MethodPost, "/api/v1/files", strings.NewReader(`{"file":"x"}`)), tok), 415, "unsupported_media_type", ""},
		{"truncated multipart", truncatedMultipart(t, tok, img), 400, "invalid_argument", ""},
		{"anonymous", fileUploadRequest(t, "", imageFile(img)), 401, "unauthorized", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "json body" {
				c.req.Header.Set("Content-Type", "application/json")
			}
			rec := ts.do(c.req)
			expectStatus(t, rec, c.status, c.code)
			if got := decodeError(t, rec).Field; got != c.field {
				t.Fatalf("field = %q, want %q", got, c.field)
			}
		})
	}
	if keys := ts.objects.Keys(); len(keys) != 0 {
		t.Fatalf("rejected uploads left objects: %v", keys)
	}
	if strings.Contains(ts.logs.String(), `"action":"file.uploaded"`) {
		t.Fatal("rejected uploads were audited as uploaded")
	}

	// 正好等于上限可以上传。
	if up := ts.upload(t, tok, atLimit); up.SizeBytes != 1024 {
		t.Fatalf("at-limit upload size = %d", up.SizeBytes)
	}
}

// truncatedMultipart 构造缺少结束分隔符、文件内容被截断的请求体。
func truncatedMultipart(t *testing.T, token string, data []byte) *http.Request {
	body, ct := multipartBody(t, imageFile(data))
	cut := body.Bytes()[:body.Len()-20]
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", bytes.NewReader(cut))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestFileUploadAccountStatus(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	inactive := ts.tokenFor(t, ts.addAccount(t, "off_user", "h", domain.RoleUser, "", domain.StatusInactive))
	risk := ts.tokenFor(t, ts.addAccount(t, "risk_user", "h", domain.RoleUser, "", domain.StatusRisk))
	expectStatus(t, ts.do(fileUploadRequest(t, inactive, imageFile(pngBytes(t)))), 403, "account_inactive")
	expectStatus(t, ts.do(fileUploadRequest(t, risk, imageFile(pngBytes(t)))), 403, "account_risk")
	if keys := ts.objects.Keys(); len(keys) != 0 {
		t.Fatalf("objects written: %v", keys)
	}
}

func TestFileAllowedTypesConfigurable(t *testing.T) {
	ts := newTestServer(t, map[string]string{"UPLOAD_ALLOWED_MIME_TYPES": "image/png"}, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	rec := ts.do(fileUploadRequest(t, tok, imageFile(jpegBytes(t))))
	expectStatus(t, rec, 400, "unsupported_file_type")
	if msg := decodeError(t, rec).Message; !strings.Contains(msg, "PNG") || strings.Contains(msg, "JPG") {
		t.Fatalf("message = %q", msg)
	}
	ts.upload(t, tok, pngBytes(t))

	// 动态配置只在环境变量未设置时生效：放开 BMP 后无需重启。
	dyn := newTestServer(t, nil, nil, nil)
	tok = dyn.login(t, seed.UserUsername, seed.DevPassword).Token
	expectStatus(t, dyn.do(fileUploadRequest(t, tok, imageFile(bmpBytes))), 400, "unsupported_file_type")
	dyn.dynamic.Set("files.allowed_mime_types", "image/bmp,image/png")
	if up := dyn.upload(t, tok, bmpBytes); up.MimeType != "image/bmp" {
		t.Fatalf("bmp mime = %q", up.MimeType)
	}
	// 非法配置（不在可上传范围内）不会放开危险类型，而是退回默认白名单。
	dyn.dynamic.Set("files.allowed_mime_types", "text/html")
	expectStatus(t, dyn.do(fileUploadRequest(t, tok, filePart{field: "file", filename: "x.html", data: []byte("<html><body>x</body></html>")})), 400, "unsupported_file_type")
	dyn.upload(t, tok, pngBytes(t))
}

func TestFileObjectStorageUnavailable(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	up := ts.upload(t, tok, pngBytes(t))

	// 未配置对象存储：上传和下载都返回稳定错误码，不写任何记录。
	ts.objects = nil
	ts.Server.objects = nil
	rec := ts.do(fileUploadRequest(t, tok, imageFile(pngBytes(t))))
	expectStatus(t, rec, 503, "object_storage_unavailable")
	expectStatus(t, ts.getFile(tok, up.URL), 503, "object_storage_unavailable")
	// 归属检查仍然在存储检查之前：别人的文件仍是 403，不存在的仍是 404。
	expectStatus(t, ts.getFile(ts.login(t, seed.User2Username, seed.DevPassword).Token, up.URL), 403, "forbidden")
	expectStatus(t, ts.getFile(tok, fileURLPrefix+"file_missing"), 404, "file_not_found")
	if strings.Count(ts.logs.String(), `"action":"file.uploaded"`) != 1 {
		t.Fatal("unavailable storage still audited an upload")
	}
}

// failingFileStore 让文件元数据写入失败，其余操作照常。
type failingFileStore struct {
	store.Store
	created []domain.StoredFile
}

func (f *failingFileStore) CreateStoredFile(_ context.Context, in domain.StoredFile) (domain.StoredFile, error) {
	f.created = append(f.created, in)
	return domain.StoredFile{}, errors.New("db down")
}

func TestFileUploadFailureLeavesNothing(t *testing.T) {
	t.Run("object store put fails", func(t *testing.T) {
		ts := newTestServer(t, nil, nil, nil)
		tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
		ts.objects.SetFailPut(fmt.Errorf("%w: connection refused", objectstore.ErrUnavailable))
		calls := &failingFileStore{Store: ts.mem}
		ts.Server.store = calls // 记录是否尝试写元数据
		expectStatus(t, ts.do(fileUploadRequest(t, tok, imageFile(pngBytes(t)))), 503, "object_storage_unavailable")
		if len(calls.created) != 0 {
			t.Fatalf("metadata written after failed put: %+v", calls.created)
		}
		if keys := ts.objects.Keys(); len(keys) != 0 {
			t.Fatalf("objects: %v", keys)
		}
	})
	t.Run("metadata insert fails", func(t *testing.T) {
		ts := newTestServer(t, nil, nil, nil)
		tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
		failing := &failingFileStore{Store: ts.mem}
		ts.Server.store = failing
		rec := ts.do(fileUploadRequest(t, tok, imageFile(pngBytes(t))))
		expectStatus(t, rec, 500, "internal_error")
		if strings.Contains(rec.Body.String(), "db down") {
			t.Fatalf("internal error leaked: %s", rec.Body)
		}
		if len(failing.created) != 1 {
			t.Fatalf("insert attempts = %d", len(failing.created))
		}
		// 已写入的对象被补偿删除，存储里不留孤儿对象。
		if keys := ts.objects.Keys(); len(keys) != 0 {
			t.Fatalf("orphan objects: %v", keys)
		}
		if ts.fileCount(t, failing.created[0].FileID) != 0 {
			t.Fatal("file record exists")
		}
	})
}

func TestFileGetNotFound(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	tok := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	for _, path := range []string{
		fileURLPrefix + "file_0123456789abcdef01234567", // 合法格式但不存在
		fileURLPrefix + "abc",
		fileURLPrefix + "file_..%2Fsecret",
		fileURLPrefix + "file_a%00b",
		fileURLPrefix + "file_" + strings.Repeat("a", 70),
	} {
		rec := ts.getFile(tok, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
			continue
		}
		if code := decodeError(t, rec).Code; code != "file_not_found" && code != "not_found" {
			t.Errorf("GET %s code = %q", path, code)
		}
	}

	// 记录存在但对象丢失：404，不返回半截响应；对象存储读取失败：503。
	up := ts.upload(t, tok, pngBytes(t))
	row, _ := ts.mem.GetStoredFile(context.Background(), up.FileID)
	ts.objects.SetFailGet(fmt.Errorf("%w: timeout", objectstore.ErrUnavailable))
	rec := ts.getFile(tok, up.URL)
	expectStatus(t, rec, 503, "object_storage_unavailable")
	if rec.Header().Get("ETag") != "" {
		t.Fatal("error response carries ETag")
	}
	ts.objects.SetFailGet(nil)
	_ = ts.objects.Delete(context.Background(), row.ObjectKey)
	expectStatus(t, ts.getFile(tok, up.URL), 404, "file_not_found")
}
