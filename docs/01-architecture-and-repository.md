# 架构、仓库与本地运行

## 技术决策

保持上游的可执行形态：Go 1.24 + `net/http`/MySQL 8 API，React 19 + TypeScript + Vite Web，Java 原生 Android（minSdk 24），MySQL、Redis、Nacos、MinIO、Milvus 2.5 + etcd。HTTP API 前缀固定为 `/api/v1`。

```text
Android app ─┐                 ┌─ MySQL: 交易、会话、审计
React Web ───┼─> Go API ───────┼─ Nacos: 动态配置/Prompt 发布
             │                 ├─ MinIO: 上传文件
             │                 ├─ Milvus: 文本/图片向量
             │                 └─ LLM/STT/TTS: 可选外部提供者
             └─ SSE <──────────┘
```

## 目标目录（先建骨架，再逐层实现）

```text
blink-shop/
  deployments/docker-compose.yml
  backend/{cmd/api,cmd/import-products,migrations,src/{agent,configcenter,domain,httpapi,imagevector,ingest,objectstore,rag,risk,store}}
  frontend/src/{api,components,features,pages,types}
  android-native/app/src/main/{java/com/blink/shop,res}
  quality/{data/eval,evals,tools}
  docs/
```

包依赖必须单向：`httpapi -> agent/store/... -> domain`；`domain` 不导入基础设施；`store` 通过接口可替换为内存 fake；`cmd/api` 只组装依赖和生命周期。前端不得直接拼接散落的 fetch，统一经 `api/http.ts`。

## 配置与启动

提交 `.env.example`，不提交 `.env`。至少覆盖 `API_ADDR`、`MYSQL_DSN`、`REDIS_ADDR`、`NACOS_*`、`MINIO_*`、`MILVUS_*`、`AI_*`、`STT_*`、`TTS_*`、`APP_ENV`、`RUN_MIGRATIONS`、`BOOTSTRAP_VECTOR_INDEX`。开发默认值可以安全运行；生产必须拒绝默认数据库密码、宽松 CORS、空/默认 AI 密钥、默认对象存储凭证。

启动顺序：`docker compose -f deployments/docker-compose.yml up -d` → `cd backend && go run ./cmd/api` → `cd frontend && npm ci && npm run dev -- --port 5173` → `cd android-native && ./gradlew :app:assembleDebug`。前端 Vite 将 `/api` 代理到 `:8080`。

## 可用性与降级

健康接口只回答进程存活；另加 readiness 检查数据库。Nacos 不可用时使用环境变量/内存默认配置；Milvus 不可用时文本检索退回 MySQL 关键词，图搜返回明确不可用；AI 不可用时启用规则规划和模板化真实数据回答；对象存储不可用时上传失败但不影响商品浏览与交易。
