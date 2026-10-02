# backend

Go 1.24 标准库 `net/http` API。接口契约见 [openapi.yaml](openapi.yaml)，请求/响应样例见 [fixtures/http/](fixtures/http/)。

```bash
set -a && source ../.env && set +a   # 可选：加载根目录 .env
go run ./cmd/seed                    # 迁移 + 写入演示数据（可重复执行）
go run ./cmd/api                     # http://localhost:8080/api/v1/health
gofmt -l . && go vet ./... && go test ./...

# MySQL 集成测试（每个用例自建临时库并删除；需要建库权限）
BLINK_TEST_MYSQL_DSN='root:blink_dev_root@tcp(127.0.0.1:3306)/' go test ./...
```

未设置 `BLINK_TEST_MYSQL_DSN` 时，本地会跳过 MySQL 集成测试（内存实现的同一套契约测试照常运行）；CI 中会启动 MySQL 服务并强制运行。

## 数据层

| 位置 | 内容 |
| --- | --- |
| `src/domain` | 领域类型、状态机（`CanTransitionTo`）、定点金额 `Money`/比例 `Rate`、ID 生成。不依赖任何基础设施 |
| `migrations/` | 版本化 SQL 迁移、迁移规则、唯一约束清单和实体关系图，见 [migrations/README.md](migrations/README.md) |
| `src/store` | `Store` 接口与可识别错误：`ErrNotFound`、`ErrConflict`（`ConflictError.Key` 为唯一键名）、`ErrInvalid` |
| `src/store/mysqlstore` | MySQL 实现与迁移执行器；所有 SQL 参数化 |
| `src/store/memstore` | 内存实现，供上层单测使用；实现同样的唯一约束和事务回滚 |
| `src/store/storetest` | 两种实现共用的契约测试 |
| `src/seed`、`cmd/seed` | 开发演示数据与写入命令 |
| `fixtures/domain/` | 领域对象 JSON 序列化样例；改了领域类型后用 `go test ./src/seed -update` 更新 |

事务通过 context 传递：

```go
err := st.WithTx(ctx, func(ctx context.Context) error {
    // 用同一个 ctx 调用的 Store 方法都在这个事务里；返回 error 或 panic 都会整体回滚。
    // 嵌套 WithTx 复用外层事务。
    return nil
})
```

`Store` 目前只包含本节点需要的方法（账户与资料、登录 token、公开目录查询、事务、种子）；其余方法随各功能节点加入，并同步补充契约测试。

## 演示数据

`go run ./cmd/seed` 先执行迁移，再按固定 ID 写入演示数据：主键已存在的记录跳过，不覆盖本地修改，所以可以重复执行；遇到其他唯一键冲突时整份种子回滚。`APP_ENV=production` 时拒绝运行。

| 账号 | 角色 | 说明 |
| --- | --- | --- |
| `blink_admin` | admin | 平台管理员 |
| `blink_merchant` | merchant | Blink 数码旗舰店 |
| `blink_merchant2` | merchant | Blink 家居生活馆（用于越权测试） |
| `blink_user` | user | 有覆盖全部状态的订单、已用优惠券和评价 |
| `blink_user2` | user | 有一张未使用的优惠券（用于越权测试） |

初始密码均为 `BlinkDev#2026`（仅限本地），数据库只保存 bcrypt 哈希。数据还包括两级分类、9 个商品（可售、库存紧张、无货、下架、风控、已删除）、知识文档与分块、促销规则和优惠券。

## 启动流程

`cmd/api` 的 `run()` 只做组装和生命周期：

1. 读取配置并解析所有 HTTP 配置；任何格式错误直接退出，并一次列出全部问题。
2. `APP_ENV=production` 时做危险配置检查（见下文），不通过则在监听端口前退出（exit 1）。
3. 连接 MySQL。连不上只记 warn，进程照常启动：`/health` 仍为 200，`/ready` 返回 503，MySQL 恢复后自动变回就绪。`RUN_MIGRATIONS` 开启时在后台执行迁移（失败每 5 秒重试）；只要还有未执行的迁移，`/ready` 就是 503。
4. 监听 `API_ADDR`；收到 SIGINT/SIGTERM 后最多等 10 秒处理完在途请求再退出。

