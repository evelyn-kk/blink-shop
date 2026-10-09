# API 与 SSE 契约

## 通用规则

- JSON 使用 snake_case；成功正文可直接是对象或 `{data: ...}`，全项目选定一种并由前端客户端统一解包。
- Bearer token 认证；公开资源仅限健康、注册登录、分类/商家/商品读取和公开头像。所有资源按当前 account 范围过滤。
- 分页统一 `page`（从 1 起）、`page_size`、`total`、`items`；默认 10，最大 100。
- 失败统一 `{ "code": "machine_code", "message": "可展示中文" }`；400 参数、401 未登录/失效、403 越权、404 不存在、409 幂等/状态/库存冲突、413 文件过大、429 限流、500 内部错误。

## 路由清单

| 域 | 方法与路径 |
| --- | --- |
| 健康/账户 | `GET /health`; `POST /auth/login,/auth/register,/auth/logout`; `GET /auth/me`; `GET,PATCH /account/profile`; `PATCH /account/contact`; `DELETE /account`; `POST /uploads/avatar` |
| 公开目录 | `GET /categories/tree,/merchants,/products,/products/{id},/products/{id}/skus,/products/{id}/reviews,/promotions` |
| 文件/图搜 | `POST /files`; `GET /files/{id}`; `POST /search/image` |
| 购物车/营销 | `GET /cart,/cart/discount-preview`; `POST /cart/items`; `PATCH,DELETE /cart/items/{id}`; `GET /coupons/available,/coupons/mine`; `POST /coupons/{id}:claim` |
| 用户订单 | `GET /orders,/orders/{id}`; `POST /orders:checkout,/orders/{id}:pay,/orders/{id}:cancel,/orders/{id}:confirm-receipt,/orders/{id}/items/{item_id}:review` |
| Agent | `POST /agent/guide-suggestions`; `GET,POST /agent/sessions`; `GET,PATCH,DELETE /agent/sessions/{id}`; `POST /agent/sessions/{id}/messages:stream`; `POST /agent/sessions/{id}:pin,/agent/sessions/{id}:summarize`; `GET /agent/runs/{id}/trace`; `POST /agent/runs/{id}:cancel` |
| 语音 | `GET /speech/realtime`; `GET /speech/tts/config`; `POST /speech/tts` |
| 商家 | `POST /merchant/products`; `PATCH,DELETE /merchant/products/{id}`; `GET /merchant/orders`; `PATCH /merchant/orders/{id}`; `GET,POST /merchant/promotions`; `PATCH /merchant/promotions/{id}`; `GET,POST /merchant/documents`; `POST /merchant/unstructured-ingestions`; `GET /merchant/reviews`; `POST /merchant/reviews/{id}:reply` |
| 平台 | `GET,PATCH /admin/accounts,/admin/merchants,/admin/products,/admin/orders,/admin/promotions,/admin/reviews`; `GET,PATCH /admin/configs`; `GET,POST,PATCH /admin/prompts`; `GET /admin/vector/status,/admin/evals,/admin/evals/reports/{id},/admin/agent/runs,/admin/agent/runs/{id}` |
| 评测 | `POST /eval/intent,/eval/rag,/eval/products,/eval/image-search` |

实现时将各路径、必填字段、请求示例和响应 fixture 维护为 OpenAPI 3.1 (`backend/openapi.yaml`)；它是前端、Android、集成测试的单一契约源。

## Agent 流式端点

请求：`{client_message_id, content, attachments:[{file_id}]}`（附件类型以服务端记录为准，只能引用本人上传的文件）。`client_message_id` 必填且在会话内唯一。响应 `Content-Type: text/event-stream`，禁用代理缓冲；先 `message_start`（含 run_id、message_id、trace_id），再 0..n 个 `thinking`、`text_delta`、`block`、`followups`，最终仅一个 `message_done` 或 `error`。客户端断开/取消要取消 context 并将 run 写为 cancelled；重复 message id 不重新调用模型而重放/返回既有 run。各事件 data 结构、心跳、取消/超时/重启的最终状态与 `error.code` 见 `backend/openapi.yaml`（AgentStreamEvent）和 `backend/README.md`“导购会话与流式回答”。

`block` 支持 `product_list`（可信 product_ids）、`comparison`（字段行）、`citation`、`action`，以及 `cart`、`order_list`、`coupon_list`、`promotion_list`、`review_list`；每种块的字段见 `backend/openapi.yaml`（`AgentBlock*`），说明见 `backend/README.md`“导购规划与工具 → 结构化块”。永远不要把模型未检索到的 product_id 渲染成可购买卡片；客户端遇到未知块类型应忽略。

## 文件与语音

文件上传用 multipart，服务端嗅探 MIME、限制大小、生成不可猜测 object key；下载要校验 owner/admin。TTS 接受 `{text, voice?}`，成功直接返回音频字节及正确 `Content-Type`；语音服务未配置返回可识别的 `*_not_enabled`，不得暴露供应商凭据。
