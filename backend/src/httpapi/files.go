package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const (
	fileURLPrefix       = "/api/v1/files/"
	fileStorageProvider = "minio"
	// multipartOverhead 是 multipart 请求体在文件内容之外的余量（分隔符、分段头、少量普通字段）。
	multipartOverhead = 64 << 10
	sniffLen          = 512
)

var (
	ErrFileMissing       = &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: "请选择要上传的文件", Field: "file"}
	ErrFileBadRequest    = &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: "文件上传请求不合法"}
	ErrFileNotFound      = &APIError{Status: http.StatusNotFound, Code: "file_not_found", Message: "文件不存在"}
	ErrFileForbidden     = &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "无权访问该文件"}
	ErrObjectStorageDown = &APIError{Status: http.StatusServiceUnavailable, Code: "object_storage_unavailable", Message: "文件存储暂不可用，请稍后再试"}
)

// fileIDPattern 只用于挡掉明显不合法的路径值；真正的存在性由数据库决定。
var fileIDPattern = regexp.MustCompile(`^file_[A-Za-z0-9_]{1,64}$`)

// 嗅探结果 → 对象 key 和下载文件名使用的扩展名。只包含 configcenter.UploadableTypes 中的类型。
var fileExtensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif", "image/bmp": ".bmp",
	"application/pdf": ".pdf",
}

type fileView struct {
	FileID      string    `json:"file_id"`
	URL         string    `json:"url"`
	MimeType    string    `json:"mime_type"`
	SizeBytes   int64     `json:"size_bytes"`
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type uploadFileResponse struct {
	File fileView `json:"file"`
}

func newFileView(f domain.StoredFile) fileView {
	return fileView{
		FileID: f.FileID, URL: fileURLPrefix + f.FileID, MimeType: f.MimeType,
		SizeBytes: f.SizeBytes, ContentHash: f.ContentHash, CreatedAt: f.CreatedAt,
	}
}

// handleUploadFile 接收 multipart 字段 file，流式写入临时文件并同时计算大小和 SHA-256；
// 类型按内容嗅探，客户端声明的 Content-Type 和文件名一律忽略。
// 顺序是“对象存储写入成功 → 写元数据”；元数据写入失败时删除刚写入的对象，不留下半条记录。
func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	if s.objects == nil {
		writeError(w, ErrObjectStorageDown)
		return
	}
	settings := settingsFromContext(r.Context())
	part, err := firstFilePart(r, ErrFileMissing, ErrFileBadRequest)
	if err != nil {
		writeError(w, err)
		return
	}
	spooled, err := spoolPart(part, settings.UploadMaxBytes)
	if err != nil {
		writeError(w, err)
		return
	}
	defer spooled.remove()

	if !containsString(settings.UploadAllowedTypes, spooled.mimeType) {
		writeError(w, &APIError{Status: http.StatusBadRequest, Code: "unsupported_file_type",
			Message: "不支持该文件类型，仅支持 " + describeTypes(settings.UploadAllowedTypes), Field: "file"})
		return
	}

	now := s.now()
	key := objectstore.NewObjectKey(now, fileExtensions[spooled.mimeType])
	if err := s.objects.Put(r.Context(), key, spooled.file, spooled.size, spooled.mimeType); err != nil {
		s.logger.ErrorContext(r.Context(), "put object failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		// 写入可能已部分完成（例如超时），尽力清理；对象没有元数据记录，客户端无法引用它。
		s.deleteObject(r.Context(), key)
		writeError(w, ErrObjectStorageDown)
		return
	}
	f, err := s.store.CreateStoredFile(r.Context(), domain.StoredFile{
		AccountID: acc.AccountID, ObjectKey: key, MimeType: spooled.mimeType, SizeBytes: spooled.size,
		ContentHash: spooled.hash, StorageProvider: fileStorageProvider, CreatedAt: now,
	})
	if err != nil {
		s.logger.ErrorContext(r.Context(), "save file record failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		s.deleteObject(r.Context(), key)
		writeError(w, ErrInternal)
		return
	}
	s.audit(r, "file.uploaded", acc.AccountID, "file_id", f.FileID, "mime_type", f.MimeType, "size", f.SizeBytes)
	writeJSON(w, http.StatusCreated, uploadFileResponse{File: newFileView(f)})
}

// deleteObject 清理孤儿对象。请求可能已被取消，因此不继承取消信号，但限定时间。
func (s *Server) deleteObject(ctx context.Context, key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.objects.Delete(ctx, key); err != nil {
		s.logger.WarnContext(ctx, "delete orphan object failed", "request_id", requestIDFromContext(ctx), "error", err)
	}
}

// handleGetFile 下载私有文件：只有上传者本人和管理员可以读取。
// 响应固定使用上传时嗅探出的类型，并禁止浏览器再次嗅探或执行其中的脚本。
func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id := r.PathValue("id")
	if !fileIDPattern.MatchString(id) {
		writeError(w, ErrFileNotFound)
		return
	}
	f, err := s.store.GetStoredFile(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrFileNotFound)
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "get file record failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	if f.AccountID != acc.AccountID && acc.Role != domain.RoleAdmin {
		writeError(w, ErrFileForbidden)
		return
	}
	if s.objects == nil {
		writeError(w, ErrObjectStorageDown)
		return
	}

	etag := `"` + f.ContentHash + `"`
	h := w.Header()
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	obj, err := s.objects.Get(r.Context(), f.ObjectKey)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			s.logger.WarnContext(r.Context(), "file object missing", "request_id", requestIDFromContext(r.Context()), "file_id", f.FileID)
			h.Del("ETag")
			writeError(w, ErrFileNotFound)
			return
		}
		s.logger.ErrorContext(r.Context(), "get object failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		h.Del("ETag")
		writeError(w, ErrObjectStorageDown)
		return
	}
	defer obj.Close()

	disposition := "attachment"
	if strings.HasPrefix(f.MimeType, "image/") {
		disposition = "inline"
	}
	h.Set("Content-Type", f.MimeType)
	h.Set("Content-Length", strconv.FormatInt(f.SizeBytes, 10))
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.FileID + fileExtensions[f.MimeType]}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, obj); err != nil {
		// 响应头已发出，只能记录；客户端会因长度不符发现截断。
		s.logger.WarnContext(r.Context(), "stream file failed", "request_id", requestIDFromContext(r.Context()), "file_id", f.FileID, "error", err)
	}
}