## 中间件链

从外到内依次为：

| 顺序 | 中间件 | 作用 |
| --- | --- | --- |
| 1 | request context | 采用合法的 `X-Request-ID`（1–64 位 `[A-Za-z0-9._-]`），否则生成 24 位 hex；回写响应头；读取本次请求生效的 HTTP 配置 |
| 2 | access log | 每个请求结束后写一条结构化日志（字段见下） |
| 3 | recover | 捕获 panic：日志记录堆栈，客户端只收到 500 `internal_error` |
| 4 | CORS | 按白名单回写 `Access-Control-Allow-Origin`；`OPTIONS` 一律 204 且不进入后续中间件 |
| 5 | IP 限流 | 认证前一层，按客户端 IP 固定窗口计数；超限 429 + `Retry-After` |
| 6 | 请求体限制 | multipart 用 `UPLOAD_MAX_BYTES`，其余用 `HTTP_MAX_BODY_BYTES`；声明长度超限直接 413，未声明长度时读取超限 413 |
| 7 | 超时 | 给 context 设截止时间；handler 超时返回且未写响应时输出 504。`*:stream` 和 `/speech/realtime` 不设超时 |
| 8 | 认证 | 解析 `Authorization: Bearer <token>`，把账户放进 context；只识别不拒绝，是否需要登录由路由的访问规则决定 |
| 9 | 账号限流 | 认证后一层，已登录请求按账号计数；未登录请求只受 IP 层约束 |
| 10 | 路由 | 未知路径 404、方法不支持 405（带 `Allow`），都是统一错误 JSON；命中后先按访问规则检查登录、账户状态和角色 |

`/health`、`/ready` 不参与限流。客户端 IP：只有直连方属于 `TRUSTED_PROXY_CIDRS` 时才读取 `X-Forwarded-For`，并从右往左跳过可信代理，取第一个不可信地址，客户端在头部前面伪造的地址不会被采用。

## 统一错误

失败响应一律为 `{"code": "...", "message": "...", "request_id": "..."}`，`message` 可直接展示，不包含 SQL、堆栈或依赖地址。

| 状态码 | code | 场景 |
| --- | --- | --- |
| 400 | `invalid_json` / `invalid_argument` | JSON 语法错误、空请求体、尾随内容 / 字段类型错误 |
| 401 | `unauthorized` | 未登录，或 token 过期、已登出、伪造 |
| 401 | `invalid_credential` | 登录时账号或密码错误（含用户不存在、已注销） |
| 403 | `forbidden` | 角色不符，或商家操作其他店铺的资源 |
| 403 | `account_inactive` / `account_risk` | 账户状态不允许该操作，见“认证与权限” |
| 404 | `not_found` | 路径不存在 |
| 409 | `username_exists` | 注册时用户名已存在 |
| 405 | `method_not_allowed` | 方法不支持 |
| 413 | `payload_too_large` | 请求体超限 |
| 415 | `unsupported_media_type` | JSON 接口收到非 `application/json` |
| 429 | `rate_limited` | 触发限流 |
| 500 | `internal_error` | 未预期错误或 panic |
| 503 | `not_ready` | `/ready` 依赖不可用 |
| 504 | `timeout` | 请求处理超时 |

业务 handler 读请求体用 `decodeJSON`，返回错误用 `writeError`：`*APIError` 原样输出，其他 error 一律按 500 处理。

## 公开商品目录

