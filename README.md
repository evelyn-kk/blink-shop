# Blink Shop

多角色 AI 导购电商：Go API + React Web 管理端 + Java 原生 Android 用户端。设计与契约见 [docs/](docs/)，协作规则见 [AGENTS.md](AGENTS.md)。

## 目录

| 目录 | 内容 |
| --- | --- |
| `backend/` | Go 1.24 API（`net/http`），`openapi.yaml` 是唯一接口契约 |
| `frontend/` | React 19 + TypeScript + Vite，商家和平台管理端 |
| `android-native/` | Java 原生 Android（minSdk 24），用户端 |
| `deployments/` | 本地依赖的 Docker Compose，详见 [deployments/README.md](deployments/README.md) |
| `quality/` | OpenAPI 校验、评测数据和工具 |

## 环境要求

- Go 1.24
- Node.js ≥ 22.12（CI 用 `.nvmrc` 中的版本）
- JDK 17 + Android SDK Platform 35（`android-native/local.properties` 写 `sdk.dir=...`，或设置 `ANDROID_HOME`）
- Docker + Compose v2

## 启动

```bash
cp .env.example .env                                    # 按需修改；.env 不提交
docker compose -f deployments/docker-compose.yml up -d  # MySQL/Redis/Nacos/MinIO/etcd/Milvus
cd backend && go run ./cmd/api                          # http://localhost:8080/api/v1/health
cd frontend && npm ci && npm run dev -- --port 5173     # /api 代理到 :8080
cd android-native && ./gradlew :app:assembleDebug       # 模拟器默认访问 http://10.0.2.2:8080/api/v1
```

## 本地检查（与 CI 一致）

```bash
cd backend && gofmt -l . && go vet ./... && go test ./...
cd frontend && npm ci && npm run lint && npm run build
cd android-native && ./gradlew test lint assembleDebug
cd quality && npm ci && npm run openapi:lint
docker compose -f deployments/docker-compose.yml config -q
```
