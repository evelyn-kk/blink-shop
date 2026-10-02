package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	avatarMaxBytes  = 2 << 20
	avatarURLPrefix = "/api/v1/uploads/avatar/"
)

var (
	ErrAvatarTooLarge = &APIError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "头像不能超过 2MB"}
	ErrAvatarType     = &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: "头像仅支持 JPG、PNG 或 WebP"}
	ErrAvatarMissing  = &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: "请选择头像文件"}
	ErrAvatarNotFound = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "头像不存在"}
)

// 头像文件名：<account_id>_<16 位 hex>.<ext>。账户 ID 是文件名前缀，用来校验头像归属。
var avatarNamePattern = regexp.MustCompile(`^acct_[A-Za-z0-9_]{1,64}_[0-9a-f]{16}\.(jpg|png|webp)$`)

var avatarTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}

// avatarDir 把头像存在本地目录（与上游一致）。3.1 接入对象存储后替换这里的实现，URL 形式不变。
// 所有文件操作经 os.Root 限定在 root 内，文件名即使绕过校验也无法逃出目录。
type avatarDir struct {
	root string
}

func (d avatarDir) dir() string {
	if d.root == "" {
		return filepath.Join("uploads", "avatar")
	}
	return d.root
}

func (d avatarDir) save(name string, data []byte) error {
	if err := os.MkdirAll(d.dir(), 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(d.dir())
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = root.Remove(name)
		return err
	}
	return f.Close()
}

func (d avatarDir) open(name string) (*os.File, error) {
	root, err := os.OpenRoot(d.dir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(name)
}

// ownedBy 判断 url 是否是该账户上传过且仍存在的头像。
func (d avatarDir) ownedBy(url, accountID string) bool {
	name, ok := strings.CutPrefix(url, avatarURLPrefix)
	if !ok || !avatarNamePattern.MatchString(name) || !strings.HasPrefix(name, accountID+"_") {
		return false
	}
	// 前缀之后必须正好是 “16 位 hex + 扩展名”，避免 acct_a 认领 acct_a_b 的文件。
	if rest := strings.TrimPrefix(name, accountID+"_"); len(rest) < 17 || rest[16] != '.' {
		return false
	}
	f, err := d.open(name)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

type avatarResponse struct {
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
	Size     int    `json:"size"`
}

// handleUploadAvatar 接收 multipart 字段 file；按内容嗅探类型，不信任客户端声明的 Content-Type 和扩展名。
// 只返回 URL，需要再调用 PATCH /account/profile 设为头像。
func (s *Server) handleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	data, err := readAvatarPart(r)
	if err != nil {
		writeError(w, err)
		return
	}
	mimeType := http.DetectContentType(data)
	ext, ok := avatarTypes[mimeType]
	if !ok {
		writeError(w, ErrAvatarType)
		return
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	name := acc.AccountID + "_" + hex.EncodeToString(b[:]) + ext
	if err := s.avatars.save(name, data); err != nil {
		s.logger.ErrorContext(r.Context(), "save avatar failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	s.audit(r, "account.avatar_uploaded", acc.AccountID, "file", name, "size", len(data))
	writeJSON(w, http.StatusOK, avatarResponse{URL: avatarURLPrefix + name, MimeType: mimeType, Size: len(data)})
}

// readAvatarPart 流式读取第一个名为 file 的分段，最多 2MB；不把整个表单读进内存或临时文件。
func readAvatarPart(r *http.Request) ([]byte, error) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "multipart/form-data" {
		return nil, &APIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "请求体必须是 multipart/form-data"}
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, invalidArgument("头像上传请求不合法")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, ErrAvatarMissing
		}
		if err != nil {
			return nil, mapUploadError(err)
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		return readLimited(part)
	}
}

func readLimited(part *multipart.Part) ([]byte, error) {
	defer part.Close()
	data, err := io.ReadAll(io.LimitReader(part, avatarMaxBytes+1))
	if err != nil {
		return nil, mapUploadError(err)
	}
	if len(data) > avatarMaxBytes {
		return nil, ErrAvatarTooLarge
	}
	if len(data) == 0 {
		return nil, ErrAvatarMissing
	}
	return data, nil
}

func mapUploadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return ErrPayloadTooLarge
	}
	return invalidArgument("头像上传请求不合法")
}

// handleGetAvatar 公开读取头像。文件名必须符合生成规则；响应禁止嗅探并带长缓存（文件名随机，内容不变）。
func (s *Server) handleGetAvatar(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !avatarNamePattern.MatchString(name) {
		writeError(w, ErrAvatarNotFound)
		return
	}
	f, err := s.avatars.open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.logger.ErrorContext(r.Context(), "open avatar failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		}
		writeError(w, ErrAvatarNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, ErrAvatarNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