- **可见性**：商品 `active` 且所属商家 `active` 才出现在列表、详情、SKU、评价接口中；下架、风控、已删除、商家停用的商品一律 404 `product_not_found`。无货但上架的商品照常展示。内部的 `Store.GetProduct` 不过滤状态（订单快照、商家后台使用）。
- **搜索**：`store.SearchTerms` 把关键词拆成最多 10 个检索词（完整关键词 + 汉字 2–4 字 n-gram + 字母数字片段），名称、品牌、分类名、标签、卖点包含任一词即命中；MySQL 端用 `LIKE`（转义 `%`/`_`）和 `JSON_SEARCH`，内存实现做同样的不区分大小写匹配，由契约测试保证一致。
- **DTO**：`src/httpapi/catalog.go` 把领域对象转换成公开结构，不输出状态、排序值、内部时间戳，评价不输出账户/订单 ID、评价人名字脱敏；数组字段始终是 `[]`，属性的 `unit` 始终存在。字段集合由 `catalog_test.go` 锁定。
- **促销**：只返回 active、在有效期内、商家营业中的促销；`?product_id=` 只返回适用于该商品的（平台/商家/商品/分类及父分类）。“当前时间”取自 `Options.Now`，测试中固定。
- **演示图片**：`assets/catalog` 下的 PNG 由 `go run ./cmd/gen-sample-images` 生成（纯几何图形、结果确定），编译时内嵌，经 `GET /api/v1/assets/...` 公开读取，种子数据引用这些地址，不需要外网。

## 商家商品管理

- **归属**：merchant_id 一律取自登录账号，请求体里写别的商家 400；读改删其他商家的商品 403（与 RBAC 矩阵一致），不存在或已删除 404。归属与状态检查在 `Store.UpdateProduct` 的行锁回调里做，不会与并发删除交错。
- **规格与派生字段**：SKU 是价格和库存的唯一来源。`domain.Product.SyncFromSKUs` 在每次写库前重算：售价 = 默认规格价格，库存 = 各规格之和，库存状态由数量推导；客户端传的 `stock_status` 忽略。请求可以提交 `skus`（整体替换：带 sku_id 更新、不带新增、未出现删除），或用上游的 `price` + `stock_quantity` 只维护默认规格。
- **状态**：商家只能在 active / inactive 间切换；风控中的商品不能自行上下架（409 `product_under_review`），但能改资料；删除 = 状态改为 deleted（终态，软删）。
- **校验**：失败返回 400 `invalid_argument`，`field` 指出字段（如 `skus[1].price`）。规则见 `openapi.yaml#/components/schemas/ProductInput`；图片只接受 https 或平台内已存在的图片，上传文件的归属校验在 3.1 补充。
- **审计**：`product.created` / `product.updated` / `product.deleted`。

## 认证与权限

- **token**：注册/登录返回 32 字节随机 token（base64url），`auth_tokens` 只存 SHA-256 摘要和过期时间。每个请求都按 token 重新读取账户，所以管理员改账户状态立即生效。登出撤销当前 token，注销撤销全部 token。
- **密码**：bcrypt（`DefaultCost`）。兼容上游遗留的 SHA-256 hex 哈希，登录成功后自动升级；bcrypt 成本低于当前设置时同样升级。用户不存在时也做一次 bcrypt 比较，避免按响应时间枚举用户名。
- **审计**：注册、登录成功/失败/被拒、登出、改资料、改联系方式、上传头像、注销、密码升级都写一条 `msg=audit` 日志，只含动作和 ID，不含密码、token、手机号、邮箱。
- **头像**：`POST /uploads/avatar` 按内容嗅探（JPG/PNG/WebP，≤2MB），文件名为 `<account_id>_<随机 16 位 hex>.<ext>`；`PATCH /account/profile` 只接受本人上传且仍存在的头像地址。读取接口公开，经 `os.Root` 限定在头像目录内。

### 路由访问规则

每条路由注册时必须用 `s.handle(pattern, access, handler)` 指定规则（`src/httpapi/auth.go`）：

| 规则 | 需要登录 | 允许角色 | inactive | risk |
| --- | --- | --- | --- | --- |
| `public` | 否 | 任何人 | — | — |
| `session` | 是 | user / merchant / admin | 允许 | 允许 |
| `account` | 是 | user / merchant / admin | 403 `account_inactive` | 只读，写操作 403 `account_risk` |
| `user` | 是 | user | 403 `account_inactive` | 只读，写操作 403 `account_risk` |
| `merchant` | 是 | merchant（且已关联店铺） | 403 `account_inactive` | 只读，写操作 403 `account_risk` |
| `admin` | 是 | admin | 403 `account_inactive` | 只读，写操作 403 `account_risk` |

