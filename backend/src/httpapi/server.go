// Package httpapi 负责 HTTP 路由、中间件和 JSON 输入输出。
package httpapi

import (
	"encoding/json"
	"net/http"
)

// NewHandler 返回挂好全部路由的 HTTP handler。
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", handleHealth)
	mux.HandleFunc("/", handleNotFound)
	return mux
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "接口不存在")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// ErrorResponse 是所有失败响应的统一结构，见 openapi.yaml#/components/schemas/Error。
type ErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{
		Code:      code,
		Message:   message,
		RequestID: w.Header().Get("X-Request-ID"),
	})
}
