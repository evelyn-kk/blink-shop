# frontend

Blink Shop Web 管理端（商家与管理员），React 19 + TypeScript strict + Vite。普通用户的交易在 Android App 里，Web 上普通用户只能浏览商品巡检。

```bash
npm ci
npm run dev        # http://localhost:5173，/api 代理到 VITE_API_PROXY_TARGET（默认 http://localhost:8080）
npm run lint       # eslint + tsc -b（含测试代码）
npm test           # vitest：组件、会话、登录页、API client 契约
npm run build
```

## 目录

| 目录 | 内容 |
| --- | --- |
| `src/api/` | 接口封装。所有请求都走 `http.ts` 的 `request`：加 `/api/v1` 前缀、带 `Authorization`、JSON 头，失败统一抛 `ApiError`（status / code / message / field / requestId） |
| `src/lib/` | 会话（`session.ts`）、全局通知（`notice.ts`）、hash 路由（`router.ts`）、请求状态（`useRequest.ts`）、格式化（`format.ts`）、页码（`paging.ts`） |
| `src/components/` | 通用组件，见下表 |
| `src/pages/` | 页面 |
| `src/test/` | 测试初始化和辅助函数 |

## 登录与会话

- 会话（token、到期时间、账户）存在 `localStorage`，刷新后恢复；其他标签页登录/退出会同步。token 只放在请求头里，不进 URL。
- 登录页先在本地检查必填（错误定位到字段并聚焦），服务端错误（账号或密码错误、停用、风控、限流）原样提示，失败后焦点回到密码框。登录成功回到原地址（`#/login?next=`，只接受站内地址），否则按角色进入默认页面：商家“我的商品”、管理员“知识资料（全部）”、普通用户“商品巡检”。已登录访问登录页直接跳走。
- 会话失效有两种来源，处理相同（清除会话、提示“登录已失效，请重新登录”）：带 token 的请求返回 401（token 被撤销或账号被注销）；页面开着时到了 `expires_at`（到期定时器）。需要登录的页面由 `RequireRole` 跳到登录页，登录后回到失效那一刻的地址。退出登录时 token 已失效不提示。
- `RequireRole` 是前端守卫，后端仍独立鉴权；角色不符显示“无权访问”，不渲染页面。

## 通用组件

| 组件 | 用途 |
| --- | --- |
| `NoticeHost` + `notify(kind, message)` | 全局通知，跨页面保留（保存后跳回列表也能看到）。成功/提示 5 秒后消失（role=status），错误手动关闭（role=alert）；相同提示不重复，最多 3 条；有文字前缀，不只靠颜色 |
| `Loading` / `Empty` / `ErrorState` | 加载、空态、错误（可重试） |
| `StatusBadge` | 状态标签：文字是状态，颜色（`tone`）只做辅助。各实体的文字和颜色在 `format.ts`（`orderStatusLabel` / `orderStatusTone` 等） |
| `DataTable` | 通用表格：`th scope="col"`，窄屏下每行变成卡片并在单元格前显示列名；没有数据时显示空态 |
| `PageLinks` | 分页链接，以服务端返回的 page / page_size 为准 |
| `ConfirmDialog` | 二次确认（原生模态 `<dialog>`）：Esc 等同取消，焦点默认在“取消”，处理中两个按钮都禁用 |
| `Field` + `invalid()` | 表单字段：必填星号、提示、错误说明，并给输入框加 `aria-invalid` / `aria-describedby` |
| `OrderActions` | 订单发货 / 取消（带确认） |

## 设计约定

颜色、圆角、触控尺寸等定义为 `styles.css` 顶部的 CSS 变量（`--brand`、`--ok`、`--warn`、`--bad`、`--info`、`--focus`、`--radius`、`--tap` 等）。所有可聚焦元素在键盘操作时显示 2px 焦点框（`:focus-visible`）；按钮等触控目标不小于 44px；金额用 `formatMoney`（人民币本地化），时间用 `formatDateTime`（本地时区）。

## 测试

- `npm test`：jsdom 环境，不连后端。`src/api/http.test.ts` 读取 `backend/fixtures/http` 的全部样例，确认每种错误都转成带中文说明的 `ApiError`、成功响应原样返回，并覆盖 token 注入、FormData、204、断网、非 JSON 错误、401 清会话。其余测试覆盖会话到期与同步、通知、各通用组件、`RequireRole`、登录页（含只用键盘登录）、路由和格式化。
- 浏览器端到端测试在 `quality/e2e/`（真实 API），其中 `shell.spec.mjs` 覆盖键盘登录与焦点框、token 撤销与到期、角色导航和跨页面通知，桌面和窄屏各跑一遍。