检查顺序：未登录 401 → 账户状态 403 → 角色 403。inactive/risk 账户不能新登录（密码正确时返回对应 403）；已注销账户的 token 全部删除，等同未登录。商家与管理员共用的资源接口，在角色检查之后还要调用 `requireMerchantOwner`：商家只能操作 `merchant_id` 与自己一致的资源，管理员不受限。

### RBAC 矩阵

当前全部路由（`TestRouteAccessTable` 保证与代码一致，新增路由必须同时更新这里和测试）：

| 路由 | 规则 |
| --- | --- |
| `GET /health`、`GET /ready` | public |
| `POST /auth/register`、`POST /auth/login` | public |
| `POST /auth/logout`、`GET /auth/me` | session |
| `GET,PATCH /account/profile`、`PATCH /account/contact`、`DELETE /account` | account |
| `POST /uploads/avatar` | account |
| `GET /uploads/avatar/{name}` | public |
| `GET /categories/tree`、`GET /merchants`、`GET /products`、`GET /products/{id}`、`GET /products/{id}/skus`、`GET /products/{id}/reviews`、`GET /promotions` | public |
| `GET /assets/{path...}` | public |
| `GET,POST /merchant/products`、`GET,PATCH,DELETE /merchant/products/{id}` | merchant（另做归属校验） |

后续节点按前缀约定（`TestRoutePrefixRoles` 检查）：`/admin/*` → admin；`/merchant/*` → merchant；`/cart`、`/orders`、`/coupons/*`、`/agent/*`、`/speech/*` → user；分类/商家/商品读取 → public。

| 场景 | 预期 |
| --- | --- |
| 未登录访问任何非 public 路由 | 401 `unauthorized` |
| user / merchant 访问 admin 路由 | 403 `forbidden` |
| merchant / admin 访问 user 交易路由 | 403 `forbidden` |
| merchant 修改其他店铺资源 | 403 `forbidden` |
| inactive 用户写交易 | 403 `account_inactive` |
| risk 用户写交易 | 403 `account_risk` |

## 配置

读取优先级：**环境变量 → 动态配置（Nacos/内存）→ 内置默认值**，空字符串视为未设置。动态配置目前只有内存实现，Nacos 在 9.1 接入。HTTP 配置每个请求重新读取，所以运行中修改动态配置会立即生效；动态配置写成非法值时，该项退回内置默认值，不会停服。

