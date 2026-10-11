package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// Command fakellm 是验收用的假模型服务（OpenAI 兼容 /chat/completions），check.py 的“慢模型实例”指向它：
// 请求里带“验收版本”（发布后的规划 Prompt）时立即返回合法的规划 JSON；否则一直不回，直到客户端放弃。
func main() {
	var n atomic.Int64
	http.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "验收版本") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"model":"fake","choices":[{"message":{"role":"assistant","content":"{\"intent\":\"guide\",\"action\":\"greeting\"}"},"finish_reason":"stop"}]}`)
			return
		}
		<-r.Context().Done()
	})
	http.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, n.Load()) })
	_ = http.ListenAndServe(":18095", nil)
}
