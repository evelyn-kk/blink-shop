package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// APIError 是可以直接返回给客户端的错误：状态码、机器码和可展示的中文说明。
type APIError struct {
	Status  int
	Code    string
	Message string
	Field   string // 出错的请求字段（如 price、skus[1].sku_name），表单据此定位；可为空
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
}

// 通用错误；业务错误在各自模块中按同样方式定义。
var (
	ErrInvalidArgument = &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: "请求参数不正确"}
	ErrInvalidJSON     = &APIError{Status: http.StatusBadRequest, Code: "invalid_json", Message: "请求体不是合法的 JSON"}
	ErrUnsupportedType = &APIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "请求体必须是 application/json"}
	ErrUnauthorized    = &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "请先登录"}
	ErrForbidden       = &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "没有权限"}
	ErrNotFound        = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "接口不存在"}
	ErrMethodNotAllow  = &APIError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "不支持该请求方法"}
	ErrConflict        = &APIError{Status: http.StatusConflict, Code: "conflict", Message: "当前状态不允许该操作"}
	ErrPayloadTooLarge = &APIError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "请求体过大"}
	ErrRateLimited     = &APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "请求过于频繁，请稍后再试"}
	ErrInternal        = &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "服务暂时不可用"}
	ErrNotReady        = &APIError{Status: http.StatusServiceUnavailable, Code: "not_ready", Message: "服务尚未就绪"}
	ErrTimeout         = &APIError{Status: http.StatusGatewayTimeout, Code: "timeout", Message: "请求处理超时，请稍后重试"}
)

// ErrorResponse 是所有失败响应的统一结构，见 openapi.yaml#/components/schemas/Error。
type ErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Field     string `json:"field,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError 输出统一错误 JSON。非 APIError 一律按 500 处理，不向客户端暴露内部细节。
func writeError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = ErrInternal
	}
	writeJSON(w, apiErr.Status, ErrorResponse{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		Field:     apiErr.Field,
		RequestID: w.Header().Get(headerRequestID),
	})
}

// decodeJSON 读取 JSON 请求体到 dst。错误已映射为 APIError（400/413/415），调用方直接 writeError 即可。
func decodeJSON(r *http.Request, dst any) error {
	// 未带 Content-Type 时按 JSON 处理；带了就必须精确是 application/json（允许 charset 等参数）。
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mediaType, _, err := mime.ParseMediaType(ct); err != nil || mediaType != "application/json" {
			return ErrUnsupportedType
		}
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return mapDecodeError(err)
	}
	// 只允许一个 JSON 值，拒绝尾随内容。
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return mapDecodeError(err)
		}
		return ErrInvalidJSON
	}
	return nil
}

func mapDecodeError(err error) error {
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		return ErrPayloadTooLarge
	case errors.As(err, &typeErr):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_argument",
			Message: fmt.Sprintf("字段 %s 类型不正确", typeErr.Field), Field: typeErr.Field}
	case errors.Is(err, io.EOF):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_json", Message: "请求体不能为空"}
	default:
		return ErrInvalidJSON
	}
}
