package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// APIError 是可以直接返回给客户端的错误：状态码、机器码和可展示的中文说明。
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
}

// 通用错误；业务错误在各自模块中按同样方式定义。
var (
	ErrInvalidArgument = &APIError{http.StatusBadRequest, "invalid_argument", "请求参数不正确"}
	ErrInvalidJSON     = &APIError{http.StatusBadRequest, "invalid_json", "请求体不是合法的 JSON"}
	ErrUnsupportedType = &APIError{http.StatusUnsupportedMediaType, "unsupported_media_type", "请求体必须是 application/json"}
	ErrUnauthorized    = &APIError{http.StatusUnauthorized, "unauthorized", "请先登录"}
	ErrForbidden       = &APIError{http.StatusForbidden, "forbidden", "没有权限"}
	ErrNotFound        = &APIError{http.StatusNotFound, "not_found", "接口不存在"}
	ErrMethodNotAllow  = &APIError{http.StatusMethodNotAllowed, "method_not_allowed", "不支持该请求方法"}
	ErrConflict        = &APIError{http.StatusConflict, "conflict", "当前状态不允许该操作"}
	ErrPayloadTooLarge = &APIError{http.StatusRequestEntityTooLarge, "payload_too_large", "请求体过大"}
	ErrRateLimited     = &APIError{http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试"}
	ErrInternal        = &APIError{http.StatusInternalServerError, "internal_error", "服务暂时不可用"}
	ErrNotReady        = &APIError{http.StatusServiceUnavailable, "not_ready", "服务尚未就绪"}
	ErrTimeout         = &APIError{http.StatusGatewayTimeout, "timeout", "请求处理超时，请稍后重试"}
)

// ErrorResponse 是所有失败响应的统一结构，见 openapi.yaml#/components/schemas/Error。
type ErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
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
		RequestID: w.Header().Get(headerRequestID),
	})
}

// decodeJSON 读取 JSON 请求体到 dst。错误已映射为 APIError（400/413/415），调用方直接 writeError 即可。
func decodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return ErrUnsupportedType
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
		return &APIError{http.StatusBadRequest, "invalid_argument", fmt.Sprintf("字段 %s 类型不正确", typeErr.Field)}
	case errors.Is(err, io.EOF):
		return &APIError{http.StatusBadRequest, "invalid_json", "请求体不能为空"}
	default:
		return ErrInvalidJSON
	}
}