| 环境变量 | 动态配置键 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `APP_ENV` | — | `development` | `development` / `test` / `production`（`prod`） |
| `API_ADDR` | — | `:8080` | 监听地址 |
| `RUN_MIGRATIONS` | — | 非生产 `true`，生产 `false` | 启动时在后台执行数据库迁移 |
| `BOOTSTRAP_VECTOR_INDEX` | — | 非生产 `true`，生产 `false` | 启动时初始化向量索引（8.x 实现） |
| `MYSQL_DSN` | — | 与 Compose 开发库一致 | 密钥类，日志中整体掩码。连接时强制 UTC 时区、`parseTime`、utf8mb4 |
| `CORS_ALLOWED_ORIGINS` | `http.cors.allowed_origins` | `*` | 逗号分隔；生产环境始终忽略 `*` |
| `TRUSTED_PROXY_CIDRS` | `http.trusted_proxy_cidrs` | 空 | 逗号分隔的 CIDR 或 IP |
| `TRUST_ALL_PROXIES` | `http.trust_all_proxies` | `false` | 仅本地调试；生产始终视为 `false` |
| `HTTP_MAX_BODY_BYTES` | `http.max_body_bytes` | `1048576` | JSON 请求体上限 |
| `UPLOAD_MAX_BYTES` | `http.upload_max_bytes` | `10485760` | multipart 上传上限 |
| `HTTP_REQUEST_TIMEOUT` | `http.request_timeout` | `30s` | 非流式请求超时 |
| `RATE_LIMIT_IP_PER_MINUTE` | `http.rate_limit.ip_per_minute` | `120` | 认证前 IP 限额 |
| `RATE_LIMIT_ACCOUNT_PER_MINUTE` | `http.rate_limit.account_per_minute` | `120` | 认证后账号限额 |
| `AUTH_TOKEN_TTL` | `auth.token_ttl` | `24h` | 登录 token 有效期（与上游一致） |
| `LOGIN_ATTEMPTS_PER_MINUTE` | `auth.login_attempts_per_minute` | `10` | 同一用户名每分钟登录尝试上限 |
| `AVATAR_UPLOAD_DIR` | — | `uploads/avatar` | 头像文件目录（相对启动目录）；3.1 接入对象存储后替换 |
| `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | — | `minioadmin` | 密钥类 |
| `MILVUS_TOKEN` | — | 空 | 密钥类 |
| `AI_API_KEY` | — | 空 | 密钥类 |

### 生产危险配置

`APP_ENV=production` 时，以下任一情况都会拒绝启动：

- `MYSQL_DSN` 无法解析，或密码为空 / `root` / `password` / `123456` / `blink_dev_password` / `blink_dev_root`；
- `CORS_ALLOWED_ORIGINS` 为空或包含 `*`；
- `MINIO_ACCESS_KEY` 或 `MINIO_SECRET_KEY` 为空或 `minioadmin`；
- `MILVUS_TOKEN` 为空或 `root:Milvus`；
- `AI_API_KEY` 为空或示例值（`changeme`、`your-api-key`、`sk-xxx`）；
- `TRUST_ALL_PROXIES` 开启。

错误信息只列出配置名，不回显配置值。

## 日志

所有日志都是 JSON 单行，写到 stdout；MySQL 驱动自身的日志也转到这里。

访问日志（`msg = "http request"`），5xx 为 `ERROR` 级别，其余为 `INFO`：

| 字段 | 说明 |
| --- | --- |
| `time` / `level` / `msg` | slog 标准字段 |
| `request_id` | 与响应头 `X-Request-ID` 一致 |
| `method` / `path` | 不记录 query string，避免 token 等参数落盘 |
| `status` | 最终状态码；handler 未写任何内容时记为 200 |
| `bytes` | 响应体字节数 |
| `duration_ms` | 处理耗时（毫秒） |
| `client_ip` | 按可信代理规则得到的客户端 IP |
| `user_agent` | 请求的 User-Agent |
| `account_id` | 已登录时出现 |

审计事件：`audit`，字段 `action`（如 `auth.login`、`auth.login_failed`、`account.deleted`）、`account_id`、`client_ip`，见“认证与权限”。

其他事件：`auth lookup failed`、`register failed`、`login lookup failed`、`create token failed`、`password rehash failed`、`account update failed`、`delete account failed`、`save avatar failed`、`config loaded`、`api listening`、`mysql unavailable at startup`、`readiness check failed`、`panic recovered`（含 `stack`）、`mysql driver`、`database migrated`、`database migration failed, will retry`、`api shutdown completed`。

### 脱敏规则

由 `src/logging` 在写出前统一处理，覆盖消息正文、所有字段（包括 `With` 和分组里的字段）、error 值，以及作为字段值传入的结构体、map、切片（按 JSON 结构递归处理）：

- 字段名包含 `authorization`、`password`、`passwd`、`token`、`secret`、`apikey`/`api_key`、`cookie`、`dsn`、`credential`（忽略大小写、`_`、`-`、`.`），值整体替换为 `***`；
- 任意字符串中的大陆手机号保留前 3 位和后 4 位（`138****5678`，支持 `+86` 前缀）；
- 邮箱保留首字符和域名（`a***@example.com`）；
- `Bearer <token>` 替换为 `Bearer ***`。
