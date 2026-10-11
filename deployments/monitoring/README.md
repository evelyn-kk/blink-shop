# 监控、SLO 与告警

API 在 `GET /metrics` 输出 Prometheus 文本格式指标（`backend/src/metrics`）。告警规则在 [`alerts.yml`](alerts.yml)，规则单测在 [`alerts_test.yml`](alerts_test.yml)，CI 的 “Security & Monitoring” 任务会用 promtool 校验并跑单测。

## 访问 /metrics

- 配置 `METRICS_TOKEN`（≥ 24 个字符）后必须带 `Authorization: Bearer <METRICS_TOKEN>`；
- 没配置时：开发环境只允许本机 TCP 直连（看连接来源，不看 `X-Forwarded-For`），生产环境返回 404（不开放）；
- 不要把 `/metrics` 暴露到公网负载均衡上，抓取走内网。

Prometheus 抓取示例：

```yaml
scrape_configs:
  - job_name: blink-shop-api        # 告警 BlinkAPIDown 按这个 job 名
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/blink_metrics_token
    static_configs:
      - targets: ["api-1.internal:8080", "api-2.internal:8080"]
rule_files:
  - /etc/prometheus/rules/blink-shop-alerts.yml   # 即本目录的 alerts.yml
```

## SLO

| 指标 | 目标 | 计算（记录规则） | 告警 |
| --- | --- | --- | --- |
| 商品目录 P95（列表、详情、SKU、评价、分类树） | < 300ms | `blink:catalog_latency_seconds:p95_5m` | BlinkCatalogSlow（10 分钟） |
| 普通接口 P95（不含 SSE / WebSocket / 上传 / 评测 / 图搜） | < 500ms | `blink:api_latency_seconds:p95_5m` | BlinkAPISlow（10 分钟） |
| 导购首个进展事件 P95 | < 1.5s（规则模式）；告警线 3s（含模型） | `blink:agent_first_event_seconds:p95_10m` | BlinkAgentFirstEventSlow |
| 结算成功率（不含库存不足、价格变化等业务拒绝） | ≥ 99% | `blink:checkout_success:ratio_15m` | BlinkCheckoutFailing（≥ 20 次才判断） |
| 导购失败率 | < 5% | `blink:agent_failed:ratio_15m` | BlinkAgentFailing（≥ 20 次才判断） |
| 5xx 比例 | < 1% | `blink:http_5xx:ratio_5m` | BlinkHigh5xxRatio（5 分钟） |
| 外部依赖失败率（按依赖） | < 10% | `blink:external_error:ratio_10m` | BlinkDependencyErrors |

压测实测（10.1，单实例、本机 MySQL）见 [`docs/10-release-readiness.md`](../../docs/10-release-readiness.md)。

## 指标

| 指标 | 类型 | 标签 | 说明 |
| --- | --- | --- | --- |
| `blink_http_requests_total` | counter | route, method, code | 路由模式（不是原始路径）、方法、状态码类别 2xx/4xx/5xx；未匹配的路由是 `unmatched` |
| `blink_http_request_duration_seconds` | histogram | route | 不含导购 SSE 和语音 WebSocket 长连接 |
| `blink_agent_runs_total` | counter | status | completed / failed / cancelled |
| `blink_agent_run_duration_seconds` | histogram | status | 从收到消息到运行结束 |
| `blink_agent_first_event_seconds` | histogram | — | 从收到消息到第一个 thinking / 文本 / 块事件 |
| `blink_sse_active_connections` / `blink_ws_active_connections` | gauge | — | 进行中的长连接数 |
| `blink_checkout_total` | counter | result | success / replayed / 业务错误码（如 insufficient_stock、price_changed）/ internal_error |
| `blink_external_calls_total` | counter | dependency, result | dependency：llm、embedding、image_embedding、milvus、nacos、minio、stt、tts；result：ok / error / timeout / canceled / breaker_open |
| `blink_external_call_duration_seconds` | histogram | dependency | 外部调用耗时 |
| `blink_breaker_open` | gauge | dependency | 熔断器是否打开（llm、embedding、milvus） |
| `blink_vector_sync_total` | counter | result | 商品向量同步任务结果 |
| `blink_db_open_connections` / `blink_db_in_use_connections` | gauge | — | 数据库连接池 |
| `blink_db_wait_count` / `blink_db_wait_seconds` | gauge（累计值） | — | 等待空闲连接的累计次数和秒数，持续增长说明池子小或有慢查询 |

标签值都是有限集合（路由模式、状态类别、依赖名、错误码），不含用户输入、ID 或错误原文。

## 熔断

模型服务（llm）、Embedding、Milvus 各有一个熔断器：连续 5 次失败（连不上、超时、5xx；调用方主动取消、Milvus 业务错误不算）后打开 30 秒，
期间直接降级（导购退回规则回答、检索退回关键词、图搜返回不可用），不再每次等满超时；冷却后只放一个探测请求，成功即恢复。Nacos 有自己的熔断和启动预算（见后端 README“动态配置中心”）。
