# quality

质量工具与评测数据。

- `npm ci && npm run openapi:lint`：校验 `backend/openapi.yaml`。
- `npm run e2e`：Web 端到端测试（Playwright，`e2e/`），用真实后端 + 前端，在桌面（1280px）和窄屏（375px）各跑一遍，截图在 `reports/e2e/screens/`。运行前：

  ```bash
  cd backend && go run ./cmd/seed                              # 需要干净的演示数据
  RATE_LIMIT_IP_PER_MINUTE=6000 RATE_LIMIT_ACCOUNT_PER_MINUTE=6000 \
    LOGIN_ATTEMPTS_PER_MINUTE=1000 go run ./cmd/api &         # 用例都来自同一 IP、同一批账号
  cd ../frontend && npx vite --port 5173 &
  cd ../quality && npx playwright install chromium && npm run e2e
  ```

  已有本地库若是旧版种子（图片指向外网占位图），用新库：`MYSQL_DSN=.../blink_shop_e2e?parseTime=true`。
- 其他桌面浏览器：`npx playwright install firefox webkit` 后 `BLINK_E2E_BROWSERS=firefox,webkit npm run e2e`（CI 默认只跑 Chromium）。
  `files.spec.mjs` 在页面里直接导入前端源码模块（`/src/api/files.ts`），只能对 Vite 开发服务器跑；对生产构建（nginx）跑其余用例时用
  `--grep-invert` 排除这三个用例，再让 Vite 把 `/api` 代理到同一个部署单独跑它（见 `docs/11-acceptance-record.md`）。
- `acceptance/`：发布验收工具（10.2）：
  - `setup.sh [端口]` / `teardown.sh`：在本地基础设施上起应用层部署（`deployments/app`，API 生产模式 + nginx），专用的库 / 账号 / 桶 / 集合，凭据随机生成，结束后全部清理；
  - `check.py`：docs/07 验收清单第 2–7 条逐条检查（并发结算、重复消息与取消、无 AI / Milvus / MinIO 降级、越权、非法输入、Prompt / 配置），
    `ACCEPT_DEGRADED_BASE` / `ACCEPT_SLOW_BASE` / `ACCEPT_MYSQL_DB` 见文件头说明；
  - `fakellm/`：慢模型实例用的假模型服务（请求里带“验收版本”时立即返回规划 JSON，否则一直不回）；
  - `lan_forward.py <局域网IP>:<端口> 127.0.0.1:<端口>`：真机验收时把只绑本机的部署转发到局域网地址，App 走 Wi-Fi 访问（才能测真实断网）；同一网络的设备都能访问，只在验收期间运行，用完停掉。
- `data/eval/`：评测 JSONL 数据集：`intent.jsonl`（意图 66 条，含 16 条留出集）、`rag.jsonl`（知识检索 38 条）、`product_search.jsonl`（商品搜索 28 条，含预算、品牌、排除、冲突、无结果和多轮追问）、`image_search.jsonl`（图片搜索 231 条，商品图变换 + 不可售商品 + 无关图片）、`agent.jsonl`（端到端导购 63 条，其中多轮 20 条、售后 / 规则 10 条、边界 9 条；可带 `expect_meaning` 语义断言）、`agent_adversarial.jsonl`（越权 / 对抗 21 条，用 `-agent-cases` 指定）、`intent_holdout_v2.jsonl`（意图留出集 v2，48 条，评测前冻结，用 `-intent-cases` 指定，不针对它改规则或 Prompt）、`rag_paraphrase_holdout.jsonl`（知识检索改写留出集 44 条）、`judge_calibration.jsonl`（语义断言裁判校准集 64 条，`-suite judge`）。每个文件第一行注释说明字段。
- `schemas/eval-report.schema.json`：评测报告格式（门槛结论、分组、逐条结果、失败样本），管理端质量评测页按它读取。
- 评测 runner 在后端：`cd backend && go run ./cmd/eval`（`-suite intent|rag|products|images|agent|all`，`-vector off|hash|env`，`-image-index memory|milvus`，`-llm off|env` 用真实模型），报告写到 `reports/eval-<时间>/`（JSON + Markdown），用法和指标见 `backend/README.md`“离线评测”。

### 压测

`cd backend && go run ./cmd/loadtest -base http://127.0.0.1:8080`（`-scenarios catalog,api,sse,checkout`、`-duration`、`-concurrency`、`-sse`、`-checkout`、`-contention`）。
会注册一次性账号并下单，**只对一次性的库跑**；目标 API 要放宽限流（`RATE_LIMIT_IP_PER_MINUTE` / `RATE_LIMIT_ACCOUNT_PER_MINUTE` / `LOGIN_ATTEMPTS_PER_MINUTE`）。
报告写到 `reports/loadtest-<时间>/`（JSON + Markdown），任一场景未达标退出码 1。目标和最近一次结果见 `docs/10-release-readiness.md`。

### 评测运行手册

1. **改了规划、检索、重排或导购回答后**：`cd backend && go test ./src/eval`（CI 同样跑），再 `go run ./cmd/eval -version=$(git rev-parse --short HEAD)` 生成报告。
2. **看结果**：命令行每套一行（`passed` / `failed`、通过率、门槛、指标）；任一套未过门槛退出码 1。详细结果在 `reports/eval-<时间>/report-<suite>.md`，
   或在 Web 管理端“质量评测”页（读取 `EVAL_REPORT_DIR`，默认就是这里）：未过门槛的套件和报告标红，详情默认只列失败用例，带原因和实际输出。
3. **失败了怎么办**：
   - 先在“质量评测 → 单条试跑”或 `POST /eval/intent|rag|products` 复现那一句，看规划、召回和重排明细；端到端用例到“调试观测”看同类运行的时间线。
   - 是代码退化就修代码；是标注本身有歧义，改数据集要在提交说明里写清理由。
   - **不要为了让留出集（`held_out`）通过去改规则**——留出集用来衡量泛化，针对它调规则就失去意义；要扩大覆盖就把新说法加进开发集，再补新的留出用例。
4. **加用例**：按文件第一行注释的字段写一行 JSON，`id` 不重复；端到端用例每条都在全新演示数据上跑，可以用 `history` 写多轮。
5. **改门槛**：在 `backend/src/eval`（`DefaultIntentMinPass` 等）和 `cmd/eval` 参数里改，报告会记下当时的门槛。
6. **真实向量 / 模型**：`-vector=env`、`-image-embedder=env` 用临时集合，跑完删除，不碰正式集合；需要的环境变量见后端 README。
- `tools/`：数据整理脚本（后续节点补充）。