func etagMatches(header, etag string) bool {
	for _, item := range strings.Split(header, ",") {
		item = strings.TrimPrefix(strings.TrimSpace(item), "W/")
		if item == etag || item == "*" {
			return true
		}
	}
	return false
}

func describeTypes(types []string) string {
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, strings.ToUpper(strings.TrimPrefix(fileExtensions[t], ".")))
	}
	return strings.Join(names, "、")
}

// firstFilePart 返回第一个名为 file 的分段，跳过其他字段；不把整个表单读进内存或临时文件。
// missing 用于没有 file 分段，invalid 用于 multipart 格式错误；请求体超限统一返回 413。
func firstFilePart(r *http.Request, missing, invalid error) (*multipart.Part, error) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "multipart/form-data" {
		return nil, &APIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "请求体必须是 multipart/form-data"}
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, invalid
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, missing
		}
		if err != nil {
			return nil, mapMultipartError(err, invalid)
		}
		if part.FormName() == "file" {
			return part, nil
		}
		_ = part.Close()
	}
}

func mapMultipartError(err, invalid error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return ErrPayloadTooLarge
	}
	return invalid
}

// spooledFile 是写到临时文件的上传内容，读位置已回到开头。
type spooledFile struct {
	file     *os.File
	size     int64
	hash     string
	mimeType string
}

func (f *spooledFile) remove() {
	_ = f.file.Close()
	_ = os.Remove(f.file.Name())
}

// spoolPart 边读边写临时文件，同时计算 SHA-256 和大小，并用前 512 字节嗅探类型；超过 limit 立即停止。
func spoolPart(part *multipart.Part, limit int64) (*spooledFile, error) {
	defer part.Close()
	tmp, err := os.CreateTemp("", "blink-upload-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	out := &spooledFile{file: tmp}
	ok := false
	defer func() {
		if !ok {
			out.remove()
		}
	}()

	hasher := sha256.New()
	head := &headBuffer{max: sniffLen}
	src := &readErrRecorder{r: io.LimitReader(part, limit+1)}
	n, err := io.Copy(io.MultiWriter(tmp, hasher, head), src)
	if err != nil {
		if src.err != nil { // 读取请求体出错：超过请求体上限，或 multipart 格式不完整
			return nil, mapMultipartError(src.err, ErrFileBadRequest)
		}
		return nil, fmt.Errorf("spool upload: %w", err)
	}
	if n > limit {
		return nil, &APIError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large",
			Message: "文件不能超过 " + formatBytes(limit), Field: "file"}
	}
	if n == 0 {
		return nil, ErrFileMissing
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind temp file: %w", err)
	}
	out.size = n
	out.hash = hex.EncodeToString(hasher.Sum(nil))
	out.mimeType, _, _ = mime.ParseMediaType(http.DetectContentType(head.buf))
	ok = true
	return out, nil
}

// readErrRecorder 记下读取端的错误，用来区分“客户端请求体有问题”和“临时文件写入失败”。
type readErrRecorder struct {
	r   io.Reader
	err error
}

func (r *readErrRecorder) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

// headBuffer 只保留写入内容的前 max 字节。
type headBuffer struct {
	buf []byte
	max int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + "MB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + "KB"
	}
	return strconv.FormatInt(n, 10) + " 字节"
}
