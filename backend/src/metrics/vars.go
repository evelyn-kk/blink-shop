package metrics

import (
	"context"
	"errors"
	"time"
)

// 全部指标集中定义在这里，告警规则（deployments/monitoring/alerts.yml）按这些名字写。
var (
	HTTPRequests = Default.Counter("blink_http_requests_total", "HTTP 请求数（route 是路由模式，未匹配为 unmatched；code 是状态码类别 2xx/4xx/5xx）", "route", "method", "code")
	HTTPDuration = Default.Histogram("blink_http_request_duration_seconds", "HTTP 请求耗时（不含 SSE、WebSocket 长连接）", nil, "route")

	AgentRuns        = Default.Counter("blink_agent_runs_total", "导购运行结束数（completed / failed / cancelled）", "status")
	AgentRunDuration = Default.Histogram("blink_agent_run_duration_seconds", "导购运行从开始到结束的耗时", nil, "status")
	AgentFirstEvent  = Default.Histogram("blink_agent_first_event_seconds", "从收到消息到推送第一个进展事件（thinking / 文本 / 块）的耗时", nil)
	SSEActive        = Default.Gauge("blink_sse_active_connections", "进行中的导购 SSE 连接数")
	WSActive         = Default.Gauge("blink_ws_active_connections", "进行中的实时语音 WebSocket 连接数")

	Checkouts = Default.Counter("blink_checkout_total", "结算请求结果（success / replayed / 错误码）", "result")

	External         = Default.Counter("blink_external_calls_total", "外部依赖调用结果（ok / error / timeout / canceled / breaker_open）", "dependency", "result")
	ExternalDuration = Default.Histogram("blink_external_call_duration_seconds", "外部依赖调用耗时", nil, "dependency")
	BreakerOpen      = Default.Gauge("blink_breaker_open", "熔断器是否打开（1 打开，0 关闭）", "dependency")

	VectorSyncs = Default.Counter("blink_vector_sync_total", "商品向量同步任务结果（ok / error）", "result")
)

// ObserveExternal 记录一次外部依赖调用。dependency 取固定的依赖名：llm、embedding、image_embedding、milvus、nacos、minio、stt、tts。
func ObserveExternal(dependency string, start time.Time, err error) {
	ExternalDuration.Since(start, dependency)
	External.Inc(dependency, ResultOf(err))
}

// ResultOf 把错误归成有限的几类（不把错误原文放进标签）。
func ResultOf(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var t interface{ Timeout() bool }
	if errors.As(err, &t) && t.Timeout() {
		return "timeout"
	}
	return "error"
}

// Track 用于 defer：`defer metrics.Track("milvus", time.Now(), &err)`，函数返回时按 err 记录一次调用。
func Track(dependency string, start time.Time, errp *error) {
	var err error
	if errp != nil {
		err = *errp
	}
	ObserveExternal(dependency, start, err)
}
