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
- `data/eval/`：评测 JSONL 数据集：`rag.jsonl`（知识检索 38 条）、`product_search.jsonl`（商品搜索 28 条，含预算、品牌、排除、冲突、无结果和多轮追问）、`image_search.jsonl`（图片搜索 231 条，商品图变换 + 不可售商品 + 无关图片）。
- 评测 runner 在后端：`cd backend && go run ./cmd/eval`（`-suite rag|products|images|all`，`-vector off|hash|env`，`-image-index memory|milvus`），报告写到 `reports/eval-<时间>/`（JSON + Markdown），用法和指标见 `backend/README.md`“离线评测”。
- `tools/`：数据整理脚本（后续节点补充）。
