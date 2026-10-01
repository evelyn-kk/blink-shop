# xzxg-shop 代码解构与迁移映射

本章是对当前相邻仓库 `../xzxg-shop` 的静态解构（不是外部假设）。它用于理解能力来源和复刻顺序；Blink Shop 应重写实现，不能复制源码或携带任何上游环境配置。

## 上游仓库的实际分层

| 上游位置 | 责任 | Blink 落位与实现要求 |
| --- | --- | --- |
| `backend/cmd/api/main.go` | 读取配置、连接 MySQL/配置中心/向量与对象存储、启动 HTTP | `backend/cmd/api`；仅做 DI 和 graceful shutdown |
| `backend/src/httpapi/server.go` | 集中声明近 90 条路由，handler 与中间件组装 | 同名模块；路由表同步生成 OpenAPI 和 handler test |
| `backend/src/httpapi/middleware.go` | CORS、请求上下文、鉴权、限流、日志、body 限制 | 先实现并测试，再接业务 handler |
| `backend/src/domain/types.go` | 跨层 DTO、角色/状态、SSE 和交易对象 | Blink 的唯一领域类型来源；禁止前端自定义不一致 DTO |
| `backend/src/store/interface.go` + `mysql.go` | Store 接口、MySQL 读写、迁移/种子 | 保留 interface + MySQL；为 Agent/handler 提供内存 fake |
| `backend/src/store/cart.go` | 购物车、库存锁、结算事务 | 先做这部分的并发和回滚测试，再开放 checkout API |
| `backend/src/agent/runtime.go`,`react.go`,`tools.go` | planner、ReAct 轮次、工具执行、最终流、输出过滤 | 功能等价重写；工具白名单和 product-id 来源校验不可省略 |
| `backend/src/agent/memory.go`,`trace_schema.go` | 会话记忆与可观测 trace schema | run、消息、trace 强关联且持久化 |
| `backend/src/rag/*`,`ingest/unstructured.go` | 切块、关键词/向量检索、重排、非结构化入库 | 分离纯函数与外部向量客户端，向量失败可回退 |
| `backend/src/imagevector/*` | 图片向量生成与相似图片商品检索 | 同一图片权限/可售状态过滤规范 |
| `backend/src/configcenter/*`,`risk/checker.go` | Nacos/内存配置与风险检查 | 配置有 TTL 和 secret mask，风险位于 LLM 之前 |
| `frontend/src/App.tsx` + `pages/*` | 角色菜单与管理/商家页面 | 用真正路由替代纯 state 切换也可以，但页面能力不能丢失 |
| `frontend/src/api/*` | API 封装与 localStorage 会话 | 统一 API client、鉴权、错误处理和类型 |
| `android-native/.../chat/*` | SSE 聊天、markdown、附件、历史抽屉 | 按 controller/renderer 分开，避免 Activity 过重 |
| `android-native/.../cart`,`order`,`product` | 用户交易页面 | 以服务端状态为准，客户端不私自计算最终金额 |
| `quality/evals/*` | JSONL 评测 runner 与报告 | Blink 复用“数据集→执行→报告”的模式，数据/断言重新编写 |

## 上游的关键实现事实（复刻时不得遗漏）

1. API 是 Go 标准库 `http.ServeMux`，路由集中；中间件顺序含 CORS、认证、限流、日志、body limit。
2. 上游采用 MySQL 作为交易、会话、追踪、Prompt 的事实来源；Nacos 是动态配置分发，而不是主业务数据库。
3. Agent 是“风险/图片兜底 → 记忆 → 小模型 planner → 大模型 ReAct 工具循环 → 最终 SSE → 追问”的链路；未配置 DashScope 时规则兜底可运行。
4. Agent 的操作工具不仅搜索，还会改变购物车、订单、券、评价，因此实施时必须把授权和结构化参数校验放在工具层，而非信任模型文本。
5. 上游明确为聊天流式请求设置 client 生成的 message id 唯一约束；这是防止断网重试重复消费的核心。
6. Web 面向商家/管理员；消费者主端是 Java 原生 Android，不是 Capacitor Web 壳。虽然 frontend 中有 Capacitor 配置，但 Android 原生工程才是完整用户体验实现。
7. 测试覆盖集中在 Agent 工具策略、记忆、相关性、图向量、非结构化入库和语音；Blink 应在这些基础上补充 HTTP 授权与并发结算测试。

## 推荐阅读路径（给实施 Agent）

先读取上游的 `migrations/001_mysql_schema.sql`、`domain/types.go` 和 `httpapi/server.go` 来冻结表/路由范围；再看 `store/cart.go` 确定交易原子性；随后看 `agent/runtime.go`、`react.go`、`tools.go` 与 `tool_policy.go`；最后分别进入 `frontend/src/App.tsx` 与 Android 的 `app/MainActivity.java`、`chat/ChatActivity.java`，核对页面和交互。每完成一个模块，都回写 Blink OpenAPI、fixture 和验收用例，而不是把上游文件直接拷入新仓库。
